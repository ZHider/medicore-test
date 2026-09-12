# 模块交接记录（M1–M6）

> **每个模块的"结构与决策"手册。** 与 `ARCHITECTURE.md`（讲整体）和 `HANDOVER.md`（讲历程）不同，
> 本文档**逐模块**记录：它由哪些文件组成、关键决策是什么、当时踩了什么坑、以及改动时要注意什么。
>
> 即使模块已完成，这些信息仍然重要——**它们回答"为什么是这样写的"，而这正是重构时最容易改错的地方。**

最后更新：工程质量基建（CI / fuzz / benchmark / race）之后（M1–M6 全部完成；提交历史见 `HANDOVER.md` §1）

> ⚠️ **端点清单以 `backend/internal/router/router.go` 或 gin 启动输出为准。**
> 本文档曾把管理端点写成 `/api/v1/audit-logs`（少了 `/admin/` 段），照文档调接口会 404。
>
> ⚠️ **本文档记录的是"当前为什么这样写"。** 审视修掉的缺陷（队列越权、
> 审计资源名、HIS 契约倒置等）记在 `HANDOVER.md` §2.7 与 `docs/FIXES.md`，
> 改代码前值得一并看；**写代码前先扫一眼 `docs/PITFALLS.md`**。

---

## 阅读指引

| 你想做什么 | 读哪一节 |
|---|---|
| 改认证/权限 | §2 M2 |
| 改号源、抢号、签到 | §4 M4 |
| 改 AI 对话或总结 | §5 M5 |
| 改任何 HTTP 接口 | §1 M1（中间件链） |
| 加列表接口 | §3 M3（游标分页） |
| 排查"数据不一致" | §4 M4（对账）+ §5 M5 |
| 改医生接诊列表 / 报告归属校验 | §6.7 M6 |
| **写代码前避险 / 出问题定位** | `docs/PITFALLS.md`（四张判据表） |
| **验收某个角色能不能用** | **§6.7 的教训：按角色横切，别按模块打勾** |

**通用前提**：所有模块都遵守 `AGENTS.md` §2 的硬性规则。本文档只记录**模块特有**的内容。

---

## 1. M1 · 基础设施

**规模**：16 文件 / 951 行　**状态**：✅ 完成

### 1.1 文件构成

```
internal/config/      配置加载 + 启动校验（★ 含密钥特征检测、本地密钥合并）
internal/logger/      Zap JSON 构造
internal/middleware/
  ├── trace.go        TraceID 生成与透传
  ├── recovery.go     panic 捕获 + 堆栈
  ├── logger.go       RequestLog（结构化访问日志）
  ├── metrics.go      Metrics（HTTP 指标采集）
  ├── jwt.go          JWT 校验
  ├── rbac.go         RequireRole 角色校验
  ├── ratelimit.go    Redis 令牌桶（Lua 原子）
  └── audit.go        审计记录投递
internal/metrics/     ★ Prometheus 注册表（独立包，见 §1.3）
internal/traceid/     32 位十六进制 ID 生成 + context 传递
internal/response/    统一响应信封（OK / Fail / FailErr / Accepted / FailWithData）
internal/errcode/     错误码定义与 HTTP 状态映射
internal/apperr/      可携带错误码的应用错误（Wrap / Code / Message）
```

### 1.2 中间件链（顺序固定，不可调换）

```
TraceID → Recovery → RequestLog → Metrics → Audit    ← 全局（r.Use）
                          ↓
              [RateLimit → JWT → RBAC]                ← 按路由挂载
```

**为什么 JWT/RBAC 不放在全局**：探针与 `/metrics` 必须免认证。
放全局会导致 Prometheus 抓取被 401，健康检查也无法在启动早期生效。

**为什么 Audit 在 Metrics 之后**：审计需要知道最终的状态码与耗时，
必须在 `c.Next()` 之后才能取到——但因为它自己也是全局中间件，
实际是在自己的 `c.Next()` 返回后组装记录。

### 1.3 关键决策

| 决策 | 理由 |
|---|---|
| **Prometheus 注册表独立成 `internal/metrics`** | 业务指标需由 **service** 注册（抢号、对账、LLM）。若注册表在 middleware 中，service 就得反向依赖 middleware。下沉后各层只依赖 `metrics` 包，无环 |
| 用**独立 registry** 而非 `prometheus.DefaultRegisterer` | 避免测试重复注册 panic；`/metrics` 只暴露本项目指标 |
| Metrics 的 `path` 标签用**路由模板**（`FullPath`）而非真实路径 | 真实路径会让每个 ID 产生独立时间序列 → **基数爆炸** |
| 统一响应信封对**未匹配路由**也生效 | 否则客户端拿到 gin 默认的纯文本 `404 page not found`，无法按 `code` 分支处理 |
| `r.HandleMethodNotAllowed = true` | gin 默认**不检测**方法不匹配，`POST /healthz` 会返回 404 而非 405 |
| TraceID 透传上游 `X-Trace-Id` | 保证跨服务链路连续；同时写入**响应头与响应体**，前端报错时可直接带上 |

### 1.4 踩过的坑

**坑 1：未知路由返回纯文本 404。**
实测发现 `curl /api/v1/nope` 返回 `404 page not found`（gin 默认），
而 spec §3.3 要求统一信封。补 `NoRoute` → 40003。

**坑 2：方法不允许返回 404 而非 405。**
同样是 gin 默认行为。补 `HandleMethodNotAllowed = true` + `NoMethod` → 40006。

**坑 3：202 响应漏掉 `trace_id`（审视时才发现的）。**
handler 里直接构造 `response.Body{...}` 忘了填 `TraceID` 字段，
导致该请求在日志与审计里**断链**（违反 spec §9.2）。
已加 `response.Accepted(c, msg, data)` helper —— 提供它的目的就是**不让调用方手拼 Body**。

**坑 4：限流 fail-open 没有指标。**
代码注释写着"故障会被 metrics 捕获"，但**当时根本没有这个指标**。
fail-open 意味着限流已静默失效，恰恰最需要告警。补 `ratelimit_errors_total`。

**坑 5：`config.Validate()` 最初用"已知坏值黑名单"。**
这个设计有两个毛病：黑名单只能拦住预先想到的值；而且**黑名单本身会把密钥字面量带回仓库**。
改为**按特征判断**（单一字符重复 / 短周期重复 / 连续升序 / 含占位词）。

### 1.5 改动时注意

- **加中间件**：想清楚它该全局还是按路由。全局中间件对 `/metrics` 也生效。
- **加指标**：在 `internal/metrics` 注册；标签**不要**用高基数维度（用户 ID、真实路径）。
- **改错误码**：`errcode.go` 里有三张表（常量、message、httpStatus），**必须同步改**，
  漏改会让错误码落到"未知错误/500"。
- **改配置**：`config.go` 的 `envBindings` 是显式列出的。
  **viper 的 `AutomaticEnv` 对嵌套键的 Unmarshal 支持不完整**，新配置项必须手动加绑定，
  否则环境变量不生效（这个坑很隐蔽：程序不报错，只是读到零值）。

---

## 2. M2 · 认证鉴权

**规模**：14 文件 / 930 行　**状态**：✅ 完成

### 2.1 文件构成

```
internal/secret/
  ├── cipher.go       AES-256-GCM 加解密
  ├── password.go     bcrypt（含 DummyPassword 时序防护）
  └── token.go        刷新令牌随机生成
internal/jwt/jwt.go   access_token 签发与校验
internal/authctx/     gin.Context 中认证信息的读写（避免各层各写字符串字面量）
internal/model/user.go + dto_auth.go
internal/repository/user.go + token.go
internal/service/auth.go
internal/handler/auth.go
```

### 2.2 双令牌模型

| 令牌 | 形式 | 有效期 | 存储 | 可撤销 |
|---|---|---|---|---|
| `access_token` | JWT HS256 | 2h | 无状态 | ❌ |
| `refresh_token` | **不透明随机串**（256 位） | 7d | Redis | ✅ |

**为什么 refresh_token 不用 JWT**：刷新令牌**必须可撤销**，无状态令牌做不到
（除非再引入黑名单，等于绕回 Redis）。既然要存 Redis，JWT 的自包含性就毫无收益。

### 2.3 关键决策

| 决策 | 理由 |
|---|---|
| `phone_hash` 用 **HMAC-SHA256(pepper, phone)** 而非 `SHA256(phone+salt)` | keyed hash 不会因拼接方式引入长度扩展问题。**关键性质仍是"确定性"**——用逐行随机盐就没法由手机号反查用户，登录彻底不可用（这是第 1 处刻意偏离 spec） |
| 刷新用 **`GETDEL` 原子消费** | 写成 `GET`+`DEL` 两步，并发刷新时同一令牌会被消费两次 → **重放窗口** |
| 登录失败**不区分**"账号不存在"与"密码错误" | 区分就是用户枚举漏洞 |
| 账号不存在时**也执行一次等价比对** | 否则响应时间差约一个 bcrypt 周期（cost=10 约 60ms），攻击者可据此枚举手机号 |
| 占位散列**用与真实口令相同的 cost 预生成** | 若占位用固定 cost 而配置改了，时间差会重新出现 |
| 注册**永远**产生 `patient` 角色 | 防止自助提权为管理员 |
| bcrypt **显式拒绝 >72 字节**输入 | bcrypt 会静默截断，长密码给出虚假安全感 |
| 限流器故障时 **fail-open** | 让登录整体不可用比短暂放开限流更严重；但必须有指标（见 M1 坑 4） |

### 2.4 限流参数（实测验证过）

| 端点 | 桶容量 | 补充速率 | 键 |
|---|---|---|---|
| 登录 | 5 | `5.0/60.0` 每秒 | 按 **IP**（`rl:login:{ip}`） |
| 抢号 | 10 | `10.0/60.0` 每秒 | 按 **用户**（`rl:grab:{uid}`） |

**为什么抢号按用户而非 IP**：同一 NAT 后的多名患者不应互相影响。
此时 JWT 已执行，能拿到用户 ID。

**令牌桶必须用 Lua**：拆成"读余量 → 计算 → 写回"三次往返，
并发请求会读到同一个旧值 → 限流被击穿。

**实测**：连续 8 次登录，第 6 次起返回 429 + `Retry-After: 12s`；
等待 14 秒后桶内 `tokens=1.39`（14×(5/60)+残余 0.22，数学自洽）。

### 2.5 踩过的坑

**坑 1：提交前忘了"先查再插"在软删除下的反直觉行为。**
`Register` 原本先查手机号是否存在再插入。但 GORM 软删除会**过滤掉已删除用户**，
而 `uk_phone_hash` 是**全局唯一**（刻意没并入 `deleted_at`，见数据库设计）。
结果："手机号属于已软删用户"时，先查说"没注册"，插入却被索引拒绝 ——
**先查再插给出了与事实相反的提示**。已删除该查询，直接依赖索引 + `ErrDuplicateKey`。

**坑 2：`IsDuplicateKeyError` 是造轮子。**
原本用 `strings.Contains(err.Error(), "Error 1062")` 匹配错误文本 ——
依赖驱动文案，换版本或改语言环境就静默失效。
改用 GORM 原生 `TranslateError: true` + `errors.Is(err, gorm.ErrDuplicatedKey)`。

**坑 3：`service` 里用了标准库 `log.Printf`。**
会输出非结构化文本，污染 Zap JSON 日志流、让基于 `level` 的告警漏掉它。
已改为注入 `*zap.Logger`（nil 时回退 `zap.NewNop()`，便于单测）。

**坑 4：密钥被提交进 git 历史。**
`--amend` 之后密钥在全对象库里**仍能搜到 4 处**。
必须三步：`amend` + `reflog expire --expire=now --all` + `gc --prune=now`。

### 2.6 改动时注意

- **改认证**：`authctx` 是唯一的上下文读写入口，不要新增 `c.Get("user_id")` 字面量。
- **新增角色**：`model.Role` 常量 + `middleware.RequireRole` 使用处。
  注意 spec §2.2 的**双层校验**：路由层只是第一层，**资源归属必须由 service 层再校验**
  （患者只能看自己的预约/病历）。
- **改密钥**：**重新生成 `aes_key` 会导致库中已加密数据永久无法解密**。
  `cmd/genlocal` 因此默认拒绝覆盖已有文件。

---

## 3. M3 · 目录查询与游标分页

**规模**：9 文件 / 898 行　**状态**：✅ 完成

### 3.1 文件构成

```
internal/model/pagination.go   ★ 游标分页通用实现（所有列表接口的地基）
internal/model/catalog.go      Department / Doctor / DoctorDepartment / Schedule
                                + 只读查询模型（DepartmentBrief / DoctorListItem 等）
internal/repository/cache.go   ★ Redis 缓存 + singleflight 防击穿
internal/repository/department.go / doctor.go / schedule.go
internal/service/catalog.go
internal/handler/catalog.go + params.go   参数解析辅助
```

### 3.2 `pagination.go` —— 所有列表接口的地基

```go
type CursorPage[T any] struct {
    Items      []T    `json:"items"`
    NextCursor uint64 `json:"next_cursor,omitempty"`
    HasMore    bool   `json:"has_more"`
}
```

**三条必须遵守的约定**：

1. **仓储层固定查 `limit+1` 条**，多出的一条只用于判断 `has_more`，**不发额外 COUNT**。
2. **`size` 上限 100**（`NormalizePageSize`）。否则 `size=999999` 会被当作合法请求，
   等价于一次全表扫描。
3. **空结果用 `model.NonNil()` 包一层**，保证序列化为 `[]` 而非 `null`
   （前端处理两者代码路径不同）。

**实测依据**（23 万行，`EXPLAIN ANALYZE`）：

| 方式 | 耗时 | 扫描行数 |
|---|---|---|
| `LIMIT 100000, 20`（OFFSET） | **137 ms** | 203,334 行 + 排序 100,020 行 |
| `WHERE id < ? ORDER BY id DESC LIMIT 20` | **0.483 ms** | **20 行** |

**相差约 284 倍**，且游标分页代价与页码无关。

### 3.3 缓存设计

```go
Load[T](ctx, cache, key, ttl, loader)   // cache-aside + singleflight
```

| 特性 | 实现 |
|---|---|
| 键 | `cache:dept_doctors:{departmentID}` |
| TTL | 5 分钟（科室医生变更频率极低） |
| **防击穿** | `singleflight.Group` 合并并发回源：热点键失效瞬间，N 个请求同时未命中，各自回源会把压力全打到 MySQL |
| **回源脱离调用方取消** | `context.WithoutCancel(ctx)` + 独立超时（见 §3.5 坑 1） |
| 降级 | 读失败视作未命中、写失败只记日志，**请求不中断** |
| 脏数据自愈 | 反序列化失败按未命中处理**并删键**，避免错误反复出现 |
| 显式失效 | `DepartmentDoctorsCacheKeys` 导出，供写路径精确失效 |

### 3.4 关键决策

| 决策 | 理由 |
|---|---|
| **手写 JOIN 必须显式带软删除条件** | GORM 的软删除作用域**只在 `Model(&Struct{})` 时生效**；用 `Table("...")` 手写 JOIN 时完全不介入 → 已删除医生会照常出现在列表 |
| 用 **LEFT JOIN** 取医生姓名 | 医生即便已软删，历史预约仍应显示当时姓名而非空白 |
| `consult_fee` 用 **`string`** 承载 DECIMAL | 金额用浮点会有舍入误差；DECIMAL 可直接扫进 string，JSON 输出字符串，两端都不丢精度 |
| 医生排班查询用 `Model(&model.Schedule{})` | 自动带软删除条件（与手写 JOIN 相反的场景） |
| 不存在的科室返回 **404 而非空列表** | 否则客户端无法区分"科室不存在"与"该科室暂无医生" |
| 日期区间反向直接 **400** | 否则静默返回空列表，让人误以为"没有排班" |

### 3.5 踩过的坑

**坑 1（最有价值）：缓存回源的 context 传染。**
singleflight 的**跟随者共享 leader 的 context**。
若 leader 的请求被取消（用户关页面、连接超时），所有健康的跟随者会一起收到
`context.Canceled` —— 拿到一个**本不该属于它们的错误**。

修复：回源用 `context.WithoutCancel(ctx)` + 独立超时上界。

**已做反向验证**：把 `loader(loadCtx)` 改回 `loader(ctx)`，
`TestCacheLoad_LoaderContextSurvivesCallerCancellation` 与
`TestCacheLoad_LeaderCancellationDoesNotFailFollower` 都失败
（`回源 context 已被取消: context canceled`）。

**坑 2：手写 JOIN 漏了软删除条件。**
这是本项目最值得记住的 GORM 陷阱。已写进 `AGENTS.md` §2.5。

**坑 3：小数据量下的 EXPLAIN 误导。**
2 行数据的表上，游标分页查询显示 `Using filesort` —— 让我一度以为索引设计有问题。
灌到 3 万行后计划自动变优。**结论：索引决策必须用有代表性的数据量验证。**

**坑 4：验证缓存命中不能只看"Redis 里有键"。**
这只能说明写进去了，不能说明读走了缓存。
**用哨兵值证伪**：往 `cache:dept_doctors:2` 写入一个假医生，
接口返回了哨兵 → 证明真的在读缓存；删键后恢复真实数据 → 证明控制组有效。

### 3.6 改动时注意

- **新增列表接口**：用 `model.NewCursorPage` + `NormalizePageSize`，
  仓储层查 `limit+1` 条。**禁止 OFFSET**。
- **新增缓存**：用 `repository.Load` 而不是自己写 `Get`/`Set`（否则丢掉防击穿与降级）。
- **写缓存相关代码**：写路径要调 `DepartmentDoctorsCacheKeys` 失效，别只依赖 TTL。
- **加 JOIN**：**逐个检查每张表的 `deleted_at IS NULL`**。

---

## 4. M4 · 抢号（全项目最复杂的模块）

**规模**：7 文件 / 1358 行　**状态**：✅ 完成

### 4.1 文件构成

```
internal/model/appointment.go     Appointment / AppointmentItem / QueueItem
internal/repository/stock.go      ★ Redis 号源（Lua 脚本）
internal/repository/idempotency.go ★ 幂等键（比对删除）
internal/repository/appointment.go 事务、乐观条件、签到排队
internal/service/appointment.go   ★ 完整编排 + 降级 + 回补
internal/scheduler/scheduler.go   cron：超时释放 + 号源对账
```

### 4.2 抢号完整链路

```
① 幂等占用   Redis SETNX idem:grab:{uid}:{key}
               ├─ 不存在        → 本次占用成功，继续
               ├─ 值=pending    → 拒绝（请求在途）→ 40005
               └─ 值=预约ID     → 幂等重放，返回上次结果
② 预热号源   Redis SETNX stock:sched:{id}  ← 必须是 SETNX（见坑 1）
③ 原子扣减   Lua：键不存在→-1 / 余量不足→0 / 成功→1
④ 落库       MySQL 事务：
               UPDATE schedules SET remaining=remaining-1
                 WHERE id=? AND status='open' AND remaining>0   ← 单条原子
               INSERT INTO appointments ...
⑤ 记录结果   Redis SET idem 键 = appointmentID
               ↓ ④ 失败
             回补 Redis 号源 + 释放幂等键
```

### 4.3 五个必须理解的设计点

| # | 设计 | 防的是什么 |
|---|---|---|
| 1 | 预热用 **`SETNX`** 而非 `SET` | `SET` 会覆盖并发已扣减的值 → 号源"还回去" → **超卖** |
| 2 | Lua 键不存在返回 **`-1`** 而非 `0` | spec 原设计返回 0（=号源不足），Redis 冷启动/键过期后**所有抢号全失败**，且提示"号源不足"极具误导性 |
| 3 | 幂等键用**比对删除**释放 | 无条件 `DEL` 会删掉**已完成**的记录 → 后续重放查不到结果 → **幂等失效** |
| 4 | MySQL 用**单条原子 UPDATE** | InnoDB 行锁下 `remaining>0` 即充分条件；比"读 version 再比对"少一次 SELECT 且无需重试循环 |
| 5 | 回补**以容量为上限**且键不存在时不创建 | 否则 `INCRBY` 凭空造出号源 → 制造超卖 |

**注意**：spec 表里定义了 `version` 字段，但**实际未使用**（单条原子 UPDATE 已足够）。
字段保留以便将来若改为"读取-计算-写回"（如按比例分配号源）时启用。

### 4.4 签到与就诊序号

序号按**实际签到时间**分配：

```sql
START TRANSACTION;
  SELECT id FROM schedules WHERE id=? FOR UPDATE;   -- 锁排班，串行化同排班签到
  SELECT COALESCE(MAX(queue_no),0)+1 ...            -- 取下一个序号
  UPDATE appointments SET checked_in_at=NOW(3), queue_no=? WHERE id=?;
COMMIT;
```

**两层保障**：事务 + `FOR UPDATE` 串行化；`uk_sched_queue` 唯一键兜底
（`queue_no` 为 NULL 时不冲突，故未签到者不受约束）。

**幂等**：已签到的预约直接返回原序号，患者重复点击不会重新排队。

**候诊队列刻意不脱敏**：医生必须能识别面前的患者，把姓名打成"张*"会让队列失去意义。
这是 spec §2.2 脱敏规则的**有意例外**。

> ⚠️ **但"不脱敏"必须配上归属校验。** 这个端点曾经只挂了 `RequireRole(doctor)`，
> service 层没有校验排班归属 —— 于是**任何医生换一个排班 ID 就能枚举全院患者的
> 姓名与 user_id**。全量审视时才发现（见 `HANDOVER.md` §2.7-B）。
>
> 现在的 `Queue(ctx, access, scheduleID)` 会校验 `schedule.DoctorID == 本人 doctors.id`
> （管理员豁免）。
>
> **规律：凡是返回未脱敏个人数据的端点，都必须问一句"它有没有做资源层归属校验"。**
> 路由层 RBAC 只回答"你是不是医生"，回答不了"这个排班是不是你的"。

### 4.5 超时释放与对账

| 任务 | 周期 | 作用 |
|---|---|---|
| 超时释放 | 1 分钟 | 把 `pending` 且 `expire_at < NOW()` 的预约置为 `expired` 并回补号源 |
| 号源对账 | 5 分钟 | 以 MySQL 为准校准 Redis |

**释放的幂等闸门**：用 `UPDATE ... WHERE status='pending'`，
`RowsAffected=0` 说明已被其他实例处理 → **跳过回补**。
多实例并发扫描同一批订单时不会重复加号。

**对账必须用 `CompareAndSet`（Lua 比对更新）而非无条件 `SET`**：
对账读 MySQL 与写 Redis 之间有**时间窗**，期间若有并发扣减，
无条件覆盖会把刚扣掉的号源还回去 —— **对账本身制造超卖**。

| 情况 | 处理 |
|---|---|
| Redis == MySQL | 无需处理 |
| Redis < MySQL | 修正（典型偏差，表现为"明明有余号却抢不到"） |
| Redis > MySQL | 修正（超卖风险方向） |
| Redis 键不存在 | **跳过** —— 是未预热或已过期，不是偏差 |

### 4.6 常量速查

| 常量 | 值 | 位置 |
|---|---|---|
| 确认时限 | 15 分钟 | `model.DefaultConfirmWindow` |
| 单轮过期限额 | 200 | `service.expiredBatchSize` |
| 超时释放周期 | `@every 1m` | `scheduler` |
| 对账周期 | `@every 5m` | `scheduler` |
| 任务超时 | 30 秒 | `scheduler.jobTimeout` |

### 4.7 踩过的坑

**坑 1：为什么预热必须 `SETNX`。**
用 `SET` 覆盖已存在的键，会把并发请求已扣减的号源"还回去"。
**这不是性能问题，是直接超卖。**

**坑 2：候诊队列包含已取消的患者。**
实测发现队列里出现了已取消的预约 —— 医生会对着一个不会来的号叫诊。
修复：队列查询加 `status IN ('pending','confirmed')`。

**坑 3：`hey`/`wrk` 测不出这个场景。**
这两个工具对所有请求发**同一个 token**，而同一用户对同一号源只能持有一个进行中预约
（`uk_active`），重复请求只会得到冲突 —— **根本触达不到并发扣减路径**。
跑出来会是一片 40004，看着"很安全"其实什么都没验证。
因此写了 `cmd/loadtest`：批量准备患者账号、各自签发自有权杖、并发抢同一号源，
结束后自动核对"成功次数 / MySQL remaining / Redis 号源键"三者是否自洽。

**实测结果**：200 患者抢 20 号源（并发度 100）→ 成功 20、号源不足 180、
耗时 172ms、MySQL 20→0、Redis 0，**三方自洽，零超卖**。

**坑 4：`unsafe` 的乐观锁。**
spec 表里有 `version` 字段，但 §7.1 的 SQL 是 `WHERE id=? AND remaining>0`，
**根本没用到 version**。这是 spec 自身的不一致，已按"单条原子 UPDATE"实现并加注释说明。

### 4.8 改动时注意

- **改号源逻辑**：先想清楚"Redis 与 MySQL 不一致时会怎样"。任何跳过回补或跳过对账的改动
  都会导致号源缓慢蒸发（表现为"总号源 20 却越来越难抢"，极难排查）。
- **改幂等**：`Complete` 用 `SET ... XX`（只在键存在时写）。
  若键已过期消失，**不应凭空创建**——那会让一个已关闭的幂等窗口重新生效。
- **验证并发**：必须用 `cmd/loadtest`，不要用 `hey`（见坑 3）。
- **有专门的测试守护关键不变量**：`TestGrab_DatabaseFailureReleasesRedisStock`
  断言"Redis 已扣减但 MySQL 回滚"时必须回补。改坏它会让测试失败。
- **医生接诊列表 `/doctor/appointments` 也挂在本模块**（M6 补全，见 §6.7）。
  它复用 `AppointmentService`，但走的是**另一组依赖接口**（`doctorAppointmentStore` + `doctorLookup`）——
  患者侧的 `appointmentStore` 完全用不到它们。加方法时想清楚加在哪一边，
  混进 `appointmentStore` 会逼着患者侧的所有测试 fake 去实现用不到的方法。
- **筛选条件必须白名单校验**（`validateAppointmentFilter`）。参数化 SQL 不会报语法错，
  拼错的状态值只会**静默返回空列表**，看起来就像"今天真的没有预约"。

---

## 5. M5 · AI 预问诊

**规模**：13 文件 / 1860 行　**状态**：✅ 完成（对话 + 总结 + 对账）

### 5.1 文件构成

```
internal/llm/
  ├── llm.go        Client 接口（★ 回调式流式）+ Message/ChatRequest
  ├── mock.go       MockClient（★ 故障注入）
  └── factory.go    按 provider 构造
backend/prompts/    ★ go:embed 的版本化提示词与 JSON Schema
internal/repository/conversation.go  进行中对话（Redis List）
internal/repository/report.go        ai_pre_reports 读写
internal/repository/summary_queue.go ★ Redis Stream 队列
internal/service/preconsult.go       对话编排
internal/service/preconsult_submit.go 提交 / 查询 / 总结处理
internal/service/summary.go          ★ JSON 提取 + Schema 校验 + 重试降级
internal/service/reconcile.go        号源对账（归在此模块交付）
internal/worker/summary.go           ★ Stream 消费者
internal/handler/preconsult.go       SSE 处理器
```

### 5.2 LLM 抽象：为什么是回调式

```go
type Client interface {
    ChatStream(ctx context.Context, req ChatRequest, onDelta func(delta string) error) error
    Complete(ctx context.Context, req ChatRequest) (string, error)
    Model() string
}
```

**流式刻意用回调而非返回 `<-chan Chunk`**：

channel 方案必须额外起一个 goroutine，并在**成功、出错、取消、调用方提前退出**
这**四条路径**上都保证关闭 —— 遗漏任何一条就是 goroutine 泄漏
（spec §7.2 专门要求处理 `ctx.Done()` 正是防这个）。
回调方案由调用方在自己的 goroutine 里驱动，**不存在额外的生命周期问题**。

**`onDelta` 返回 error 时立即中止并原样返回** —— 这通常意味着客户端已断开，
service 据此判断"不必继续调 LLM"。

### 5.3 SSE 实现要点

```
POST /appointments/:id/preconsult/chat
  → 校验归属与状态        ← 失败仍返回统一 JSON 信封
  → 落用户消息            ← 先落库：重试时不必重新输入
  → startStream()         ← 首次吐数据时才写响应头
  → LLM 流式 → 逐段写 delta 事件
  → 落助手回复            ← 用 WithoutCancel 的 context，保证写成功
  → done 事件
```

**`startStream()` 延迟的意义**：让前置校验失败时仍能用统一信封，
而不是"先发 200 再报错"。

**流已开始后的错误只能通过 `error` 事件传递**（HTTP 状态码早已发出）。

**必须设 `X-Accel-Buffering: no`**：否则反向代理会把流式输出攒成一整块再下发。
生产环境 Nginx 同样需要 `proxy_buffering off`。

**`fetch` + `ReadableStream` 而非 `EventSource`**：后者只支持 GET，
无法携带消息体与 `Authorization` 头。

### 5.4 结构化总结：校验与降级

```
调用 Complete → 提取 JSON → Schema 校验
     ├─ 通过 → structured_json 入库，status=done
     └─ 失败 → 追加纠正指令，重试 1 次
                └─ 仍失败 → 降级：structured_json=null, fallback=true
                            保留原始对话供医生查看
```

**JSON 提取做三级尝试**（真实模型经常在 JSON 外包裹说明文字）：
1. 整体就是 JSON
2. 剥 Markdown 代码围栏 ` ```json ... ``` `
3. 取首个 `{` 到末个 `}` 之间

**刻意不做更花哨的解析**：混乱到这三步都救不回来时，正确做法是
**降级让医生看原始对话**，而不是在解析器上继续堆代码。

**降级不是流程失败**：`summarize()` **不返回 error**，
所有失败都通过 `Fallback + Reason` 表达 —— 医生应该看到原始对话，而不是一片空白。

### 5.5 Stream worker：至少一次投递

| 机制 | 作用 |
|---|---|
| 消费者组 + `XREADGROUP BLOCK` | 有任务立即醒来，不靠轮询空转 |
| **失败的消息不确认** | 留在 pending，等待 `XAUTOCLAIM` 重投 |
| 启动时 + 周期 `ClaimStale` | 认领崩溃 worker 遗留的任务；**没有它报告会永远停在 processing** |
| 投递次数 ≥ 3 → 死信队列 | 避免毒消息无限重投堵住主队列 |
| 死信时**同时标记报告失败** | 否则报告永远停在 processing，对客户端与"正在处理"无法区分 |
| `MAXLEN ~ 10000` | 防队列无上限增长吃光 Redis 内存 |

**重试次数取自 Redis 的投递计数**（`XPENDING`）而非报告行的 `attempt_count`：
后者只在**成功抢占后**递增，"抢占前就失败"（如数据库断连）也需要计数，
否则那类失败会**永远重试下去**。

**`MarkProcessing` 允许 `processing → processing`**：
`XAUTOCLAIM` 重投的正是上次崩溃时停在 processing 的消息，
拒绝它会让任务永远卡住。

**消费者名带主机名与 PID**：多实例时能从 `XPENDING` 区分是哪个进程留下的未确认消息。

### 5.6 提示词版本化

`prompts/*.md` 与 `*.json` 用 **`go:embed`** 编进二进制：

- 不会有"部署时漏拷 prompts 目录"的环境差异
- 版本号与代码版本绑定，`PromptVersion()` 随报告入库以便追溯（spec §7.2）
- **Schema 在启动时编译**：写错却在线上才发现，代价是整条降级路径静默生效

**三个文件必须同版本号**：`preconsult_v1.md`（对话）、
`preconsult_summary_v1.md`（总结）、`preconsult_summary_v1.json`（Schema）。
`prompts_test.go` 有专门的版本一致性用例守护这一点。

### 5.7 对话为什么存 Redis

放 `preconsult:conv:{appointmentID}`（List，TTL 24h，上限 40 条）而非直接写报告行：

1. 对话过程中**每轮都要追加**，写 JSON 列是"读-改-写"，多标签页并发会互相覆盖
2. **报告行应在提交时才产生**，避免堆积大量 `status=pending` 的空壳记录

提交时把完整对话一次性落到报告行，然后清掉 Redis 副本。

### 5.8 踩过的坑

**坑 1：`{}` 是合法的 JSON Schema（最隐蔽的一个）。**
我原本写测试断言 `{}` 应该编译失败 —— **测试失败了，因为它是合法的**，
含义是"任何内容都通过"。

**这才是真正危险的**：Schema 被误改成空对象（或必填字段被删光）时，
**编译成功、校验永远通过、降级路径永不触发**，
垃圾数据被当成结构化结果入库，**而且没有任何报错**。

修复：加 `SummaryValidator.SelfCheck()` —— 用空对象做反向验证，
把这个陷阱变成**启动即失败**。

**坑 2：202 响应漏掉 `trace_id`。**
直接构造 `response.Body` 忘了填 `TraceID`，导致该请求在日志与审计里断链。
已加 `response.Accepted` helper。

**坑 3：配了真实上游会静默回退 mock。**
`llm.NewClient` 对 `openai`/`dashscope`/`deepseek` **明确返回错误**（刻意设计）：
生产环境里"以为接了真模型、其实返回的是假数据"比直接挂掉危险得多。

**坑 4：Mock 按 byte 分片会截断中文。**
必须按 **rune** 切分，否则前端显示乱码。有专门的测试断言每个分片都是合法 UTF-8。

**坑 5：Mock 的回复必须确定性。**
按 user 消息数量推断轮次，而不是随机 —— 否则测试没法写断言。

### 5.9 故障注入不是玩具

`llm.mock.summary_mode` 与 `stream_error_at` 存在的唯一理由是：
**spec G3 的验收标准「100% 通过 Schema 校验**或**优雅降级」需要降级路径真的被执行过**。
没有故障注入，降级分支永远是未经验证的代码。

| 配置值 | 模拟的真实故障 |
|---|---|
| `summary_mode: invalid_json` | 模型在 JSON 外套了说明文字/代码块（**最常见**） |
| `summary_mode: invalid_schema` | JSON 合法但缺必填字段 |
| `summary_mode: error` | 上游直接报错 |
| `summary_mode: timeout` | 上游卡住 |
| `stream_error_at: N` | 流到第 N 段时连接中断 |

**实测（`invalid_json`）**：
```
attempts=2, fallback=true, structured_json IS NULL
conversation 2 轮保留
XPENDING=0, 死信队列=0
llm_calls_total{purpose="summary",result="success"}=2   ← 两次调用都成功，只是输出不合法
```

### 5.10 改动时注意

- **改 Client 接口**：保持回调式。改成 channel 会重新引入 goroutine 泄漏风险。
- **改 Schema**：必须与 prompt 同版本号；改完跑 `prompts` 包测试。
  另注意 `SelfCheck` 会拦住"过于宽松"的 Schema —— 如果新 Schema 允许空对象通过，
  **启动会失败**（这是有意的）。
- **改降级逻辑**：`summarize()` 不应返回 error。降级是**正常路径**，不是异常。
- **加新的流式端点**：参考 `handler/preconsult.go` 的 `startStream` 模式。
- **真实 LLM 接入**：实现 `llm.Client` 接口 + 在 `factory.go` 注册即可，
  **service 层一行不用改**（这是抽象层的价值）。

---

## 6. M6 · 病历处方 / HIS 同步 / 审计

**状态**：✅ 完成

### 6.1 文件构成

```
internal/audit/audit.go          ★ 异步批量审计写入器 + 资源/动作推导
internal/middleware/audit.go     审计中间件（挂在链尾）
internal/repository/audit.go     audit_logs 批量插入
internal/service/audit.go + handler/audit.go   审计查询（admin）

internal/model/record.go         病历 + 处方明细
internal/repository/record.go    病历事务写入
internal/service/record.go       ★ 归属校验（authorize）
internal/handler/record.go

internal/service/access.go       ★ 共用访问者身份 Access{UserID, Role}

internal/mockhis/                模拟 HIS 数据源（his 契约的适配器）+ dataset.json
internal/his/                    ★ HIS 对接的数据契约（只放类型，不放接口）
internal/model/synclog.go
internal/repository/sync.go      三阶段 upsert
internal/repository/synclog.go
internal/service/sync.go         ★ 三阶段编排 + errgroup 限并发
internal/handler/sync.go

# M6 补全（医生工作台，见 §6.7）——复用 M4 的 appointment 文件，无新增文件
internal/model/appointment.go        + DoctorAppointmentItem / DoctorAppointmentFilter
internal/repository/appointment.go   + ListByDoctorCursor / ListAllCursor
internal/repository/doctor.go        + FindIDByUserID（从 MedicalRecordRepo 迁来）
internal/service/appointment.go      + ListForDoctor / validateAppointmentFilter
internal/service/preconsult_submit.go + authorizeReport
```

> **为什么有独立的 `internal/his` 包**：`SyncService` 的入参原本是
> `*mockhis.Dataset` —— 业务逻辑包依赖了一个名字里带 mock 的实现包，
> 于是"接真实 HIS 只需换实现"这句话是假的（真实适配器得 import 一个 mock 才能拿到类型）。
> 现在契约在 `his`，`mockhis` 只是它的一个适配器，并负责把数据文件里
> "相对今天 N 天"的写法换算成绝对日期，不再泄漏到业务层。
> 接口仍按本项目惯例由使用方 `service` 声明（见 `deps.go` 的说明）。
>
> **规律：接口签名里出现某个"实现方"的包名，就说明抽象放错了位置。**

### 6.2 审计：异步批量写入（一个必须明说的取舍）

| 方案 | 优点 | 代价 |
|---|---|---|
| 同步写 | 审计永不丢失 | 每个请求多一次 DB 往返，写接口延迟翻倍 |
| **异步写（已选）** | 请求路径不受 DB 影响 | 进程崩溃时缓冲区记录丢失 |

选异步 + 有界缓冲（`BufferSize=2048`）的理由：审计是**事后追溯**用途，
不在业务关键路径；丢失窗口被限制在 batch size（100）或 flush interval（500ms）之内，
且丢弃与失败**都有指标可告警**（`audit_entries_total{result="dropped"|"error"}`）。

**若合规要求零丢失**：把 `Record` 改成直接落库即可，其余代码不变。

**六个设计点**：

| 设计 | 理由 |
|---|---|
| 缓冲满时**丢弃而非阻塞** | 审计价值再高，也不值得让一次 DB 抖动演变成全站请求堆积 |
| `Entry` **刻意不含请求体** | 请求体可能含手机号、口令、病情描述 —— 复制进审计表等于把脱敏工作全部作废 |
| 资源/动作从 **`FullPath` 推导** | 用真实路径会让每个 ID 产生不同"资源名"，审计查询无法按资源聚合 |
| `trailingActions` 映射表 | 覆盖"末段是动作而非资源"的路由：`/appointments/:id/cancel` → action=`cancel`，否则会被记成笼统的 `create` |
| `scopeSegments` 跳过**权限域** | `/api/v1/admin/*` 的第 3 段是权限域而不是资源。曾直接取第 3 段，导致三个管理端点的 resource 全塌成 `admin`，按资源聚合时混成一条 |
| `Stop()` **必须幂等** | 启动失败回滚路径与 `main` 的 defer 都可能调用它；重复调用会 close 已关闭 channel 而 panic —— 恰恰在最需要优雅退出的时刻 |

> ⚠️ **这个 bug 曾被两个测试同时固化**：`audit_test.go` 断言 resource 是 `admin`，
> `http_test.go` 也查 `resource = 'admin'`。测试全绿，问题却一直在
> （见 `HANDOVER.md` §2.7-C）。**测试里出现"当前实现恰好返回的值"时，
> 要回代码确认那是不是正确值。**
>
> 另一个坑：`audit.truncate` 按**字节**截断会把中文字符切成两半，
> utf8mb4 列在严格模式下直接报错，**整批 100 条审计一起插入失败**。
> 现已回退到字符边界，并有专门用例守护。

### 6.3 病历：资源层归属校验（spec §2.2 双层校验的第二层）

```go
// service/record.go
func (s *RecordService) authorize(...) error {
    // 患者：只能看自己的
    // 医生：只能看自己接诊的
    return apperr.New(errcode.ErrNotOwner, "无权查看他人病历")
}
```

**这是路由层 RBAC 之外的必需一层**：`RequireRole(doctor)` 只保证"是医生"，
不保证"是**这位患者的**接诊医生"。spec §2.2 明确要求双层校验。

**同一套机制现在被三个 service 共用**，访问者身份统一为 `service.Access`：

| service | 校验函数 | 对应资源 |
|---|---|---|
| `RecordService` | `authorize` | 病历 |
| `PreconsultService` | `authorizeReport` | 预问诊报告 |
| `AppointmentService` | 无需（`ListForDoctor` 用 `doctorIDOf` 分流） | 预约列表 |

`Access` 之所以放在 `service` 包顶层（`access.go`）而不是某个模块文件里：
最初它叫 `RecordAccess` 并定义在 `record.go`，
但预问诊和预约列表也需要同一身份，放在任一模块下都会让别的模块产生
"要 import 病历模块"的错觉依赖。

**"由 `users.id` 解析 `doctors.id`" 的转换同样被收敛**：
从 `MedicalRecordRepo.FindDoctorIDByUserID` 迁到 `DoctorRepo.FindIDByUserID`。
它是医生档案的基本查询，三个模块都要用，挂在 `MedicalRecordRepo` 下是错的依赖方向
（会让人以为"查医生档案得先有病历仓储"）。

### 6.4 HIS 同步：三阶段而非一把梭

```
阶段 1：科室（无依赖）
阶段 2：医生（依赖科室）
阶段 3：排班（依赖医生 + 科室）
```

**为什么分阶段**：排班依赖医生、医生依赖科室。若并发处理全部实体，
排班会因为医生还没落库而**大量失败**。

**限并发 5**（spec §7.3，`errgroup.SetLimit`）：外部系统往往有连接数或 QPS 限制，
一次性打出全部请求容易被限流甚至封禁。

**`upsert by external_id`**：外部 ID 是同步锚点，保证重复同步不产生重复数据。

**排班的 `remaining` 不被重置**：已抢走的号源不能因为一次同步就"复活"。
集成测试 `TestSync_DoesNotResetRemaining` 专门守护这一点。

**其他集成测试守护的行为**：
`TestSync_ScheduleDatesAreFresh`（日期要滚到今天之后）、
`TestSync_DoctorPasswordsAreUniqueAndHashed`（不能所有医生共用口令）、
`TestSync_ChineseRoundTrips`（中文编码往返正确）。

### 6.5 端点

| Method | Path | 角色 |
|---|---|---|
| POST | `/api/v1/appointments/:id/record` | doctor |
| GET | `/api/v1/appointments/:id/record` | 任意登录角色（service 内再校验归属） |
| GET | `/api/v1/records` | 任意登录角色（游标分页） |
| GET | `/api/v1/doctor/appointments` | doctor / admin（**M6 补全**，见 §6.7） |
| GET | `/api/v1/appointments/:id/preconsult/report` | patient / doctor / admin（**M6 补全**，归属校验见 §6.3） |
| GET | `/api/v1/admin/audit-logs` | admin（游标分页） |
| POST | `/api/v1/admin/sync/:source` | admin |
| GET | `/api/v1/admin/sync-logs` | admin（游标分页） |

> ⚠️ 后三条的前缀是 **`/api/v1/admin/`**。早期版本的本文档与 `ARCHITECTURE.md`
> 都漏写了 `/admin/` 段，照那份清单调接口会得到 404 —— 代码一直是对的，错的是文档。

### 6.6 改动时注意

- **审计**：新增路由**无需改动**审计代码 —— `Describe()` 会自动从 `FullPath` 推导。
  但若新增的路由"末段是动作而非资源"（如 `/xxx/:id/approve`），
  需要往 `trailingActions` 加映射，否则会被记成笼统的 `create`。
- **病历**：任何新的病历读取入口都**必须**经过 `authorize`。
  路由层的 `RequireRole` 不检查资源归属。
- **同步**：新增实体类型时要考虑它依赖谁，插到正确的阶段。
  把一个依赖医生的实体放进阶段 1 会导致大量失败。
- **同步的 `remaining`**：**绝不能被同步覆盖**。这是 `TestSync_DoesNotResetRemaining`
  守护的不变量 —— 覆盖它意味着患者已抢到的号被"复活"，直接导致超卖。

### 6.7 医生工作台补全（M6 之后的一轮）

**背景**：M1–M6 全部标记"完成"之后，把 spec §2.1 的角色能力矩阵横过来复查了一遍，
发现医生侧有**两处真实缺口** —— 不是文档漏写，而是接口根本不存在：

| spec §2.1 要求 | 补全前 | 补全后 |
|---|---|---|
| 医生查看**本人**预约列表 | ❌ 只有患者视角的 `/appointments` | ✅ `GET /doctor/appointments` |
| 医生查看患者**预问诊报告** | ❌ 报告接口只对 patient 开放 | ✅ 开放给 patient / doctor / admin |

**这不是锦上添花，是规格缺口**：spec §8 的医生工作台没有这两个接口就搭不起来。

> **教训：模块维度全部打勾 ≠ 角色维度没有缺口。**
> 此前验收是逐模块自测的，所以 M4 自测通过、M5 自测通过，但"医生坐下来用一遍"这条路径
> 从来没有被完整走过。**收尾时应按角色走完整工作流，而不是按模块走。**

**四个设计决定**：

| 决定 | 理由 |
|---|---|
| 医生列表用**独立端点**而非复用 `/appointments` | 两者响应形态不同（患者关心"我约了哪位医生"，医生关心"这位患者是谁、病历写了没有"），合并会让两边都返回一半无用字段 |
| `DoctorAppointmentItem` 用**独立结构**而非复用 `AppointmentItem` | 同上；硬塞在一起会让 `omitempty` 与字段语义互相污染 |
| 依赖接口**独立声明**（`doctorAppointmentStore`） | 患者侧完全用不到这两个方法；混进 `appointmentStore` 会逼患者侧所有测试 fake 去实现它们 |
| `has_record` 用 **LEFT JOIN + `IS NOT NULL`** | INNER JOIN 会让"还没写病历"的预约整条消失 —— 恰好过滤掉医生最需要看的那些 |

**一个容易写错的细节**：`has_record` 的判定必须是 `mr.id IS NOT NULL`，
**不能**写成 `mr.id = 0`。LEFT JOIN 未匹配时 `mr.id` 是 `NULL`，
而 `NULL = 0` 求值为 `NULL`（既不真也不假），扫进 `bool` 会得到 `false` ——
**碰巧结果正确，但语义是错的**：一旦换成别的判定列或改成内连接就会出错。

**报告的归属校验**（`PreconsultService.authorizeReport`）分三个分支：

```
admin   → 直接放行（spec §2.1 全部读权限）
patient → appt.user_id   必须等于自己
doctor  → appt.doctor_id 必须等于本人 doctors.id（不是 users.id！）
```

缺了 doctor 分支，任何医生改一下 URL 里的 ID 就能读到全院患者的病情描述 ——
这是本轮补全里**唯一有真实隐私后果**的一处。

**新增测试**（都做过"改坏实现它会红吗"的自检）：

- **单元 +18**：`appointment_doctor_test.go`（角色分流、筛选白名单、无医生档案）、
  `preconsult_access_test.go`（管理员直通、患者本人/他人、接诊/非接诊医生、无档案）
- **集成 +11**：`test/integration/doctor_test.go` —— 含**非接诊医生拿到 40302**，
  且其接诊列表中看不到该患者的预约（两个端点交叉验证，避免只修一个漏一个）

---

## 7. 模块间的依赖关系

```
M1 基础设施（config / logger / middleware / response / errcode / apperr / metrics / traceid）
      ↓ 被所有模块使用
M2 认证（secret / jwt / authctx / user）
      ↓ 提供 user_id 与角色
M3 目录（pagination / cache / department / doctor / schedule）
      ↓ 提供 schedule 与 doctor_id
M4 抢号（stock / idempotency / appointment / scheduler）
      ↓ 提供 appointment 与号源
M5 AI（llm / prompts / conversation / report / summary_queue / worker）
      ↓ 依赖 appointment 校验归属
M6 医疗与同步（record / sync / audit）+ 医生工作台补全（见 §6.7）
```

**几个跨模块的耦合点**（改动时要一起看）：

| 耦合点 | 涉及模块 | 说明 |
|---|---|---|
| `repository.Cache` | M3 ↔ M5 | M5 的 `reconcile.go` 用 M4 的 `stockStore.CompareAndSet` |
| `appointmentStore` 接口 | M4 ↔ M5 | M5 用 `FindItemByID` 校验归属（SQL 层带 `user_id`，不泄露他人预约是否存在） |
| `doctorAppointmentStore` 接口 | M4 ↔ M6 | 医生/管理员视角的接诊列表读取能力，**与患者侧 `appointmentStore` 分开声明** |
| `doctorLookup` 接口 | M4 ↔ M5 ↔ M6 | 三个模块都要"由 `users.id` 解析 `doctors.id`"；实现在 `DoctorRepo.FindIDByUserID`，映射逻辑收敛在 `service/access.go` 的 `resolveDoctorID` |
| `his.Dataset` 契约 | M1 ↔ M6 | `service` 依赖 `his` 契约（**不依赖 `mockhis`**）；`mockhis` 只是 `his` 的一个适配器 |
| `service.Access` | M4 ↔ M5 ↔ M6 | 三个 service 共用的访问者身份；**改它的字段要同时改三处调用方** |
| `audit.Recorder` | M1 ↔ M6 | 中间件依赖最小接口，便于测试替换 |
| `app.Build()` | 全部 | **改任何模块的构造签名都要改这里**，集成测试复用同一套装配 |
| `metrics.Register` | 全部 | 各模块在 `init()` 注册自己的指标 |

---

## 8. 一句话回顾每个模块最该记住的事

| 模块 | 一句话 |
|---|---|
| **M1** | 中间件链顺序固定；JWT/RBAC 按路由挂载（探针要免认证）；错误码三张表要同步改 |
| **M2** | refresh_token 必须可撤销所以用 Redis 不用 JWT；防枚举要同时防文案与**时间** |
| **M3** | 禁止 OFFSET（284 倍差距）；手写 JOIN 必须带软删除条件；缓存回源要脱离调用方取消 |
| **M4** | 预热用 `SETNX`；键不存在返回 -1 不是 0；幂等键比对删除；对账用 CompareAndSet |
| **M5** | 流式用回调不用 channel；降级是正常路径不是异常；`{}` 是合法 Schema 所以要自检 |
| **M6** | 审计异步但有丢失窗口；HIS 同步分三阶段；排班 `remaining` 不被重置；**模块打勾 ≠ 角色无缺口，收尾要按角色横切复查**；**业务包不得依赖 mock 包** |

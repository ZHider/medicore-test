# MediCore Backend

Go 1.27 + Gin 单体后端。分层架构由 spec §3.2 强制约束。

## 目录结构

```
backend/
├── cmd/server/main.go        # 装配依赖、启动、优雅关闭
├── configs/config.yaml       # 配置（敏感项用环境变量覆盖）
├── internal/
│   ├── config/               # 配置加载与启动前校验（Viper）
│   │
│   ├── handler/              # 参数绑定+校验；禁止 SQL/GORM
│   ├── service/              # 业务逻辑、事务边界、归属校验；禁止 http.Context
│   ├── repository/           # 唯一允许 GORM 的层
│   ├── model/                # PO / DTO / 枚举
│   ├── middleware/           # TraceID / Recovery / RequestLog / Metrics / JWT / RBAC / RateLimit
│   ├── router/               # 路由与中间件链组装
│   │
│   └── apperr/ authctx/ errcode/ jwt/ logger/
│       mask/ response/ secret/ traceid/          # 无状态工具包
```

**依赖方向只能向下**：`handler → service → repository → model`。

> 说明：工具包直接平铺在 `internal/` 下，没有采用 `internal/pkg/` 分组。
> `internal` 本身已阻止外部导入，再加一层 `pkg` 是冗余层级。
> 这一处刻意偏离 spec §3.2，采用 Go 社区惯例。

**接口由使用方定义**：`service` 在 `deps.go` 中声明它所需的**最小**能力接口
（`userStore` / `tokenStore` / `healthChecker`），而不是依赖 `repository` 的具体类型。
因此 service 层单测可注入内存实现，完全不依赖 MySQL / Redis。

## 本地运行

```bash
# 1. 生成本地密钥（仅首次）
#    仓库中不含任何密钥，因此必须先执行这一步
go run ./cmd/genlocal

# 2. 起依赖（mysql + redis，首次自动执行 docs/schema.sql）
nub run deps:up

# 3. 起后端
MEDICORE_MYSQL_PASSWORD=medicore123 go run ./cmd/server
# Windows PowerShell: $env:MEDICORE_MYSQL_PASSWORD='medicore123'; go run ./cmd/server
```

## 密钥管理

**仓库中不保存任何密钥**，包括开发用密钥——密钥扫描工具不会区分"仅开发"与否。

| 环境 | 密钥来源 |
|---|---|
| 本地开发 | `go run ./cmd/genlocal` 生成 `configs/config.local.yaml`（已被 `.gitignore` 排除，启动时自动合并） |
| 生产 | 环境变量 `MEDICORE_SECURITY_AES_KEY` / `_PHONE_PEPPER` / `_JWT_SECRET` |

`cmd/genlocal` 默认拒绝覆盖已有文件：重新生成会换掉 `aes_key`，
导致数据库中已加密的手机号/身份证**永久无法解密**，需要显式加 `-force`。

启动时会校验密钥（`internal/config/validate.go`），按**特征**而非黑名单判断可预测性：
单一字符重复、短周期重复、连续升序序列、含 `change-me`/`example` 等占位词，一律拒绝启动。
用特征而非黑名单，是因为黑名单既拦不全，本身又会把密钥字面量带回仓库。

> 注意：`.gitignore` 只能防住"未来"，防不住"已经提交过"。
> 若密钥曾进入历史，必须改写历史（本仓库用 `git commit --amend` 处理过），
> 且**已泄露的密钥应视为失效**，即使历史被清理也要轮换。

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `MEDICORE_CONFIG` | `configs/config.yaml` | 配置文件路径 |
| `MEDICORE_APP_ENV` | `dev` | `prod` 时 gin 切换 ReleaseMode |
| `MEDICORE_APP_PORT` | `8080` | 监听端口 |
| `MEDICORE_LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |
| `MEDICORE_MYSQL_HOST` / `_PORT` / `_USER` / `_PASSWORD` / `_DATABASE` | — | 数据库连接 |
| `MEDICORE_REDIS_ADDR` / `_PASSWORD` / `_DB` | — | Redis 连接 |
| `MEDICORE_SECURITY_AES_KEY` | — | 32 字节 hex，AES-256-GCM 主密钥 |
| `MEDICORE_SECURITY_PHONE_PEPPER` | — | 手机号/身份证摘要的 HMAC pepper |
| `MEDICORE_SECURITY_JWT_SECRET` | — | JWT 签名密钥 |

## 端点

### 运维（M1）

| Method | Path | 说明 |
|---|---|---|
| GET | `/healthz` | 存活探针，不探测外部依赖 |
| GET | `/readyz` | 就绪探针，探测 mysql + redis，任一故障返回 503 |
| GET | `/metrics` | Prometheus 指标 |

### 认证（M2）

| Method | Path | 鉴权 | 说明 |
|---|---|---|---|
| POST | `/api/v1/auth/register` | 公开 | 患者注册（仅 patient 角色，防止自助提权） |
| POST | `/api/v1/auth/login` | 公开 + **限流 5/分钟** | 返回双令牌 |
| POST | `/api/v1/auth/refresh` | 公开 | 刷新令牌，**一次性轮换** |
| POST | `/api/v1/auth/logout` | 需登录 | 撤销刷新令牌 |
| GET | `/api/v1/auth/me` | 需登录 | 返回本人脱敏资料 |

### 业务查询（M3）

| Method | Path | 说明 |
|---|---|---|
| GET | `/api/v1/departments` | 科室列表（启用中） |
| GET | `/api/v1/departments/:id/doctors` | 科室下医生列表，**Redis 缓存** |
| GET | `/api/v1/departments/:id/schedules?date=&cursor=&size=` | 按科室+就诊日查询号源，游标分页 |
| GET | `/api/v1/doctors/:id` | 医生详情（含所属科室，多对多） |
| GET | `/api/v1/doctors/:id/schedules?from=&to=&cursor=&size=` | 医生排班，游标分页 |

全部需要登录（院内数据不应匿名可见）。

### 预约与抢号（M4）

| Method | Path | 角色 | 说明 |
|---|---|---|---|
| POST | `/api/v1/appointments` | patient | 抢号（幂等键 + Lua 扣减），**限流 10/分钟按用户** |
| GET | `/api/v1/appointments?cursor=&size=` | patient | 我的预约，游标分页 |
| POST | `/api/v1/appointments/:id/cancel` | patient | 取消并回补号源 |
| POST | `/api/v1/appointments/:id/checkin` | patient | 签到，按签到顺序分配就诊序号 |
| GET | `/api/v1/schedules/:id/queue` | doctor/admin | 候诊队列（按序号升序） |

### AI 预问诊（M5）

| Method | Path | 角色 | 说明 |
|---|---|---|---|
| POST | `/api/v1/appointments/:id/preconsult/chat` | patient | **SSE 流式对话** |
| GET | `/api/v1/appointments/:id/preconsult/history` | patient | 取回进行中的对话（刷新页面后恢复上下文） |
| POST | `/api/v1/appointments/:id/preconsult/submit` | patient | **提交并触发异步总结**，返回 202 |
| GET | `/api/v1/appointments/:id/preconsult/report` | patient | 查询结构化总结与处理状态 |

### RBAC

`middleware.RequireRole(model.RoleAdmin, ...)` 挂在 JWT 之后使用。
注意这只是**路由层**校验；资源归属仍必须由 service 层校验（spec §2.2 双层校验）。

## 安全设计要点（M2）

| 关注点 | 做法 |
|---|---|
| 手机号存储 | AES-256-GCM，密文 `nonce(12B)‖ct‖tag`；接口输出统一 `138****5678` |
| 手机号查找 | HMAC-SHA256(pepper, phone) **确定性**摘要，命中 `uk_phone_hash` 索引 |
| 口令 | bcrypt cost≥10；显式拒绝 >72 字节输入（避免静默截断造成的安全错觉） |
| 防用户枚举 | 登录失败统一返回"手机号或密码错误"，不区分账号是否存在 |
| 防时序枚举 | 账号不存在时执行**等价 bcrypt 比对**，避免用响应时间区分账号是否存在 |
| 生产密钥守卫 | `APP_ENV=prod` 时若密钥为空或仍是开发默认值，**拒绝启动** |
| access_token | JWT HS256 / 2h，显式限定算法（防 `alg=none` 与算法混淆） |
| refresh_token | 不透明随机串 256 位 / 7d 存 Redis；**必须可撤销**，故不用 JWT |
| 刷新轮换 | `GETDEL` 原子消费，旧令牌立即失效，杜绝重放窗口 |
| 限流 | Redis 令牌桶 Lua 脚本（原子）；限流器故障时 fail-open 并留痕 |
| 错误包装 | `apperr` 携带错误码向上传递，handler 统一转换（spec §3.2 铁律 2） |

## AI 预问诊设计（M5a）

### 为什么流式接口用回调而不是 channel

`llm.Client.ChatStream(ctx, req, onDelta)` 是回调式，而不是返回 `<-chan Chunk`：

channel 方案必须额外起一个 goroutine，并在**成功、出错、取消、调用方提前退出**
这四条路径上都保证关闭，遗漏任何一条就是 goroutine 泄漏
（spec §7.2 专门要求处理 `ctx.Done()` 正是为了防这个）。
回调方案由调用方在自己的 goroutine 里驱动，不存在额外的生命周期问题。

### 错误响应的形式取决于流是否已开始

| 阶段 | 响应形式 |
|---|---|
| 流未开始（参数/权限/预约状态校验失败） | 统一 JSON 信封，前端仍可按 `code` 分支 |
| 流已开始（上游中途失败） | SSE `error` 事件 —— HTTP 状态码早已发出，改不了 |

`startStream()` 刻意延迟到第一次真正要吐数据时才写响应头，
这样前置校验失败还能用信封，而不是先发 200 再报错。

### 对话存在 Redis 而非直接写报告行

- 每轮都要追加，写 `ai_pre_reports.conversation` JSON 列是"读-改-写"，多标签页会互相覆盖；
- 报告行应在**提交**时才产生，避免堆积大量 `status=pending` 的空壳记录。

键 `preconsult:conv:{appointmentID}`，TTL 24 小时，上限 40 条防内存打满。

### 失败时不保存半截回复

上游中途失败时**只保留用户消息**，不写入残缺的助手回复——
留下半截会让下一轮模型看到不完整的上下文，用户重试时反而更混乱。
用户消息先落库，保证重试时不必重新输入。

### 提示词版本化

`prompts/preconsult_v1.md` 用 `go:embed` 编进二进制。
好处是不会有"部署时漏拷 prompts 目录"的环境差异，版本号也与代码版本绑定。
`PromptVersion()` 会随报告入库以便追溯（spec §7.2）。

### Mock 的故障注入不是玩具

`llm.mock.summary_mode` 与 `stream_error_at` 让 G3 的验收标准
（「100% 通过 Schema 校验**或**优雅降级」）可以被真正验证——
没有故障注入就只能测到成功路径，降级分支永远是未经验证的代码。

配置真实上游（openai/dashscope/deepseek）会在**启动时明确报错**而不是静默回退到 mock：
生产环境里"以为接了真模型、其实返回的是假数据"比直接挂掉危险得多。

### 实测

```
SSE 流式：13 个 delta 事件以约 20ms 间隔逐段到达，末尾 done 事件，总耗时 277ms
故障注入：3 个 delta 后收到 error 事件 code=50201；历史中只留用户消息，无半截回复
刷新恢复：GET /preconsult/history 返回完整 4 条对话
```

## AI 结构化总结设计（M5b）

### 为什么异步（决策 #8）

`POST /preconsult/submit` 立即返回 **202 Accepted**，总结在 Redis Stream 队列里跑：

- 用户提交后很可能**刷新或关闭页面**——刷新不等于取消任务，同步等待会让总结随连接一起消失；
- LLM 有速率限制，队列天然提供背压与削峰；
- 总结耗时数秒到数十秒，同步会拖垮接口的 P99。

### 校验与降级（spec §7.2）

```
调用 Complete → 提取 JSON → Schema 校验
     ↓ 失败
   追加纠正指令，重试 1 次
     ↓ 仍失败
  降级：structured_json = null，fallback = true，保留原始对话供医生查看
```

**提取 JSON 做了三级尝试**：整体是 JSON → 剥 Markdown 围栏 → 取首个 `{` 到末个 `}`。
真实模型经常在 JSON 外面包一层说明文字或 ` ```json ` 代码块，直接反序列化会失败。
刻意不做更花哨的解析：混乱到这三步都救不回来时，正确做法是降级让医生看原始对话，
而不是在解析器上继续堆代码。

**降级不是流程失败**：`summarize()` 不返回 error，所有失败都通过 `Fallback + Reason` 表达——
医生应该看到原始对话，而不是一片空白。

### Schema 自检：防止校验静默失效

`{}` 是**合法**的 JSON Schema，含义是"任何内容都通过"。如果 Schema 文件被误改成空对象
（或必填字段被删光），编译会成功、每次校验都通过，于是降级路径永不触发、
垃圾数据被当成结构化结果入库，**而且没有任何报错**。

`SummaryValidator.SelfCheck()` 用空对象做反向验证，把这个陷阱变成启动即失败。

### Stream worker 的至少一次投递

| 机制 | 作用 |
|---|---|
| 消费者组 + `XREADGROUP BLOCK` | 有任务立即醒来，不靠轮询空转 |
| **不确认失败的**消息 | 留在 pending，等待 `XAUTOCLAIM` 重新认领 |
| 启动时 + 周期性 `ClaimStale` | 认领崩溃 worker 遗留的任务；没有它那些报告会永远停在 processing |
| 投递次数 ≥ 3 → 死信队列 | 避免一条毒消息无限重投堵住主队列 |
| 死信时同时标记报告失败 | 否则报告永远停在 processing，对客户端与"正在处理"无法区分 |
| `MAXLEN ~ 10000` | 防队列无上限增长吃光 Redis 内存 |

重试次数取自 **Redis 的投递计数**（`XPENDING`）而非报告行的 `attempt_count`：
后者只在成功抢占后递增，"抢占前就失败"（如数据库连接断了）也需要计数，
否则那类失败会永远重试下去。

### 实测（降级路径，`summary_mode=invalid_json`）

```
202 响应            : trace_id 已带上
status              : done, fallback=true
structured          : null（已降级）
error_message       : 结构化输出未通过校验（尝试 2 次），已降级为原始对话
conversation        : 2 轮对话保留，医生端可查看
DB                  : fallback=1, attempts=2, structured_json IS NULL
Redis Stream        : XPENDING=0（全部确认）, 死信队列=0
```

正常路径（`summary_mode=valid`）：提交后 **1 秒内** 完成，返回完整结构化 JSON，
含 `prompt_version=v1` 与 `llm_model` 便于追溯。

## 抢号设计（spec §7.1，含两处修正）

```
幂等占用 → 预热号源 → Redis 原子扣减 → MySQL 事务落库 → 记录幂等结果
                                    ↓ 失败
                              回补 Redis 号源 + 释放幂等键
```

**修正 1：Redis 键不存在不再等同于"号源不足"。** spec 的原始 Lua 在键不存在时返回 0，
而 0 表示号源不足——这会让 Redis 冷启动或键过期后的**所有**抢号全部失败，
且错误提示是"号源不足"，排查时极具误导性。现改为返回独立的 `-1`，
服务层据此先预热再重试一次。

**修正 2：幂等键用"比对删除"释放。** 失败清理时若直接 `DEL`，
会把已完成的记录一并删掉——请求 A 成功后请求 B 超时清理，
一次无条件 DEL 就让后续重放再也查不到结果。改为仅当值仍是"处理中"占位值时才删除。

其他关键点：

- **预热必须用 `SETNX`。** 若用 `SET` 覆盖已存在的键，会把并发请求已扣减的号源"还回去"，直接造成超卖。
- **回补以容量为上限，且键不存在时不创建。** 否则 `INCRBY` 会凭空造出值为 qty 的键，制造超卖。
- **MySQL 扣减用单条原子 UPDATE**（`WHERE id=? AND remaining>0`）而非"读 version 再比对"。
  单条 UPDATE 在 InnoDB 行锁下已是原子，`remaining>0` 即充分条件；后者要多一次 SELECT 加重试循环。
  表上的 `CHECK (remaining >= 0)` 是最后一道防线。
- **超时释放是幂等闸门。** 用"带状态条件的 UPDATE"（`WHERE status='pending'`）判断是否真的发生了状态跃迁，
  多实例并发扫描同一批订单时只有一个会回补号源。
- **事务内不含网络调用**（spec §3.2 铁律 3）：Redis 扣减在进入事务之前完成。

### 候诊队列刻意不脱敏

队列会返回患者姓名原文。医生必须能识别面前的患者，把姓名打成"张*"会让队列失去意义；
该接口仅对 doctor/admin 开放。这是 spec §2.2 脱敏规则的**有意例外**。

### 号源对账（兜底一致性）

抢号是"先扣 Redis 再写 MySQL"。若进程在两步之间崩溃、或失败后回补 Redis 也失败，
两边就会产生偏差。`ReconcileStock` 按如下规则以 MySQL 为准校准 Redis：

| 情况 | 处理 |
|---|---|
| Redis == MySQL | 无需处理 |
| **Redis < MySQL** | 修正。典型偏差（回补失败/崩溃），表现为"明明有余号却抢不到" |
| **Redis > MySQL** | 修正。超卖风险方向，必须向下压回 |
| Redis 键不存在 | **跳过**。属于尚未预热或已自然过期，不是偏差；若当成 0 去写，会把本可从数据库加载的号源压死 |

**修正必须用比对更新（`CompareAndSet`）而不是无条件 `SET`。**
对账读取 MySQL 与写入 Redis 之间存在时间窗，无条件覆盖会把并发请求刚扣掉的号源"还回去"，
反而制造超卖。比对失败就放弃本次修正，交给下一轮——偏差会多存在一会儿，但绝不会因此多放号。

周期由 `scheduler.stock_reconcile` 配置（默认 5 分钟）。
每次修正都会打 Warn 日志并累加 `stock_reconcile_total{result="corrected"}`，可直接配置告警。

实测（把对账周期临时调成 10 秒）：

```
Redis < MySQL : 1  -> 0    （3 秒内修正）
Redis > MySQL : 26 -> 19   （6 秒内修正）
键不存在      : 未被误创建
stock_reconcile_total{result="corrected"} 2
```

## 并发压测

```bash
nub run backend:loadtest -- -users 200 -concurrency 100
```

**为什么不直接用 hey/wrk**：这两个工具对所有请求发同一个 token，
而同一用户对同一号源只能持有一个进行中预约（`uk_active`），
重复请求只会得到冲突，根本触达不到并发扣减路径。
`cmd/loadtest` 先批量准备患者账号、为每个账号签发自有权杖，再并发抢同一个号源。

实测结果（200 患者抢 20 个号源，并发度 100）：

```
成功抢到      : 20
号源不足      : 180
耗时          : 172ms
MySQL 余号    : 20 -> 0
Redis 号源键  : 存在=true 值=0
✅ 无超卖：成功次数、MySQL 余号、Redis 号源三者自洽
```

工具结束时会自动核对三方数据（成功次数 / MySQL remaining / Redis 号源键）是否自洽，
任何一项对不上都会以非零退出码失败。

## 依赖与安全

### 依赖面偏大（已知并接受）

```
直接依赖 15 个（含仅测试用的 miniredis / testify）
编译进二进制的包 459 个
二进制体积 ~32 MB（linux/amd64，-s -w）
```

Gin v1.12 会**传递引入两个本项目完全用不到的重型模块，且都真的被编译进二进制**：

| 模块 | Gin 中的用途 | 我们的实际需要 |
|---|---|---|
| `go.mongodb.org/mongo-driver/v2` | BSON 绑定 | 不需要（只用 Redis） |
| `github.com/quic-go/quic-go` | HTTP/3 | 不需要（只提供 HTTP/1.1 + SSE） |

Gin 内部直接 import，无法用 build tag 关闭。这既解释了二进制体积，
也意味着**攻击面被无意中放大**——`quic-go` 的历史漏洞会出现在我们的扫描结果里。
换路由框架的代价远大于收益，因此接受现状，但必须靠扫描持续盯住。

### 漏洞扫描（spec §9.3）

```bash
nub run backend:vuln      # 需先 go install golang.org/x/vuln/cmd/govulncheck@latest
```

首次扫描发现 6 个漏洞，**修复 5 个**：`quic-go` 1 个、`edwards25519` 1 个、
`x/crypto` 4 个中的 3 个。其中 **`x/crypto` 是直接依赖**（bcrypt 就在里面），
不是"别人的问题"。

残留 1 个：`GO-2026-5932`（`x/crypto`，**官方无修复版本**，且我们的代码未调用该路径），
只能接受并持续监控。

**建议把 `backend:vuln` 纳入提交前检查**——本次是手工跑才发现这些漏洞的。

## 模块进度

- [x] **M1** 配置 / 日志 / 中间件链 / 健康检查 / 分层骨架 / dev compose
- [x] **M2** 认证鉴权：AES-GCM + HMAC 摘要 + bcrypt + JWT 双令牌 + Redis 刷新轮换 + RBAC + 令牌桶限流
- [x] **M3** 科室 / 医生（多对多）/ 排班查询 + 游标分页 + Redis 缓存（含缓存击穿防护）
- [x] **M4** 抢号（幂等 + Lua 原子扣减 + 预热自愈 + 乐观条件 + cron 超时释放 + 签到队列）
- [x] **M4.5** 号源对账（以 MySQL 为准校准 Redis，比对更新防覆盖）
- [x] **M5a** AI 预问诊对话（LLM 抽象 + Mock 故障注入 + 提示词版本化 + SSE 流式 + Redis 对话存储）
- [x] **M5b** AI 结构化总结（Schema 校验 + 重试降级 + Redis Stream 异步 worker + 死信）
- [ ] M6 病历 / 处方 / 审计 / HIS 同步

## 演示数据

```bash
go run ./cmd/seed      # 幂等，可反复执行
```

写入 4 个科室、12 名医生（含科室关联）、每人 7 天 × 上午/下午的排班（共约 168 条）。
执行完会**清除科室医生缓存**，否则此前缓存下的空列表会掩盖新数据直到 TTL 过期。

## 游标分页

所有列表接口统一使用游标分页，**禁止 OFFSET**（spec §5.2）：

```
GET /api/v1/departments/1/schedules?date=2026-09-12&size=2
→ { "items": [...], "next_cursor": 57, "has_more": true }
GET ...&cursor=57
```

- 仓储层固定查询 `limit+1` 条，多出的一条只用于判断 `has_more`，**不发额外 COUNT**。
- `size` 上限 100：否则 `size=999999` 会被当作合法请求，等价于一次全表扫描。
- 实测对比（23 万行）：`OFFSET 100000` 耗时 137ms、扫描 20 万行；游标 0.483ms、恒定 20 行。

## 缓存设计

科室医生列表走 Redis 缓存（spec §6.1），键 `cache:dept_doctors:{id}`，TTL 5 分钟。

- **缓存击穿防护**：`repository.Load` 用 `singleflight` 合并并发回源。
  热点键失效瞬间若有大量请求同时未命中，各自回源会把压力全打到 MySQL。
- **降级不静默**：缓存读写失败都不阻断请求（退化为每次回源），但一律记日志，
  否则缓存长期失效也无人察觉。
- **脏数据自愈**：反序列化失败按未命中处理并删除该键，避免错误反复出现。
- **显式失效**：写路径调用 `InvalidateDepartmentCache`，不只依赖 TTL。

## 测试

```bash
nub run backend:test      # go test ./...
```

152 个用例，service 层不依赖任何外部服务（注入内存实现）：

| 包 | 覆盖内容 |
|---|---|
| `secret` | 加解密往返、随机 nonce、篡改检测、摘要确定性、bcrypt 边界 |
| `jwt` | 签发解析、过期、错密钥、错签发方、**alg=none 伪造拒绝** |
| `mask` | 手机号/身份证/姓名脱敏，且结果不等于原文 |
| `config` | 密钥非空/长度/可预测性（单一字符、短周期、升序、占位词） |
| `model` | 游标解析、size 收敛、分页标志与探测行裁剪 |
| `middleware` | JWT 缺失/非法/过期三态、RBAC 放行与拒绝、无令牌优先 401 |
| `llm` | mock 流式分片不切断多字节字符、ctx 取消即停、onDelta 失败即停、故障注入、四种总结模式、上游未实现时明确报错 |
| `repository` | 缓存回源不受调用方取消失效、命中后不再回源、脏数据自愈、回源有独立超时上界 |
| `worker` | 成功即确认、未超限失败不确认（留给 XAUTOCLAIM 重投）、超限转死信并标记报告失败、消息体损坏丢弃、投递次数未知时保守重试、Stop 幂等 |
| `service` | 认证全流程 + 时序防护；目录查询；**抢号全部分支**（幂等重放、在途拒绝、号源不足、预热重试、数据库失败回补、唯一键恢复、排班不存在/已关闭）；**对账**（双方向修正、键不存在跳过、并发修改时放弃、分批扫描、单条失败不中断）；**预问诊**（空/超长消息、归属与状态校验、上游错误映射、失败不留半截回复、历史累积） |

抢号部分最关键的用例是 **`TestGrab_DatabaseFailureReleasesRedisStock`**：
它断言"Redis 已扣减但 MySQL 回滚"时必须回补号源。若不回补，
号源会凭空蒸发，表现为"总号源 20 却越来越难抢"这种极难排查的现象。

时序防护用例经过反向验证：临时移除防护后该用例失败（比值 0.00），确认其确实能捕获回归。


## 中间件链顺序（spec §3.1，固定）

```
TraceID → Recovery → RequestLog → Metrics → [RateLimit → JWT → RBAC]
```

方括号内三段在后续模块接入，且 **JWT/RBAC 挂在业务路由组而非全局**——探针与 `/metrics` 必须免认证。

## 已实现的行为约定

- **统一响应信封**：`{code, message, data, trace_id}`，成功 `code=0`。
  未匹配路由返回 40003，方法不支持返回 40006，二者同样是 JSON 信封（非 gin 默认纯文本）。
- **TraceID**：入口生成 32 位十六进制；上游带 `X-Trace-Id` 则沿用；同时写响应头与响应体。
- **日志**：Zap JSON，字段 `trace_id/method/path/status/latency_ms/client_ip`；5xx 走 Error 级别便于告警。
- **指标**：`http_requests_total{method,path,status}`、`http_request_duration_seconds{method,path}`。
  `path` 用路由模板而非真实路径，避免高基数。
- **优雅关闭**：SIGINT/SIGTERM 触发，10 秒超时。

业务模块可用 `middleware.Registry()` 注册自定义指标（如 `appointment_grab_total`、`llm_calls_total`）。


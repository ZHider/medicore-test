# 04 · M4 —— 抢号（全项目最复杂的模块）

| | |
|---|---|
| **对应提交** | `4a72f1d`；对账与清死代码 `0cca626`；缺陷修正在 `6b23e57`、`dddd194` |
| **规模** | 7 文件 / 1358 行（`MODULES.md` §4） |
| **涉及包** | `repository/stock`（Lua）`repository/idempotency` `repository/appointment` `service/appointment` `service/reconcile` `scheduler` `handler/appointment` |
| **本篇性质** | **事后整理**。来源：`MODULES.md` §4、`HANDOVER.md` §2.1-G/§2.2/§2.7-B,D/§2.8、`FIXES.md` H2、`docs/sql-optimization.md`；§三⑥ 为本次新补查 |
| **结论** | 交付达成；**本模块贡献了全项目唯一的 HIGH 级信息泄露与唯一的并发 flaky**；spec 在此有 **3 处设计缺陷** |

---

## 一、需求拆解

| spec 出处 | 要求 | 落地 |
|---|---|---|
| §7.1 | 幂等检查 `SETNX` | `repository/idempotency`（三态：占用 / 已完成 / 处理中） |
| §7.1 | Redis Lua 原子扣减 | `repository/stock`（`EVALSHA` 单次往返） |
| §7.1 | MySQL 事务落库 + 乐观锁 | `AppointmentRepo.Create`（**实际用单条原子 UPDATE，见 §二**） |
| §7.1 | 补偿：Lua 回补 + 删幂等键 | `releaseStockForRedis` + 比对删除 |
| §7.1 | 过期释放：cron 每分钟扫 `expire_at < NOW()` | `scheduler` → `ReleaseExpired` |
| §9.3 | 抢号限流 10 次/分钟 | `middleware.RateLimit`，**按用户而非按 IP** |
| §9.1 | 100 并发零超卖 | `cmd/loadtest` 实测 |

**spec 在此有 3 处设计缺陷（均已修正）**：

| # | spec 的问题 | 后果 | 修正 |
|---|---|---|---|
| 1 | Lua 在**键不存在**时返回 `0`（=号源不足） | Redis 冷启动/键过期后**所有抢号全部失败**，且提示"号源不足"极具误导性 | 返回独立的 `-1`（`StockNotLoaded`），调用方先预热再重试 |
| 2 | 幂等键失败清理用**无条件 `DEL`** | 会把**已完成**的记录一并删掉，后续重放查不到结果 → **幂等失效** | 改为 Lua **比对删除**（只删仍处于"处理中"的键） |
| 3 | 排班唯一键 `(doctor_id, sched_date, time_slot)` | 医生-科室改为多对多后，**专家无法跨科室排班**（同一天同一半天只能出一个科室的号） | 唯一键加入 `department_id` |

---

## 二、方案选型

| 决策点 | 被否决的方案 | 选定 | 理由 |
|---|---|---|---|
| 号源预热 | `SET` | **`SETNX`** | `SET` 会**覆盖并发已扣减的值** → 号源"还回去" → **超卖** |
| MySQL 落库的并发控制 | 读 `version` → 比对 → 写回 | **单条原子 `UPDATE ... WHERE remaining > 0`** | InnoDB 行锁下 `remaining > 0` 即是充分条件；省一次 SELECT 且无需重试循环 |
| `version` 字段 | spec 表里定义了它 | **保留但不使用** | 单条原子 UPDATE 已足够；保留字段以便将来改为"读取-计算-写回"（如按比例分配号源）时启用 |
| 幂等重放的响应 | 重新创建 | **返回首次创建的预约** | 保证响应结构与首次创建**完全一致** |
| 候诊号序 | 按预约创建顺序 | **按实际签到顺序** | spec 决策；用 `checked_in_at` + `FOR UPDATE` 锁排班串行化 |
| 限流维度 | 按 IP | **按用户（JWT 已执行）** | 同一 NAT 后的多名患者不应互相影响 |
| 对账更新 | 无条件 `SET` | **`CompareAndSet`（比对更新）** | 无条件 `SET` 会**在对账过程中制造超卖** |

---

## 三、§10.2 检查清单

### ① 分层与架构

- **Lua 脚本全部在 `repository/stock.go`**，`service` 只见 `Decrement/Release/CompareAndSet/EnsureLoaded` 四个语义化方法。
- 事务边界在 `service`；**事务内无网络调用**（Redis 扣减在进入事务之前完成）。这一条在本模块是硬约束，因为抢号是全项目唯一"Redis + MySQL 双写"的路径。
- 状态机（`pending/confirmed/cancelled/expired`）的判定收敛到 `model.IsActive*`，避免各处内联条件（曾经 `IsActive` 与内联条件各写一份，加新状态时必漏改一处）。

**判定：通过。**

### ② 错误与并发

| 检查项 | 情况 |
|---|---|
| 错误包装 | ✅ 但**曾有一处违反**：`recoverDuplicate` 把**任意错误**当成"重复抢号"报 409 —— 数据库抖动被说成"您已预约"，真实原因既没包装也没日志。已改为 `errors.Is(err, ErrNotFound)` 分支 + 其余走 `apperr.Wrap` |
| goroutine 退出路径 | cron 任务受 `scheduler` 的停止信号控制；`ReleaseExpired` 单条失败**不中断整批**（否则一条坏数据会永久阻塞清理任务） |
| 共享状态 | 并发正确性**不依赖 Go 侧锁**，而是靠 Redis 单线程 + InnoDB 行锁 —— 这是本模块的核心设计选择 |
| 多实例并发 | 过期释放用"带状态条件的 UPDATE"作幂等闸门：只有仍为 `pending` 时才命中，因此多实例同时扫到同一批订单**不会重复回补号源** |

**判定：通过（含一处已修的错误分类缺陷）。**

### ③ 性能与 SQL

**本节数字来自 `docs/sql-optimization.md` 的实测**：

| 查询 | 选用索引 | 实际读取行数 | 热态耗时 |
|---|---|---|---|
| Q24 号源扣减 UPDATE | `PRIMARY` | ~1 | — |
| Q3 医生接诊列表 | `idx_doctor_cursor` | 21 | 0.595 ms |
| Q7 候诊队列 | `uk_sched_queue` | 20 | — |
| Q6 cron 扫描超时 pending | `PRIMARY`（正向） | **4,000**（拿 200 条） | 1.48 ms |

**两项判定**：

- **Q7 候诊队列走 `uk_sched_queue (schedule_id, queue_no)`** —— 既是唯一键又天然提供 `ORDER BY queue_no`，
  一次索引查找同时满足约束与排序。这是"唯一键当查询索引用"的正确姿势。
- **Q6 没有用 `idx_status_expire`**（`possible_keys` 里有它，但选了 `PRIMARY`）：
  因为 `ORDER BY id ASC` 与 `idx(status, expire_at)` 的列序冲突。
  **代价 ≈ `LIMIT ÷ pending占比`** —— 当前 pending 占 5%，读 4000 行拿 200 条；
  若 pending 占比降到 0.1% 就会退化成扫 20 万行。
  已在 `sql-optimization.md` §4.4 写明触发条件与改法（`ORDER BY expire_at`）。

**Redis 侧**：扣减是**单次 Lua 往返**。基准实测扣减 419 µs ≈ 裸 `PING` 422 µs
⇒ **Lua 开销可忽略，时间全在传输上**。这也是为什么"把 Lua 拆成多次调用"是纯损失：
往返次数翻倍，且**破坏原子性** → 直接打开超卖的口子。

### ④ KISS 原则

| 出现的"多余" | 判定 |
|---|---|
| 5 个 Lua 脚本（扣减 / 回补 / 比对释放 / 比对更新 / 预热） | **必要** —— 每个都是一次原子操作，合并会破坏原子性 |
| `StateChangeResult` 携带 `ScheduleTotal` | **必要** —— 让 service 不必再查一次排班就能回补 Redis 号源 |
| 幂等键的**三态**而非两态 | **必要** —— "处理中"与"已完成"必须区分，否则无法实现安全的比对删除 |
| `version` 字段（**从不使用**） | ⚠️ **可议**。保留的理由已写进注释（将来改用读-算-写时启用）。当前属"为将来保留的旋钮"，与本项目"删掉它会怎样"的原则相抵触 —— 但它是 spec 表结构的一部分，删列影响面更大，故保留并注明 |

### ⑤ 基础卫生

| 检查项 | 情况 |
|---|---|
| 输入校验 | `schedule_id` 必填；幂等键缺失时**服务端补一个 UUID**（仍能防"同一次提交被重放"） |
| 敏感数据脱敏 | 患者预约列表脱敏；**候诊队列刻意不脱敏**（医生要叫号）—— 这条例外**必须**配归属校验，见 §四第 1 条 |
| 硬编码 | `expiredBatchSize = 200`、`reconcileBatchSize = 500` 是常量且有注释；无密钥 |

### ⑥ AI 幻觉

本次新补查（方法见 `README.md`）。M4 是**第三方 API 用得最"野"的模块**，
也是幻觉风险最高的地方：

| API | 存在性 | 语义核对结果 |
|---|---|---|
| `redis.NewScript` / `Script.Run` / `EvalSha` | ✅ | 用法正确：`Run` 内部先 `EVALSHA` 失败再 `EVAL` 回退 |
| `redis.SetArgs{KeepTTL: true}` | ✅ | **语义坑**：不设它时 `SET` 会**清掉 TTL** —— 这正是本模块踩过的缺陷（§四第 2 条） |
| `redis.ZAddArgs` 之外的 `INCRBY` 语义 | ✅ | 键不存在时 `INCRBY` **会创建键** → 凭空造出号源。已在 Lua 里加"键不存在不创建"的守卫 |
| `redis.Nil` 与 Lua 返回值的区分 | ✅ | Lua 返回的 `-1` 与 `redis.Nil` 是两回事，代码分开处理，正确 |
| `errgroup.SetLimit`（对账用） | ✅ | 见 M6 篇 |
| `clause.Locking{Strength: "UPDATE"}` | ✅ | 用于 `Cancel`/`CheckIn` 的 `FOR UPDATE`，用法正确 |

**结论：0 个幻觉符号；1 个语义坑（`SET` 清 TTL）已在真实缺陷中出现并修复。**

---

## 四、本模块暴露的真实缺陷

### 1（HIGH）· 候诊队列没有资源层归属校验 —— 信息泄露

`GET /schedules/:id/queue` 返回**未脱敏**的患者姓名与 `user_id`（这是**有意的**：
医生要能叫号），但它**只挂了 `RequireRole(doctor)`**，service 层完全没有归属校验。

**后果：任何医生换一个排班 ID，就能枚举全院患者的姓名与 `user_id`。**

修复：`Queue(ctx, access, scheduleID)` 校验 `schedule.DoctorID == 本人 doctors.id`（管理员豁免）。

> **它与不久之前刚修的"预问诊报告对医生开放"是同一类漏洞。**
> 两次都是同一个根因：**路由层 RBAC 被当成了资源层授权**。
> 路由层只回答"你是不是医生"，回答不了"这个排班是不是你的"。
>
> **规律（已上升为硬规则）**：凡是返回未脱敏个人数据的端点，
> 都必须问一句"它有没有做资源层归属校验"。
> 这也是后来"**按角色横切复查**"这一步的由来 —— 逐模块自测永远发现不了这类缺口。

### 2 · 号源回补的钳位用裸 `SET`，清掉了 TTL

`stockReleaseScript` 用裸 `SET` 做容量钳位 → **清掉 `SETNX` 设的 TTL** →
号源键变成**永久键**。而历史排班又不在对账扫描范围内 —— **错值再也不会被修正**。

修复：加 `KEEPTTL`（与兄弟脚本一致）。

> **教训**：同一个模块里的相似脚本，参数要**成组核对**。
> 这条是"兄弟脚本不一致"被发现的 —— 另外几条都带了 `KEEPTTL`，只有它漏了。

### 3 · `recoverDuplicate` 把任意错误当成"重复抢号"

见 §三②。用户被告知"您已预约"，而真实原因是数据库抖动。

### 4 · `CheckIn` 直接序列化 `*time.Time`

全接口统一 `%Y-%m-%d %H:%M:%S`，**只有这一个字段是 RFC3339** —— 前端要特判。
已改用 `model.APITimeLayout` 格式化。

### 5 · `cmd/seed` 用 `time.Now().Truncate(24h)`

截的是 **UTC 零点**。UTC+8 下 08:00 之前算出的"今天"**早一天**，
演示排班从昨天开始。已改用本地时区构造当天零点。

### 6（flaky）· 等待条件比断言落后一步（约 40% 失败）

```go
// 等的是"业务逻辑被调用"
for { if s.callCount() > 0 { break } ... }
// 断言的却是"确认已完成" —— 而 handle() 里 Ack 在 SummarizeReport 之后
assert.Equal(t, 1, q.ackCount())
```

**它是间歇失败而非必现，所以极易被当成"机器慢"而忽略。**
复现手段：**在 CPU 负载下**跑（单跑几乎不触发）。

> 这与本模块的关联在于：**并发代码的测试可信度**与并发代码本身同等重要。
> 一个 40% 概率失败的用例，最终会训练人"重跑一次"，整套测试的可信度随之归零。

---

## 五、反向验证记录

| 被测性质 | 反向验证方式 | 结果 |
|---|---|---|
| **候诊队列归属校验** | `TestQueue_OtherDoctorsScheduleIsForbidden`：移除归属校验 | **越权者拿到了队列内容 → 变红 ✅** |
| **HIS 同步不重置 `remaining`** | `TestSync_DoesNotResetRemaining` | ✅（覆盖它意味着已抢到的号被"复活"→ 超卖） |
| **100 并发零超卖** | `cmd/loadtest`（`nub run backend:loadtest`） | ✅ 实测零超卖 |
| 号源扣减是单次往返 | `BenchmarkStockDecrement` vs 裸 `BenchmarkRedisPing` | ✅ 419 µs ≈ 422 µs |
| 对账不制造超卖 | `service/reconcile_test.go`（`reconcileBatchSize+1` 的边界） | ✅ |
| 幂等重放返回一致结构 | `service/appointment_test.go` | ✅ |
| flaky 用例 | 修完**在 CPU 负载下**连跑 20 次 | ✅ 全绿（修复前 12 次里红 1 次） |

---

## 六、遗留与未验证

| 项 | 说明 |
|---|---|
| `version` 字段从不使用 | 保留并注明理由（见 §三④）。**若将来确认不改"读-算-写"，应删列** |
| cron 扫描未用 `idx_status_expire` | 已量化 + 写明触发条件（`sql-optimization.md` §4.4）。**当前判定为不改** |
| 对账的**全量遍历**成本 | `ListReconcilable` 按 `id > afterID` 升序分批扫 500 条。实测读 500 行 / 0.293 ms —— 但这是**每 5 分钟一次的全表遍历**，排班量增长后会线性变差 |
| 号源与 MySQL 的**最终一致**只有 5 分钟窗口 | 对账周期是 5 分钟。Redis 抖动后最长 5 分钟内号源可能不准，属有意取舍 |
| 无 M4 专属 fuzz 目标 | Lua 脚本与状态机没有 fuzz 覆盖。**风险较低**（输入是结构化参数而非不可信文本），但状态机的错误分支确实多 |

---

## 交叉引用

- 结构与决策：`docs/MODULES.md` §4（含 5 个必须理解的设计点）
- **性能实测**：`docs/sql-optimization.md` 的 Q3 / Q6 / Q7 / Q16 / Q24 与 §4.4
- 基准：`docs/benchmark/README.md` §二（号源扣减）
- 缺陷账：`docs/FIXES.md` H2
- 原始记录：`docs/HANDOVER.md` §2.2（spec 缺陷 2、3）、§2.7-B/D、§2.8
- 接手规则：`AGENTS.md` §2.4（Redis 三条不变量）、§5.11（flaky）

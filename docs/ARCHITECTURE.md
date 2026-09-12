# MediCore 架构详解

> 面向"要改动代码"的读者。讲清各部分**为什么这样设计**，而不只是列出结构。
>
> **配套文档**：`AGENTS.md`（规则速查）、`docs/MODULES.md`（逐模块的结构与决策，
> **改具体代码前先查那份**）、`docs/HANDOVER.md`（历程与教训）。

---

## 一、系统全景

```
                         ┌──────────────────────────┐
   浏览器 ───────────────►│  Nginx（计划中，未实现）  │
                         │  / → Vue3 静态资源        │
                         │  /api → backend:8080     │
                         └────────────┬─────────────┘
                                      │
                         ┌────────────▼─────────────────────┐
                         │  Go / Gin 单体后端                │
                         │                                  │
                         │  中间件链（顺序固定）              │
                         │  TraceID → Recovery → RequestLog │
                         │  → Metrics → [RateLimit → JWT    │
                         │  → RBAC] → Audit                 │
                         │                                  │
                         │  handler → service → repository  │
                         │                                  │
                         │  后台：scheduler（cron）          │
                         │        summaryWorker（Stream）    │
                         │        auditWriter（批量落库）     │
                         └───┬──────────┬──────────┬────────┘
                             │          │          │
                       ┌─────▼───┐ ┌────▼────┐ ┌───▼─────────┐
                       │ MySQL 8 │ │ Redis 7 │ │ LLM API     │
                       │ 事实来源 │ │ 缓存/锁  │ │ (当前 mock) │
                       │ + 审计   │ │ /队列    │ │             │
                       └─────────┘ └─────────┘ └─────────────┘
```

**单体分层而非微服务**（spec §1.3 显式声明）：单人全栈项目，拆分只增加运维复杂度。

---

## 二、分层与依赖方向

### 2.1 四层主干

```
handler  →  service  →  repository  →  model
  │            │             │
  │            │             └─ 唯一允许出现 GORM 的层
  │            └─ 事务边界、归属校验、业务编排；禁止 gin.Context
  └─ 参数绑定/校验、调用 service、统一响应；禁止 SQL/GORM
```

**为什么严格分层**：医疗系统的权限与数据一致性要求高。
分层让"谁能访问什么"（service 的归属校验）与"怎么存"（repository）分离，
Review 时可以逐层检查，而不是在一个大函数里找。

**唯一偏离**：`model` 的 PO 使用 `gorm.DeletedAt`（见 `HANDOVER.md` §4）。

### 2.2 接口由使用方定义

这是理解本项目结构的关键，也是最容易改错的地方。

`service/deps.go` 中声明 service **所需的最小能力接口**：

```go
type stockStore interface {
    EnsureLoaded(ctx context.Context, s *model.Schedule) error
    Decrement(ctx context.Context, scheduleID uint64, qty int) (repository.StockResult, error)
    Release(ctx context.Context, scheduleID uint64, qty, capacity int) (int, error)
    Current(ctx context.Context, scheduleID uint64) (int, bool, error)
    CompareAndSet(ctx context.Context, scheduleID uint64, expected, desired int) (bool, error)
}
```

`repository.StockRepo` **隐式满足**它——没有任何 `implements` 声明。

**带来三个好处**：

1. **可测试**：service 单测注入内存 fake，200 个用例秒级跑完，不需要 MySQL/Redis。
2. **耦合可见**：接口就是 service 到底用了哪些能力的清单。
3. **接口保持最小**：`appointmentJanitor` 只声明 `ListExpiredPending` + `Expire`，
   定时任务不必实现 `appointmentStore` 的全部 7 个方法。

**新增 service 依赖的正确做法**：在 `deps.go` 加接口，在对应 `*_test.go` 加内存 fake。
**不要**直接引用 `*repository.XxxRepo` 具体类型——那会让 service 无法单测。

### 2.3 依赖装配集中在 `internal/app`

```go
// cmd/server/main.go 只做：加载配置 → app.Build → app.Start → 等信号 → app.Stop
```

**为什么独立成包**：集成测试需要复用**同一套装配**。
若测试自己重新搭一遍，测的只是"测试搭的东西"，
而真实启动路径（中间件顺序、依赖注入、后台任务注册）仍未被覆盖。

`app.App` 暴露 `Engine *gin.Engine`，集成测试直接交给 `httptest` 使用。

**`Start()` 的失败回滚**：任一项启动失败会逆序停止已启动的部分，避免留下孤儿 goroutine。
**`Stop()` 的顺序**：worker → scheduler → auditWriter（最后停，让停机过程中的审计记录也能落库）。

---

## 三、各项机制详解

### 3.1 抢号：并发正确性的核心

这是全项目最复杂的流程，也是 spec G2 的验收对象。

```
① 幂等占用     Redis SETNX idem:grab:{uid}:{key}
                  ├─ 已存在且是 ID → 返回上次结果（幂等重放）
                  └─ 已存在且是 pending → 拒绝（请求在途）
② 预热号源     Redis SETNX stock:sched:{id}
③ 原子扣减     Lua: 键不存在→-1 / 余量不足→0 / 成功→1
④ 落库         MySQL 事务：UPDATE remaining-1 WHERE remaining>0 + INSERT appointment
⑤ 记录结果     Redis SET idem 键 = appointmentID
                  ↓ ④失败：回补 Redis 号源 + 释放幂等键
```

**五个必须理解的设计点**：

| 设计 | 防的是什么 |
|---|---|
| 预热用 `SETNX` 而非 `SET` | `SET` 会覆盖并发已扣减的值 → 号源"还回去" → **超卖** |
| Lua 键不存在返回 `-1` 而非 `0` | spec 原设计返回 0（=号源不足），Redis 冷启动后**所有抢号全失败** |
| 幂等键用比对删除释放 | 无条件 `DEL` 会删掉**已完成**的记录 → 幂等失效 |
| MySQL 用单条原子 `UPDATE ... WHERE remaining>0` | InnoDB 行锁下已原子；比"读 version 再比对"少一次 SELECT 且无需重试循环 |
| `CHECK (remaining >= 0 AND remaining <= total)` | 任何路径都不可能把号源扣成负数 |

**注意**：spec 表里定义了 `version` 字段，但**实际未使用**。
单条原子 UPDATE 已足够，加 version 反而要多一次查询和重试循环。
字段保留是为了将来若改为"读取-计算-写回"（如按比例分配号源）时可以启用。

### 3.2 号源对账：兜底的最终一致性

抢号是"先扣 Redis 再写 MySQL"。若进程在两步之间崩溃，或失败后回补 Redis 也失败，
两边就产生偏差。`ReconcileStock` 每 5 分钟以 MySQL 为准校准 Redis：

| 情况 | 处理 |
|---|---|
| Redis == MySQL | 无需处理 |
| Redis < MySQL | 修正（典型偏差，表现为"明明有余号却抢不到"） |
| Redis > MySQL | 修正（超卖风险方向） |
| Redis 键不存在 | **跳过**——是未预热或已过期，不是偏差 |

**必须用 `CompareAndSet`（Lua 比对更新）而非无条件 `SET`**：
对账读取 MySQL 与写入 Redis 之间有**时间窗**，期间若有并发扣减，
无条件覆盖会把刚扣掉的号源还回去 —— **对账本身制造超卖**。

比对失败就放弃本次修正，交给下一轮。偏差多存在一会儿，但绝不会因此多放号。

### 3.3 AI 预问诊：流式 + 异步

**对话（SSE 流式）**：

```
POST /appointments/:id/preconsult/chat
  → 校验归属与状态（失败仍返回统一 JSON 信封）
  → 落用户消息（先落库：重试时不必重新输入）
  → startStream() 首次吐数据时才写响应头
  → LLM 流式 → 逐段写 delta 事件
  → 落助手回复（用 WithoutCancel 的 context，保证写成功）
  → done 事件
```

**`startStream()` 延迟的意义**：让前置校验失败时仍能用统一信封，
而不是"先发 200 再报错"。流已开始后的错误只能通过 `error` 事件传递（HTTP 状态码早已发出）。

**失败时不保存半截回复**：残缺上下文会让下一轮模型更混乱。但用户消息保留。

**总结（异步 + 降级）**：

```
POST .../submit → 建报告(pending) → XADD → 202 Accepted
worker：XREADGROUP → MarkProcessing 抢占 → LLM → 提取 JSON → Schema 校验
        ├─ 通过 → 写 structured_json，status=done
        └─ 失败 → 追加纠正指令重试 1 次
                  └─ 仍失败 → structured_json=null, fallback=true，保留原始对话
        → XACK
```

**为什么异步**（决策 #8）：用户提交后很可能刷新或关闭页面——**刷新不等于取消任务**；
且 LLM 有速率限制，队列天然提供背压。

**JSON 提取做三级尝试**：整体是 JSON → 剥 Markdown 围栏 → 取首末花括号之间。
真实模型经常在 JSON 外包裹说明文字。刻意不做更花哨的解析：
混乱到这三步救不回来时，正确做法是降级让医生看原始对话。

**为什么对话存 Redis 而非直接写报告行**：
每轮追加写 JSON 列是"读-改-写"，多标签页会互相覆盖；且报告行应在提交时才产生。

### 3.4 Stream worker：至少一次投递

| 机制 | 作用 |
|---|---|
| 消费者组 + `XREADGROUP BLOCK` | 有任务立即醒来，不靠轮询空转 |
| **失败的消息不确认** | 留在 pending，等待 `XAUTOCLAIM` 重投 |
| 启动时 + 周期 `ClaimStale` | 认领崩溃 worker 遗留的任务；没有它报告会永远停在 processing |
| 投递次数 ≥ 3 → 死信队列 | 避免毒消息无限重投堵住主队列 |
| 死信时同时标记报告失败 | 否则报告永远停在 processing，对客户端与"正在处理"无法区分 |
| `MAXLEN ~ 10000` | 防队列无上限增长吃光 Redis 内存 |

**重试次数取自 Redis 的投递计数**（`XPENDING`）而非报告行的 `attempt_count`：
后者只在成功抢占后递增，"抢占前就失败"（如数据库断连）也需要计数，
否则那类失败会永远重试下去。

**消费者名带主机名与 PID**：多实例部署时能从 `XPENDING` 区分是哪个进程留下的未确认消息。

### 3.5 缓存：防击穿 + 降级不静默

`repository.Cache.Load[T]` 实现 cache-aside：

```
GetJSON → 命中则返回
  ↓ 未命中
singleflight(group.Do)  ← 合并并发回源，防击穿
  ↓ 二次检查（等待期间可能已有人填好）
用 WithoutCancel(ctx) + 独立超时 回源
  ↓
SetJSON
```

**`WithoutCancel` 是必须的**：singleflight 的跟随者共享 leader 的 context。
leader 的请求被取消（用户关页面）时，所有健康跟随者会一起收到 `context.Canceled`
——拿到一个本不该属于它们的错误。

**降级原则**：缓存读写失败都不阻断请求（退化为每次回源），但**一律记日志**，
否则缓存长期失效也无人察觉。反序列化失败按未命中处理并**删键**，避免脏数据反复报错。

### 3.6 审计：异步批量写入

**这是一个必须明说的取舍**：

| 方案 | 优点 | 代价 |
|---|---|---|
| 同步写 | 审计永不丢失 | 每个请求多一次 DB 往返，写接口延迟翻倍 |
| **异步写（已选）** | 请求路径不受 DB 影响 | 进程崩溃时缓冲区记录丢失 |

选异步 + 有界缓冲（2048）：审计是**事后追溯**用途，不在业务关键路径；
丢失窗口被限制在 batch size / flush interval 之内，且丢弃与失败都有指标可告警。

**缓冲满时丢弃而非阻塞**：审计价值再高，也不值得让一次数据库抖动演变成全站请求堆积。

**`Entry` 刻意不含请求体**：请求体可能含手机号、口令、病情描述，
复制进审计表等于把脱敏工作全部作废。

**资源与动作从 `FullPath` 推导**而非真实路径：
用真实路径会让每个 ID 产生不同的"资源名"，审计查询无法按资源聚合。
`trailingActions` 映射表覆盖"末段是动作而非资源"的路由
（`/appointments/:id/cancel` → action=`cancel` 而非笼统的 `create`）。

### 3.7 HIS 同步：三阶段而非一把梭

```
阶段 1：科室（无依赖）
阶段 2：医生（依赖科室）
阶段 3：排班（依赖医生 + 科室）
```

**为什么分阶段**：排班依赖医生、医生依赖科室。若并发处理全部实体，
排班会因为医生还没落库而大量失败。

**限并发 5**（spec §7.3）：外部系统往往有连接数或 QPS 限制，
一次性打出全部请求容易被限流甚至封禁。用 `errgroup.SetLimit` 实现。

**`upsert by external_id`**：外部 ID 是同步的锚点，保证重复同步不产生重复数据。
**排班的 `remaining` 不被重置**：已抢走的号源不能因为一次同步就"复活"。
（有专门的集成测试 `TestSync_DoesNotResetRemaining` 守护这一点。）

---

## 四、数据库设计要点

完整 DDL 见 `docs/schema.sql`，评审过程见 `docs/database-review.md`。这里只讲设计决策。

### 4.1 关键约束

| 表 | 约束 | 作用 |
|---|---|---|
| `appointments` | `uk_active`（生成列） | 等效"部分唯一索引"：同一用户不能重复占用同一号源，取消/过期/软删后自动释放 |
| `appointments` | `uk_sched_queue (schedule_id, queue_no)` | 同排班内就诊序号不重复；NULL 不冲突，故未签到者不受约束 |
| `appointments` | `uk_user_idem (user_id, idempotency_key)` | 幂等键按用户隔离（全局唯一会让跨用户互相干扰） |
| `schedules` | `CHECK (remaining >= 0 AND remaining <= total)` | 号源不可能为负 |
| `schedules` | `uk_doctor_dept_date_slot` | 含科室——多对多后专家可在同午别跨科室出诊 |
| `ai_pre_reports` | `uk_appointment` | 一次预约一份报告，保证 submit 幂等 |

### 4.2 `uk_active` 生成列的巧妙之处

MySQL 没有 PostgreSQL 那样的部分唯一索引。用生成列等效实现：

```sql
active_uk VARCHAR(48) GENERATED ALWAYS AS (
  IF(status IN ('pending','confirmed') AND deleted_at IS NULL,
     CONCAT(user_id, ':', schedule_id), NULL)
) STORED,
UNIQUE KEY uk_active (active_uk)
```

取消/过期/软删除后该列变 `NULL`，而 MySQL 唯一索引**允许多行 NULL**，
所以用户随后可以重新预约。已实测验证。

### 4.3 软删除与唯一键的冲突（反直觉）

**唯一键保持全局唯一，不并入 `deleted_at`**。

原因：MySQL 中 `NULL != NULL`，把 `deleted_at` 加进唯一键**反而会让唯一性彻底失效**
——两个未删除的同手机号用户都能插入。

删除后重建走 service 层**"复活"**（`UPDATE ... SET deleted_at = NULL`）。
对 HIS 同步尤其正确：`external_id` 是 upsert 锚点，重新同步应复活而非插重复行。

### 4.4 敏感字段

```
phone_enc    VARBINARY(128)   AES-256-GCM，格式 nonce(12B) ‖ ct ‖ tag
phone_hash   CHAR(64)         HMAC-SHA256(pepper, phone)，ASCII + ascii_bin
```

**`phone_hash` 必须是确定性的**（用全局 pepper，不能用逐行随机盐）：
登录时要由手机号算出 hash 去查找。用随机盐就只能全表扫描逐行解密比对。

**实测密文长度**：11 位手机号 → 12 + 11 + 16 = 39 字节 → 78 位十六进制。

---

## 五、可观测性

### 5.0 全部端点清单（28 条）

| 分类 | Method | Path | 角色 |
|---|---|---|---|
| **运维** | GET | `/healthz` `/readyz` `/metrics` | 免认证 |
| **认证** | POST | `/api/v1/auth/register` | 公开 |
| | POST | `/api/v1/auth/login` | 公开 + **限流 5/分钟（按 IP）** |
| | POST | `/api/v1/auth/refresh` | 公开 |
| | POST | `/api/v1/auth/logout` | 登录 |
| | GET | `/api/v1/auth/me` | 登录 |
| **目录** | GET | `/api/v1/departments` | 登录 |
| | GET | `/api/v1/departments/:id/doctors` | 登录（Redis 缓存） |
| | GET | `/api/v1/departments/:id/schedules` | 登录（游标分页） |
| | GET | `/api/v1/doctors/:id` | 登录 |
| | GET | `/api/v1/doctors/:id/schedules` | 登录（游标分页） |
| **预约** | POST | `/api/v1/appointments` | patient + **限流 10/分钟（按用户）** |
| | GET | `/api/v1/appointments` | patient（游标分页） |
| | POST | `/api/v1/appointments/:id/cancel` | patient |
| | POST | `/api/v1/appointments/:id/checkin` | patient |
| | GET | `/api/v1/schedules/:id/queue` | doctor / admin |
| **AI** | POST | `/api/v1/appointments/:id/preconsult/chat` | patient（**SSE**） |
| | GET | `/api/v1/appointments/:id/preconsult/history` | patient |
| | POST | `/api/v1/appointments/:id/preconsult/submit` | patient（返回 **202**） |
| | GET | `/api/v1/appointments/:id/preconsult/report` | patient |
| **医疗** | POST | `/api/v1/appointments/:id/record` | doctor |
| | GET | `/api/v1/appointments/:id/record` | 任意登录角色 |
| | GET | `/api/v1/records` | 任意登录角色（游标分页） |
| **管理** | GET | `/api/v1/audit-logs` | admin（游标分页） |
| | POST | `/api/v1/sync/:source` | admin |
| | GET | `/api/v1/sync-logs` | admin（游标分页） |

**统一响应信封**：`{code, message, data, trace_id}`；成功 `code=0`。
未匹配路由 → 40003，方法不支持 → 40006（同样是 JSON 信封）。

**错误码段**：`400xx` 业务校验 / `401xx` 认证 / `403xx` 权限 / `429xx` 限流 / `500xx` 服务端 / `502xx` LLM 上游。

### 5.1 TraceID 贯通

入口生成 32 位十六进制（或沿用上游 `X-Trace-Id`），写入：
**context → 响应头 → 响应体 → 日志 → 审计表**。

> 曾有一个 bug：202 响应直接构造 `response.Body` 漏掉了 `trace_id`，
> 导致该请求在日志与审计里断链。已加 `response.Accepted` helper 修正。

### 5.2 指标清单

| 指标 | 标签 | 说明 |
|---|---|---|
| `http_requests_total` | method, path, status | `path` 用**路由模板**而非真实路径，避免高基数 |
| `http_request_duration_seconds` | method, path | 直方图 |
| `appointment_grab_total` | result | success / no_stock / duplicate / in_flight / error |
| `stock_reconcile_total` | result | corrected 一旦上涨说明出现偏差并已修正 |
| `llm_calls_total` | purpose, result | chat / summary × success / error / timeout / canceled |
| `summary_tasks_total` | result | done / retry / dead_letter / malformed |
| `audit_entries_total` | result | written / **dropped** / error |
| `ratelimit_errors_total` | — | 限流器 fail-open 计数 |

**注册表在 `internal/metrics`**（独立包而非放在 middleware）：
业务指标需由 service 注册，让 service 反向依赖 middleware 是错的。

### 5.3 日志

Zap JSON，字段 `trace_id / method / path / status / latency_ms / client_ip / user_id / role`。
**5xx 走 Error 级别**，便于告警规则直接基于 `level` 过滤。

---

## 六、测试策略

### 6.1 两层结构

| 层 | 数量 | 运行 | 覆盖 |
|---|---|---|---|
| 单元 | 200 | `nub run backend:test` | service 核心状态机、工具包、worker 决策 |
| 集成 | 20 | `nub run backend:test:integration` | 全部 25 个端点的行为、RBAC、审计 |

**集成测试复用 `app.Build()` 的同一套装配**，因此覆盖真实中间件顺序与依赖注入。
用独立数据库 `medicore_test` 与 Redis DB 1，由 `deploy/mysql/init-test-db.sql` 创建。

### 6.2 覆盖率现状（诚实版）

| 包 | 覆盖率 | 评价 |
|---|---|---|
| `mask` / `jwt` / `worker` / `llm` / `prompts` / `secret` | 88–100% | ✅ |
| `service` | 72.9% | ✅ 核心价值所在 |
| `model` | 51.2% | 可接受 |
| **`repository`** | **5%** | ⚠️ 1628 行 SQL 主要靠集成测试保障 |
| **`handler`** | **0%（单元）** | ⚠️ 但集成测试覆盖了全部端点行为 |

**整体 31.5%，但这个数字低估了实际保障**：spec §11.1 要求的是"覆盖 service 层核心逻辑"，
而 service 达到 72.9%。真实盲区是 repository 的 SQL 细节（游标分页边界、
软删除条件遗漏、`RowsAffected` 分支）。

### 6.3 反向验证过的测试

关键测试都做过"改坏实现看是否失败"：

| 测试 | 改坏后的表现 |
|---|---|
| `TestLogin_UnknownUserTimingIsEqualized` | 比值 0.00 → 失败 |
| `TestCacheLoad_LoaderContextSurvivesCallerCancellation` | `context canceled` → 失败 |

**没有反向验证过的测试，不能算测试。**

---

## 七、配置

| 来源 | 优先级 | 用途 |
|---|---|---|
| `configs/config.yaml` | 基础 | 非敏感默认值 |
| `configs/config.local.yaml` | 覆盖 | 本地密钥（gitignored，由 `cmd/genlocal` 生成） |
| 环境变量 `MEDICORE_*` | 最高 | 生产注入 |

**启动时校验**（`config.Validate()`）：

- 密钥非空、长度达标、**按特征拒绝可预测值**（单一字符重复 / 短周期重复 /
  连续升序 / 含占位词）。用特征而非黑名单：黑名单既拦不全，本身又会把密钥字面量带回仓库。
- `bcrypt_cost >= 10`
- TTL 为正

**Schema 自检**：`SummaryValidator.SelfCheck()` 用空对象反向验证，
防止 Schema 被改成 `{}` 导致校验静默失效。

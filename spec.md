# MediCore · 医疗数据中台与AI预问诊平台
## 总体详细规格说明书（Overall Spec v2.0）

> **文档定位**：本文档是整个项目 Spec-Driven Development 的“单一事实来源”（Single Source of Truth）。后续所有 AI 代码生成、人工 Review、测试验证均以本 Spec 为基准。本项目的核心目标是展示**扎实的全栈工程能力**与**高效的 AI Vibe Coding 实践**。

---

> ## ⚠️ 实现现状与修正（**读正文之前先读这一段**）
>
> 本文档是 **v2.0 需求基线**，写于 M1 之前。正文**刻意保留原貌**，以便追溯"最初要做什么"。
> 实现过程中修正了 4 处缺陷、并刻意偏离了 6 处 —— **下面这些才是权威**：
>
> | 我想知道 | 权威来源 | 正文的偏差 |
> |---|---|---|
> | 实际有哪些端点、各自什么权限 | `backend/internal/router/router.go` | §6.1 只列了 11 条，**实际 29 条** |
> | 实际表结构 | `docs/schema.sql` | §5.1 是草案：只有 4 张表（实际 11 张），且含**必然建表失败**的索引 |
> | 实际架构与中间件顺序 | `docs/ARCHITECTURE.md`、`AGENTS.md` §3.2 | §3.1 的顺序与实现不同，且漏了 Audit |
> | 实际技术栈与版本 | `AGENTS.md` §2.7、`go.mod`、`frontend/package.json` | §4 的版本锁定全部过期 |
> | **刻意偏离 spec 的 6 处** | `docs/HANDOVER.md` §4（唯一事实来源） | 正文没有记录 |
> | 现在健不健康、还剩什么 | `docs/FIXES.md` §1 / §7 | §12.2 的勾选状态已过期 |
>
> 正文里每一处已知错误都就地标了 **⟂** 并给出替代来源。
> **不要照着正文的 DDL / 端点表 / Lua 脚本直接实现。**

---

## 1. 项目概述

### 1.1 背景
医院因部门细化、系统增多（HIS/HDR/ODS），导致“信息孤岛”、权限管理欠缺、患者就诊效率低等问题。本项目构建一个轻量级医疗数据中台，统一管理人员/科室/排班数据，提供 AI 预问诊提升就诊效率，并以规范的工程实践保障系统的高可用与医疗数据合规。

### 1.2 项目目标

| # | 目标 | 可验证标准 |
|---|---|---|
| G1 | 实现三角色（管理员/医生/患者）的预约挂号全流程 | 患者可完成“选科室→选医生→抢号→AI预问诊→医生查看”闭环 |
| G2 | 高并发抢号不超卖，数据库查询高性能 | `hey -n 1000 -c 100` 压测下号源扣减准确零超卖；列表查询采用游标分页，无深翻页性能问题 |
| G3 | AI 预问诊输出结构化且具备高可用性 | LLM 返回 100% 通过 JSON Schema 校验或触发优雅降级，SSE 流式输出无卡顿 |
| G4 | 系统具备完善的可观测性与基础合规性 | Prometheus 指标正常暴露，TraceID 全链路贯通；敏感数据加密存储且接口脱敏输出 |
| G5 | 一键容器化部署（Windows Docker Desktop） | `docker compose up -d --build` 后 3 分钟内全服务健康 |
| G6 | AI Vibe Coding 过程留痕，体现代码质量把控 | `docs/ai-vibe-coding/reviews/` 下 ≥5 篇高质量 Review 记录 |

### 1.3 范围界定（KISS 原则的显式声明）

**In-Scope（做）：**
- Go/Gin 单体后端（严格分层架构）
- Vue3 SPA 前端（TypeScript + Pinia + Element Plus）
- MySQL + Redis 双存储
- LLM API 集成（OpenAI 兼容协议，SSE 流式）
- Docker Compose 编排（nginx/go/mysql/redis 四服务）
- Prometheus 指标暴露 + 前端简易看板

**Out-of-Scope（明确不做，并记录理由）：**

| 不做的事 | 理由 |
|---|---|
| 微服务拆分 | 单人全栈项目，单体分层足够，拆分徒增运维复杂度 |
| Kubernetes 编排 | 岗位要求为 Docker Desktop 部署；K8s 认知以文档和指标暴露体现 |
| Kafka/RabbitMQ | 用 Redis Stream/定时任务实现异步与调度，依赖最少 |
| xxl-job | 用 `robfig/cron/v3` 进程内调度，避免额外部署中间件 |
| 真实 HIS 系统对接 | 用 Mock 同步接口模拟外部系统数据源，聚焦中台逻辑 |

---

## 2. 用户角色与权限模型（RBAC）

### 2.1 角色定义

| 角色 | 权限范围 |
|---|---|
| **patient（患者）** | 注册/登录、浏览科室医生排班、预约挂号、AI 预问诊、查看本人病历与预约 |
| **doctor（医生）** | 登录、查看本人排班与预约列表、查看患者预问诊报告与病历、书写诊断/处方 |
| **admin（管理员）** | 全部读权限 + 科室/医生/排班管理、数据同步触发、审计日志查询、看板查看 |

### 2.2 权限与合规设计规则

- **双层校验**：路由层（JWT中间件解析角色）+ 资源层（Service 内校验资源归属，如患者只能看自己的病历）。
- **医疗合规**：患者手机号/身份证采用 AES-GCM 加密存储，SHA256 Hash 用于唯一性校验与登录查找；API 响应在 DTO 层统一脱敏（如 `138****5678`）。
- **认证方案**：JWT（access_token 2h + refresh_token 7d 存 Redis），密码 `bcrypt` 加密。

---

## 3. 系统架构

### 3.1 总体架构图

```text
                        ┌────────────────────────────┐
        :80             │  Nginx                     │
 浏览器 ────────────────►│  / → Vue3 静态资源          │
                        │  /api → backend:8080       │
                        └─────────────┬──────────────┘
                                      │
                        ┌─────────────▼──────────────┐
                        │  Go/Gin Backend :8080      │
                        │ ┌────────────────────────┐ │
                        │ │ 中间件链（顺序固定）      │ │
                        │ │ TraceID → Recovery →   │ │
                        │ │ RequestLog → RateLimit │ │
                        │ │ → Metrics → JWT → RBAC │ │
                        │ └────────────────────────┘ │
                        │  handler → service → repo  │
                        │        │                   │
                        │  ┌─────▼─────┐ ┌─────────┐ │
                        │  │ AI 校验层  │ │ cron    │ │
                        │  │ (LLM网关) │ │ 调度器   │ │
                        │  └─────────┘ └─────────┘ │
                        └───┬─────────┬─────────┬───┘
                            │         │         │
                      ┌─────▼──┐ ┌────▼───┐ ┌───▼────────┐
                      │MySQL 8 │ │Redis 7 │ │LLM API     │
                      │业务+审计│ │缓存/锁/ │ │(DashScope/ │
                      │        │ │限流/流  │ │ DeepSeek)  │
                      └────────┘ └────────┘ └────────────┘
```

> ⟂ **实际中间件链**（`router.go`）：
> `TraceID → Recovery → RequestLog → Metrics → Audit`（**全局挂载**），
> `[RateLimit → JWT → RBAC]`（**按路由挂载** —— 探针与 `/metrics` 必须免认证，
> 否则 Prometheus 抓取会被 401）。上图把 RateLimit 排在 Metrics 之前、且**漏了 Audit**。
> 权威版本：`AGENTS.md` §3.2、`docs/ARCHITECTURE.md` §一。

### 3.2 后端分层架构（强制约束）

```text
internal/
├── middleware/   # 拦截器：TraceID, JWT, RBAC, 限流, 审计日志
├── handler/      # 参数绑定+校验、调用service、统一响应；禁止出现SQL和GORM
├── service/      # 业务逻辑、事务边界、权限归属校验、AI校验层；禁止直接操作http.Context
├── repository/   # 数据访问，唯一允许出现GORM的层；方法签名面向业务
├── model/        # 实体(PO)、请求/响应DTO、枚举常量
└── pkg/          # 无状态工具：crypto/mask/sse/llm/errcode/jwt
```

> ⟂ **实际没有 `internal/pkg/`**：工具包**平铺**在 `internal/` 下
> （`secret` / `mask` / `errcode` / `jwt` / `apperr` / `authctx` / `traceid` / `metrics` /
> `logger` / `response`）。`internal` 本身已阻止外部导入，再加一层 `pkg` 是零语义的冗余层级。
> 这是**第 2 处刻意偏离 spec**，见 `docs/HANDOVER.md` §4。
> 另外实现里多了 `app`（依赖装配）、`audit`（异步审计）、`worker`（Stream 消费者）、
> `scheduler`、`llm` / `his` / `mockhis` 等包。

**分层铁律（Review 时的检查清单）：**
1. 依赖方向只能向下：handler → service → repository → model，禁止反向依赖。
2. 错误必须包装上下文向上传递，handler 层统一转换为错误码。
3. 跨表写操作必须在 service 层用事务包裹，事务内禁止网络调用（如 LLM/Redis）。

### 3.3 统一响应与错误码

```json
// 成功: { "code": 0, "message": "ok", "data": {...}, "trace_id": "abc123" }
// 失败: { "code": 40002, "message": "号源不足", "data": null, "trace_id": "abc123" }
```
- `0`: 成功 | `400xx`: 业务校验 | `401xx`: 认证 | `403xx`: 权限 | `429xx`: 限流 | `500xx`: 服务端 | `502xx`: LLM 上游错误。

---

## 4. 技术选型与版本锁定

| 组件 | 版本 | 选型理由 |
|---|---|---|
| Go | 1.22 | 稳定版，支持泛型与新版 net/http |
| Gin | v1.10 | JD 指定，生态成熟 |
| GORM | v1.25 | Go 主流 ORM，自动参数化防注入 |
| go-redis | v9 | 官方推荐，context 支持完善 |
| Viper/Zap | v1.19/v1.27 | 配置管理与结构化日志（可观测性基础） |
| robfig/cron | v3 | 轻量定时任务 |
| Vue | 3.4 + TS 5 | 现代前端标准 |
| Element Plus | 2.x | 成熟的后台 UI 组件库 |
| MySQL/Redis | 8.0 / 7-alpine | 主流关系型与内存数据库 |

> ⟂ **实际版本以 `AGENTS.md` §2.7 为准**（下表全部过期）：
> Go **1.27.1**（不是 1.22）· Gin **v1.12.0** · GORM **v1.31.2** · Viper **v1.21.0** ·
> Vue **^3.5.42** · TypeScript **~5.9.3**（**TS 7 当前不可用**，原因见 `AGENTS.md` §5.2 ——
> 这一点本文档完全没提，照抄会踩坑）。
> 另外 spec 未列而实现必需的：`go-redis v9`、`robfig/cron v3`、`golang-jwt/jwt v5`、
> `santhosh-tekuri/jsonschema v5`、`prometheus/client_golang`、`go-playground/validator`、
> `golang.org/x/sync`（`singleflight` / `errgroup`）。

---

## 5. 数据库设计

### 5.1 核心表结构（DDL 草案）

> ⟂ **本草案有 4 类问题，不要照着执行。权威 DDL 是 `docs/schema.sql`（11 张表）。**
> 逐处修正见下面的行内注释；设计理由见 `docs/database-review.md`。
>
> | 本草案的问题 | 实际 |
> |---|---|
> | `schedules` 没有 `status` 列，却建了 `idx_date_status (sched_date, status)` | **建表必然报错**。已补 `status` 列 |
> | 只有 4 张表 | 实际 **11 张**（另有 `departments` / `doctors` / `doctor_departments` / `medical_records` / `prescription_items` / `audit_logs` / `sync_logs`） |
> | 全程没有软删除 | 统一 `deleted_at` + `gorm.DeletedAt`（第 3 处刻意偏离） |
> | `phone_hash` 用 `SHA256(phone+salt)` | 改用 **HMAC-SHA256(pepper, phone)**（第 1 处刻意偏离，理由见 `HANDOVER.md` §4） |

```sql
-- 用户表（三角色共用，敏感字段加密）
CREATE TABLE users (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  role          ENUM('admin','doctor','patient') NOT NULL,
  name          VARCHAR(64)  NOT NULL,
  phone_enc     VARBINARY(128) NOT NULL COMMENT 'AES-GCM加密手机号',
  phone_hash    CHAR(64)     NOT NULL COMMENT 'SHA256(phone+salt)，用于唯一约束和登录',  -- ⟂ 实际是 HMAC-SHA256(pepper, phone)
  password_hash VARCHAR(72)  NOT NULL,  -- ⟂ 实际 VARCHAR(100)
  created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uk_phone_hash (phone_hash)
) ENGINE=InnoDB;

-- 排班表（号源载体，乐观锁设计）
CREATE TABLE schedules (
  id         BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  doctor_id  BIGINT UNSIGNED NOT NULL,
  sched_date DATE NOT NULL,
  time_slot  ENUM('am','pm') NOT NULL,
  total      INT NOT NULL,
  remaining  INT NOT NULL COMMENT '剩余号源',
  version    INT NOT NULL DEFAULT 0 COMMENT '乐观锁版本号',  -- ⟂ 实现未使用；并发靠 `remaining > 0` 的条件更新 + uk_active 生成列
  status     ENUM('open','closed','cancelled') NOT NULL DEFAULT 'open',  -- ⟂ 本草案漏了这一列，而下一行的索引引用了它
  UNIQUE KEY uk_doctor_date_slot (doctor_id, sched_date, time_slot),  -- ⟂ 实际含 department_id（多对多后专家要能跨科室排班）
  KEY idx_date_status (sched_date, status)  -- ⟂ 列缺失 = 建表必然失败；另一处见 database-review.md
) ENGINE=InnoDB;

-- 预约表（幂等与游标分页设计）
CREATE TABLE appointments (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id       BIGINT UNSIGNED NOT NULL,
  schedule_id   BIGINT UNSIGNED NOT NULL,
  status        ENUM('pending','confirmed','cancelled','expired') NOT NULL DEFAULT 'pending',
  idempotency_key CHAR(36) NOT NULL COMMENT '幂等键',
  expire_at     DATETIME(3) NOT NULL COMMENT '15分钟支付/确认截止',
  created_at    DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uk_idempotency (idempotency_key),
  KEY idx_user_created (user_id, created_at DESC) -- 游标分页索引  -- ⟂ 实际 idx_user_cursor (user_id, id DESC)
  -- ⟂ 实际还有：deleted_at、department_id、checked_in_at、queue_no、
  --    以及 uk_active 生成列唯一键（软删除与唯一约束共存的关键，见 database-review.md §4.3）
) ENGINE=InnoDB;

-- AI 预问诊报告表
CREATE TABLE ai_pre_reports (
  id             BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  appointment_id BIGINT UNSIGNED NOT NULL,  -- ⟂ 实际有唯一约束（一次预约一份报告）
  structured_json JSON NOT NULL COMMENT 'LLM结构化输出（经Schema校验）',  -- ⟂ 实际可空：降级时置 null（这与 §7.2 自己写的"置 null"矛盾）
  conversation   JSON NOT NULL COMMENT '完整对话记录',
  llm_model      VARCHAR(64) NOT NULL,
  prompt_version VARCHAR(16) NOT NULL,
  created_at     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
  -- ⟂ 实际还必须有：status ENUM('pending','processing','done','failed')、fallback、
  --    attempt_count、error_msg、started_at、finished_at（决策 #8 改异步之后缺一不可）
) ENGINE=InnoDB;
```

### 5.2 高性能 SQL 规范
1. **游标分页强制**：所有列表接口禁止 `OFFSET` 深翻，统一 `WHERE id < :last_id ORDER BY id DESC LIMIT :size`。
2. **N+1 防护**：GORM 关联查询必须用 `Preload` 或 `JOIN`。
3. **事务最小化**：事务内禁止网络调用（LLM/Redis），只包含 DB 写。

---

## 6. API 设计（RESTful）

### 6.1 核心端点清单

| 模块 | Method | Path | 角色 | 说明 |
|---|---|---|---|---|
| **认证** | POST | /api/v1/auth/login | 公开 | 登录，返回双token |
| **排班** | GET | /api/v1/departments/:id/doctors | 登录 | 科室下医生列表（Redis缓存） |
| **预约** | POST | /api/v1/appointments | patient | 抢号（幂等键+Lua扣减） |
| | GET | /api/v1/appointments?cursor=&size= | patient | 我的预约（游标分页） |
| | POST | /api/v1/appointments/:id/cancel | patient | 取消（回补号源） |
| **AI** | POST | /api/v1/appointments/:id/preconsult/chat | patient | SSE流式对话 |
| | POST | /api/v1/appointments/:id/preconsult/submit | patient | 结束对话，触发结构化总结 |
| **管理** | POST | /api/v1/admin/sync/:source | admin | 手动触发模拟HIS同步 |
| **运维** | GET | /healthz, /readyz, /metrics | - | 探针与 Prometheus 指标 |

> ⟂ **上表只列了 11 条"核心"端点，实际有 29 条。** 权威来源是
> `backend/internal/router/router.go`（或启动一次看 gin 打印的路由）。
> 下表是本文档**遗漏**、但已实现的 18 条 —— 其中「医生工作台」两条还是
> 后来按 §2.1 的角色能力矩阵**倒查出来的规格缺口**，说明这张表当时确实不完整：
>
> | 模块 | Method | Path |
> |---|---|---|
> | 认证 | POST | `/auth/register`、`/auth/refresh`、`/auth/logout` |
> | | GET | `/auth/me` |
> | 目录 | GET | `/departments`、`/departments/:id/schedules`、`/doctors/:id`、`/doctors/:id/schedules` |
> | AI | GET | `/appointments/:id/preconsult/history`、`/appointments/:id/preconsult/report` |
> | 预约 | POST | `/appointments/:id/checkin` |
> | | GET | `/doctor/appointments`（医生工作台接诊列表）、`/schedules/:id/queue`（候诊队列） |
> | 管理 | GET | `/admin/audit-logs`、`/admin/sync-logs` |
> | 病历 | POST/GET | `/appointments/:id/record` |
> | | GET | `/records` |
>
> 另外注意：管理端点的前缀是 **`/admin/`**（文档曾漏写这一段，照抄会 404）。

---

## 7. 核心流程详细设计

### 7.1 抢号流程（高并发与一致性）

1. **幂等检查**：`SETNX idem:{key}`，防止重复提交。
2. **Redis Lua 原子扣减**：
   ```lua
   local remaining = tonumber(redis.call('GET', KEYS[1]))
   if remaining == nil or remaining < tonumber(ARGV[1]) then return 0 end
   redis.call('DECRBY', KEYS[1], ARGV[1])
   return 1
   ```
   > ⟂ **这段脚本有致命缺陷，实现已改。** 键不存在时返回 `0`，而调用方把 `0` 解读为
   > "号源不足" —— 于是 **Redis 冷启动时所有抢号全部失败，且提示误导**。
   > 实际返回 `-1`（`StockNotLoaded`）表示"未预热"，调用方先预热再重试。
   > 另外实现里多了"以容量为上限、键不存在时不创建"的回补钳位。
   > 见 `AGENTS.md` §2.4、`docs/HANDOVER.md` §2.2 缺陷 #2。

3. **MySQL 事务落库**：INSERT 预约记录 + UPDATE 排班（乐观锁 `WHERE id=? AND remaining > 0`）。
   > ⟂ 实现用的是**单条原子条件 UPDATE**（`remaining > 0` + 检查 `RowsAffected`），
   > `version` 字段实际未被使用。
4. **补偿机制**：若 MySQL 事务失败，Lua 回补 INCR + 删除幂等键。
   > ⟂ **删幂等键必须"先比对再删"**（compare-and-delete）。无条件 `DEL` 会误删
   > 已经完成的那条记录，幂等直接失效。见 `HANDOVER.md` §2.2 缺陷 #3。
5. **过期释放**：cron 每分钟扫描 `expire_at < NOW()` 的 pending 订单，置为 expired 并回补号源。
   > ⟂ 实现还加了"幂等闸门"，避免多实例并发扫描时**重复回补**导致号源凭空变多。
   > 另有一个每 5 分钟的**号源对账**任务（以 MySQL 为准校准 Redis）——
   > 本文档没有，但它是"进程崩溃无法补偿"的唯一兜底。

### 7.2 AI 预问诊流程（LLM 可靠性设计）

> ⟂ **本节描述的是"同步总结"，实现已改成异步（决策 #8），且强制用 Redis Stream。**
> 这与 §1.3"用 Redis Stream/定时任务实现异步与调度"是自洽的，本节漏写了。
> 实际链路：
>
> ```
> submit → 校验权限/归属 → 建报告(pending) → XADD 入队 → 立即返回 202 { report_id, status }
> worker → XREADGROUP → 抢占 → LLM 总结 → Schema 校验 → 失败重试 1 次 → 降级
>       → 写报告(done/failed) → XACK
> ```
>
> 为什么必须异步：用户刷新/关页面 ≠ 任务取消；LLM 有限流需要背压；LLM 延迟不可控会拖垮 P99。
> 权威版本：`docs/MODULES.md` §5.5、`docs/ARCHITECTURE.md` §3.4、`AGENTS.md` §3.4。

1. **SSE 流式对话**：前端 `fetch` + `ReadableStream`，后端透传 LLM stream。必须处理 `ctx.Done()` 防止 goroutine 泄漏。
2. **结构化总结与降级**：
   - 对话结束触发 LLM 总结（要求输出 JSON）。
   - 后端使用 `jsonschema` 校验。
   - **降级策略**：校验失败重试1次；仍失败或超时，则保存原始对话文本，`structured_json` 置 null，标记 `fallback=true`，医生端展示原始对话。
   > ⟂ 实现里加了一条本节没有的关键防线：**`{}` 是合法的 JSON Schema**，
   > 若 Schema 被误改成空对象，校验会永远通过、降级永不触发、垃圾数据静默入库。
   > `SummaryValidator.SelfCheck()` 在启动时用空对象做反向验证，把它变成启动失败。
3. **Prompt 版本化**：System Prompt 存于 `backend/prompts/preconsult_v1.md`，版本号入库，便于追溯。

### 7.3 模拟 HIS 数据同步
- `pkg/mockhis/` 内置 JSON 数据源。
  > ⟂ **实际位置是 `internal/his`（契约，只放类型）+ `internal/mockhis`（适配器）。**
  > 契约不能放 mock 包里 —— 否则业务层会反向依赖 mock，真实 HIS 适配器也得 import 它。
- 同步策略：upsert by `external_id`，使用 `errgroup` 并发处理（限并发 5）。
  > ⟂ 实现是**三阶段**（departments → doctors → schedules），且**同步绝不重置 `remaining`** ——
  > 把 `remaining` 与 `total` 一起覆盖等于每次同步都把已预约号源还回去，**每次同步都是一次超卖**。
- 记录 `sync_logs`（total/succeeded/failed）。

---

## 8. 前端设计（Vue3）

### 8.1 核心页面与组件
- **AI 预问诊对话组件**：SSE 流式打字机效果，断线重连提示，组件卸载时 `AbortController.abort()`。
- **预约列表**：游标分页无限滚动（Intersection Observer）。
- **管理看板**：ECharts 渲染预约趋势、科室 TOP、系统健康度（拉取 `/metrics` 关键值）。

### 8.2 工程规范
- **Axios 封装**：请求拦截注入 token 与幂等键（uuid），401 自动 refresh 重试，响应拦截统一错误提示。
- **状态与路由**：Pinia 管理 auth/appointment；登录后按 role 动态注册路由。
- **TypeScript**：所有 API 响应定义 interface，与后端 DTO 严格对齐。

---

## 9. 非功能需求（性能、可观测性与基础质量）

### 9.1 性能指标
- 抢号接口 P99 < 100ms（100并发下），零超卖。
- 列表查询 P99 < 50ms（缓存命中或索引命中）。

### 9.2 可观测性
- **日志**：Zap JSON 格式，包含 `trace_id/user_id/method/path/status/latency`。
- **TraceID**：入口中间件生成，贯穿请求→日志→审计表。
- **Prometheus**：暴露 `http_requests_total`, `http_request_duration_seconds`, `appointment_grab_total`, `llm_calls_total`。

### 9.3 基础质量与合规（原安全审计精简版）
- **参数化查询**：GORM 默认参数化，禁止 `Raw` 拼接 SQL。
- **敏感数据**：AES-GCM 加密存储，DTO 层统一脱敏。
- **接口限流**：Redis 令牌桶，登录 5次/min，抢号 10次/min。
- **依赖与配置**：`govulncheck` 扫描依赖；Docker 容器非 root 运行，敏感配置走环境变量。

---

## 10. AI Vibe Coding 工作流规范（项目一等公民）

### 10.1 流程（每个功能模块强制走完）
1. **需求拆解**：从本 Spec 摘取模块需求 → 写成模块化 mini-spec。
2. **方案选型**：让 AI 给出 2-3 套实现方案 → 人工按 KISS/性能 选定。
3. **AI 生成**：按 `prompts/system-rules.md` 生成代码（嵌入分层铁律）。
4. **人工 Review**：按 §10.2 检查清单审查，产出 review 记录。
5. **测试反馈**：跑单测/集成测试 → 失败信息回喂 AI 修正。

### 10.2 全栈质量 Review 检查清单（每篇 Review 必含）
- [ ] **分层与架构**：是否越界（handler 出现 GORM？service 出现 Context？）
- [ ] **错误与并发**：错误是否包装？goroutine 是否有退出路径（ctx）？共享状态是否加锁？
- [ ] **性能与SQL**：是否有 N+1？索引是否命中？事务内是否有网络调用？
- [ ] **KISS 原则**：是否引入了不必要的设计模式/抽象？
- [ ] **基础卫生**：输入是否校验？敏感数据是否脱敏？是否有硬编码？
- [ ] **AI 幻觉**：调用的库函数/参数是否真实存在（对照官方文档）？

### 10.3 交付物目录
```text
docs/ai-vibe-coding/
├── WORKFLOW.md              # 流程文档
├── prompts/
│   ├── system-rules.md      # 全局规则提示词
│   └── preconsult_v1.md     # AI预问诊 system prompt
└── reviews/                 # ≥5篇，如 01-skeleton.md, 02-redis-lua.md...
```

> ⟂ **实际文件名不同**：`reviews/01-m1-skeleton.md` … `06-m6-records-sync.md`（**6 篇**，覆盖 M1–M6）。
> 另外 `prompts/system-rules.md` 与 `prompts/preconsult_v1.md` **只留指针、不放正文**：
> 全局规则在仓库根 `AGENTS.md`，运行期提示词在 `backend/prompts/`（`go:embed` 不允许跳出包目录）。
> 这两处分别是第 5、6 处刻意偏离，见 `docs/HANDOVER.md` §4。

---

## 11. 测试与部署

### 11.1 测试策略
- **单元测试**：testify + miniredis + sqlmock。覆盖 service 层核心逻辑（抢号状态机、Lua扣减、LLM校验降级）。
  > ⟂ **实际没有用 `sqlmock`**（`go.mod` 里没有这个依赖）。分层做法是：
  > service 只依赖**使用方声明的小接口**，测试注入内存 fake —— 所以单元测试**不需要数据库**。
  > SQL / Lua 的语义另由**直测真实 MySQL/Redis 的集成测试**保障（fake 里的 `SETNX` 只是一行 map 赋值，
  > 写错成 `SET` 也发现不了）。现状：268 单元 + 67 集成，见 `docs/FIXES.md` §1。
- **集成测试**：httptest + 真实依赖。覆盖核心 API happy path 与越权校验。
- **压力测试**：hey/wrk 验证抢号并发指标。
  > ⟂ 实际用 `cmd/loadtest`（Go 自写并发压测）—— hey/wrk 测不出超卖，见 `AGENTS.md` §3.1。

### 11.2 部署方案（Docker Compose）
- **服务编排**：mysql, redis, backend (多阶段构建), frontend (nginx)。
- **网络与端口**：mysql/redis 不映射宿主机端口（仅内部网络），frontend 暴露 80 端口。
- **SSE 支持**：Nginx 配置 `proxy_buffering off; proxy_read_timeout 300s;`。
- **Makefile**：提供 `make dev`, `make test`, `make bench` 等快捷命令。
  > ⟂ **没有 Makefile**（本机也没有 `make`）。任务入口是根目录 `package.json` 的
  > `nub run <script>` —— 这是第 4 处刻意偏离。清单见 `AGENTS.md` §4。
  > 实际起全栈的命令是 `nub run stack:init` → `stack:up` → `stack:seed`，见 `deploy/README.md`。

---

## 12. 里程碑与交付清单

> ⟂ **本节是"历史计划（as-of v2.0）"，不是现状。** 实际的阶段划分、交付物与剩余工作
> 见 `docs/HANDOVER.md` §1.1（提交时间线）与 `docs/FIXES.md` §1 / §7（指标与风险）。

### 12.1 里程碑
- **M1 (W1)**：骨架与认证。compose环境、分层骨架、中间件链、JWT/RBAC。
- **M2 (W2)**：预约与数据管理。抢号全流程、cron同步、游标分页、EXPLAIN优化。
- **M3 (W3)**：AI预问诊与前端。SSE对话、Schema校验降级、Vue3全部页面。
- **M4 (W4)**：打磨与交付。单测补齐、压测报告、README、演示视频。
> 实际执行到 **M6**（病历处方 / HIS 同步 / 审计 / 医生工作台补全），另有两轮全量代码审视与一轮工程质量基建。

### 12.2 最终交付 Checklist
- [x] 可运行：一键起全栈（实现为 `nub run stack:up`，不是根目录 `docker compose`）。
- [x] 代码质量：后端分层无越界、`golangci-lint`（v2）零 error、前端 `eslint` + `typecheck` 通过。
- [x] AI 证据：`docs/ai-vibe-coding/`（WORKFLOW + prompts 指针 + **6 篇** reviews）。
- [x] 性能证据：`docs/benchmark/`、`docs/sql-optimization.md`（EXPLAIN/EXPLAIN ANALYZE 对比）。
- [ ] 演示：3-5 分钟录屏（注册→抢号→AI预问诊→医生查看→管理看板）—— **唯一未做的一项**。

---

> ⟂ **以下"下一步行动建议"是 v2.0 立项时的开场白，早已完成，仅作历史留存。**
>
> ~~确认此 Spec v2.0 后，我们可以直接开始 **M1 启动包** 的生成，包括：~~
> 1. ~~调整后的 `prompts/system-rules.md`（全栈质量约束提示词）~~
> 2. ~~基础 `docker-compose.yml` 与 `init.sql`~~
> 3. ~~后端项目骨架代码（main.go / 中间件链 / 分层目录结构）~~
>
> **现在的入口**：`docs/README.md`（文档索引）、`AGENTS.md` §1（立刻要做的三件事）。

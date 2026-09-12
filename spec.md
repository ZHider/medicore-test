# MediCore · 医疗数据中台与AI预问诊平台
## 总体详细规格说明书（Overall Spec v2.0）

> **文档定位**：本文档是整个项目 Spec-Driven Development 的“单一事实来源”（Single Source of Truth）。后续所有 AI 代码生成、人工 Review、测试验证均以本 Spec 为基准。本项目的核心目标是展示**扎实的全栈工程能力**与**高效的 AI Vibe Coding 实践**。

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

---

## 5. 数据库设计

### 5.1 核心表结构（DDL 草案）

```sql
-- 用户表（三角色共用，敏感字段加密）
CREATE TABLE users (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  role          ENUM('admin','doctor','patient') NOT NULL,
  name          VARCHAR(64)  NOT NULL,
  phone_enc     VARBINARY(128) NOT NULL COMMENT 'AES-GCM加密手机号',
  phone_hash    CHAR(64)     NOT NULL COMMENT 'SHA256(phone+salt)，用于唯一约束和登录',
  password_hash VARCHAR(72)  NOT NULL,
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
  version    INT NOT NULL DEFAULT 0 COMMENT '乐观锁版本号',
  UNIQUE KEY uk_doctor_date_slot (doctor_id, sched_date, time_slot),
  KEY idx_date_status (sched_date, status)
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
  KEY idx_user_created (user_id, created_at DESC) -- 游标分页索引
) ENGINE=InnoDB;

-- AI 预问诊报告表
CREATE TABLE ai_pre_reports (
  id             BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  appointment_id BIGINT UNSIGNED NOT NULL,
  structured_json JSON NOT NULL COMMENT 'LLM结构化输出（经Schema校验）',
  conversation   JSON NOT NULL COMMENT '完整对话记录',
  llm_model      VARCHAR(64) NOT NULL,
  prompt_version VARCHAR(16) NOT NULL,
  created_at     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
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
3. **MySQL 事务落库**：INSERT 预约记录 + UPDATE 排班（乐观锁 `WHERE id=? AND remaining > 0`）。
4. **补偿机制**：若 MySQL 事务失败，Lua 回补 INCR + 删除幂等键。
5. **过期释放**：cron 每分钟扫描 `expire_at < NOW()` 的 pending 订单，置为 expired 并回补号源。

### 7.2 AI 预问诊流程（LLM 可靠性设计）

1. **SSE 流式对话**：前端 `fetch` + `ReadableStream`，后端透传 LLM stream。必须处理 `ctx.Done()` 防止 goroutine 泄漏。
2. **结构化总结与降级**：
   - 对话结束触发 LLM 总结（要求输出 JSON）。
   - 后端使用 `jsonschema` 校验。
   - **降级策略**：校验失败重试1次；仍失败或超时，则保存原始对话文本，`structured_json` 置 null，标记 `fallback=true`，医生端展示原始对话。
3. **Prompt 版本化**：System Prompt 存于 `backend/prompts/preconsult_v1.md`，版本号入库，便于追溯。

### 7.3 模拟 HIS 数据同步
- `pkg/mockhis/` 内置 JSON 数据源。
- 同步策略：upsert by `external_id`，使用 `errgroup` 并发处理（限并发 5）。
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

---

## 11. 测试与部署

### 11.1 测试策略
- **单元测试**：testify + miniredis + sqlmock。覆盖 service 层核心逻辑（抢号状态机、Lua扣减、LLM校验降级）。
- **集成测试**：httptest + 真实依赖。覆盖核心 API happy path 与越权校验。
- **压力测试**：hey/wrk 验证抢号并发指标。

### 11.2 部署方案（Docker Compose）
- **服务编排**：mysql, redis, backend (多阶段构建), frontend (nginx)。
- **网络与端口**：mysql/redis 不映射宿主机端口（仅内部网络），frontend 暴露 80 端口。
- **SSE 支持**：Nginx 配置 `proxy_buffering off; proxy_read_timeout 300s;`。
- **Makefile**：提供 `make dev`, `make test`, `make bench` 等快捷命令。

---

## 12. 里程碑与交付清单

### 12.1 里程碑
- **M1 (W1)**：骨架与认证。compose环境、分层骨架、中间件链、JWT/RBAC。
- **M2 (W2)**：预约与数据管理。抢号全流程、cron同步、游标分页、EXPLAIN优化。
- **M3 (W3)**：AI预问诊与前端。SSE对话、Schema校验降级、Vue3全部页面。
- **M4 (W4)**：打磨与交付。单测补齐、压测报告、README、演示视频。

### 12.2 最终交付 Checklist
- [ ] 可运行：`docker compose up -d --build` 一键起全栈。
- [ ] 代码质量：后端分层无越界、`golangci-lint` 零 error、前端 `eslint` 通过。
- [ ] AI 证据：`docs/ai-vibe-coding/` 完整（WORKFLOW + prompts + ≥5篇 reviews）。
- [ ] 性能证据：`docs/benchmark/` 压测报告、`docs/sql-optimization.md`（EXPLAIN对比）。
- [ ] 演示：3-5 分钟录屏（注册→抢号→AI预问诊→医生查看→管理看板）。

---

> **下一步行动建议**：
> 确认此 Spec v2.0 后，我们可以直接开始 **M1 启动包** 的生成，包括：
> 1. 调整后的 `prompts/system-rules.md`（全栈质量约束提示词）
> 2. 基础 `docker-compose.yml` 与 `init.sql`
> 3. 后端项目骨架代码（main.go / 中间件链 / 分层目录结构）

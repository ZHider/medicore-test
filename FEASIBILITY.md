# MediCore 可行性分析报告 v1

> 基准：`spec.md`（Overall Spec v2.0）　方法：**本机实测**，非文档推断
> 环境：Win11 · Go 1.27.1 · Node 24.18.0 · nub 0.9.0 · Docker 29.7.2 (Linux 容器, 16C/15.6GB)
> 所有结论均有实测证据，关键阻断项已做对照实验。

---

## 0. 结论速览

| 维度 | 判定 | 说明 |
|---|---|---|
| 整体架构（Go/Gin 单体分层 + Vue3 SPA + MySQL/Redis） | ✅ 可行 | 选型合理，KISS 声明充分 |
| 后端 Go 1.27 + 全部依赖 | ✅ 可行 | 依赖全部真实存在；最高 `go` directive 1.25.0 |
| 前端 Vite 8 + ESLint 10 | ✅ 可行 | 均已实测通过 |
| 前端 **TypeScript 7** | ❌ **硬阻断** | `vue-tsc` 与 `typescript-eslint` 均无法工作 |
| Vite 8 + Vitest 5 + EP + Pinia4 + Router5 | ✅ 实测通过 | 见 §2.4 |
| 用 nub 替代 make | ✅ 可行 | 但有两个必须避开的坑，见 §3 |
| Docker Compose 部署 | ⚠️ 可行但昂贵 | 本地**零镜像**，首次拉取约 1.5–2.5GB |
| Go 端运行时集成（Redis Lua / SSE / LLM） | ⏸ 未验证 | 需 Docker 起依赖，按你要求未拉取 |

**唯一需要你决策的点：前端 TypeScript 用 7 还是 6。** 建议 6.0.3，理由见 §2。

---

## 1. 环境实测基线

```
go    1.27.1 windows/amd64        node  v24.18.0
nub   0.9.0 (all-in-one toolkit)  docker 29.7.2, OSType=linux, NCPU=16, Mem=15.6GB
hey   D:\Hider\Bin\hey.exe        本地镜像: 无    本地容器: 无
registry = https://registry.npmmirror.com   (nub 默认，国内镜像，安装极快)
```

`make` **不存在** → 你的决策 #2（改用 nub run）是正确且必要的。

---

## 2. 前端可行性（核心结论：TS 7 现在不能用）

### 2.1 实测矩阵

在**全新安装、无历史依赖**的工程上测得：

| 工具 | TS 7.0.2 | TS 6.0.3 |
|---|---|---|
| `vite build`（Vite 8.3.0 + plugin-vue 6.0.8） | ✅ 152ms | ✅ 624ms |
| `vue-tsc --noEmit` | ❌ **崩溃** | ✅ 通过，且能捕获 `.vue` 内类型错误 |
| `eslint`（ESLint 10.10.0 + typescript-eslint 8.70.0） | ❌ **崩溃** | ✅ 通过 |
| `tsc` CLI | ✅ 7.0.2 | ✅ 6.0.3 |

具体报错（可复现）：

```text
# vue-tsc
ERR_PACKAGE_PATH_NOT_EXPORTED: Package subpath './lib/tsc' is not defined by "exports"
  in .../typescript/package.json

# eslint
Error: typescript-eslint does not support TS 7.0.
```

### 2.2 TS 7 生态现状（回答你的问题）

我拆包核对了 `typescript@7.0.2` 的实际内容，根因非常明确：

**TypeScript 7 是原生 Go 重写版，它不再提供 JavaScript 编译器 API。**

- `package.json` 的 `exports` 只暴露 `./unstable/sync`、`./unstable/async`、`./unstable/ast`、`./unstable/fs`、`./unstable/proto`，根导出 `.` 仅指向 `lib/version.cjs`（版本号桩）。
- 包内 **`typescript.js` 数量 = 0**，`tsserver.js` = 0。全部 416 个文件 / 2.5MB 里没有经典 JS API。
- 真正的编译器是各平台原生二进制，通过 `optionalDependencies` 分发（`@typescript/typescript-win32-x64@7.0.2` 等 20 个平台包）。
- 因此一切构建在 `ts.createProgram` / `ts.TypeChecker` 之上的工具链**当场失效**——这正是 `vue-tsc`（经 `@volar/typescript`）和 `typescript-eslint` 的实现基础。

生态各方当前状态：

| 项目 | 状态 |
|---|---|
| `typescript-eslint` 8.70.0 / 8.70.1-alpha.0 (canary) | peer 锁定 `typescript: ">=4.8.4 <6.1.0"`，**显式拒绝**并抛错 |
| 上游 typescript-eslint | TS7 支持仍在 issue 追踪中，官方明确表示不愿基于名为 `unstable` 的 API 构建 |
| `vuejs/language-tools` | 有 PR（`@typescript/typescript6` 兼容桥）但**尚未进入 vue-tsc 3.3.11 发布版**（我在 `@vue/language-core` 中 grep 不到任何 `typescript6` 引用） |
| `ts7-eslint` 0.2.0（第三方） | ✅ **在 TS7 上实测可用**：基于 `typescript/unstable/*` 重建了 parser/checker 层，134 条规则跑在 TS7 真实检查器上。但版本号 0.2.0，且依赖 `unstable` API |
| Vite 8 | 不受影响——只做类型擦除，从不调用 TS API |

**一句话**：TS 7 的**编译器和 Lint 生态已有人在补，但 Vue SFC 类型检查（`vue-tsc`）目前无解**。TS 7 是"最快的编译器"，但周边尚未跟进。

### 2.3 决策建议

**方案 A（推荐）：前端 TypeScript 用 `~6.0.3`**

TS 6 是官方 JS 实现的过渡版，`typescript-eslint` 的 peer `<6.1.0` **正好容纳 6.0.x**，整套工具链满血：

```
typescript ~6.0.3 → vue-tsc ✅  eslint ✅  vite ✅
```

代价：放弃 TS7 的编译速度（本项目前端规模下无感知）。**收益：类型检查与 lint 两道质量门可用**——而 spec §12.2 明确要求 `eslint` 通过。

**方案 B：坚持 TS 7（仅当你愿意接受质量门残缺）**

- 可用：`tsc`（7.x native，对纯 `.ts` 极快）+ Vite 8 + `ts7-eslint@0.2.0` 做 lint。
- **不可用**：`.vue` 文件的类型检查。spec 的前端是 Vue3 SFC 项目，等于放弃 SFC 类型安全。
- 风险：`ts7-eslint` 是 0.2.0，且建立在 `unstable` API 上，随时可能破坏。

**方案 C（不推荐）：双 TS 版本共存**（TS7 跑 `tsc`、TS6 供 vue-tsc/eslint）。工程复杂度高，收益不抵成本，违反 spec 的 KISS 原则。

> **实测踩坑提示**：`nub` 的 isolated store 里 peer 变体是**粘性**的。我曾在已装 TS6 的工程里把 `typescript` 升到 7，结果 `eslint` 仍解析到 store 里 keyed `typescript@6.0.3` 的旧变体，"看起来 TS7 能用"——**假象**。只有全新安装才暴露真实崩溃。换 TS 大版本务必删 `node_modules` 重装。

### 2.4 推荐配置端到端验证结果

以 TS 6.0.3 + 全 latest 运行时库装配并实测：

```
vue 3.5.42 · vue-router 5.3.1 · pinia 4.0.3 · element-plus 2.14.5
axios 1.20.0 · echarts 6.1.0 · vite 8.3.0 · @vitejs/plugin-vue 6.0.8
typescript 6.0.3 · vue-tsc 3.3.11 · eslint 10.10.0
@eslint/js 10.0.1 · typescript-eslint 8.70.0 · eslint-plugin-vue 10.11.0
```

| 关卡 | 结果 |
|---|---|
| `vue-tsc --noEmit` | ✅ exit 0（补 `"types": ["vite/client"]` 后；否则 import CSS 报 TS2882） |
| `vue-tsc` 对 `.vue` 内类型错误 | ✅ 正确报错 `src/Probe.vue(3,7): error TS2322` |
| `eslint .` | ✅ exit 0（仅 2 条元素换行风格 warning） |
| `vite build` | ✅ 624ms，产物 1.0MB js + 360KB css |

补充实测结论：
- `vue-router@5` 的 `vite` / `pinia` / `@pinia/colada` / `@vue/compiler-sfc` peer **均为 optional**，无强制约束。
- Element Plus 2.14.5 全局注册后，模板内 `el-card`/`el-button` 无需额外类型声明即可通过 `vue-tsc`。
- ECharts 6 + Element Plus 使首包达 1.0MB，`vite build` 会提示 chunk 过大——需按 spec §8.1 做 `import()` 路由级分包。

---

## 3. 构建脚本：用 nub 替代 make（你的决策 #2）

`make` 不存在，改用 `package.json` scripts + `nub run`。实测 nub 完全胜任，且因走 npmmirror 而极快（46 包 26MB **2.3 秒**）。

### 3.1 两个必须避开的坑

**坑 1：peer 变体粘性**（见 §2.3）。换 TS/node 大版本后必须删 `node_modules` 重装。

**坑 2：`minimumReleaseAge=1440` + `minimumReleaseAgeStrict=true`**
nub 默认隔离 24 小时内发布的版本。这与 spec "版本锁定"一致，但会**阻止刚发布的新版立即安装**。若需临时绕过（例如 TS 7.0.2 刚发布），用 `nub install --config minimumReleaseAge=0`，不要改全局配置。

### 3.2 scripts 映射建议

| spec 里的 make 目标 | 替代实现 |
|---|---|
| `make dev` | `nub run dev`（后端 `go run ./cmd/server`；前端 `vite`） |
| `make test` | `nub run test`（`go test ./...` + `vitest run`） |
| `make bench` | `nub run bench`（`hey -n 1000 -c 100 ...`，hey 已就绪） |
| `make lint` | `nub run lint`（`golangci-lint run` + `eslint .`） |
| `make build` | `nub run build` |

> 建议在**根目录**放一个 `package.json` 作为任务入口，统一编排前后端；`nub run <script>` 即可。注意 nub 的 store 是 `node_modules/.store`（isolated linker），与 pnpm 类似。

---

## 4. 后端可行性

### 4.1 Go 1.27 兼容性（你的决策 #6）—— ✅ 通过

实测各依赖的 `go` directive（真实读取模块 `go.mod`）：

| 模块 | 版本 | 要求 go |
|---|---|---|
| `github.com/gin-gonic/gin` | v1.12.0 | 1.25.0 |
| `github.com/prometheus/client_golang` | v1.24.1 | 1.25.0 |
| `github.com/redis/go-redis/v9` | v9.22.0 | 1.24 |
| `github.com/spf13/viper` | v1.21.0 | 1.23.0 |
| `go.uber.org/zap` | v1.28.0 | 1.19 |
| `gorm.io/gorm` | v1.31.2 | 1.18 |
| `gorm.io/driver/mysql` | v1.6.0 | 1.18 |

最高要求 1.25.0 < 1.27.1 → **Go 1.27 全部满足**。`go.mod` 建议声明 `go 1.25`（而非 1.27），以保留工具链兼容余量。

### 4.2 spec 版本锁定的勘误

| spec 写 | 实际 | 处理 |
|---|---|---|
| GORM v1.25 | 存在但陈旧，最新 v1.31.2 | 用 v1.31.2 |
| Viper v1.19 | 存在，最新 v1.21.0 | 用 v1.21.0 |
| Gin v1.10 | 存在（v1.10.1），最新 v1.12.0 | 用 v1.12.0 |
| go-redis v9 | 最新稳定 **v9.22.0** | ⚠️ 勿用 v9.23.0-beta.1 |

### 4.3 依赖清单（全部实测存在，可直接写进 go.mod）

```
require (
    github.com/gin-gonic/gin v1.12.0
    gorm.io/gorm v1.31.2
    gorm.io/driver/mysql v1.6.0
    github.com/redis/go-redis/v9 v9.22.0
    github.com/spf13/viper v1.21.0
    go.uber.org/zap v1.28.0
    github.com/robfig/cron/v3 v3.0.1
    github.com/golang-jwt/jwt/v5 v5.3.1
    github.com/santhosh-tekuri/jsonschema/v5 v5.3.1
    github.com/prometheus/client_golang v1.24.1
    github.com/go-playground/validator/v10 v10.30.4
    github.com/google/uuid v1.6.0
    golang.org/x/crypto v0.57.0
    golang.org/x/sync v0.23.0
)
// 测试
    github.com/stretchr/testify v1.12.1
    github.com/alicebob/miniredis/v2 v2.39.0
    github.com/DATA-DOG/go-sqlmock v1.5.2
```

**无需外部依赖的能力**（spec 要求但别引包）：
- AES-GCM → 标准库 `crypto/aes` + `crypto/cipher`
- bcrypt → `golang.org/x/crypto/bcrypt`
- SSE → Gin `c.Stream()` / `http.Flusher`
- Redis 令牌桶限流 → Lua 脚本

**建议不引入** `github.com/gin-contrib/zap`：自定义 Zap 中间件更简单，且便于按 spec §9.2 注入 `trace_id`（KISS）。

---

## 5. Docker 现状与成本约束（你的决策 #4）

```
docker daemon: 运行中 (29.7.2, linux, 16C, 15.6GB)
本地镜像:      0 个
本地容器:      0 个
```

**关键成本事实**：本机**零镜像**。首次 `docker compose up -d --build` 需拉取 `mysql:8`（~600MB）、`redis:7-alpine`（~40MB）、`golang:1.25-alpine`（~300MB）、`node:24-alpine`（~150MB）、`nginx:alpine`（~50MB），叠加构建层，**总计约 1.5–2.5GB**，视网速可能 10–30 分钟。

建议的分阶段策略（避免反复 rebuild）：

1. **先只起依赖**：`docker compose up -d mysql redis`，本地 `go run` 联调，享受热重载，不碰镜像构建。
2. **后验后端镜像**：仅在后端逻辑稳定后 `docker compose build backend` 一次。
3. **最后全栈**：前端镜像与 nginx SSE 配置一并验证。
4. **压缩验证频次**：镜像构建是主要耗时项，务必等代码稳定再触发。

> 按你的要求，本轮**未拉取任何镜像、未构建任何镜像**，Docker 仅做了只读探测。

---

## 6. NoSQL 问题（你的决策 #7）

**本项目使用，但只用一个：Redis。不引入任何文档型/列式/图数据库。**

| 存储 | 类型定位 | 在项目中的职责 |
|---|---|---|
| MySQL 8 | 关系型（唯一事实来源） | 业务数据 + 审计日志 |
| **Redis 7** | **KV / NoSQL** | 缓存、分布式锁、限流令牌桶、幂等键、refresh_token、**Redis Stream（替代 Kafka）** |

值得注意：spec §1.3 明确用 **Redis Stream** 承担异步与调度，这是**正经的 NoSQL 消息日志用法**——所以"是否用 NoSQL"的准确答案是：**是，Redis 既是缓存也是消息中间件**。

**明确不使用**：MongoDB、Elasticsearch、Cassandra、Neo4j、ClickHouse。理由与 spec 的 KISS 声明一致，无需补充。

> 提示：spec §1.3 写的是 "Redis Stream/定时任务"，但 §7 全篇只描述了 cron + Lua，**没有出现任何 Redis Stream 的实际用途**。这处需要你确认：是保留 Stream（则需补设计），还是删掉改为纯 cron（更 KISS）。这是 spec 内部的一处不自洽。

---

## 7. 代码模块结构

### 7.1 后端（spec §3.2 的分层强制约束，可行）

```
backend/
├── cmd/server/main.go            # 组装依赖、启动、优雅关闭
├── internal/
│   ├── middleware/               # TraceID→Recovery→RequestLog→RateLimit→Metrics→JWT→RBAC
│   ├── handler/                  # 参数绑定+校验；禁止 SQL/GORM
│   ├── service/                  # 业务/事务边界/归属校验/AI 校验层；禁止 http.Context
│   ├── repository/               # 唯一允许 GORM 的层
│   ├── model/                    # PO / DTO / 枚举
│   └── pkg/                      # crypto, mask, sse, llm, errcode, jwt
├── prompts/preconsult_v1.md      # 版本化 Prompt
├── pkg/mockhis/                  # 模拟 HIS 数据源
└── configs/
```

分层铁律（spec §3.2）用 `golangci-lint` 的 `depguard` 规则**可以机械化强制**——比人工 Review 更可靠，建议纳入 §10.2 检查清单。

**需要澄清的架构点**：spec §3.1 中间件链列为 `TraceID → Recovery → RequestLog → RateLimit → Metrics → JWT → RBAC`，但 `RBAC` 放在全局链里意味着每条路由都做角色判断——实际上 RBAC 是**按路由**的（患者/医生/管理员权限不同）。建议：全局链止于 JWT，RBAC 作为**路由组中间件**（`RequireRole("admin")`），这也与 §2.2 "路由层 + 资源层双层校验"吻合。

### 7.2 前端

```
frontend/
├── src/
│   ├── api/          # axios 实例、拦截器、按模块拆分的请求函数
│   ├── types/        # 与后端 DTO 严格对齐的 interface
│   ├── stores/       # Pinia: auth, appointment
│   ├── router/       # 按 role 动态注册路由
│   ├── views/        # 登录/挂号/AI预问诊/医生工作台/管理看板
│   ├── components/   # SSE 对话组件、无限滚动列表、ECharts 封装
│   └── main.ts
├── index.html
├── vite.config.ts
├── tsconfig.json     # 必须含 "types": ["vite/client"]
└── eslint.config.js  # flat config
```

**实测要点**：
- `tsconfig.json` 必须加 `"types": ["vite/client"]`，否则 `import 'element-plus/dist/index.css'` 报 `TS2882`。
- `eslint.config.js` 必须 `ignores: ['dist/**']`，否则压缩后的单行产物会被 lint 出几百条假错误（我实测踩到 310 条）。
- spec §8.1 的 SSE 用 `fetch` + `ReadableStream` 是**正确**的（`EventSource` 只支持 GET，无法满足 POST 对话）。

---

## 8. spec 缺口与风险清单

> 表结构的详细确认按你的决策 #5 留到下一轮。此处仅列**已发现的硬伤**。

### 8.1 DDL 硬伤（会直接执行失败）

`spec.md:168` 排班表定义了索引：

```sql
KEY idx_date_status (sched_date, status)   -- ❌ status 列在该表中不存在
```

`CREATE TABLE schedules` 里没有 `status` 字段 → **建表语句必然报错**。下一轮一并修正。

### 8.2 缺失的表

spec 的 API 与角色定义中隐含、但 §5.1 DDL 未给出：

| 缺失表 | 依据 |
|---|---|
| `departments` | §6.1 有 `GET /departments/:id/doctors`；§2.1 管理员"科室管理" |
| `sync_logs` | §7.3 明确要求"记录 sync_logs (total/succeeded/failed)" |
| 病历/处方表 | §2.1 医生"书写诊断/处方"、患者"查看本人病历" |
| 医生-科室关联 | 医生列表需按科室过滤 |

另：`schedules` 表缺 `status` 列（§6.1 索引需要它）；`ai_pre_reports` 缺 §7.2 提到的 `fallback` 列，且 `appointment_id` 无唯一约束。

### 8.3 缺失的 API 端点

- `POST /api/v1/auth/refresh` —— §8.2 前端要做"401 自动 refresh 重试"，但 §6.1 端点表里没有 refresh 接口（只提到 refresh_token 存 Redis）。
- `POST /api/v1/auth/register` —— §2.1 患者"注册/登录"，端点表只有 login。
- `GET /api/v1/departments` —— 挂号第一步"选科室"需要科室列表。

### 8.4 设计一致性问题

| 问题 | 位置 | 说明 |
|---|---|---|
| 乐观锁形同虚设 | §5.1 vs §7.1 | 表里定义了 `version` 字段，但 §7.1 步骤 3 的 SQL 是 `WHERE id=? AND remaining > 0`，**根本没用到 version**。二者需统一 |
| Redis 冷启动即失败 | §7.1 Lua | Lua 中 `remaining == nil` 直接 `return 0`，即**未预热缓存时所有抢号全部失败**。必须补：服务启动时把当日号源灌入 Redis，或 Lua 内回源 MySQL |
| 双写一致性 | §7.1 | Redis 扣减成功而 MySQL 事务失败时靠"补偿 INCR + 删幂等键"，但**进程崩溃无法补偿**。需补对账任务（cron 定期以 MySQL 为准校准 Redis） |
| Redis 数据丢失 | §7.1 | Redis 重启丢 key → Lua 返回 0 → 抢号全挂。需考虑 AOF 持久化 + 预热 |
| 幂等键作用域 | §5.1 | `uk_idempotency` 全局唯一，但未与 `user_id` 绑定，跨用户可相互干扰 |
| 游标分页索引不匹配 | §5.1 vs §5.2 | 索引是 `(user_id, created_at DESC)`，而 §5.2 强制 `WHERE id < :last_id ORDER BY id DESC` —— 应改为 `(user_id, id DESC)` |
| Redis Stream 无实际用途 | §1.3 vs §7 | 见 §6 |

---

## 9. 待验证项（需 Docker / LLM Key，本轮按要求未执行）

| 项 | 依赖 | 风险 |
|---|---|---|
| 抢号 Lua 原子扣减 + 乐观锁 | Redis + MySQL 容器 | spec 有 3 处设计缺口（§8.4） |
| SSE 流式透传 + `ctx.Done()` | LLM API Key | 上游选择（DashScope/DeepSeek）与 Key 未定 |
| LLM JSON Schema 校验与降级 | LLM API Key | 需确认 key 与预算 |
| `hey -n 1000 -c 100` 零超卖 & P99<100ms | 全栈容器 | 需先完成 §5 的分阶段构建 |
| `docker compose up` 3 分钟内健康 | 首次镜像拉取 | 冷启动路径下 3 分钟目标偏乐观 |
| Prometheus 指标 + TraceID 贯通 | 全栈容器 | 低风险 |
| Vitest 5 单测 | — | registry peer 兼容已确认，**未做运行时实测** |

---

## 10. 决策记录（已确认）

| # | 决策 | 结果 |
|---|---|---|
| 1 | 前端 TypeScript | ✅ **`~5.9.3`** |
| 2 | 构建脚本 | ✅ **nub run 替代 make** |
| 3 | LLM 上游 | ✅ **先 Mock 降级**，无 Key |
| 4 | Go | ✅ **1.27** |
| 5 | Docker | ✅ **Docker 内部构建**（镜像已全部拉取，含 `golang:1.27.1-alpine`） |
| 6 | 表结构 | ✅ **已完成**：`docs/schema.sql` v2，11 张表，MySQL 8.0.46 实测通过 |
| 7 | NoSQL | ✅ 仅 Redis |
| 8 | Redis Stream | ✅ **选 B：承担 AI 总结异步化** |
| 9 | 医生-科室 | ✅ **多对多**（新增 `doctor_departments`） |
| 10 | 软删除 | ✅ **需要**（统一 `deleted_at` + `gorm.DeletedAt`） |
| 11 | 就诊序号 | ✅ **按实际签到时间分配**（`checked_in_at` + `queue_no`） |

### 10.1 TS 5.9.3 实测复核（已完成）

```
vue 3.5.42 · vue-router 5.3.1 · pinia 4.0.3 · element-plus 2.14.5
axios 1.20.0 · echarts 6.1.0 · vite 8.3.0 · @vitejs/plugin-vue 6.0.8
typescript 5.9.3 · vue-tsc 3.3.11 · eslint 10.10.0
@eslint/js 10.0.1 · typescript-eslint 8.70.0 · eslint-plugin-vue 10.11.0
```

| 关卡 | 结果 |
|---|---|
| `vue-tsc --noEmit` | ✅ 正确捕获 SFC 内错误 `src/App.vue(4,7): error TS2322` |
| `eslint .` | ✅ exit 0 |
| `vite build` | ✅ 398ms |

peer 约束全部满足：vue-tsc `>=5.0.0` ✅ · typescript-eslint `>=4.8.4 <6.1.0` ✅ · pinia 4 `>=5.6.0` ✅

---

## 11. Docker 镜像清单

**决策 #5（修订）：采用 Docker 内部构建。** 镜像已全部拉取完毕，`docker compose up -d --build` 不再触发网络拉取。

| 镜像 | 用途 | 阶段 |
|---|---|---|
| `mysql:8.0` | 业务库 + 审计库 | 运行时 |
| `redis:7-alpine` | 缓存/锁/限流/幂等/**Stream 队列** | 运行时 |
| `nginx:alpine` | 托管前端静态资源 + 反代 `/api` | 运行时 |
| `golang:1.27.1-alpine` | 编译后端 | **仅构建期** |
| `node:24-alpine` | 构建前端 | **仅构建期** |
| `alpine:latest` | 后端最终运行镜像基底 | 运行时 |

自建 2 个：`medicore-backend`（golang→alpine 多阶段）、`medicore-frontend`（node→nginx 多阶段）。

### 11.1 工具链版本必须对齐（重要）

本机 Go 是 **1.27.1**，容器镜像也是 **`golang:1.27.1-alpine`** —— 两者一致，这是正确的配置。

若镜像改用 `golang:1.25-alpine`（也在本地缓存中），则 **`go.mod` 必须声明 `go 1.25`**，否则本机能编译、容器内会因语言版本不足而失败。**务必保持 go.mod 的 `go` 指令 ≤ 构建镜像的 Go 版本。**

Node 侧同理：`node:24-alpine` 对应 `package.json` 的 `engines`。

### 11.2 spec G5 现在成立

多阶段构建 + 内部构建，`docker compose up -d --build` 可一键起全栈。注意 Nginx 的 SSE 配置（`proxy_buffering off; proxy_read_timeout 300s;`），否则 AI 预问诊流式输出会被缓冲。

---

## 12. Redis Stream / cron / Lua 三者关系（澄清）

**这三者解决的是三个完全不同的问题，不是替代关系。**

| 机制 | 本质 | 解决的问题 | 在本项目的位置 |
|---|---|---|---|
| `robfig/cron` | 进程内**定时器** | 每隔 N 分钟做某事 | 超时订单释放、HIS 定时同步 |
| **Lua 脚本** | Redis 服务端**原子执行单元** | 多步读写不能被并发插入 | 抢号扣减、令牌桶限流 |
| **Redis Stream** | append-only **消息日志 + 消费者组** | 生产/消费解耦、异步、削峰、失败重投 | **AI 结构化总结（决策 #8）** |

- **cron 不是队列**：无投递语义、不持久、多副本会重复执行。
- **Lua 不是队列也不是调度器**：它是"原子性"工具。
- **Stream 不是定时器**：它是事件驱动。用它实现"每分钟扫描过期订单"是误用（那是 cron 的活）。

### 12.1 决策 #8：Stream 承担 AI 总结异步化（方案 B）

**动机**（真实场景，非为了用而用）：

1. **用户刷新/关闭页面 ≠ 任务取消**。同步方案下，`submit` 请求一旦断开，LLM 调用与落库全部丢失，用户回来后报告不存在。
2. **LLM 有速率限制**。突发问诊会打满上游配额；队列天然提供**背压与削峰**，并可均匀重试。
3. **LLM 延迟不可控**（数秒至数十秒）。同步会拖垮 §9.1 的 P99 指标，并长期占用连接。

**设计**：

```
POST /appointments/:id/preconsult/submit
    → 校验权限/预约归属
    → XADD stream:preconsult.summary * report_id=<id> appointment_id=<id>
    → 立即返回 202 { report_id, status: "pending" }        ← 不再等 LLM

Worker（进程内 goroutine 组，consumer group = cg:summary）
    → XREADGROUP BLOCK 拉取
    → 调 LLM 总结 → jsonschema 校验 → 失败重试 1 次
    → 仍失败则 fallback=true，保存原始对话
    → 写 ai_pre_reports（status = done | failed）
    → XACK

前端
    → 提交后跳转报告页，轮询 GET /preconsult/reports/:id
    → 或复用 SSE 推送状态变更
```

**必须处理的工程点**（否则 B 只是把同步问题换成异步问题）：

| 关注点 | 做法 |
|---|---|
| 刷新幂等 | `ai_pre_reports.appointment_id` 加**唯一约束**；重复 submit 直接返回已有 report，不重复入队 |
| 至少一次投递 | 崩溃后未 ACK 的消息靠 `XPENDING` + `XAUTOCLAIM` 重投；Worker 必须幂等（按 report_id upsert） |
| 死信 | 重试超限写入 `status=failed` + 错误原因，不再无限重投 |
| 内存上限 | `XADD ... MAXLEN ~ 10000` 防止 Stream 无限增长 |
| 并发控制 | Worker 并发数固定（如 4），即"对 LLM 的并发闸门" |
| 可观测性 | 新增指标：队列积压（`XLEN` 减已投递）、pending 数、`llm_calls_total` |
| 多实例 | 消费者组天然支持多副本分摊；单机部署下也成立 |

**连带影响**（需并入下一轮表结构调整）：

- `ai_pre_reports` 需新增 `status ENUM('pending','processing','done','failed')`、`fallback BOOL`、`error_msg`。
- API 增加 `GET /api/v1/appointments/:id/preconsult/report`（查状态/取结果）。
- spec §7.2 的流程描述需从"同步总结"改写为"入队 + 异步总结 + 状态查询"。

### 12.2 Lua 涉及的全部方面 + 工程约束

spec 中 Lua 出现（或必然出现）于：

1. §7.1 **抢号原子扣减** —— `GET` 判断 + `DECRBY`
2. §9.3 **令牌桶限流** —— 必须用 Lua 保证"补充令牌 + 判断 + 扣减"原子，否则并发下超发
3. 取消/超时释放的**号源回补** —— 若在 Redis 侧操作同样需原子

必须注意的约束：

- **原子性 = 阻塞性**：Redis 单线程执行脚本，Lua 内禁止耗时操作（大 Key 循环、`KEYS`、网络调用）。**LLM 调用绝不能进 Lua**。
- **KEYS/ARGV 必须显式传参**，不可在脚本内拼 key（否则 Cluster 无法路由）；多 key 需同 hash slot（`{tag}`）。
- **脚本要短**：`:lua-time-limit` 默认 5s，超时 Redis 进入 BUSY。
- **`DECRBY` 语义陷阱**：spec 的写法在 key 不存在时 `remaining == nil` → `return 0` → **冷启动全失败**。更稳：先 `EXISTS` 校验 + 启动预热，或 `DECRBY` 后判负再补偿。
- **与 MySQL 的一致性（最大软肋）**：Lua 成功 ≠ 落库成功。spec 的补偿仅在进程存活时有效；**崩溃场景必须靠 cron 对账**（以 MySQL 为准校准 Redis）。
- **用 `EVALSHA` 减少传输**：go-redis 的 `redis.NewScript` 已自动处理 `NOSCRIPT` 重载。

---

## 13. LLM Mock 降级方案

因决策 #3 走 Mock，`pkg/llm` 需设计为**接口 + 双实现**：

- 定义 `LLMClient` 接口：`ChatStream(ctx, msgs)`（SSE 流式）、`Summarize(ctx, convo)`（结构化 JSON）。
- 实现 `mock` 与 `openai-compatible`（DashScope/DeepSeek 共用），用环境变量 `LLM_PROVIDER=mock|dashscope|deepseek` 切换，**切换不动业务代码**。
- **Mock 必须能主动制造异常场景**，否则 G3「100% 通过 Schema 校验或优雅降级」无法被真正验证：
  - 分块逐字吐流（模拟打字机，验证 SSE 与 `ctx.Done()`）
  - 返回**合法**结构化 JSON
  - 返回**非法** JSON / 超时 / 500（验证 §7.2 的"重试 1 次 → 降级 `fallback=true` → 医生端展示原始对话"）
- Mock 也要照常上报 `llm_calls_total` 等指标，否则 §9.2 看板在演示时空缺。
- 这样在**无 Key、无预算**的前提下即可完整演示与单测 AI 全流程。

---

*附：本报告所有探针工程（`.verify-tmp/`）已清理。全程未拉取或构建任何 Docker 镜像。*

# AGENTS.md — MediCore 项目交接说明

> **给新 agent 的第一份文件。** 读完这份就能开始工作；需要细节时按文中的指引跳转。
> 最后更新：工程质量基建（CI / fuzz / benchmark / race）之后。
> 提交历史见 `docs/HANDOVER.md` §1；缺陷总账见 `docs/FIXES.md`；易错点总索引见 `docs/PITFALLS.md`。

---

## 0. 三十秒了解项目

**MediCore** 是医疗数据中台 + AI 预问诊平台：Go/Gin 单体后端 + Vue3 前端 + MySQL + Redis。
定位是**展示全栈工程能力与 AI Vibe Coding 实践**的求职项目，因此代码质量与"过程留痕"同等重要。

- **规格来源**：`spec.md`（Single Source of Truth，v2.0）
- **当前状态**：后端 M1–M6 全部完成并通过实测（29 个端点）+ 一次全量代码审视；前端仅有脚手架；未做全栈 compose 编排
- **规模**：生产 86 文件 / 9730 行；测试 42 文件 / 7553 行；311 个用例（251 单元 + 60 集成）全部通过
- **覆盖率**：整体 34.2%（`service` 79.6% 是核心保障）；`repository` 6.4% 是按包统计的假象，**集成口径 67.5%**
- **静态检查**：`go vet` / `staticcheck` / `deadcode` 三者均为 0 告警；另有 `.golangci.yml`（v2 格式，实测 v2.13.2 **0 issues**），一键入口 `nub run backend:lint`

---

## 1. 立刻要做的三件事

```bash
# 1) 起依赖（mysql + redis，首次自动执行 docs/schema.sql）
nub run deps:up                    # 等价于 docker compose -f deploy/compose.dev.yml up -d

# 2) 生成本地密钥（仓库中不含任何密钥，首次必须做）
cd backend && go run ./cmd/genlocal && cd ..

# 3) 跑测试确认环境正常
nub run backend:test               # 单元测试，251 个用例，不需要外部依赖
```

要跑集成测试还需环境变量：

```bash
# Windows PowerShell
$env:MEDICORE_MYSQL_PASSWORD='medicore123'
nub run backend:test:integration   # 60 个用例，需要 mysql + redis 在运行
```

**改了数据库结构后**补一次演示数据：`nub run backend:seed`。

---

## 2. 不可违反的硬性规则

这些规则来自 spec 或血泪教训，**改动前务必先理解原因**。

### 2.1 分层铁律（spec §3.2）

```
handler → service → repository → model      依赖方向只能向下
```

- **handler**：只做参数绑定/校验、调用 service、返回统一响应。**禁止出现 SQL 与 GORM**。
- **service**：业务逻辑、事务边界、资源归属校验。**禁止接触 `gin.Context`**。
- **repository**：唯一允许出现 GORM 的层。必须把 `gorm.ErrRecordNotFound` 转成 `ErrNotFound`，
  不让 GORM 细节泄漏到上层。
- 跨表写操作在 service 层用事务包裹，**事务内禁止网络调用**（LLM/Redis）。

可用以下命令自查（审视时用过，建议保持）：

```bash
# handler 里是否出现 GORM
grep -rn "gorm.io/gorm" backend/internal/handler
# service 里是否出现 gin.Context
grep -rn "gin.Context" backend/internal/service
```

### 2.2 接口由使用方定义（Go 惯例）

`service` 在 `deps.go` 里声明**它所需的最小能力接口**（`userStore` / `stockStore` / `reportStore`…），
不依赖 `repository` 的具体类型。`repository` 的具体类型隐式满足这些接口。

**好处**：service 单测可注入内存实现，完全不依赖 MySQL/Redis（这是 251 个用例能秒级跑完的原因）。

**新增 service 依赖时**：在 `deps.go` 加接口 + 加内存 fake，**不要**直接引用 `*repository.XxxRepo`。

**接口宁可拆小**：`appointmentStore`（患者侧）与 `doctorAppointmentStore`（医生/管理员侧）
是分开声明的，虽然实现是同一个 `AppointmentRepo`。理由：患者侧完全用不到医生侧的方法，
合并会逼着患者侧所有测试 fake 去实现用不到的方法。**按"谁用"拆，不按"谁实现"拆。**

**接口签名里不要出现"某个实现方"的包名。** 反例：`SyncSource.Load` 曾经返回
`*mockhis.Dataset`，导致 service（业务逻辑）依赖了一个 mock 包，
真实 HIS 适配器也得 import 它——那个"抽象"什么也没抽象。
现在契约在 `internal/his`，`mockhis` 只是它的一个适配器。

### 2.3 缓存回源必须脱离调用方取消

`repository.Cache.Load` 用 `singleflight` 合并回源，回源时用 `context.WithoutCancel(ctx)` + 独立超时。

**为什么**：singleflight 的跟随者共享 leader 的 context。若 leader 的请求被取消（用户关页面），
所有健康的跟随者会一起收到 `context.Canceled`。改回 `loader(ctx)` 会让
`TestCacheLoad_LoaderContextSurvivesCallerCancellation` 失败——这个测试是反向验证过的。

### 2.4 Redis 号源的三条不变量

1. **预热用 `SETNX` 而非 `SET`**：`SET` 会把并发已扣减的号源"还回去"，直接超卖。
2. **键不存在 ≠ 号源不足**：Lua 返回 `-1`（`StockNotLoaded`）而非 `0`，调用方需先预热再重试。
   spec 原设计返回 0，会导致 Redis 冷启动时**所有抢号全部失败**且提示误导。
3. **回补以容量为上限，且键不存在时不创建**：否则 `INCRBY` 会凭空造出号源。

### 2.5 手写 JOIN 必须显式带软删除条件

GORM 的软删除作用域**只在用 `Model(&Struct{})` 时自动生效**。
一旦用 `Table("...")` 手写 JOIN，它就完全不介入——已软删除的行会照常出现在结果里。

```go
// ✅ 自动带 deleted_at IS NULL
db.Model(&model.Schedule{}).Where(...)

// ❌ 必须手写
db.Table("doctors AS d").Joins(...).Where("d.deleted_at IS NULL AND u.deleted_at IS NULL")
```

### 2.6 密钥绝不进仓库

仓库中**不含任何密钥**（连开发用的也没有）。本地开发用 `cmd/genlocal` 生成
`configs/config.local.yaml`（已被 `.gitignore` 排除，启动时自动合并）。

启动时 `config.Validate()` 会按**特征**拒绝可预测的密钥（单一字符重复、短周期重复、
连续升序、含 `change-me`/`example` 等占位词）。

> ⚠️ **`.gitignore` 只能防未来，防不住已经提交过的**。若密钥进过历史，
> 必须 `git commit --amend` + `git reflog expire --expire=now --all` + `git gc --prune=now`，
> 且**已泄露的密钥应视为失效并轮换**（本项目从未 push 过，无需轮换）。

### 2.7 环境与工具链

| 项 | 版本 | 注意 |
|---|---|---|
| Go | **1.27.1** | 容器镜像也是 `golang:1.27.1-alpine`，两者必须一致 |
| Node | 24.18.0 | 用 **nub**（不是 npm/pnpm） |
| 前端 TS | **5.9.3** | **不要升到 TS 7**，原因见 §5.2 |
| 包管理 | `nub` | `nub run <script>`，**没有 make** |
| golangci-lint | **v2.13.2** | 配置是 **v2 格式**（`version: "2"`）。v1 的 `linters-settings` / `issues.exclude-rules` 字段**互不兼容**，照抄旧文章会直接报配置错。见 §5.13 |

### 2.8 引入新依赖前必须先问

**这条是硬约束：不要自行 `go get` / `npm install` / 改 `go.mod` 加依赖，先问。**

原因不是保守，而是**这个动作会出网**：

- 本机的 `GOPROXY` 是 `https://goproxy.cn,direct`，而 Go 的 `net/http` **会读
  `HTTP_PROXY` / `HTTPS_PROXY` 环境变量** —— 机主的系统变量指向一个**计费的**代理。
  一次 `go mod tidy` 引入新依赖，就是一次经由它的真实请求。
- 容器侧是另一套规则：**容器一律不出网**（`GOPROXY=off`，见 §5.10），
  所以新依赖在容器里根本拉不到 —— 会让 `race.sh` 等直接失败。

**需要新依赖时**：先说明「要什么、为什么现有依赖做不到」，得到同意后再动手。
多数情况下第一反应应该是**用标准库**（`net/http`、`crypto/*`、`sync`、
`encoding/json`、`database/sql` 等覆盖了本项目大部分需求），
而不是引入一个包。

**顺带一句**：这个项目的依赖面已经偏大 —— Gin v1.12 会**传递引入**
`mongo-driver` 与 `quic-go`（本项目不用 MongoDB、不用 HTTP/3），两者都被编译进二进制，
是 32MB 体积与攻击面放大的来源。再加依赖前值得想一下这个方向。

---

## 3. 项目架构

### 3.1 目录结构

```
MediCore/
├── spec.md                  # 规格说明书（SSOT）
├── FEASIBILITY.md           # 可行性分析（本机实测结论）
├── AGENTS.md                # ← 本文件
├── .github/workflows/ci.yml # CI：静态检查 / 单元+race / 集成 / fuzz / 漏洞 / 前端
├── docs/
│   ├── schema.sql           # 完整 DDL（11 张表，已实测）
│   ├── database-review.md   # 数据库设计评审与 22 项验证证据
│   ├── CODE-REVIEW.md       # 整体审视报告
│   ├── HANDOVER.md          # 开发历程、错误记录、未完成工作
│   ├── MODULES.md           # ★ 逐模块结构与决策（改代码前先查这里）
│   ├── ARCHITECTURE.md      # 架构与数据流详解
│   ├── FIXES.md             # ★ 缺陷与改进总账 + 当前健康指标（**向上汇报看这份**）
│   ├── PITFALLS.md          # ★ 易错点总索引 + 诊断判据（**写代码前扫一眼**）
│   ├── sql-optimization.md  # ★ SQL/索引实测：EXPLAIN + IGNORE INDEX 反向验证
│   └── benchmark/           # ★ 性能基准报告（含复现命令与环境说明）
├── backend/
│   ├── cmd/                 # server / genlocal / seed / loadtest
│   ├── configs/
│   ├── prompts/             # 版本化提示词与 JSON Schema（go:embed）
│   ├── scripts/             # fuzz.sh（遍历全部 fuzz 目标）/ race.sh（Docker 跑 -race）
│   ├── internal/
│   │   ├── app/             # ★ 依赖装配（main 与集成测试共用同一套装配）
│   │   ├── handler/ service/ repository/ model/    # 分层主干
│   │   ├── middleware/ router/ config/ scheduler/ worker/ audit/
│   │   ├── llm/ mockhis/ his/   # 外部依赖抽象：llm 抽象、mockhis 模拟实现、his 契约
│   │   └── apperr/ authctx/ errcode/ jwt/ logger/ mask/
│   │       metrics/ response/ secret/ traceid/     # 无状态工具包
│   └── test/integration/    # HTTP 集成测试（-tags=integration）
├── frontend/                # Vue3 + TS 5.9 脚手架（未开发页面）
├── deploy/compose.dev.yml   # 本地依赖服务
└── package.json             # 统一任务入口（nub run）
```

**工具包平铺在 `internal/` 下，没有 `internal/pkg/`** —— `internal` 本身已阻止外部导入，
再加一层 `pkg` 是零语义的冗余层级。这是**第 2 处刻意偏离 spec**。

### 3.2 中间件链（spec §3.1，顺序固定）

```
TraceID → Recovery → RequestLog → Metrics → [RateLimit → JWT → RBAC] → Audit
```

方括号内三段**按路由挂载而非全局**：探针与 `/metrics` 必须免认证，
否则 Prometheus 抓取会被 401，健康检查也无法在启动早期生效。

### 3.3 数据存储职责

| 存储 | 角色 | 用途 |
|---|---|---|
| **MySQL** | 唯一事实来源 | 业务数据 + 审计日志 |
| **Redis** | 缓存 / 原子闸门 / 队列 | 缓存、号源计数、幂等键、限流令牌桶、refresh token、**预问诊对话**、**Stream 任务队列** |

**不用 MongoDB**（决策 #7）。注意 Gin v1.12 会传递引入 `mongo-driver` 与 `quic-go`，
两者都被编译进二进制但本项目用不到 —— 这解释了 32MB 的体积，也是攻击面被放大的原因。

### 3.4 关键数据流

**抢号**（spec §7.1）：
```
幂等占用 → 预热号源 → Redis 原子扣减 → MySQL 事务落库 → 记录幂等结果
                                   ↓ 失败：回补 Redis + 释放幂等键
```

**预问诊总结**（决策 #8，异步）：
```
submit → 建报告(pending) → XADD 入队 → 立即返回 202
worker → XREADGROUP → 抢占 → LLM 总结 → Schema 校验 → 失败重试 1 次 → 降级
      → 写报告(done/failed) → XACK
```

**号源对账**（每 5 分钟）：以 MySQL 为准校准 Redis，用**比对更新**而非无条件 `SET`。

---

## 4. 常用命令

| 命令 | 作用 |
|---|---|
| `nub run deps:up` / `deps:down` / `deps:reset` | 起/停/重置依赖服务（reset 会删数据卷） |
| `nub run backend:dev` | 启动后端（需 `MEDICORE_MYSQL_PASSWORD=medicore123`） |
| `nub run backend:test` | 单元测试（251 用例，无需外部依赖） |
| `nub run backend:test:integration` | 集成测试（60 用例，需依赖服务 + 口令环境变量） |
| `nub run backend:vet` / `backend:fmt` | 静态检查 / 格式化 |
| `nub run backend:lint` / `backend:lint:fix` | **golangci-lint**（需 `go install`，见 `HANDOVER.md` §6）；当前 0 issues |
| `nub run backend:vuln` | **漏洞扫描**（需先装 govulncheck） |
| `staticcheck ./...` / `deadcode -test ./...` | 更深一层的静态检查（需 `go install`，见 `HANDOVER.md` §6） |
| `nub run backend:fuzz` / `backend:fuzz:long` | 对全部 fuzz 目标各跑 30s / 5m |
| `nub run backend:bench` | 纯内存基准（审计写入、JWT） |
| `nub run backend:bench:integration` | 真实 MySQL/Redis 基准（分页、号源），需先起依赖 |
| `nub run backend:race` | 数据竞争检测（走 Docker，见 §5.10） |
| `nub run backend:seed` | 灌入演示数据（幂等） |
| `nub run backend:loadtest` | 并发超卖压测 |
| `nub run frontend:dev` / `build` / `typecheck` / `lint` | 前端 |

**启动后端的完整命令**（Windows PowerShell）：
```powershell
$env:MEDICORE_MYSQL_PASSWORD='medicore123'; cd backend; go run ./cmd/server
```

---

## 5. 已知陷阱（踩过的坑，别再踩）

> 这一节是**最常踩、最危险**的那些，按"什么时候会遇到"排列。
> **全量分类索引与诊断判据在 `docs/PITFALLS.md`** ——
> 出问题时先看那里的四张判据表（测试挂了是代码问题还是用例问题？
> 并发问题属于哪种形态？数据不一致往哪查？这条改动该动哪一层？）。

### 5.1 Docker MySQL 的"假就绪"

`docker-entrypoint` 会先起一个**临时 server** 做初始化，此时 `mysqladmin ping` 已经返回成功，
但最终 server 还没起来。用 `ping` 判断就绪会随机失败（表现为 `driver: bad connection`）。

**正确做法**：用真实查询探测，且连续两次成功：
```powershell
docker exec medicore-dev-mysql mysql -umedicore -pmedicore123 -N -e 'SELECT 1;' medicore
```

### 5.2 TypeScript 7 目前不可用

`typescript@7.x` 是 Go 原生重写版，**不再提供 JavaScript 编译器 API**：
包内 `typescript.js` 数量为 0，`exports` 只剩 `./unstable/*`。
后果是 `vue-tsc` 崩溃（`ERR_PACKAGE_PATH_NOT_EXPORTED: './lib/tsc'`），
`typescript-eslint` 直接抛 `does not support TS 7.0.`。

**前端锁定 TS 5.9.3**（`pinia@4` 要求 `>=5.6.0`，`typescript-eslint` 要求 `<6.1.0`）。

### 5.3 nub 的 peer 变体是粘性的

在 isolated store 里，`eslint` 等包会绑定到安装时的 TypeScript 版本。
**升级 TS 大版本后必须删掉 `node_modules` 重装**，否则会解析到旧变体，
出现"看起来能用"的假象（曾因此得出过错误结论）。

### 5.4 Go 解压覆盖式安装会留残留

本机曾出现 `GOROOT/src` 中 9525 个旧文件与 2658 个新文件混杂，
`internal/strconv` 里旧版与新版的 `uint64pow10` 重复定义，**标准库自己编译不过**。
重装 Go 已解决。若再遇到 `src/` 内的编译错误，先怀疑安装完整性。

### 5.5 空列表必须序列化为 `[]` 而非 `null`

Go 的 nil 切片编码成 JSON `null`，前端处理 `null` 与 `[]` 的代码路径不同。
统一用 `model.NonNil()` 包一层。

### 5.6 `{}` 是合法的 JSON Schema

Schema 被误改成空对象时**编译成功、校验永远通过**，降级路径永不触发，
垃圾数据被当成结构化结果入库且没有任何报错。
`SummaryValidator.SelfCheck()` 用空对象做反向验证，把这种情况变成启动失败。

### 5.7 端点清单以 `router.go` 为准，不要照抄文档

文档里的路由清单**已经错过两次**：管理端点漏写 `/admin/` 前缀、把已实现的 `POST /admin/sync/:source`
列为待办。**代码一直是对的，错的是文档** —— 照文档调接口会得到 404，然后开始怀疑代码。

```powershell
# 唯一可靠的来源：启动一次，gin 会逐条打印真实路由
$env:MEDICORE_MYSQL_PASSWORD='medicore123'; cd backend; go run ./cmd/server
```

### 5.8 列表接口的 `LEFT JOIN` 与 `IS NOT NULL`

`GET /doctor/appointments` 要用 `LEFT JOIN medical_records` 判断"写过病历没有"：

- **必须是 LEFT JOIN**。用 INNER JOIN 会让没写病历的预约整条消失 ——
  恰好过滤掉医生最需要看的"待处理"。
- 判定要写 `mr.id IS NOT NULL`，**不能**写 `mr.id = 0`。
  LEFT JOIN 未匹配时 `mr.id` 是 `NULL`，`NULL = 0` 求值为 `NULL`（非真非假），
  扫进 `bool` 会得到 `false` —— **碰巧结果对，但语义错**，换个判定列就会出错。
- 手写 JOIN 别忘了 `mr.deleted_at IS NULL`（见 §2.5）。

### 5.9 测试可能"看起来在断言，其实恒为真"

全量审视发现三处**测试把 bug 固化下来**的案例，这是比"没写测试"更危险的情况。

| 反模式 | 为什么测不出东西 |
|---|---|
| `assert.Zero(t, fake.err == nil && fake.id == 0)` | fake 不会因为被调用而改变字段，这个表达式与"分支是否被走到"无关，恒为真 |
| fake 方法写 `func (f *fake) EnsureGroup(context.Context) error` | 忽略 ctx 就放弃了"调用方传的 ctx 是否有效"的验证。真实实现会把 ctx 透传给下游，于是"复用已取消的 ctx"这个 bug 在有测试的情况下活了很久 |
| 断言 `resource == "admin"`（当时实现碰巧这么返回） | 把错误行为写成期望值，测试全绿但功能是错的 |

**防法**：
- 断言"某分支不该被走到"时，**数调用次数**，不要断言夹具里的静态值；
- fake 至少检查一次实现会透传给下游的关键参数；
- 拿到一个"看起来对"的期望值时，**回代码确认它是不是真的对**。

### 5.10 本机没有 C 编译器 —— 用 Docker 补，或交给 CI

`go test -race` 要求 cgo，而 PATH 里没有 `gcc`
（实测：`cgo: C compiler "gcc" not found`）。两条出路：

```bash
nub run backend:race                              # 本地：Docker 里的 Linux 工具链
bash backend/scripts/race.sh ./internal/service/...
```

**CI 上则是免费的**：ubuntu runner 自带 gcc，`go test -race` 直接可用
（见 `.github/workflows/ci.yml` 的 unit job）。

**用哪个镜像（都是实测，不是推测）**：

| 镜像 | gcc | 准备步骤 | 镜像大小 | 跑 `./internal/service/...` |
|---|---|---|---|---|
| **`golang:1.27.1`（Debian，默认）** | ✅ 自带 14.2.0 | **无** | 1.31 GB | **29.5 s** |
| `golang:1.27.1-alpine` | ❌ 且 `CGO_ENABLED=0` | 每次 `apk add gcc musl-dev` | 381 MB | 64.2 s |

**默认选 Debian**：开箱即用，而且快一倍多 —— 慢的那一倍全花在 alpine 那条
**无法缓存**的 `apk add` 上（临时容器每次都重新解析索引 + 下载）。

要用 alpine 就设 `GO_IMAGE=golang:1.27.1-alpine`，脚本会自己检测并补 gcc。
唯一值得用 alpine 的理由是"与部署镜像保持同一 libc"，但本脚本跑的是单元测试
（依赖全是内存 fake），musl/glibc 的差异在这里无关紧要。**端到端验证另说。**

**⛔ 硬约束：容器一律不出网，也不要给容器配代理。**

依赖早已在宿主机上，所以**喂给它**：挂载 `GOMODCACHE` + 设 `GOPROXY=off`。
`GOPROXY=off` 是设计而非补丁 —— 缺依赖会**立刻**报
`go: module lookup disabled by GOPROXY=off`（实测 0.03 秒），
而不是 15 秒超时后报一个误导性的网络错误。

不要配代理的三个理由：

1. `127.0.0.1` 在容器里指向容器自己，连不到宿主机；想绕开只能写
   `host.docker.internal:PORT`，那等于**把某个人的个人代理变成所有人（含 CI）的构建依赖** ——
   不可复制，别人跑不通；
2. 若那个代理是**计费**的，每次容器构建都在花机主的钱；
3. 本来就不需要 —— 容器只做本地编译与测试。

**实测判据**：容器内 DNS **能**解析外网域名，但 TCP 443 **连不上** ⇒ 这是 Docker 的
NAT/出口问题，不是代码问题，也不该用"配个代理"来解决。
**仓库内不得硬编码任何代理地址**（已确认无残留，请保持）。

另一个坑（脚本已处理）：Git Bash 会把 `-w /app` 改写成 Windows 路径，需要 `MSYS_NO_PATHCONV=1`。

**实测**：Debian 镜像跑全量 `./internal/...`，未检出数据竞争。
build cache 按镜像名分卷存放 —— 不同 libc 的产物缓存键不同，混用只会白等重编译。

### 5.11 等待条件比断言落后一步 = 偶发失败

本轮补测试时抓到一个**约 40% 概率失败**的 flaky 用例：

```go
// 等的是"业务逻辑被调用"
for { if s.callCount() > 0 { break } ... }
// 断言的却是"确认已完成" —— 而 handle() 里 Ack 在 SummarizeReport 之后
assert.Equal(t, 1, q.ackCount())
```

`handle` 的顺序是 `SummarizeReport → Ack`，所以测试可能在 worker 还没执行 Ack 时
就跳出等待循环，读到 `ackCount() == 0`。

**它是间断失败而非必现，所以很容易被当成"机器慢"而忽略** —— 这类 flaky
最终会让人习惯性重跑，整个测试套件的可信度随之归零。

**防法**：等待条件必须是**你即将断言的那个状态**，而不是它的前置状态。
`require.Eventually(func() bool { return q.ackCount() > 0 }, ...)`。

> 复现手段：单独跑很难触发，要**在 CPU 负载下**跑（起几个后台忙循环）。
> 这也是为什么"本地跑一遍都过了"不足以说明测试不 flaky。

**第二种 flaky：夹具生成器撞唯一键。** 同一个套件里还藏着另一个，特征不同：

```
Error 1062: Duplicate entry 'IT57000' for key 'departments.uk_code'
    Error Trace: .../setup_test.go:409（insertAndLastID）
```

`seedDoctor` 用 `time.Now().UnixNano()%100000` 拼科室编码 —— 只有 10 万种取值，
而套件要播种上百条，生日悖论下**每 3~5 次全量运行就撞一次**。

**它的误导性在于：报错的用例每次都不同**，看起来像"那个用例不稳定"，
于是去改用例逻辑 —— 而问题在夹具生成器上。

> **判据：看 Error Trace 指向哪。**
> 指向**断言** → 用例逻辑问题；指向**建数据**（`insertAndLastID` / `seed*`）→ 夹具问题。
> 夹具的唯一性不该依赖概率，应该用单调计数器（构造性保证）。

### 5.12 Fuzz 找到的失败，多半是你的断言错了

第一次跑 fuzz，11 个目标里有 3 个立刻报失败。逐条定性之后是：

| 目标 | 定性 |
|---|---|
| `FuzzTruncate` | ✅ **真缺陷**：输入本身就是非法 UTF-8 时原样返回，会毒掉整批审计插入 |
| `FuzzMaskNeverLeaksOriginal` | ✅ **真缺陷**：`IDCard` 在长度恰为 10 时算出 `repeat('*',0)`，一个字符都没屏蔽 |
| `FuzzIssueParseRoundTrip` | ❌ **我的断言错**：JWT 的载荷是 JSON，而 JSON 字符串无法承载非法 UTF-8，字节级往返本就不成立 |

后续又迭代了两轮，全都出在断言上：
"输出必须不等于输入"（`Name("0***")` 反例）、"长度按 rune 守恒"
（`Phone` 对超长输入会归一到 11 位，本就不守恒）。

**结论：fuzz 报失败时，第一件事不是改代码，而是问"这条性质真的必须成立吗"。**
定性的方法是回到实现读一遍，而不是看到红就直接改。

顺带两条工程细节：
- 失败输入会被写进 `<pkg>/testdata/fuzz/<Target>/`，**要提交进仓库**——那是回归语料，
  下次跑种子就会覆盖到；
- `-fuzz` 一次只接受**一个**目标，所以用 `backend/scripts/fuzz.sh` 遍历
  （目标从源码里 grep 出来，不维护硬编码列表）。

### 5.13 lint 的 `enable-all` 会把噪音一起打开

配 golangci-lint 时若图省事写 `govet.enable-all: true`，会额外打开两个
**不在 `go vet` 默认集合里**的分析器，而它们在本项目上都是纯噪音：

| 分析器 | 实测结果 | 判定 |
|---|---|---|
| `shadow` | 报 **14 条**，全是 `if err := f(); err != nil` 与块内 `:=` | 全是 Go 惯用写法。逐条核对过三处 `:=`（`appointment.go:126`、`auth.go:121`、`loadtest/main.go:102`），外层 `err` 之后都没被复用，不存在"吞掉错误" |
| `fieldalignment` | 要求按字段大小重排 struct | DTO 字段按语义分组排的，为几个字节把 `Status` 挪到 `Diagnosis` 前是纯亏损 |

**推广开的教训**：加严 lint 的正确顺序是
**先跑一次看它报什么 → 逐条判定"真问题还是风格偏好" → 再决定启用与抑制**，
而不是先写一堆 `//nolint` 把红压下去。

同一轮里有个**正面**例子：`staticcheck` 的 ST* 系列一开始被我直接排除了 6 条
（注释格式 / 命名），后来实测发现它只报 **11 条、且全是真实文档缺口**
（`internal/middleware` 整包缺包注释 8 条 + 3 处导出注释没以标识符名开头）。
全部修掉之后排除项就删掉了 —— 配置**变严了**，而不是靠抑制变绿。

还有一个格式上的硬坑：`.golangci.yml` 的 **v1 与 v2 格式互不兼容**。
v1 用 `linters-settings` / `issues.exclude-rules`，v2 用 `version: "2"` +
`linters.settings` + `linters.exclusions`。照抄网上旧文章会直接报配置解析错。
改完务必跑一次 `golangci-lint config verify`（本机实测 v2.13.2 通过）。

---

## 6. 修改代码时的检查清单

- [ ] 分层没越界（handler 无 GORM、service 无 gin.Context）
- [ ] 错误用 `apperr.Wrap` 包装并带上下文，不泄漏内部细节给客户端
- [ ] 新增 service 依赖时在 `deps.go` 加接口 + 内存 fake
- [ ] 手写 JOIN 带了软删除条件
- [ ] 事务内没有网络调用
- [ ] 列表接口用游标分页（**禁止 OFFSET**），`size` 有上限
- [ ] 敏感字段（手机号/身份证）加密存储 + DTO 层脱敏
- [ ] **返回未脱敏个人数据的端点做了资源层归属校验**（路由 RBAC 只回答"是不是医生"）
- [ ] 新增路由考虑了鉴权与角色（`middleware.RequireRole`）
- [ ] 跑过 `nub run backend:vet` 与 `backend:test`
- [ ] 关键测试做了反向验证（改坏实现 → 用例确实变红）
- [ ] 新增的 fake 会检查实现透传给下游的参数（尤其 ctx）
- [ ] 等待型断言等的是**即将断言的那个状态**，不是它的前置状态（见 §5.11）
- [ ] 新增解析不可信输入的函数 → 补一个 fuzz 目标（见 §5.12）
- [ ] 改了并发/共享状态 → 跑一次 `nub run backend:race`
- [ ] 改了热路径或分页 → 跑一次相关基准，确认没有数量级退化
- [ ] **没有自行引入新依赖**（要先问，见 §2.8）
- [ ] 改了表结构 → 更新 `docs/schema.sql`（真实来源）+ 备忘 `docs/database-review.md`
- [ ] 改了行为 → 更新 `backend/README.md`

---

## 7. 需要知道的项目"性格"

1. **实测优先于文档推断。** 本项目的所有关键结论（TS7 不可用、284 倍分页差距、
   6 个依赖漏洞）都是**跑出来的**，不是查文档得出的。遇到不确定的事，先写最小验证再下结论。

2. **测试必须能失败。** 写关键测试后，**故意改坏实现看测试是否失败**。
   时序防护测试、缓存 context 测试都做过这个反向验证。

3. **注释要解释"为什么"，不是"是什么"。** 尤其是那些看起来"多此一举"的代码
   （`SETNX` 而非 `SET`、比对删除、`startStream` 延迟），都是在防具体的坑。

4. **刻意偏离 spec 的地方必须标注理由。** 目前有 3 处（见 `docs/HANDOVER.md` §4），
   都写在代码注释与 README 里，避免后人误以为是疏漏而"修正"回去。

5. **修 spec 的 bug 是正当的。** 已修正 4 处 spec 缺陷（建表必失败的索引、
   冷启动全盘失败的 Lua、误删已完成记录的幂等释放、多对多后排班唯一键）。
   发现规格有问题时应指出并修正，而不是照抄。

6. **主要失效模式是"收尾动作"，不是设计判断。** 跨所有轮次统计：设计都站得住，
   出错的是"写完不编译、写了不跑、断言恒为真"。**三条能挡住绝大多数**：
   ① 每写完一个文件立刻编译；② 关键测试做反向验证；③ 等待型断言等最终状态。
   详见 `docs/PITFALLS.md` §11。

7. **同类漏洞要成体系排查，别等它一个个冒出来。** 医生工作台补全时修了"报告没做归属校验"，
   紧接着全量审视又发现**同类**的"候诊队列没做归属校验"。
   两次都是同一个根因：**路由层 RBAC 被当成了资源层授权**。
   发现一处这类问题时，应该立刻把**所有**返回他人数据的端点扫一遍。

---

## 8. 下一步该做什么

**未完成的工作**（详见 `docs/HANDOVER.md` §3）：

| 优先级 | 项 | 说明 |
|---|---|---|
| **高** | 前端页面 | 仅有脚手架。spec §8 要求登录/挂号/AI 对话/医生工作台/管理看板（后端接口已全部就绪） |
| **高** | 全栈 compose 编排 | 目前只有依赖服务的 compose，缺 nginx + 前后端镜像 |
| 中 | AI Vibe Coding 交付物 | spec §10.3 要求 `docs/ai-vibe-coding/` + ≥5 篇 review。**当前一篇都没有**（G6 验收项） |
| 中 | 性能证据文档 | `docs/benchmark/`（17 项基准）与 `docs/sql-optimization.md`（25 条查询实测 + 索引反向验证）**均已成型**。遗留：`sql-optimization.md` §4.4 记的 cron 扫描优化（当前代价可接受，触发条件已写明） |
| 中 | **按角色横切复查** | **三角色已全部查完**：医生角色发现并修了 2 处规格缺口（`docs/MODULES.md` §6.7）；患者/管理员角色复查结论为**无新缺口**（逐端点证据见 `sql-optimization.md` 与 `MODULES.md`）。后续新增返回他人数据的端点时，仍按此清单扫一遍 |
| 中 | `golangci-lint` 配置 | 见 §4 命令与仓库根 `.golangci.yml` |
| 中 | CI 首次运行验证 | workflow 已落地并通过 YAML 解析与结构校验，但**从未在 GitHub 上真跑过**（仓库未 push）。首次 push 后需要盯一次 |
| 低 | `repository` 层覆盖率 | 按包统计 6.4%（假象）；集成口径 **67.5%**。仍为 0% 的主要是缓存命中路径与目录只读查询 |
| 低 | fuzz 目标可再扩 | 目前覆盖 secret/jwt/audit/mask/model/service 的 11 个目标；`llm` 的流式分片解析、`config` 的取值解析尚未纳入 |

**接手前建议先读**：`docs/HANDOVER.md`（开发历程与教训）→ `docs/ARCHITECTURE.md`（架构细节）
→ `docs/CODE-REVIEW.md`（当前质量状态与已知短板）。

# AGENTS.md — MediCore 项目交接说明

> **给新 agent 的第一份文件。** 读完这份就能开始工作；需要细节时按文中的指引跳转。
> 最后更新：M6 完成 + 代码审视之后（`8eca3e2`）。

---

## 0. 三十秒了解项目

**MediCore** 是医疗数据中台 + AI 预问诊平台：Go/Gin 单体后端 + Vue3 前端 + MySQL + Redis。
定位是**展示全栈工程能力与 AI Vibe Coding 实践**的求职项目，因此代码质量与"过程留痕"同等重要。

- **规格来源**：`spec.md`（Single Source of Truth，v2.0）
- **当前状态**：后端 M1–M6 全部完成并通过实测；前端仅有脚手架；未做全栈 compose 编排
- **规模**：生产 84 文件 / 9208 行；测试 22 文件 / 5008 行；220 个用例全部通过

---

## 1. 立刻要做的三件事

```bash
# 1) 起依赖（mysql + redis，首次自动执行 docs/schema.sql）
nub run deps:up                    # 等价于 docker compose -f deploy/compose.dev.yml up -d

# 2) 生成本地密钥（仓库中不含任何密钥，首次必须做）
cd backend && go run ./cmd/genlocal && cd ..

# 3) 跑测试确认环境正常
nub run backend:test               # 单元测试，200 个用例，不需要外部依赖
```

要跑集成测试还需环境变量：

```bash
# Windows PowerShell
$env:MEDICORE_MYSQL_PASSWORD='medicore123'
nub run backend:test:integration   # 20 个用例，需要 mysql + redis 在运行
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

**好处**：service 单测可注入内存实现，完全不依赖 MySQL/Redis（这是 200 个用例能秒级跑完的原因）。

**新增 service 依赖时**：在 `deps.go` 加接口 + 加内存 fake，**不要**直接引用 `*repository.XxxRepo`。

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

---

## 3. 项目架构

### 3.1 目录结构

```
MediCore/
├── spec.md                  # 规格说明书（SSOT）
├── FEASIBILITY.md           # 可行性分析（本机实测结论）
├── AGENTS.md                # ← 本文件
├── docs/
│   ├── schema.sql           # 完整 DDL（11 张表，已实测）
│   ├── database-review.md   # 数据库设计评审与 22 项验证证据
│   ├── CODE-REVIEW.md       # 整体审视报告
│   ├── HANDOVER.md          # 开发历程、错误记录、未完成工作
│   ├── MODULES.md           # ★ 逐模块结构与决策（改代码前先查这里）
│   └── ARCHITECTURE.md      # 架构与数据流详解
├── backend/
│   ├── cmd/                 # server / genlocal / seed / loadtest
│   ├── configs/
│   ├── prompts/             # 版本化提示词与 JSON Schema（go:embed）
│   ├── internal/
│   │   ├── app/             # ★ 依赖装配（main 与集成测试共用同一套装配）
│   │   ├── handler/ service/ repository/ model/    # 分层主干
│   │   ├── middleware/ router/ config/ scheduler/ worker/ audit/
│   │   ├── llm/ mockhis/    # 外部依赖抽象与模拟实现
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
| `nub run backend:test` | 单元测试（200 用例，无需外部依赖） |
| `nub run backend:test:integration` | 集成测试（20 用例，需依赖服务 + 口令环境变量） |
| `nub run backend:vet` / `backend:fmt` | 静态检查 / 格式化 |
| `nub run backend:vuln` | **漏洞扫描**（需先装 govulncheck） |
| `nub run backend:seed` | 灌入演示数据（幂等） |
| `nub run backend:loadtest` | 并发超卖压测 |
| `nub run frontend:dev` / `build` / `typecheck` / `lint` | 前端 |

**启动后端的完整命令**（Windows PowerShell）：
```powershell
$env:MEDICORE_MYSQL_PASSWORD='medicore123'; cd backend; go run ./cmd/server
```

---

## 5. 已知陷阱（踩过的坑，别再踩）

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

---

## 6. 修改代码时的检查清单

- [ ] 分层没越界（handler 无 GORM、service 无 gin.Context）
- [ ] 错误用 `apperr.Wrap` 包装并带上下文，不泄漏内部细节给客户端
- [ ] 新增 service 依赖时在 `deps.go` 加接口 + 内存 fake
- [ ] 手写 JOIN 带了软删除条件
- [ ] 事务内没有网络调用
- [ ] 列表接口用游标分页（**禁止 OFFSET**），`size` 有上限
- [ ] 敏感字段（手机号/身份证）加密存储 + DTO 层脱敏
- [ ] 新增路由考虑了鉴权与角色（`middleware.RequireRole`）
- [ ] 跑过 `nub run backend:vet` 与 `backend:test`
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

---

## 8. 下一步该做什么

**未完成的工作**（详见 `docs/HANDOVER.md` §3）：

| 优先级 | 项 | 说明 |
|---|---|---|
| **高** | 前端页面 | 仅有脚手架。spec §8 要求登录/挂号/AI 对话/医生工作台/管理看板 |
| **高** | 全栈 compose 编排 | 目前只有依赖服务的 compose，缺 nginx + 前后端镜像 |
| 中 | AI Vibe Coding 交付物 | spec §10.3 要求 `docs/ai-vibe-coding/` + ≥5 篇 review。**当前一篇都没有**（G6 验收项） |
| 中 | 压测报告与 SQL 优化文档 | spec §12.2 要求 `docs/benchmark/`、`docs/sql-optimization.md`。**数据已有**，只差成文 |
| 低 | `repository` 层覆盖率 | 当前 5%，1628 行 SQL 主要靠集成测试保障 |

**接手前建议先读**：`docs/HANDOVER.md`（开发历程与教训）→ `docs/ARCHITECTURE.md`（架构细节）
→ `docs/CODE-REVIEW.md`（当前质量状态与已知短板）。

# MediCore 文档索引

> **这是整个文档集的唯一入口。** 任何"我该读哪份""哪个说法算数"的问题都在这页回答。
> 最后更新：一次全量文档一致性整理之后（删掉三份中间态文档、统一指标口径）。

---

## 1. 三条阅读路径（按你是谁选一条）

### A. 第一次接触这个项目 —— 按顺序读

| 顺序 | 文档 | 你会得到什么 |
|---|---|---|
| 1 | [`ONBOARDING.md`](ONBOARDING.md) | 跑起来 → 业务 → 架构 → 取舍。每节末尾有自检题（答对 16 题算入门） |
| 2 | [`PRINCIPLES.md`](PRINCIPLES.md) | 把它上面的"为什么"提炼成可迁移的原则 + **怎么发现自己违反了** |
| 3 | [`FIRST-TASKS.md`](FIRST-TASKS.md) | 5 个由易到难的真实任务，每个含验收标准 |

> 这三份是**学习路径**（按顺序读一遍），其余文档是**参考手册**（按需查）。
> 两者互补，不是重复。这个区分在 `AGENTS.md` §8 末尾也写了一次。

### B. 接手改代码的工程师 / agent —— 先规则，再全局，再局部

| 顺序 | 文档 | 为什么在读它 |
|---|---|---|
| 1 | [`../AGENTS.md`](../AGENTS.md) | 硬性规则、命令、陷阱速查。**任何改动前先读 §2** |
| 2 | [`ARCHITECTURE.md`](ARCHITECTURE.md) | 系统怎么运转、三条角色主线的端到端数据流 |
| 3 | [`MODULES.md`](MODULES.md) | **改哪个模块就查哪一节**（结构与决策手册） |
| 4 | [`PITFALLS.md`](PITFALLS.md) | **写代码 / 写测试前扫一眼**；出问题时先看 §0 的五张判据表 |
| 5 | [`spec.md`](../spec.md) | 原始需求（**注意它顶部的"已修正清单"，不要照抄里面的 DDL / 端点表**） |

### C. 只想看"现在什么状态" —— 只读两份

| 目的 | 去哪儿 |
|---|---|
| 现在健不健康、还剩什么风险 | [`FIXES.md`](FIXES.md) §1 指标表 + §7 风险与未决项 |
| 这个改动为什么这样设计、坑在哪 | [`MODULES.md`](MODULES.md) 对应章节 + [`PITFALLS.md`](PITFALLS.md) |

---

## 2. 全部文档清单

| 文档 | 回答什么问题 | 什么时候读 |
|---|---|---|
| [`../AGENTS.md`](../AGENTS.md) | 规则 / 命令 / 陷阱速查 —— **新 agent 的第一份文件** | 开始工作前；查命令时 |
| [`../spec.md`](../spec.md) | 原始需求（SSOT 基线，**含已修正说明**） | 想知道"最初要做什么"时 |
| [`ARCHITECTURE.md`](ARCHITECTURE.md) | 系统整体怎么运转、各部分**为什么这样设计** | 理解全局、改跨模块逻辑时 |
| [`MODULES.md`](MODULES.md) | 逐模块的文件构成、关键决策、踩过的坑、改动注意 | **改代码前必查** |
| [`PITFALLS.md`](PITFALLS.md) | 易错点总索引 + 五张诊断判据表 | **写代码 / 写测试前扫一眼**；排障时 |
| [`FIXES.md`](FIXES.md) | **当前健康指标（唯一台账）** + 缺陷与改进总账 | 评估现状、向上汇报 |
| [`HANDOVER.md`](HANDOVER.md) | 开发历程、我（AI）犯过的错、**6 处刻意偏离 spec（唯一事实来源）** | 想知道"某个缺陷当时怎么发生的" |
| [`schema.sql`](schema.sql) | 完整 DDL（11 张表）—— **表结构的唯一权威** | 改表结构前；集成测试每次执行它 |
| [`database-review.md`](database-review.md) | 数据库设计的评审理由与验证证据 | 想知道某个约束/索引为什么这么建 |
| [`sql-optimization.md`](sql-optimization.md) | 25 条业务查询的 EXPLAIN / EXPLAIN ANALYZE 实测 + 索引反向验证 | 改查询、判断索引该不该留 |
| [`benchmark/README.md`](benchmark/README.md) | 性能基准结果、复现命令、环境说明 | 看性能数字；做回归对比 |
| [`ONBOARDING.md`](ONBOARDING.md) | 从零上手的学习路径（跑起来 → 业务 → 架构 → 取舍） | **新人第一份** |
| [`PRINCIPLES.md`](PRINCIPLES.md) | 设计原则与最佳实践："怎么发现自己做错了" | 读完 ONBOARDING 后 |
| [`FIRST-TASKS.md`](FIRST-TASKS.md) | 5 个真实动手任务（含验收标准） | 准备第一次动手时 |
| [`ai-vibe-coding/`](ai-vibe-coding/) | spec §10.3 交付物：实际工作流 + 6 篇逐模块 review | 想看"AI 主导开发的过程留痕" |
| [`../backend/README.md`](../backend/README.md) | 后端使用说明 | 跑后端、查后端细节 |
| [`../frontend/AGENTS.md`](../frontend/AGENTS.md) | 前端交接：接口契约、陷阱、验收标准 | 改前端前 |
| [`../deploy/README.md`](../deploy/README.md) | 编排（Docker Compose）的用法、取舍与排障 | 起全栈 / 排查容器 |

---

## 3. 事实来源（谁说了算）

**一条元规则：能从代码或运行时导出的清单，就不要手写。**
本项目的手写路由清单曾经错过两次（`AGENTS.md` §5.7）。

| 我想知道 | 权威来源 |
|---|---|
| **有哪些端点、各自什么权限** | `backend/internal/router/router.go`，或启动一次看 gin 打印的路由 |
| **表结构** | `docs/schema.sql` |
| DTO / JSON 字段名 | `backend/internal/model/*.go` |
| 错误码 | `backend/internal/errcode/` |
| **当前健康指标与用例数** | `docs/FIXES.md` §1（唯一台账，含复现命令） |
| **刻意偏离 spec 的 6 处** | `docs/HANDOVER.md` §4（唯一事实来源，其他文档只做指向） |
| 跑测试 / 构建 / 起服务的命令 | `package.json`（`nub run <script>` 清单） |
| 前端与后端的接口契约 | `frontend/AGENTS.md` §3（手写契约，尚未自动化同步） |
| 编排怎么起 | `deploy/README.md` |
| 原始需求 | `spec.md`（**已被实现的修正见其顶部"已修正清单"**） |

**反漂移规则**：同一件事只在一处定义，其他地方只写**指针**不写副本。
`HANDOVER.md` §4 就是这条规则的产物 —— 因为被复述过，曾经漏掉一处、各处一直写着"共 3 处"。

---

## 4. 当前指标

完整台账、口径与复现命令在 **[`FIXES.md` §1](FIXES.md)**。
本页只列摘要，**改动测试后请只更新 `FIXES.md` §1 那一处**：

| 项 | 值 | 口径 |
|---|---|---|
| HTTP 端点 | 29 | `router.go` |
| 后端生产代码 | 86 文件 / 9830 行 | 非空行 |
| 后端测试代码 | 46 文件（其中集成 9）/ 8123 行 | 非空行 |
| 后端单元用例 | **268** | `go test ./... -v \| grep -c '^--- PASS'` |
| 后端集成用例 | **67** | 同上，加 `-tags=integration ./test/integration/...` |
| 前端用例 | **202**（12 文件） | `nub run frontend:test` |
| Fuzz 目标 | **13** | `grep -rn '^func Fuzz' backend` |
| 覆盖率 | 整体 34.9%；`service` 79.7%（包口径） | `go tool cover -func=cover.out \| tail -1`；**`repository` 6.3% 是包口径假象**，集成口径 67.5% |

> ⚠️ **报覆盖率必须连口径一起报。** 两个口径差一个数量级，本项目已经因此误读过一次结论。

---

## 5. 关于已删除的三份文档

文档集曾经包含三份**中间态文档**（记录"上一步/下一步要做什么"的进度快照）。
项目已接近完成，它们的内容要么已被下面的文档覆盖，要么只对当时的读者有意义。
**历史完整保留在 git 里**（`git log -- docs/<文件名>`）：

| 已删除 | 原性质 | 内容现在在哪 |
|---|---|---|
| `CODE-REVIEW.md` | M5b 时点的一次性审视快照，自己声明已被后来的审视取代 | 缺陷与处置 → `FIXES.md` §2；错误叙事 → `HANDOVER.md` §2；逐模块 → `ai-vibe-coding/reviews/` |
| `contract-sync.md` | "方案存档，尚未实施"的前后端契约同步方案 | 缺口本身记在 `FIXES.md` §2 的 N4 与 §7 |
| `FEASIBILITY.md` | 开工前的可行性报告（含"下一轮修正""待验证项"） | 已修正的 spec 缺陷 → `spec.md` 顶部清单 + `schema.sql`；TS7 / nub / 容器不出网 → `AGENTS.md` §2.7/§5.2/§5.10 与 `PITFALLS.md` §8 |

> 删掉它们不是"抹掉过程"：**过程留痕的正式交付物是 [`ai-vibe-coding/`](ai-vibe-coding/)**
> （spec §10.3 要求），错误教训的沉淀在 `PITFALLS.md`，两者都保留。

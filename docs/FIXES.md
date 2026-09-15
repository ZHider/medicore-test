# 缺陷与改进总账

> **这份文档回答两个问题**：现在健康吗？这一路修了什么、代价多大？
>
> 面向**观测与向上汇报**。想找"哪些地方容易写错"，看 `docs/PITFALLS.md`。
> 想找"当时是怎么发生的"，看 `docs/HANDOVER.md`（按时间线的原始记录）。
> 文档全集与阅读顺序：**`docs/README.md`**。

---

## 一、当前健康指标（一眼看）

> **这一节是整个文档集的唯一指标台账。** `AGENTS.md` §0 与 `docs/README.md` §4 只放摘要，
> 并指回这里 —— **改了测试或基准，只更新这一节**。

*所有数字都是本机实测，口径与复现命令见 §8。*

| 维度 | 数值 | 判定 |
|---|---|---|
| 端点 | **29 条** | 见 `backend/internal/router/router.go`（权威来源） |
| 生产代码 | 86 文件 / 9830 行（非空行） | — |
| 测试代码 | 46 文件（其中集成 9）/ 8123 行 | 测试/生产 ≈ 0.83 |
| 单元用例 | **268** | 全绿 |
| 集成用例 | **67** | 全绿 |
| 前端用例 | 202（12 文件） | `nub run frontend:test` |
| 整体覆盖率 | **34.9%** | 按包口径，见下方说明 |
| `service` 覆盖率 | **79.7%** | spec §11.1 要求的就是 service 核心逻辑 |
| **`repository` 覆盖率** | 包口径 6.3% / **集成口径 67.5%** | 后者才是真实的 |
| 静态检查 | `go vet` / `staticcheck` / `deadcode` | **三者均 0 告警** |
| Lint | `golangci-lint` v2.13.2（`.golangci.yml`） | **0 issues**；入口 `nub run backend:lint` |
| SQL / 索引 | 25 条查询实测 + 3 组 `IGNORE INDEX` 反向验证 | 见 `docs/sql-optimization.md` |
| 数据竞争 | `-race` 全量通过（`./internal/...`，**24 个包结果行**） | **未检出竞争** |
| Fuzz | **13** 个目标 × 30s | 全部通过（回归语料已入库） |
| Benchmark | 分页 / 号源 / 审计 / JWT | 见 `docs/benchmark/`（含环境与"不测什么"） |
| CI | 6 关卡（static / unit+race / integration / fuzz / vuln / frontend） | workflow 已落地，**尚未在 GitHub 真跑过** |
| 依赖漏洞 | 6 → **1**（`GO-2026-5932`，上游无修复且代码未调用） | 已接受 |

### 交付完成度

| 层 | 状态 | 依据 |
|---|---|---|
| **后端** | ✅ 完成 | 29 条端点全部实现并实测，M1–M6 全部完成，spec G2/G3/G4 达成 |
| **前端** | ✅ 完成 | 三角色页面全部实现；202 个 vitest 通过；`typecheck`/`lint`/`build` 零错误零告警。见 `frontend/AGENTS.md` |
| **编排（G5）** | ✅ 完成 | `deploy/compose.yml` 一键起 mysql + redis + backend + nginx，四容器 healthy。见 `deploy/README.md` |

**项目已达"功能完整"**；剩下的是**验证类**工作（浏览器人工验收、编排异常路径等），
不是缺失的功能。完整清单见 §7。

### 覆盖率的两个口径（**报数时必须说清**）

```
按包统计（go test ./... -cover；集成测试是独立包，执行不计入）：
    整体 34.9%   service 79.7%   repository 6.3%   handler 0%

集成口径（-coverpkg=./internal/... 单独统计集成测试）：
    整体 69.7%   repository 67.5%   service 62.4%   handler 77.1%
```

**`repository` 那 6.3% 是假象**，本项目已经因此误读过一次结论
（把 6.3% 当成"数据访问层几乎没测"）。报数时请连口径一起报。

---

## 二、缺陷总账（按严重度）

共 **19 处缺陷**：6 处 HIGH/安全，6 处功能性，7 处一致性/边界。
每一条都注明了**怎么发现的**——这比缺陷本身更有参考价值。

> **H4 与 H5 是同一轮里、由同一个问题牵出来的**：本来只是想搞清楚
> "错误追踪号到底怎么用"，结果发现它**既查不到**（H5）**又不安全**（H4）。
> 两条都不在任何测试的覆盖范围内，而且都**不报错** —— 一条是静默少写数据，
> 一条是功能看起来正常工作（界面显示得好好的），只是查不回来。

### 🔴 高：会导致静默故障或信息泄露

#### H1 · 消费者组重建被自己的 context 掐死

| | |
|---|---|
| **位置** | `internal/worker/summary.go` |
| **症状** | Redis 被 flush 后，worker 反复记录同一条错误日志，**所有总结任务静默卡死**。而这段代码的注释写的正是"要避免任务静默卡死" |
| **根因** | 读队列用的 `ctx` 在 `Read` 返回后立即 `cancel()`，紧接着 `NOGROUP` 分支却复用它调 `EnsureGroup` → 永远返回 `context.Canceled`。**恢复路径从未成功过一次** |
| **修复** | 重建用独立的短超时 context（`recoveryTimeout`） |
| **验证** | **反向验证**：把 bug 注入回去 → `TestLoop_RecreatesMissingConsumerGroup` 变红（断言"重建时 ctx 不应已失效"） |
| **为何长期未发现** | 测试 fake 的 `EnsureGroup(context.Context)` **忽略了 ctx**。忽略参数 = 放弃对该参数的验证 |

#### H2 · 候诊队列没有资源层归属校验（信息泄露）

| | |
|---|---|
| **位置** | `internal/service/appointment.go` `Queue` |
| **症状** | **任何医生换一个排班 ID，就能拿到全院患者的姓名与 `user_id`**（该接口刻意不脱敏） |
| **根因** | 只挂了路由层 `RequireRole(doctor)`。RBAC 只回答"你是不是医生"，回答不了"这个排班是不是你的" |
| **修复** | `Queue(ctx, access, scheduleID)` 校验 `schedule.DoctorID == 本人 doctors.id`（管理员豁免） |
| **验证** | 单测 5 例 + 集成 5 例；反向验证：移除校验后用例变红 |
| **备注** | 与"预问诊报告对医生开放"是**同一类漏洞**，两处相隔不久分别暴露 → 说明当时是逐点修补而非按类别排查 |

#### H3 · `race.sh` 返回成功，但一次都没跑（**证据可信度**类缺陷）

| | |
|---|---|
| **位置** | `backend/scripts/race.sh` / `package.json` 的 `backend:race` |
| **症状** | `nub run backend:race` **1 秒返回、退出码 0、零行测试输出**。35 秒的 `-race` 根本没跑，而调用方收到的结论是"未检出数据竞争" |
| **根因** | 两个独立的坑叠加（详见 `HANDOVER.md` §2.10）：① 脚本按 `uname -s` 分支决定是否做 Windows→POSIX 路径转换，而 `nub` 执行脚本时用的**不是 Git Bash**（`uname -s = Windows_NT`），于是把 `D:/...`、`C:\Users\...` 原样丢给 docker；② 第一版预检只验证"容器起得来"，拦不住主运行被静默吞掉 |
| **修复** | 三处缺一不可：① 路径转换改为**无条件**（幂等），并去掉非可移植的 GNU sed `\L`；② **内容预检** —— `/app/go.mod` 第一行必须是 `module*`；③ **输出兜底** —— 主运行 `tee` 落日志，零行即 `exit 1` |
| **验证** | 反向验证两处：`GO_IMAGE=no-such-image:v0` → exit 1 + 明确报错；nub 路径 → 从静默 exit 0 变成 exit 1 + 排查提示。正规路径 Git Bash 直跑 **36 秒 / 24 个包结果行 / 无 `DATA RACE`** |
| **为何最危险** | 它是本项目唯一一类**不改变任何测试结果**的错误：所有测试照旧全绿，只有"证据的可信度"被掏空。而且它天然符合预期 —— 本来就期望"没发现竞争"，"什么都没跑"恰好也是"没发现竞争" |
| **推广** | 任何**检测类**命令（race / fuzz / lint / vuln）都要问：**"如果它什么都没做，我会看出来吗？"** 看不出来就得加产出断言（判据选**内容**，不选退出码） |

#### H4 · 追踪号可被客户端伪造，用来抹掉整批审计记录

| | |
|---|---|
| **位置** | `internal/middleware/trace.go` + `internal/audit/audit.go` |
| **症状** | 一个**未认证**请求带上超长的 `X-Trace-Id`，就能让包含它的那一批审计记录**整批**插入失败（`BatchSize = 100`，含其他用户的记录） |
| **根因** | 三段串起来才成立：① `TraceID` 是**全局**中间件，排在认证**之前**，`X-Trace-Id` 由客户端完全控制；② 该值被原样写进 `audit_logs.trace_id CHAR(32) CHARACTER SET ascii`；③ 审计是**批量**写入的，MySQL 严格模式下 `Data too long` 让整条多行 `INSERT` 失败 |
| **实测证据** | `ERROR 1406 (22001): Data too long for column 'trace_id' at row 2`，且同批**合法的那一行也没进去**（`rows before = 132 → after = 132`） |
| **修复** | ① 信任边界：`traceid.Sanitize` —— 只有 32 位十六进制才沿用（归一为小写），否则丢弃重生成，**绝不截断**（截断会造出一个"看起来合法"的假追踪号）。② 审计层再走一次同样的净化作为第二道防线，因为失败模式是"带走一批" |
| **验证** | 单测 6 例 + 中间件 5 例 + 集成 4 例；两个新 fuzz 目标（`FuzzSanitize` / `FuzzIsValid`）各跑 20s、合计 **1400 万次执行无失败**；反向验证：`TraceID()` 改回原样透传 → 3 条单测 + 集成用例变红 |
| **为何长期未发现** | 与 `FuzzTruncate` 修的是**同一个失败模式**（那边是 `User-Agent`），`truncate()` 的注释里甚至已经写明"一个构造出来的请求就能带走一批审计记录"。**修法只套到了当时出问题的那一个字段上**，没有回头把所有"客户端可控 + 写入定长列"的字段一起扫一遍 |

#### H5 · 追踪号只显示不查询 —— 文档承诺了一个不存在的功能

| | |
|---|---|
| **位置** | `internal/handler/audit.go` / `internal/service/audit.go` / `internal/repository/audit.go` / `AdminAuditLogsView` |
| **症状** | 界面把追踪号拼进每个错误提示（`号源不足（追踪号 3f2a…）`）并提示"可在后端日志与审计中检索"，但 `/admin/audit-logs` **只认** `resource/action/from/to/user_id` —— 按追踪号查询在这套产品里**根本做不到** |
| **根因** | `audit_logs` 上建了 `KEY idx_trace (trace_id)`，`AuditFilter` 里却没有 `TraceID` 字段，**该索引在 Go 代码中从未被引用过一次**（死 schema）。schema 与查询两侧都按"将来要查"设计过，只是中间那一段没接上 |
| **修复** | 三层接上 `trace_id` 过滤（handler → service → repository），前端审计页加追踪号输入框并在每行加「筛选」动作。格式非法返回 **`40001`**，与 `user_id` 的静默忽略**刻意相反** —— 忽略会返回全部日志，管理员会误判影响面 |
| **验证** | 服务层 5 例（含 5 种非法格式）、集成 6 例（含两条断言"恰好命中 1 条"的闭环用例）；`EXPLAIN` 实测走 `idx_trace`、`type=ref`、`rows=1` |
| **顺带抓到一个恒真用例** | 「按追踪号查恰好命中 1 条」第一版**在删掉仓储层 `WHERE` 之后依然通过**：测试库每次运行从零建库，只发一个请求时未过滤的查询同样只返回 1 条。**是反向验证把它暴露出来的**，不是它自己在报错。修法是刻意造**两条**不同追踪号的记录，让"过滤没生效"必然多出一条 |

#### H6 · 刷新页面必「页面不存在」（路由守卫的判断顺序）

| | |
|---|---|
| **位置** | `frontend/src/router/index.ts` 的 `beforeEach` |
| **症状** | 登录后能正常进管理看板，**一按 F5 就显示「页面不存在」**。三个角色、所有页面都一样 |
| **根因** | 角色路由是登录后才注册的，所以刷新时目标地址**必然先被 `/:pathMatch(.*)*`（catchAll）匹配**。而 catchAll 带着 `public: true`，守卫里 `if (to.meta.public) return true` 排在 `installRoleRoutes()` **之前** → 直接放行 → 角色路由永远装不上 |
| **顺带一处死代码** | 原本用来救这个场景的 `if (justInstalled && to.name === 'not-found') return to.fullPath`，因为上面的提前返回**永远走不到** |
| **修复** | 顺序改为：已登录 → **先装角色路由** → 再判 public。另：未登录遇到 not-found 时**先去登录页**（登录前无法区分"地址不存在"与"需要登录"，直接 404 会给收藏了受保护地址的用户一个撒谎的结论） |
| **验证** | `router.spec.ts` 22 例（三个角色各自的刷新/深链接 + 登出残留 + redirect 分支）；反向验证：把 public 判断挪回最前 → **15 条变红** |

#### H7 · 路由"声明"与"注册"不一致（界面点了没反应）

| | |
|---|---|
| **位置** | 同上，`ROUTES_BY_ROLE` 手写映射 |
| **症状** | **医生工作台的「AI 报告」按钮点不动**；**管理员侧栏的「医生工作台」点不动**。不报错，就是没反应 |
| **根因** | 注册哪些路由由**数组分组**决定，而每个路由自己声明的 `meta.roles` 是另一套。`preconsult-report` 声明 patient/doctor/admin 却只挂在 patient 组；`workbench` 声明 doctor/admin 却只挂在 doctor 组。未注册的 name 被 `router.push` 解析时以"无匹配"**中断导航**，静默失败 |
| **修复** | `routesForRole(role)` 改为从 `meta.roles` **推导**注册集合，分组数组只用于阅读 |
| **验证** | `router.spec.ts` 里一条**不变量**用例：凡 `meta.roles` 声明允许某角色访问的路由，该角色登录后必须 `hasRoute`。反向验证：改回按分组 → 不变用例**直接点名** `preconsult-report 声明 doctor 可访问，却没有为该角色注册` |

#### M6 · 三角色共用视图调用患者专属接口

| | |
|---|---|
| **位置** | `frontend/src/views/ReportView.vue`（`meta.roles` 是 patient/doctor/admin） |
| **症状** | 医生/管理员打开预问诊报告页：**「重新获取」会打 `POST .../preconsult/submit`（仅患者）**、「去完成预问诊」/「返回对话」跳到未注册给他们的 `preconsult-chat`；另外 `listMine` 也是患者专属，白拿一个 403。页面看起来一切正常，点下去 403 或毫无反应 |
| **根因** | 页面最初按患者写，后来被复用到三个角色，但**没有区分"谁能调哪个接口"** |
| **修复** | 加 `isPatient` 判断：非患者不调 `listMine`/`submit`（「重新获取」退化为重新拉取），患者专属按钮按角色收起。`WorkbenchView` 的「书写病历」同理对管理员隐藏 |
| **验证** | `ReportView.spec.ts` 11 例（逐角色断言"不该发的调用没发、不该有的按钮没有"）；反向验证：去掉角色判断 → **6 条变红** |
| **为何长期未发现** | 三个角色的**页面都能正常渲染**，缺陷只在"点击之后的权限"上 —— 静态检查和"页面能打开"都看不出来。这与 H6/H7 是同一类：**没有报错、也没有崩溃，只是功能静默失效** |
#### 环境事实（不是缺陷，但会持续坑人）

- **PATH 上的 `bash` 是 WSL 启动器**（`C:\WINDOWS\system32\bash.exe`），
  本机无 WSL 发行版 → `execvpe(/bin/bash) failed`。可用的是
  `C:\Program Files\Git\bin\bash.exe`。文档里的 `bash xxx.sh` 一律按此理解。
- **`nub run <script>` 用的不是 Git Bash**：实测 `uname -s` 报 `Windows_NT`、`pwd` 报 `D:/...`。
  任何按 `uname` 判断平台行为的脚本都会走错分支。

### 🟠 中：功能性缺陷

#### M1 · 号源回补的钳位会清掉 TTL

| | |
|---|---|
| **位置** | `internal/repository/stock.go` `stockReleaseScript` |
| **症状** | 号源键被钳位后变成**永不过期**；`SETNX` 也不会再刷新它。历史排班又不在 `ListReconcilable`（只扫 `sched_date >= today`）范围内，**这个错值再也不会被对账修正** |
| **根因** | 钳位用裸 `redis.call('SET', key, cap)`，而兄弟脚本 `stockCompareAndSetScript` 是带 `KEEPTTL` 的 —— 不一致，不是策略 |
| **修复** | 加 `KEEPTTL` |
| **验证** | `TestStock_ReleaseClampPreservesTTL`；反向验证：去掉 `KEEPTTL` 后用例变红 |

#### M2 · 「重复抢号」把数据库抖动误报成用户冲突

| | |
|---|---|
| **位置** | `internal/service/appointment.go` `recoverDuplicate` |
| **症状** | 幂等恢复时的查询只要返回**任何**错误，都当成"您已预约该号源，请勿重复抢号"（409）。用户被告知自己重复下单，实际是服务降级；**底层错误既没包装也没日志，完全无法排查** |
| **根因** | `if err != nil` 一刀切，没有区分 `ErrNotFound`（真的冲突）与其它错误 |
| **修复** | `errors.Is(err, ErrNotFound)` → 冲突；其余 → `apperr.Wrap(ErrInternal)` |
| **验证** | 单测覆盖两个分支 |

#### M3 · `FailReport` 恒返回 nil，报告卡在 `processing`

| | |
|---|---|
| **位置** | `internal/service/preconsult_submit.go` |
| **症状** | 落库失败只写日志，worker 以为已标记失败，**报告永远停在 `processing`** —— 正是这段代码要避免的状态 |
| **根因** | `FailReport` 无条件 `return nil`；`saveFailure` 吞掉错误 |
| **修复** | `saveFailure` 返回错误并向上传播 |
| **验证** | 两个调用点都改为检查返回值 |

#### M4 · 审计 UA 截断会切出非法 UTF-8（**两个阶段**）

这条值得单列，因为它**被发现过两次**，第二次比第一次更隐蔽。

| 阶段 | 问题 | 修复 |
|---|---|---|
| 第一次（审视） | 按**字节**硬切会切断多字节字符 | 回退到最近的字符起始字节 |
| 第二次（fuzz） | 即使加上了边界回退，**输入本身就是非法 UTF-8** 时仍原样透传 —— HTTP 头是字节串，客户端可以发 `User-Agent: \xf0`，Go 的 `net/http` 不做 UTF-8 校验 | 截断后再做一次 `ToValidUTF8` |

**后果**：目标列是 utf8mb4，MySQL 严格模式下拒绝非法字节 →
**一条畸形 UA 让整批 100 条审计记录一起插入失败**。

**为什么第二次才发现**：第一次的测试只覆盖了"合法多字节被切坏"，
没覆盖"输入本来就非法"。手工列举用例只能覆盖想得到的情况 —— **这就是需要 fuzz 的地方**。

顺带修掉 `truncate(s, max)` 在 `max <= 0` 时的 slice bounds panic。

#### M5 · `IDCard` 在长度恰为 10 时完全不脱敏

| | |
|---|---|
| **位置** | `internal/mask/mask.go` |
| **症状** | `IDCard("0000000000")` **原样返回**：`repeat('*', len-10)` 算出 0 个星号 |
| **根因** | 守卫是 `len < 10`，而头 6 + 尾 4 之外**至少还要留一位**，真实边界是 `len < 11` |
| **修复** | 改判 `len < 11` → 整体打码（与 `Phone` 的"过短就整体打码"逻辑一致） |
| **可达性** | **当前不可达**（注册接口 `binding` 强制 `len=18`，`UserProfile` 也不返回身份证，且 `IDCard` 在生产代码里**从未被调用**）。属潜在缺陷 —— 但一个叫"脱敏"却会原样返回的导出函数留着迟早被误用 |

### 🟡 低：一致性、边界与文档

| # | 问题 | 修复 |
|---|---|---|
| L1 | `cmd/seed` 用 `time.Now().Truncate(24h)` 截的是 **UTC 零点**；UTC+8 下 08:00 前算出的"今天"早一天，演示排班从昨天开始 | 用本地时区构造当天零点 |
| L2 | `CheckIn` 直接序列化 `*time.Time`，全接口唯一一个 RFC3339 字段，前端要特判 | 统一 `model.APITimeLayout` |
| L3 | `secret/cipher.go` 包注释写成 `Package crypto`，与包名不符（ST1000） | 改正 |
| L4 | `conversation.List` 注释说"跳过损坏条目"，代码其实是整体失败 | 保留"失败"行为并改正注释，说明**为什么不静默跳过**（医疗数据丢一条会让 AI 基于不完整信息下结论） |
| L5 | 审计 `Describe` 把三个管理端点的 `resource` 全记成 `admin`，按资源聚合时混成一条 | 新增 `scopeSegments` 跳过权限域 |
| L6 | `database-review.md` 把"由 service 复活软删除行"写成了已完成，**代码里根本没有**（无 `Unscoped()` / `deleted_at = NULL` / `.Delete()` 调用） | 如实改写 + 记入待办，并注明"加删除功能前必须先实现复活" |
| L7 | 文档把管理端点写成 `/api/v1/audit-logs`（少了 `/admin/`），把已实现的同步端点列为待办 | 已修正，并立规：**路由清单以 `router.go` 或 gin 启动输出为准** |
| L8 | **`secret/password.go` 把 bcrypt 的超长行为说成"会被静默截断"** —— 实测当前依赖 `x/crypto v0.48.0` 的 `GenerateFromPassword` 对 >72 字节**返回 `ErrPasswordTooLong`**，不截断；而**比对路径 `CompareHashAndPassword` 反倒不做长度检查**。原注释的理由写反了 | 改正两处注释（`password.go` + `secret_test.go`），并写明真实理由：生成侧是"与库版本无关的明确错误"，比对侧才是需要守卫的那一侧。**代码行为零改动** |
| L9 | `HANDOVER.md` §4「刻意偏离 spec」表写着"共 3 处"，但**漏了"用 `nub run` 替代 Makefile"**（spec §11.2 明确要求 Makefile，而 `package.json` 描述与 `AGENTS.md` §2.7 都写了它） | 补全为 6 处，并把该表定为**唯一事实来源**；其他文档的复述一律改为指向，消除漂移面 |

### 🟡 低：**未修但已记录**的缺口（本轮 review 发现）

| # | 问题 | 为什么不修 / 何时该修 |
|---|---|---|
| N1 | **`errcode` 三张表（常量 / `messages` / `httpStatus`）的一致性无自动化守护** | 代码注释明确要求"必须同步改，漏改会让错误码落到未知错误/500"，但 `internal/errcode/` **没有测试文件**，这条规则目前只靠人记。**建议补一个测试**：遍历三张表的 key 集合断言三者相等。成本约 20 行 |
| N2 | **HIS 同步不失效科室医生缓存** | 核实：`DepartmentDoctorsCacheKeys` 只在 `cmd/seed` 与测试里被调用，`service/sync.go` 对 cache/redis **零引用**。同步更新医生后，科室医生列表最坏 **5 分钟**内是旧的（由 TTL 兜底）。属已知取舍；若要即时生效，在同步收尾处调用该函数即可 |
| N3 | 审计日志按时间范围查询的**隐含假设** | `sql-optimization.md` Q19（按 `created_at` 范围查）走 `PRIMARY` 而非 `idx_created` —— 这**依赖 `created_at` 与 `id` 同向增长**（审计只追加不更新），当前成立。**若将来引入回填/乱序写入，这个假设会被打破而查询悄悄变慢**，计划也会退化。记录备查 |
| N4 | **前端类型与后端 DTO 之间没有自动化同步机制** | 当前靠 `frontend/AGENTS.md` §3 的手写契约 + 人工对照。**这是刻意接受的取舍，不是遗漏** —— 本项目一贯立场是"生成解决省事、测试解决可信"，而契约测试目前不可用（曾评估过一层零依赖的"契约金文件 + 双向测试"方案，未采纳）。需要时再单独立项 |

---

## 三、结构与重复（不是缺陷，但会持续产生缺陷）

| 项 | 问题 | 处理 |
|---|---|---|
| **`service` 反向依赖 mock 包** | `SyncSource.Load` 返回 `*mockhis.Dataset` —— 业务逻辑包依赖了一个名字里带 mock 的实现包，"接真实 HIS 只需换实现"是假的 | 新增 `internal/his` 契约包；`mockhis` 降为适配器，并负责把"相对今天 N 天"换算成绝对日期，不再泄漏到业务层 |
| `doctorIDOf` 三份实现 | `appointment.go` / `record.go` / `preconsult_submit.go` 各一份，其中两份逐字节相同 | 收敛为 `service/access.go` 的 `resolveDoctorID` |
| 预约"是否占用号源"两处判断 | `(*Appointment).IsActive` 与 `preconsult.go` 内联条件 | 抽出 `model.IsActiveAppointmentStatus`，两处共用 |
| `sync_logs` 的 SELECT 列重复 | 同一文件里写了两遍 | 抽成 `syncLogSelect` 常量 |
| "空串转 nil" 三种写法 | `audit.optionalString`（不 Trim）/ `service.optionalStringPtr`（Trim）/ sync.go 内联 | service 内统一为 `optionalTrimmedString`，并在名字上点明与 audit 版本的差异 |

**死代码清零**（`deadcode` 与 `staticcheck` 现均为 0）：
`mockhis.Dataset.Summary`、`SyncLogRepo.LastFinished`、`MedicalRecordRepo.FindByID`、
`fakeStore.batchCount`、`fakeRecordStore.doctorIDErr`。

**无人使用的"通用性"**：
`SyncDeps.Concurrency`（spec 定死 5，生产从不设）、`UpsertDoctor` 的返回 ID、
`UpsertSchedule` 的 `created`、`runPhase` 那个**永远不可达**的 errgroup 错误通道
（三个 upsert 都返回 nil，读起来却像"失败会中止整批"）。

**从不写入的字段**：
- `sync_logs.trace_id` → **接上**（手动触发时能把同步记录与同一次请求的审计串起来）；
- `audit_logs.detail` → **删除**（连列一起）。从没写过也没读过，而审计的设计前提正是"不存请求体"；
  留一个空 JSON 列只会诱使后人往里塞敏感数据。

**重复逻辑收敛**：见 `docs/PITFALLS.md` 的"重复与抽象"一节。

---

## 四、性能数据（全部本机实测）

完整报告与复现命令：**`docs/benchmark/README.md`**。

| 项 | 数值 | 意义 |
|---|---|---|
| **游标分页 vs OFFSET**（20 万行） | 游标深浅页 **1.01 / 1.21 ms**（与页码无关）；OFFSET 深页 **38.81 ms** | 深页处 **38×**。形状与 `database-review.md` 的"284 倍"一致、量级不同（不同查询与口径），两个都保留并写明条件 |
| **号源扣减**（Redis Lua） | 419 µs；**裸 `PING` 422 µs** | **同量级 ⇒ Lua 开销可忽略，时间全在传输上**。因此"把 Lua 拆成多次调用"是纯损失（往返翻倍 + 破坏原子性） |
| 号源扣减（4 并发） | 131 µs/op ≈ 7,600 ops/s | 瓶颈在往返延迟而非 Redis 本身 |
| 审计 `Record`（每请求） | **275 ns** | 异步批量写的价值：请求路径无数据库往返 |
| 审计 `Record`（缓冲满） | **125 ns**（比正常路径更快） | **背压路径不阻塞**这条设计承诺有了数字 |
| JWT 校验（每请求） | **4.4 µs** | 相对一次 DB 往返（本机 1 ms 量级）可忽略 |
| `-race` 全量 `./internal/...` | Debian 镜像约 36 s，**24 个包结果行** | 服务包 `service` 26.7 s 是长尾；判据见 `AGENTS.md` §5.10.1 |

### 一个被纠正的错误判断

第一次看到号源扣减 419 µs，**以为是 Lua 慢**。加了一个裸 `PING` 基线才定性：
PING 也是 422 µs，**时间几乎全在传输上**。

> 本机是 Windows + Docker Desktop，MySQL/Redis 都经端口转发访问，
> 绝对延迟被环境放大了（同宿主机 Linux 通常几十微秒）。
> **基准的用途是同机对比与回归检测，不是吞吐承诺。**

### `-race` 镜像选择（实测纠正过一次）

| 镜像 | gcc | 准备步骤 | 大小 | `./internal/service/...` |
|---|---|---|---|---|
| **`golang:1.27.1`（Debian）← 默认** | ✅ 自带 14.2.0，`CGO_ENABLED=1` | **无** | 1.31 GB | **29.5 s** |
| `golang:1.27.1-alpine` | ❌ 且 `CGO_ENABLED=0` | 每次 `apk add gcc musl-dev` | 381 MB | 64.2 s |

**一开始想当然选了 alpine**（因为部署镜像就是它），理由写的是"保持同一 libc"。
实测后换掉：那个理由只对端到端验证成立，而本场景跑的是单元测试（依赖全是内存 fake）；
代价却是慢一倍多 —— 慢的全在那条**无法缓存**的 `apk add` 上。

> **教训：把"听起来合理的理由"和"实测的代价"分开看。** 前者容易变成不验证的借口。

---

## 五、工程基建

| 项 | 内容 | 状态 |
|---|---|---|
| **CI** | `.github/workflows/ci.yml`，6 关卡按反馈速度排序 | ⚠️ **从未在 GitHub 真跑过**（仓库未 push） |
| **静态检查** | `go vet` / `staticcheck` / `deadcode` 三者 0 告警；另新增 `.golangci.yml`（v2 格式，**实测 0 issues**）与一键入口 `nub run backend:lint` | ✅ |
| **SQL 优化** | `docs/sql-optimization.md`：25 条查询的 EXPLAIN/EXPLAIN ANALYZE + 3 组索引反向验证（`IGNORE INDEX`） | ✅ |
| **AI Vibe Coding 留痕（G6）** | `docs/ai-vibe-coding/`：WORKFLOW + prompts 指针 + **6 篇**逐模块 review；每篇含 spec §10.2 的六项清单 | ✅ 后端部分（前端 review 仍缺） |
| **Fuzz** | **13** 目标，`backend/scripts/fuzz.sh` 遍历（目标从源码 grep，不维护硬编码列表） | ✅ 全部通过 |
| **Benchmark** | 分页 / 号源 / 审计 / JWT，`docs/benchmark/` | ✅ |
| **`-race`** | `backend/scripts/race.sh`（Docker），CI 上用 ubuntu 原生 gcc | ✅ 通过（24 个包结果行） |
| **任务入口** | `nub run backend:fuzz` / `:fuzz:long` / `:bench` / `:bench:integration` / `:race` / `:lint` | ✅ |

### Fuzz 的净收益

**跑 15 秒就找出 2 个真实缺陷**（M4 的第二阶段、M5）。
同时暴露了 **3 处我自己写错的断言**——这条同样重要，见 `docs/PITFALLS.md`。

---

## 六、flaky 治理

修掉 **2 个**间歇失败。它们的**诊断路径**比修复本身更有价值。

### F1 · 等待条件比断言落后一步（约 40% 失败）

```go
for { if s.callCount() > 0 { break } ... }   // 等"业务逻辑被调用"
assert.Equal(t, 1, q.ackCount())             // 断言"确认已完成"（发生在其之后）
```

`handle` 的顺序是 `SummarizeReport → Ack`，测试可能在 worker 还没 Ack 时就往下走。

### F2 · 夹具生成器撞唯一键（约 1/3~1/5 失败）

```
Error 1062: Duplicate entry 'IT57000' for key 'departments.uk_code'
    Error Trace: .../setup_test.go:409（insertAndLastID）
```

`seedDoctor` 用 `time.Now().UnixNano()%100000` 拼科室编码 —— 只有 10 万种取值，
而套件要播种上百条，生日悖论下必然碰撞。

**它比单个 flaky 用例更坏**：报错的用例**每次都不同**，看起来像"那个用例不稳定"，
很容易被逐个"修"一遍却始终修不好。

**判据（已写入 `docs/PITFALLS.md`）**：
Error Trace 指向**断言** → 用例逻辑问题；指向**建数据** → 夹具问题。

修复后集成测试**连跑 6 次全绿**。

---

## 七、风险与未决项（上报时要说清的部分）

> **这里是"还剩什么没做"的唯一清单。** `AGENTS.md` §8 与 `docs/README.md` 都指向这里。

### 7.1 未完成的工作（**都是验证类，不是缺失的功能**）

| 优先级 | 项 | 说明 |
|---|---|---|
| 中 | **浏览器人工验收** | 前端与编排的结论全部来自 HTTP 层 + 静态检查，**没有在浏览器里打开过**。页面渲染、移动端布局、ECharts 绘制需人工走一遍（清单见 `frontend/AGENTS.md` §7.1）。**但"路由与角色"这一块已不再依赖人工**：`router.spec.ts`（22 例）与 `ReportView.spec.ts`（11 例）覆盖了角色 × 动作，见 §2 的 H6/H7/M6 |
| 中 | 前端三处未被真实数据走到的分支 | `fallback=true` 降级（mock LLM 恒返回 `fallback=false`）、`/readyz` 的 503、`40002` 号源不足 —— 代码照契约写了，但没有被真实数据走到过 |
| 中 | **编排异常路径** | 未测"依赖挂掉"的表现：`docker compose -f deploy/compose.yml stop mysql` 后 `readyz` 是否转 503、backend 是否变 unhealthy |
| 中 | **前端 review 缺失** | spec §10.3 的 G6 交付物目前只有后端 6 篇（M1–M6）。前端页面已完成，但没有对应 review |
| 中 | `golangci-lint` 未纳入 `go.mod` 工具依赖 | 版本号写在 `ci.yml` 而不是 `go.mod` 的 `tool` 指令。等 Go 的 tool directive 或 `mise`/`aqua` 能统一管理时应收敛 |
| 低 | `sql-optimization.md` §4.4 的 cron 扫描 | 当前读 4000 行拿 200 条（1.48 ms）**可接受**。**触发条件已写明**：pending 占比降到 1% 以下时把 `ORDER BY id` 改为 `ORDER BY expire_at` |
| 低 | `audit_logs` 分区 / 归档 | 写入最频繁的表，长期运行需要 TTL 或分区 |
| 低 | 真实 LLM 接入 | `llm.NewClient` 对 `openai`/`dashscope`/`deepseek` **明确返回错误**（刻意设计）。真实厂商的限流、超时、流式分片边界都未被真实验证 |
| 低 | 上游支持 TS7 后升级前端 | 目前锁定 TS 5.9.3，见 `AGENTS.md` §5.2 |
| 低 | fuzz 目标可再扩 | 已覆盖 audit / jwt / mask / model / secret / service / traceid 共 **13** 个；`llm` 的流式分片解析、`config` 的取值解析尚未纳入 |

### 7.2 风险

| 风险 | 说明 | 缓解 |
|---|---|---|
| **CI 未验证** | workflow 只做过 YAML 解析与结构校验，**从未真正执行** | 首次 push 后必须盯一次；`frontend` job 的 `nub` 安装是最可能出问题的一环 |
| **`repository` 直测只压关键不变量** | 缓存命中路径、目录只读查询仍为 0% | 有意取舍：那两类出错代价低 |
| **`handler` 单元覆盖 0%** | 由集成测试保障行为 | 有意取舍，**但不是遗漏**（已在文档多处标明） |
| **性能数据依赖本机环境** | Windows + Docker Desktop 放大了绝对延迟 | 只作同机对比；换机器需重测并更新环境表 |
| **软删除"复活"未实现** | 当前无入口会软删，缺口不可达 | **加管理端删除功能前必须先实现** |
| **前后端契约靠人工同步（N4）** | 改了后端 DTO 不会自动让前端变红 | 见 §2 的 N4；改 `internal/model` 时同时核对 `frontend/src/types/api.ts` 与 `frontend/AGENTS.md` §3 |
| **`GO-2026-5932`** | 上游无修复，代码未调用该路径 | 已接受并记录 |

---

## 八、怎么复现这里的所有结论

```bash
# 一次性验证项目健康度（约 2 分钟）
nub run deps:up
cd backend && go run ./cmd/genlocal && cd ..
nub run backend:test                 # 268 用例
nub run backend:vet                  # 0
$env:MEDICORE_MYSQL_PASSWORD='medicore123'
nub run backend:test:integration     # 67 用例
nub run backend:vuln                 # No vulnerabilities found

# 更深一层（各自几十秒到几分钟）
nub run backend:fuzz                 # 13 个 fuzz 目标
nub run backend:race                 # 数据竞争（走 Docker）
nub run backend:bench                # 纯内存基准
nub run backend:bench:integration    # 真实 MySQL/Redis 基准
```

**文档全集与阅读顺序**：`docs/README.md`（唯一入口）。
`docs/PITFALLS.md` 是易错点总索引。

# 缺陷与改进总账

> **这份文档回答两个问题**：现在健康吗？这一路修了什么、代价多大？
>
> 面向**观测与向上汇报**。想找"哪些地方容易写错"，看 `docs/PITFALLS.md`。
> 想找"当时是怎么发生的"，看 `docs/HANDOVER.md`（按时间线的原始记录）。

---

## 一、当前健康指标（一眼看）

*最后更新：工程质量基建完成之后。**所有数字都是本机实测**，复现命令见各节。*

| 维度 | 数值 | 判定 |
|---|---|---|
| 端点 | 29 条 | spec §2.1/§8 的接口已全部就绪 |
| 生产代码 | 86 文件 / 9730 行 | — |
| 测试代码 | 42 文件 / 7553 行 | 测试/生产 ≈ 0.78 |
| 单元用例 | **251**（240 常规 + 11 个 fuzz 目标） | 全绿 |
| 集成用例 | **60** | 全绿，**连跑 6 次稳定** |
| 整体覆盖率 | **34.2%** | 见下方口径说明 |
| `service` 覆盖率 | **79.6%** | spec §11.1 要求的就是 service 核心逻辑 |
| **`repository` 覆盖率** | 包口径 6.4% / **集成口径 67.5%** | 后者才是真实的 |
| 静态检查 | `go vet` / `staticcheck` / `deadcode` | **三者均 0 告警** |
| Lint | `golangci-lint` v2.13.2（`.golangci.yml`） | **0 issues**；入口 `nub run backend:lint` |
| SQL / 索引 | 25 条查询实测 + 3 组 `IGNORE INDEX` 反向验证 | 见 `docs/sql-optimization.md` |
| 数据竞争 | `-race` 全量通过 | **未检出竞争** |
| Fuzz | 11 目标 × 30s | 全部通过（回归语料已入库） |
| Benchmark | 17 项 | 见 `docs/benchmark/` |
| CI | 6 关卡（static / unit+race / integration / fuzz / vuln / frontend） | workflow 已落地，**尚未在 GitHub 真跑过** |
| 依赖漏洞 | 6 → **1**（`GO-2026-5932`，上游无修复且代码未调用） | 已接受 |

### 后端完成度 vs 项目完成度（**汇报时必须分开说**）

**后端功能完整**：29 条端点全部实现并实测，M1–M6 全部完成，spec G2/G3/G4 达成。

**项目整体未完成** —— spec §12.2 的交付清单里还有两项，**都不属于后端**：

| 未完成项 | 影响 |
|---|---|
| **前端页面**（spec §8：登录/挂号/AI 对话/医生工作台/管理看板） | G1「三角色全流程闭环」只完成了后端一半 —— 患者目前无法真正走完流程 |
| **全栈 compose 编排**（nginx + 前后端镜像） | G5「一键起全栈」未达成；现有 `deploy/compose.dev.yml` 只起 mysql/redis |

**已补齐的交付物**：

| 项 | 内容 |
|---|---|
| 性能证据 | `docs/benchmark/`（17 项基准）+ `docs/sql-optimization.md`（25 条查询 EXPLAIN 实测 + 3 组 `IGNORE INDEX` 反向验证）。注意两份**口径不同、数字不可互相替代**，理由写在 `sql-optimization.md` §0 |
| 代码质量证据 | `.golangci.yml`（v2，**0 issues**）+ `nub run backend:lint` + CI 关卡 |
| **G6 AI Vibe Coding 留痕** | `docs/ai-vibe-coding/`：WORKFLOW + prompts 指针 + **6 篇**逐模块 review（M1–M6）。**性质是事后整理**，每篇标注来源；前端无页面故无前端 review |

**所以："后端基本完成"成立；"项目完成"不成立。** 详见 `MODULES.md` §0.2。

### 覆盖率的两个口径（**报数时必须说清**）

```
按包统计（go test ./...：集成测试是独立包，执行不计入）：
    整体 34.2%   service 79.6%   repository 6.4%   handler 0%

集成口径（-coverpkg 单独统计集成测试）：
    repository 67.5%   service 62.4%   handler 76.6%
```

**`repository` 那 6.4% 是假象**，本项目已经因此误读过一次结论
（把 6.4% 当成"数据访问层几乎没测"）。报数时请连口径一起报。

---

## 二、缺陷总账（按严重度）

共 **14 处缺陷**：2 处 HIGH/安全，5 处功能性，7 处一致性/边界。
每一条都注明了**怎么发现的**——这比缺陷本身更有参考价值。

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
| L9 | `HANDOVER.md` §4「刻意偏离 spec」表写着"共 3 处"，但**漏了"用 `nub run` 替代 Makefile"**（spec §11.2 明确要求 Makefile，而 `package.json` 描述与 `AGENTS.md` §2.7 都写了它） | 补全为 6 处，并把该表定为**唯一事实来源**；`CODE-REVIEW.md` §6.2 的复述改为指向，消除漂移面 |

### 🟡 低：**未修但已记录**的缺口（本轮 review 发现）

| # | 问题 | 为什么不修 / 何时该修 |
|---|---|---|
| N1 | **`errcode` 三张表（常量 / `messages` / `httpStatus`）的一致性无自动化守护** | 代码注释明确要求"必须同步改，漏改会让错误码落到未知错误/500"，但 `internal/errcode/` **没有测试文件**，这条规则目前只靠人记。**建议补一个测试**：遍历三张表的 key 集合断言三者相等。成本约 20 行 |
| N2 | **HIS 同步不失效科室医生缓存** | 核实：`DepartmentDoctorsCacheKeys` 只在 `cmd/seed` 与测试里被调用，`service/sync.go` 对 cache/redis **零引用**。同步更新医生后，科室医生列表最坏 **5 分钟**内是旧的（由 TTL 兜底）。属已知取舍；若要即时生效，在同步收尾处调用该函数即可 |
| N3 | 审计日志按时间范围查询的**隐含假设** | `sql-optimization.md` Q19（按 `created_at` 范围查）走 `PRIMARY` 而非 `idx_created` —— 这**依赖 `created_at` 与 `id` 同向增长**（审计只追加不更新），当前成立。**若将来引入回填/乱序写入，这个假设会被打破而查询悄悄变慢**，计划也会退化。记录备查 |

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
| `-race` 全量 `./internal/...` | Debian 镜像 ~70 s | 见下方镜像对比 |

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

## 五、工程基建（这一轮新增）

| 项 | 内容 | 状态 |
|---|---|---|
| **CI** | `.github/workflows/ci.yml`，6 关卡按反馈速度排序 | ⚠️ **从未在 GitHub 真跑过**（仓库未 push） |
| **静态检查** | `go vet` / `staticcheck` / `deadcode` 三者 0 告警；另新增 `.golangci.yml`（v2 格式，**实测 0 issues**）与一键入口 `nub run backend:lint` | ✅ |
| **SQL 优化** | `docs/sql-optimization.md`：25 条查询的 EXPLAIN/EXPLAIN ANALYZE + 3 组索引反向验证（`IGNORE INDEX`） | ✅ |
| **AI Vibe Coding 留痕（G6）** | `docs/ai-vibe-coding/`：WORKFLOW + prompts 指针 + **6 篇**逐模块 review；每篇含 spec §10.2 的六项清单 | ✅ 后端部分 |
| **Fuzz** | 11 目标，`backend/scripts/fuzz.sh` 遍历（目标从源码 grep，不维护硬编码列表） | ✅ 全部通过 |
| **Benchmark** | 17 项，`docs/benchmark/` | ✅ |
| **`-race`** | `backend/scripts/race.sh`（Docker），CI 上用 ubuntu 原生 gcc | ✅ 通过 |
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

| 风险 | 说明 | 缓解 |
|---|---|---|
| **CI 未验证** | workflow 只做过 YAML 解析与结构校验，**从未真正执行** | 首次 push 后必须盯一次；`frontend` job 的 `nub` 安装是最可能出问题的一环 |
| **`repository` 直测只压关键不变量** | 缓存命中路径、目录只读查询仍为 0% | 有意取舍：那两类出错代价低 |
| **`handler` 单元覆盖 0%** | 由集成测试保障行为 | 有意取舍，**但不是遗漏**（已在文档多处标明） |
| **性能数据依赖本机环境** | Windows + Docker Desktop 放大了绝对延迟 | 只作同机对比；换机器需重测并更新环境表 |
| **软删除"复活"未实现** | 当前无入口会软删，缺口不可达 | **加管理端删除功能前必须先实现** |
| **`GO-2026-5932`** | 上游无修复，代码未调用该路径 | 已接受并记录 |
| **前端仍是脚手架** | spec §8 的页面一个都没做 | 高优先级待办 |

**未完成项完整清单**：`docs/HANDOVER.md` §3。

---

## 八、怎么复现这里的所有结论

```bash
# 一次性验证项目健康度（约 2 分钟）
nub run deps:up
cd backend && go run ./cmd/genlocal && cd ..
nub run backend:test                 # 251 用例
nub run backend:vet                  # 0
$env:MEDICORE_MYSQL_PASSWORD='medicore123'
nub run backend:test:integration     # 60 用例
nub run backend:vuln                 # No vulnerabilities found

# 更深一层（各自几十秒到几分钟）
nub run backend:fuzz                 # 11 个 fuzz 目标
nub run backend:race                 # 数据竞争（走 Docker）
nub run backend:bench                # 纯内存基准
nub run backend:bench:integration    # 真实 MySQL/Redis 基准
```

**文档地图**：`docs/HANDOVER.md` 顶部有全部文档的阅读顺序。
`docs/PITFALLS.md` 是易错点总索引。

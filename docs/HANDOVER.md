# MediCore 开发历程与交接记录

> 本文档记录**做过什么、错过什么、还剩什么**。
> 与 `ARCHITECTURE.md`（讲"系统长什么样"）、`MODULES.md`（讲"每个模块怎么写的"）互补：
> 这份讲"它是怎么变成现在这样的"。
> 最后更新：M6 医生工作台补全之后

---

## 文档地图

接手这个项目时，建议按以下顺序阅读：

| 顺序 | 文档 | 回答什么问题 |
|---|---|---|
| 1 | `AGENTS.md` | 规则、命令、陷阱速查 —— **先读这个** |
| 2 | 本文档 | 做过什么、错过什么、还剩什么 |
| 3 | `docs/ARCHITECTURE.md` | 系统整体怎么运转 |
| 4 | `docs/MODULES.md` | **每个模块的结构与决策**（改代码前必查） |
| 5 | `docs/CODE-REVIEW.md` | 当前质量状态与已知短板 |
| 6 | `spec.md` | 原始需求（注意：它有 4 处缺陷已被修正） |

---

## 一、已完成的工作

### 1.1 阶段总览

| 阶段 | 提交 | 内容 |
|---|---|---|
| **准备** | `28cb2a3` | 规格分析、可行性实测、数据库设计、后端骨架 |
| M1 | `28cb2a3` | 配置 / 日志 / 中间件链 / 健康检查 / 分层骨架 |
| M2 | `28cb2a3` | 认证鉴权（AES-GCM + bcrypt + JWT 双令牌 + RBAC + 限流） |
| M3 | `e94b93a` | 科室 / 医生多对多 / 排班 + 游标分页 + Redis 缓存 |
| M4 | `4a72f1d` | 抢号（幂等 + Lua + cron 释放 + 签到队列） |
| M4.5 | `0cca626` | 号源对账 + 清理死代码 |
| M5a | `64f37bd` | AI 预问诊对话（LLM 抽象 + SSE 流式） |
| M5b | `430c77d` | AI 结构化总结（Schema 校验 + 降级 + Stream worker）+ 修复缓存技术债 |
| 审视 | `3ddf67e` | 整体审视 + 修复 6 个依赖漏洞 + 补 prompts 测试 |
| M6-补 | `0bc436d` | 审计日志（补 spec 缺口）+ 抽取 `internal/app` 装配 |
| M6-测 | `3f0207c` | HTTP 集成测试层 + worker 消费者组自愈 |
| M6 | `8eca3e2` | 病历 / 处方 / HIS 模拟同步 |
| **M6+** | `e3072ab` | **医生工作台补全**：接诊列表 + 预问诊报告对医生开放（spec §2.1 的两处角色缺口） |
| **审视²** | `6b23e57` | **全量代码审视**：修 1 个 HIGH 级恢复路径缺陷、1 处队列越权、1 处 API 契约倒置；清空死代码与无用泛化；`staticcheck`/`deadcode` 归零 |
| **补测** | 本次提交 | **针对关键路径补测**（见 §2.8）：直测 repository 层的 Lua/SQL 不变量、限流与令牌撤销、纯逻辑判定；并修掉一个 40% 概率失败的 flaky 用例 |

### 1.2 关键交付物

**后端**（86 文件 / 9695 行生产代码）：

| 模块 | 能力 |
|---|---|
| 认证 | 注册 / 登录 / 刷新轮换 / 登出 / 个人资料；AES-GCM 加密存储、HMAC 确定性摘要、bcrypt、防用户枚举、**防时序枚举** |
| 目录 | 科室、医生（多对多科室）、排班查询；游标分页；Redis 缓存（含防击穿） |
| 预约 | 抢号（幂等 + Lua 原子扣减）、取消、签到排队、候诊队列；cron 超时释放；**号源对账** |
| AI | SSE 流式对话、异步结构化总结（Schema 校验 + 重试降级 + 死信）、提示词版本化 |
| 医疗 | 病历与处方写入/读取（含归属校验） |
| 医生工作台 | 本人接诊列表（含患者姓名与 `has_record`）、预问诊报告对医生开放（含归属校验） |
| HIS | 模拟数据源同步（upsert、并发限流、同步日志） |
| 审计 | 异步批量写入审计日志，含资源/动作推导 |
| 运维 | 健康探针、Prometheus 指标（HTTP / 抢号 / 对账 / LLM / 队列 / 审计） |

**测试**：32 文件 / 6676 行（其中集成 7 文件 / 1984 行），**300 个用例全部通过**

| 类型 | 数量 | 运行方式 |
|---|---|---|
| 单元测试 | 240 | `nub run backend:test`（无需外部依赖） |
| 集成测试 | 60 | `nub run backend:test:integration`（需 mysql + redis） |

**覆盖率**：整体 **34.1%**（`service` 79.5%、`audit` 94.8%、`mockhis` 92.3%、
`worker` 90.4%、`jwt` 91.7%、`llm` 89.5%、`prompts` 88.2%、`secret` 87.8%、`mask` 100%、
`model` 55.8%）

> ⚠️ **`repository` 那 6.4% 是按包统计的假象**：集成测试是独立包，
> `go test ./...` 的 profile 不合并它的执行。用 `-coverpkg` 单独统计集成测试，
> **`repository` 实际覆盖 66.8%**、`service` 62.4%、`handler` 76.6%。
> 报告覆盖率时要说清楚用的是哪种口径，否则会得出"数据访问层几乎没测"的错误结论。

**静态检查**：`go vet` 0、`staticcheck` 0、`deadcode` 0（后两者为本次审视新引入）
**注**：`go test -race` 本机不可用（缺 cgo 的 C 编译器），详见 §2.7-G

**文档**：`spec.md`、`FEASIBILITY.md`、`docs/schema.sql`、`docs/database-review.md`、
`docs/CODE-REVIEW.md`、`AGENTS.md`、`backend/README.md`

**工具命令**：`cmd/genlocal`（密钥生成）、`cmd/seed`（演示数据）、`cmd/loadtest`（并发压测）

### 1.3 关键实测结论（都是跑出来的，不是查文档）

| 结论 | 数据 |
|---|---|
| TypeScript 7 当前不可用 | 包内 `typescript.js` 数量为 **0**；`vue-tsc` 崩溃、`typescript-eslint` 抛错 |
| 游标分页 vs OFFSET | 23 万行：**0.483ms vs 137ms**（约 284 倍） |
| 抢号零超卖 | 200 患者抢 20 号源，恰好 20 成功、180 号源不足、耗时 172ms |
| 依赖漏洞 | `govulncheck` 发现 **6 个**，修复 5 个 |
| 缓存修复有效性 | 反向验证：改回旧实现后 2 个测试失败（比值 0.00） |
| 时序防护有效性 | 反向验证：移除防护后比值降到 **0.00** |

---

## 二、过程中出现的错误

> 这一节是本文档最重要的部分。**我自己的错误单独列**，因为它们的模式比个例更有价值。

### 2.1 我（AI）引入的错误

#### A. 同类错误重复犯错：凑导入的占位语句（**4 次**）

```go
var _ = strings.TrimSpace   // 写了但其实没用到 strings
var _ = fmt.Sprintf
var _ = errors.Is
var _ = time.Now
```

**为什么发生**：我写完文件后倾向直接写下一个，而不是立刻编译。
于是在"删掉了某个引用"之后，用一句 `var _ =` 把未使用的导入"修好"，而不是删掉导入。

**危害**：这类代码让读者困惑（为什么要这么写？），是典型的"看着能过但很脏"。

**根因**：**在快速产出多个文件时跳过了"写完即编译"**。

**已修复**：全部删除。审视时全项目扫描确认无残留。

#### B. 写出占位符文件就当作完成（**3 次**）

```go
import "github.com/medicore/backend/internal/pkg_authctx_placeholder"  // 根本不存在的包
```

**为什么发生**：我在一个消息里连续写多个文件，写到某个文件时注意力已经转移到"下一个文件"，
于是写下骨架就以为写完了。

**危害**：如果没跑编译就提交，会直接破坏构建。

**已修复**：当场发现并重写。**教训：写完一个文件立刻验证**。

#### C. 写出空壳函数并配上"解释性"注释（**2 次**）

```go
// clearDoctorCache 清空科室医生列表缓存。
func clearDoctorCache(db *gorm.DB) error {
    _ = db
    return nil   // ← 什么都没做
}
```

以及种子命令里一个收了参数却完全没用到的 `externalID`。

**危害**：比 A/B 更隐蔽——它**看起来是实现的**，注释还描述了一个不存在的功能。
调用方会以为缓存被清理了，实际没有，然后困惑于"为什么数据没更新"。

**更糟的一次**：`ratelimit.go` 里我写了注释"故障会被 metrics 捕获"，
但当时**根本没有这个指标**。fail-open 路径意味着限流已静默失效，恰恰最需要告警。
后来补了 `ratelimit_errors_total` 才让注释成立。

**教训：注释不能描述尚不存在的实现。**

#### D. 错误的测试断言（**3 次**）

1. `assert.Empty(t, u.PasswordHash)` —— 本意是"散列非空"，语义写反了。
2. `strings.Repeat("a1b2c3d4", 8)` 当作"强密钥" —— 它本身就是短周期重复，
   被正确的校验拒绝了（**实现的判断是对的，测试的夹具错了**）。
3. 断言 `{}` 应该编译失败 —— 但 **`{}` 是合法的 JSON Schema**。

**第 3 次最有价值**：它暴露了一个真实风险——Schema 被误改成 `{}` 会让校验**静默失效**，
降级路径永不触发，垃圾数据被当成结构化结果入库且无任何报错。
于是加了 `SummaryValidator.SelfCheck()`，把这个陷阱变成启动即失败。

**教训：测试写错方向比没有测试更危险，因为它给人虚假的安全感。**
**但测试失败时，先怀疑测试本身，再怀疑实现。**

#### E. 违反自己的技术判断

`service/health.go` 最初的 `Ready()` 用 channel 装 probe → `close` → `range` → 起 goroutine
→ 再用另一个 channel 收结果。本质就是"并发跑两个函数"，却绕了三个数据结构。
**我自己在写的时候就觉得别扭，但还是写完了。** 后来改成 `sync.WaitGroup`。

**教训：感觉别扭的代码就是别扭的，当场改，不要留着。**

#### F. 修 A 时顺手造一个 B

清理死代码时引入了 `opCreate` 常量，只用了一次，又是过度抽象，随后又删掉。

**教训：修 bug 时保持最小改动。**

#### G. 测试夹具的签名错误（2 次）

```go
func (f *fakeStockStore) Release(_ context.Context, _, _, capacity int)  // 第一个参数应是 uint64
```

以及 `fakeScheduleStore` 漏实现新增的接口方法。

**这类错误无害且立刻被发现**（编译不过），但反映同一个模式：**写完没立刻验证**。

### 2.2 规格（spec.md）本身的缺陷 —— 已修正 4 处

| # | 问题 | 后果 | 修正 |
|---|---|---|---|
| 1 | `schedules` 表的 `KEY idx_date_status (sched_date, status)` 引用了**不存在的列** | **建表必然失败** | 补齐 `status` 列 |
| 2 | 抢号 Lua 在键不存在时返回 `0`（=号源不足） | Redis 冷启动后**所有抢号全部失败**，且提示"号源不足"极具误导性 | 返回独立的 `-1`，调用方先预热再重试 |
| 3 | 幂等键失败清理用无条件 `DEL` | 会把**已完成**的记录一并删掉，后续重放查不到结果，幂等失效 | 改为 Lua 比对删除 |
| 4 | 医生-科室改多对多后，排班唯一键未含科室 | 专家无法在同一午别跨科室出诊 | 唯一键加入 `department_id` |

另有 3 处索引/设计不匹配：游标分页索引写成 `(user_id, created_at DESC)` 却配 `ORDER BY id DESC`；
缺失 6 张表（科室、医生、病历、处方、审计、同步日志）；`ai_pre_reports` 缺异步化所需字段。

### 2.3 环境问题（非代码，但耗时）

| 问题 | 现象 | 处理 |
|---|---|---|
| **Go 安装残留** | `GOROOT/src` 内 9525 旧文件 + 2658 新文件混杂，标准库 `internal/strconv` 自身编译不过 | 重装 Go |
| **Docker MySQL 假就绪** | `mysqladmin ping` 已成功但最终 server 未起，报 `driver: bad connection` | 改用真实查询探测且连续两次成功 |
| **nub peer 变体粘性** | 升级 TS 后 eslint 仍解析到旧变体，**让我得出过错误结论** | 删 `node_modules` 重装 |
| **Docker 镜像拉取昂贵** | 首次约 1.5–2.5GB | 分阶段拉取，避免反复 rebuild |

### 2.4 安全事件：密钥进入了 git 历史

**经过**：我在提交前检查了"暂存区有什么"，但**没意识到密钥已经写进提交对象**。
用户在复查时指出"仓库中不应该有任何密钥被提交"。

**处理（三步，缺一不可）**：

```bash
git commit --amend                                    # 1. 新提交不含密钥
git reflog expire --expire=now --all                  # 2. 断开旧提交的唯一引用
git gc --prune=now                                    # 3. 物理删除悬空对象
```

**关键验证**：`--amend` 之后我用 `git cat-file --batch-all-objects --batch` 搜过，
**密钥在全对象库里仍能搜到 4 处**，旧提交通过 reflog 依然可达。
只有执行第 2、3 步后才真正清除。

**因为仓库从未 push，无需轮换密钥。** 若已 push，**必须轮换**——克隆过的人手里已有旧密钥。

**改进**：仓库中现在一个密钥都不含（连开发用的也没有），
本地密钥由 `cmd/genlocal` 生成到 gitignored 的 `configs/config.local.yaml`。

### 2.5 审视发现的既有问题

| 问题 | 说明 |
|---|---|
| 6 个依赖漏洞 | `govulncheck` 从未运行过；`x/crypto`（bcrypt 所在）有 4 个 |
| `prompts` 包覆盖率 0% | 却在启动关键路径上 |
| 202 响应 `trace_id` 为空 | 直接构造 `response.Body` 漏掉了链路 ID，违反 spec §9.2 |
| `ratelimit_errors_total` 缺失 | 注释声称有，实际没有（见 §2.1-C） |
| Gin 传递引入无用依赖 | `mongo-driver` + `quic-go` 被编译进二进制，攻击面被放大 |
| **管理端点清单漏写 `/admin/` 前缀** | `docs/ARCHITECTURE.md` 与 `docs/MODULES.md` 都写成 `/api/v1/audit-logs`，实际是 `/api/v1/admin/audit-logs`。**代码一直是对的，错的是文档** |
| **已实现的端点被列为"未完成"** | `POST /admin/sync/:source` 早已实现，文档里却写着待办 |

后两条的教训：**路由清单不要手写**。以 `backend/internal/router/router.go`
或 gin 启动时的 debug 输出为准，那是唯一不会骗人的来源。

### 2.6 医生工作台补全这一轮的错误

| 错误 | 表现 | 根因 |
|---|---|---|
| 测试 fake 被"简化"弄坏既有用例 | 把 map 形式的医生查询改成单值，`TestRecord_Write_RejectsOtherDoctorsPatient` 与 `TestRecord_Write_RequiresDoctorProfile` 直接失败 | 改夹具时只看了新用例的需要，**没跑全包** |
| 测试辅助函数重复定义 | `doctorAccess`/`patientAccess`/`adminAccess` 在两个测试文件里各写了一份 | 新建文件时没先 `grep` 一遍 |
| 注释与实现矛盾 | `QueueItem.PatientName` 注释写着"需要脱敏"，实现却是刻意不脱敏 | 实现改了，注释没跟上 |

**共同点仍是"收尾动作被跳过"**：改完夹具立刻跑一次全包测试，与新建文件前 `grep` 一次，
两步都能挡住。这与 §5.4 的判断一致——问题不在设计能力，在于每步之后的确认动作。

### 2.7 全量代码审视（医生工作台补全之后）

用三个并行审查 + 三个静态工具（`staticcheck` / `deadcode` / `go vet`）把后端过了一遍。
**真正的收获不是"发现了很多问题"，而是发现了三类此前完全没被察觉的问题类型。**

#### A. 一个 HIGH 级缺陷：恢复路径自己被 context 掐死

`worker/summary.go` 的消费者组重建分支复用了**已经 cancel** 的 context：

```go
ctx, cancel := context.WithTimeout(...)
msgs, err := w.queue.Read(ctx, ...)
cancel()                              // ← 已经取消
if repository.IsNoGroupError(err) {
    w.queue.EnsureGroup(ctx)          // ← 永远返回 context.Canceled
```

于是"Redis 被 flush 后自动重建消费者组"这条恢复路径**从来没能成功过**，
循环只会反复记同一条错误日志——而这正是该分支的注释所声称要避免的
"所有总结任务静默卡死"。修复：重建用独立的 context。

**它此前测不出来，是因为测试里的 fake 忽略了 ctx。** 修 fake（让它在 ctx 失效时
返回错误并计数）后，把 bug 改回去用例确实变红——已做反向验证。

> **教训：mock/fake 忽略参数，等于放弃对该参数的验证。**
> 凡是实现会把参数透传给下游的，fake 至少要检查它是否有效。

#### B. 一个信息泄露：候诊队列只做了路由层鉴权

`GET /schedules/:id/queue` 返回**未脱敏**的患者姓名与 `user_id`
（这是有意的：医生要能叫号），但它只挂了 `RequireRole(doctor)`，
service 层**完全没有归属校验** —— 任何医生换个排班 ID 就能枚举全院患者身份。

这与刚修完的"预问诊报告对医生开放"是**同一类漏洞**，说明当时是逐点修补而不是
按类别排查。修复：`Queue` 接收 `Access`，校验排班归属（管理员豁免）。

#### C. 三处"测试把 bug 固化下来"

这是本轮最值得记的一类问题——测试不但没发现问题，反而**把错误行为钉死**：

| 位置 | 固化了什么错误 |
|---|---|
| `audit_test.go` 的 `Describe` 用例 | 断言 `/api/v1/admin/audit-logs` 的 resource 是 `admin`，而正确值是 `audit-logs`。三个管理端点的资源名全塌成 `admin`，按资源聚合时混成一条 |
| `http_test.go` 的审计用例 | 同上，查的是 `resource = 'admin'`，与上面一起把 bug 锁死 |
| `appointment_doctor_test.go:151` | `assert.Zero(t, f.doctor.err == nil && f.doctor.doctorID == 0)` —— fake 不会因为被调用而改变字段，这个断言恒为真，**即使管理员分支真去查了医生档案也测不出来** |

**教训：断言要观察"行为"，不要观察"夹具的静态字段"。**
判断某个分支有没有被走到，就数调用次数，不要断言夹具里的值。

#### D. 其余确认并修掉的缺陷

| 缺陷 | 后果 | 修法 |
|---|---|---|
| `stockReleaseScript` 的钳位用裸 `SET` | 清掉 `SETNX` 设的 TTL，号源键变成永久键；历史排班又不在对账扫描范围内，错值再也不会被修正 | 加 `KEEPTTL`（与兄弟脚本一致） |
| `recoverDuplicate` 把任意错误当成"重复抢号" | 数据库抖动被报成 409 冲突，用户被告知"您已预约"，真实原因既没包装也没日志 | `errors.Is(ErrNotFound)` 分支 + 其余走 `apperr.Wrap` |
| `FailReport` 无条件 `return nil` | 落库失败只写日志，worker 以为已标记失败，报告留在 `processing` | 返回并向上传播错误 |
| `audit.truncate` 按字节硬切 | UA 含中文时切出非法 UTF-8，utf8mb4 列会让**整批 100 条审计一起插入失败** | 回退到字符边界 |
| `cmd/seed` 用 `time.Now().Truncate(24h)` | 截的是 UTC 零点，UTC+8 下 08:00 前算出的"今天"早一天，演示排班从昨天开始 | 用本地时区构造当天零点 |
| `CheckIn` 直接序列化 `*time.Time` | 全接口统一 `%Y-%m-%d %H:%M:%S`，只有这一个字段是 RFC3339，前端要特判 | 用 `model.APITimeLayout` 格式化 |
| `secret/cipher.go` 包注释写 `Package crypto` | 与包名不符，godoc 不会挂载（ST1000） | 改正 |
| `conversation.List` 注释说"跳过损坏条目" | 代码其实是整体失败，注释与实现相反 | 保留"失败"行为并改正注释，说明为什么不静默跳过 |

#### E. 死代码与无人使用的"通用性"

`deadcode` + `staticcheck` 现在都是 0 告警。清掉的具体项：

| 类别 | 项 |
|---|---|
| 死函数 | `mockhis.Dataset.Summary`、`SyncLogRepo.LastFinished`、`MedicalRecordRepo.FindByID` |
| 死测试辅助 | `fakeStore.batchCount`、`fakeRecordStore.doctorIDErr` |
| 从不设置的旋钮 | `SyncDeps.Concurrency`（spec 定死 5，生产从不设，测试设的也是默认值） |
| 无人使用的返回值 | `UpsertDoctor` 的医生 ID、`UpsertSchedule` 的 `created` |
| 从不写入的字段 | `sync_logs.trace_id`（改为**真正写入**，见下）、`AuditLog.Detail`（连列一起删除） |
| 不可达分支 | `runPhase`/`run` 的 errgroup 错误通道——三个 upsert 永远返回 nil，那段代码读起来却像"失败会中止整批" |

**`sync_logs.trace_id` 没有删而是接上了**：它与项目"TraceID 贯通"的设计一致，
手动触发同步时能把 `sync_logs` 与同一次请求的审计记录串起来。
**`audit_logs.detail` 则是删掉了**：它从没被写入也没有读取方，
而审计模块的设计前提正是"不存请求体"（请求体可能含手机号、口令、病情描述）。
留一个空的 JSON 列，只会诱使后人往里塞敏感数据。列已从 `docs/schema.sql` 移除。

#### F. 一个结构性问题：service 反向依赖了 mock 包

`service/sync.go` 的 `SyncSource.Load` 签名是 `(*mockhis.Dataset, error)`，
于是**业务逻辑包依赖了一个名字里带 mock 的实现包**——真实 HIS 的适配器
将不得不 import 一个 mock 来拿类型。那个"抽象接口"实际什么也没抽象。

修复：新增 `internal/his` 契约包（只放数据类型，不放接口，遵循本项目
"接口由使用方声明"的惯例）；`mockhis` 变成它的一个适配器，并把
"相对今天 N 天"这种数据文件写法在适配器内部换算成绝对日期，
不再泄漏到业务层。`app.go` 里那个只为丢弃 ctx 而存在的 `hisSource` 适配器随之删除。

> **教训：接口签名里出现"实现方"的包名，就说明抽象放错了位置。**

#### G. 一个值得记的环境结论

`go test -race` 在本机**跑不了**：`-race` 需要 cgo，而 PATH 里没有 C 编译器
（`gcc not found`）。这是实测结论，不是猜测。涉及并发的改动目前只能靠
代码审查 + 单测的可重复性来保障。

### 2.8 针对关键路径的补测

审视清掉了死代码与缺陷，但**"没测"本身就是缺陷的温床**——所以紧接着做了一轮
**只针对关键路径**的补测。原则：不加为了凑覆盖率而存在的测试，
每一组用例都要能回答"它守护的是哪条会出事的不变量"。

**排序依据不是覆盖率数字，而是"出事的代价 × 排查难度"**：

| 补测对象 | 为什么它排在最前面 |
|---|---|
| **号源 Lua 脚本** | 超卖是整个项目最贵的失败；而且这部分**只有打真实 Redis 才测得出**——fake 里"SETNX"就是一行 map 赋值，写错成 SET 也不会有人发现 |
| **幂等键的比对删除** | 曾经修过这个 spec 缺陷，但**修复本身从来没有测试守护** |
| **超时释放的幂等闸门** | 多实例并发扫描时重复回补 → 号源凭空变多 |
| **刷新令牌的键拼法** | 三个方法共用键前缀，拼错一处的症状是"登出静默失败" |
| **限流器** | 此前**从未被任何测试执行过**——所有用例都在开头清限流键来绕开它 |
| **纯逻辑判定**（`ParseReportID` / `parseOptionalTime` / `IsActive…`） | 便宜，且各自编码一个影响面很大的判断 |

**做法：先把"没被任何测试碰过"的地方找出来，再决定测哪个。**
具体是把 coverage profile 里 0% 的函数列出来，逐个问"它错了会怎样"——
而不是从列表顶部开始一个个补。

> 注意一个口径陷阱：`go test ./...` 的 profile **不合并集成测试的执行**，
> 于是 handler / repository 会显示 0%。要判断"是否真的没测"，得结合
> 代码阅读与 `-coverpkg` 的单独统计，不能只看那个 0%。

#### 三层测试的分工在这一轮被明确

```
service 单测    →  编排逻辑（注入内存 fake，快，240 个用例秒级跑完）
repository 直测 →  真实 SQL / Lua 的语义（本轮新增，慢但只压在关键不变量上）
HTTP 集成测试   →  端到端行为、路由、中间件、状态码
```

**为什么必须有中间那一层**：fake 里一行 Go 代码就实现的行为，
在 Lua 脚本和带条件的 UPDATE 里是完全不同的语义。
端到端测试能发现"表现不对"，但很难指出是哪条语句错了；
而"重复回补"这类 bug 在端到端测试里要么不触发，要么触发得毫无规律。

#### 反向验证（本轮新增 6 项）

每条关键用例都做了"改坏实现 → 确认变红 → 还原"：

| 注入的 bug | 用例是否变红 |
|---|---|
| 预热 `SETNX` → `SET`（覆盖已扣减号源） | ✅ |
| 去掉号源钳位的 `KEEPTTL` | ✅ |
| 幂等释放改成无条件 `DEL` | ✅ |
| 去掉超时释放的 `RowsAffected` 闸门 | ✅ |
| 限流 Lua 永远返回"放行" | ✅ |
| 登出用了不一致的 Redis 键前缀 | ✅ |

#### 顺带修掉一个 flaky 用例（约 40% 概率失败）

补测时全量跑测试，偶然撞上 `TestLoop_ConsumesQueuedMessages` 失败。
单跑十次不复现，**在 CPU 负载下 12 次里失败 1 次**——典型的时序竞争：

```go
// 等的是"业务逻辑被调用"
for { if s.callCount() > 0 { break } ... }
// 断言的却是"确认已完成"，而 handle() 里 Ack 在 SummarizeReport 之后
assert.Equal(t, 1, q.ackCount())
```

等待条件比断言**落后一步**，于是测试偶尔会在 worker 还未确认时就往下走。

**这类问题真正的危害是它会训练人忽略失败**：间断红灯 → 习惯性重跑 →
测试套件失去可信度。已改为等待最终状态，并在负载下复跑 20 次确认归零。

> **教训：等待型断言必须等"你即将断言的那个状态"，而不是它的前置状态。**
> 另外，"本地跑一遍都过了"不足以说明不 flaky——要**在负载下**跑。

---

## 三、还没做的工作

### 3.1 高优先级

| 项 | 现状 | spec 依据 |
|---|---|---|
| **前端页面** | 仅有 create-vue 脚手架（`App.vue` 还是模板） | §8 要求登录/挂号/AI 对话/医生工作台/管理看板 |
| **全栈 compose 编排** | 只有 `deploy/compose.dev.yml`（依赖服务），缺 nginx + 前后端镜像 | §11.2、G5「一键起全栈」 |

### 3.2 中优先级

| 项 | 说明 |
|---|---|
| `docs/ai-vibe-coding/` | spec §10.3 要求 WORKFLOW.md + prompts + **≥5 篇** review。**当前一篇都没有**（G6 验收项） |
| `docs/benchmark/` + `docs/sql-optimization.md` | spec §12.2 要求压测报告与 EXPLAIN 对比。**数据已有**（284 倍、零超卖），只差成文 |
| 前端管理看板拉 `/metrics` | spec §8.1 要求 ECharts 渲染系统健康度 |
| **按角色横切复查其余角色** | 按**医生**角色走完整工作流发现 2 处缺口；随后全量审视又发现**同类**的候诊队列越权（§2.7-B）。**患者与管理员角色仍未系统复查** —— 这个"同类漏洞逐个冒出来"的模式说明该做一次成体系的排查，而不是等它自己暴露 |
| `golangci-lint` 配置 | 目前只手工跑 `staticcheck` + `deadcode`；纳入常规链路才能防止死代码回流 |

### 3.3 低优先级 / 可选

| 项 | 说明 |
|---|---|
| `repository` 层覆盖率 | 按包统计 6.4%（假象），集成测试实际 66.8%。仍为 0% 的主要是缓存命中路径与目录只读查询 |
| `handler` 单元覆盖率 | 0%，但集成测试已覆盖全部 29 个端点的行为 |
| `config` 覆盖率 33.7% | 密钥校验已覆盖，配置加载分支未覆盖 |
| **软删除后的"复活"逻辑未实现** | `database-review.md` §4.2 曾把"由 service 复活 `deleted_at = NULL`"写成已完成，实际上代码里没有任何 `Unscoped()`/`deleted_at = NULL` 调用。当前没有任何入口会软删这些表，所以**缺口不可达**；但**加管理端删除功能之前必须先实现复活**，否则 HIS 同步会永久失败（详见该节说明） |
| `audit_logs` 分区/归档 | 写入最频繁的表，长期运行需要 TTL 或分区 |
| 真实 LLM 接入 | `llm.NewClient` 对 `openai`/`dashscope`/`deepseek` **明确返回错误**（刻意设计，见 §4.3） |
| 上游 typescript-eslint 支持 TS7 后升级前端 | 目前锁定 TS 5.9.3 |

---

## 四、刻意偏离 spec 的地方

**共 3 处，均有代码注释与 README 说明。请勿当作疏漏"修正"回去。**

| # | spec 原文 | 实际实现 | 理由 |
|---|---|---|---|
| 1 | `phone_hash = SHA256(phone + salt)` | **HMAC-SHA256(pepper, phone)** | keyed hash 不会因拼接方式引入长度扩展问题；输出同为 64 位十六进制，兼容 `CHAR(64)`。**关键性质仍是"必须确定性"**——用逐行随机盐会让登录彻底不可用 |
| 2 | `internal/pkg/` 目录结构 | 工具包**平铺**在 `internal/` 下 | `internal` 本身已阻止外部导入，再加 `pkg` 是零语义的冗余层级（Go 社区共识） |
| 3 | "repository 是唯一允许出现 GORM 的层" | `model` 的 PO 使用 `gorm.DeletedAt` | 换取 GORM **自动**为所有查询追加 `deleted_at IS NULL`。换成 `*time.Time` 就得每处手写，而我已经踩过一次漏写的坑（手写 JOIN 不带软删除条件，已删除医生出现在列表里） |

---

## 五、这个项目的"性格"（给接手者）

### 5.1 实测优先于文档推断

所有关键结论都是**跑出来的**：
TypeScript 7 不可用（拆包验证包内无 `typescript.js`）、
284 倍分页差距（23 万行 EXPLAIN ANALYZE）、
6 个依赖漏洞（`govulncheck`）、
缓存修复有效性（反向改坏看测试是否失败）。

**遇到不确定的事，先写最小验证再下结论。** 我在 TS7 问题上曾因 nub 的 peer 粘性
得出过**错误结论**（以为 TS7 能用），是重新干净安装后才推翻的。

### 5.2 测试必须能失败

写好关键测试后，**故意把实现改坏，确认测试真的会失败**。
做过的反向验证：

| 测试 | 改坏后 |
|---|---|
| `TestLogin_UnknownUserTimingIsEqualized` | 比值降到 0.00 → 失败 ✅ |
| `TestCacheLoad_LoaderContextSurvivesCallerCancellation` | `context canceled` → 失败 ✅ |
| `TestLoop_RecreatesMissingConsumerGroup` | 重建改用已取消的 ctx → "ctx 不应已失效" 断言失败 ✅ |
| `TestQueue_OtherDoctorsScheduleIsForbidden` | 移除归属校验 → 越权者拿到了队列内容 ✅ |

**没有反向验证过的测试，不能算测试。**
反过来也要小心：**看起来在断言、实则恒为真的测试更危险**——它给人已经覆盖的错觉。
本项目已发现三处（见 §2.7-C），都是"断言夹具静态字段而非行为"导致的。

### 5.3 注释解释"为什么"

项目里那些看起来"多此一举"的代码，都是在防具体的坑：

| 代码 | 防的是 |
|---|---|
| 预热用 `SETNX` 而非 `SET` | 覆盖已扣减的号源 → 超卖 |
| 幂等键比对删除而非 `DEL` | 误删已完成记录 → 幂等失效 |
| `startStream()` 延迟到首次吐数据 | 先发 200 再报错 |
| 对账用 `CompareAndSet` | 对账本身制造超卖 |
| 缓存回源 `WithoutCancel` | leader 取消波及健康跟随者 |
| 号源钳位用 `SET ... KEEPTTL` | 裸 `SET` 清掉 TTL，号源键变永久键 |
| 消费者组重建用独立 ctx | 复用已取消的 ctx → 恢复路径永不成功 |
| `audit.truncate` 回退到字符边界 | 切出非法 UTF-8 → 整批审计插入失败 |
| 登录失败也执行假比对 | 响应时间枚举账号 |

### 5.4 我（AI）的错误模式

**核心问题不是设计能力，而是收尾动作。**

- 设计判断（回调式流式、比对删除、预热语义）经反向验证都站得住
- 但"写完立刻编译"这一步经常被跳过，导致 §2.1 的 A/B/G 类错误反复出现

**给接手 agent 的建议**：如果你也是 AI，
**每写完一个文件就跑一次 `go build ./...`**，别攒一批再编译。
本项目 8 个自我引入的错误里，5 个会在这一步被立刻发现。

### 5.5 第二类错误模式：写"看起来在断言"的测试

§5.4 讲的是收尾动作被跳过。全量审视（§2.7）暴露了**另一类**独立的问题：
测试写出来了、也跑了、也是绿的，但它其实什么都没验证。

三种具体形态：

| 形态 | 例子 |
|---|---|
| 断言夹具的静态字段 | `assert.Zero(t, fake.err == nil && fake.id == 0)` —— fake 不因被调用而变，恒为真 |
| fake 忽略参数 | `EnsureGroup(context.Context)` 直接忽略 ctx，于是"用已取消的 ctx 调用"这个 bug 测不出来 |
| 把错误行为写进期望值 | 断言 `/admin/*` 的审计 resource 是 `admin`，等于把 bug 固化 |

**防法**：写"分支不该被走到"的断言时，数**调用次数**；写 fake 时，
把实现会透传给下游的参数至少检查一次；拿到一个"看起来对"的期望值时，
回到代码确认它是不是真的对。

---

## 六、快速验证项目健康度

```powershell
# 一次性确认全部正常
cd D:\Hider\Code\MediCore
nub run deps:up
$env:MEDICORE_MYSQL_PASSWORD='medicore123'
nub run backend:vet                    # 应为 0
nub run backend:test                   # 240 个用例通过
nub run backend:test:integration       # 60 个用例通过
nub run backend:vuln                   # No vulnerabilities found
```

**当前预期结果**：全部通过，漏洞 0，集成测试 60/60，单元测试 240/240。

静态检查（本次审视引入，建议纳入常规链路）：

```powershell
cd backend
go install honnef.co/go/tools/cmd/staticcheck@latest
go install golang.org/x/tools/cmd/deadcode@latest
& "$env:GOPATH\bin\staticcheck.exe" ./...   # 应为 0
& "$env:GOPATH\bin\deadcode.exe" -test ./... # 应为 0
```

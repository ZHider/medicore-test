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
| **M6+** | 本次提交 | **医生工作台补全**：接诊列表 + 预问诊报告对医生开放（spec §2.1 的两处角色缺口） |

### 1.2 关键交付物

**后端**（85 文件 / 9519 行生产代码）：

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

**测试**：25 文件 / 5578 行（其中集成 4 文件 / 1338 行），**249 个用例全部通过**

| 类型 | 数量 | 运行方式 |
|---|---|---|
| 单元测试 | 218 | `nub run backend:test`（无需外部依赖） |
| 集成测试 | 31 | `nub run backend:test:integration`（需 mysql + redis） |

**覆盖率**：整体 **32.2%**（`service` 75.9%、`audit` 95.6%、`worker` 89.9%、
`llm` 89.5%、`prompts` 88.2%、`secret` 87.8%、`jwt` 91.7%、`mask` 100%；
`repository` 4.8%、`handler` 单元 0%，二者由集成测试保障行为）

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
| **按角色横切复查其余角色** | 本轮按**医生**角色走完整工作流，发现 2 处真实缺口（接诊列表缺失、报告接口没对医生开放）。**患者与管理员角色尚未同样复查一遍** —— 大概率还有同类缺口（见 §2.6 与 `MODULES.md` §6.7） |

### 3.3 低优先级 / 可选

| 项 | 说明 |
|---|---|
| `repository` 层覆盖率 | 当前 4.8%。约 1700 行 SQL 主要靠 31 个集成测试保障 |
| `handler` 单元覆盖率 | 0%，但集成测试已覆盖全部 29 个端点的行为 |
| `config` 覆盖率 33.7% | 密钥校验已覆盖，配置加载分支未覆盖 |
| 真实 LLM 接入 | `llm.NewClient` 对 `openai`/`dashscope`/`deepseek` **明确返回错误**（刻意设计，见 §4.3） |
| `audit_logs` 分区/归档 | 写入最频繁的表，长期运行需要 TTL 或分区 |
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

**没有反向验证过的测试，不能算测试。**

### 5.3 注释解释"为什么"

项目里那些看起来"多此一举"的代码，都是在防具体的坑：

| 代码 | 防的是 |
|---|---|
| 预热用 `SETNX` 而非 `SET` | 覆盖已扣减的号源 → 超卖 |
| 幂等键比对删除而非 `DEL` | 误删已完成记录 → 幂等失效 |
| `startStream()` 延迟到首次吐数据 | 先发 200 再报错 |
| 对账用 `CompareAndSet` | 对账本身制造超卖 |
| 缓存回源 `WithoutCancel` | leader 取消波及健康跟随者 |
| 登录失败也执行假比对 | 响应时间枚举账号 |

### 5.4 我（AI）的错误模式

**核心问题不是设计能力，而是收尾动作。**

- 设计判断（回调式流式、比对删除、预热语义）经反向验证都站得住
- 但"写完立刻编译"这一步经常被跳过，导致 §2.1 的 A/B/G 类错误反复出现

**给接手 agent 的建议**：如果你也是 AI，
**每写完一个文件就跑一次 `go build ./...`**，别攒一批再编译。
本项目 8 个自我引入的错误里，5 个会在这一步被立刻发现。

---

## 六、快速验证项目健康度

```powershell
# 一次性确认全部正常
cd D:\Hider\Code\MediCore
nub run deps:up
$env:MEDICORE_MYSQL_PASSWORD='medicore123'
nub run backend:vet                    # 应为 0
nub run backend:test                   # 218 个用例通过
nub run backend:test:integration       # 31 个用例通过
nub run backend:vuln                   # No vulnerabilities found
```

**当前预期结果**：全部通过，漏洞 0，集成测试 31/31，单元测试 218/218。

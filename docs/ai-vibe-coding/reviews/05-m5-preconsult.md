# 05 · M5 —— AI 预问诊（LLM 抽象 / SSE / Schema 校验降级 / Stream worker）

| | |
|---|---|
| **对应提交** | `64f37bd`（对话 + SSE）、`430c77d`（结构化总结 + Stream worker + 修缓存技术债）；缺陷修正在 `6b23e57`、`dddd194`、`ecaa04c` |
| **规模** | 13 文件 / 1860 行（`MODULES.md` §5，**后端最大的模块**） |
| **涉及包** | `llm`（抽象 + mock）`prompts`（go:embed）`service/preconsult` `service/preconsult_submit` `service/summary` `worker/summary` `repository/conversation` `repository/report` `repository/summary_queue` `handler/preconsult` |
| **本篇性质** | **事后整理**。来源：`MODULES.md` §5、`HANDOVER.md` §2.1-D/E、§2.7-A/D、`FIXES.md` H1、`docs/PITFALLS.md` §6；§三⑥ 为本次新补查 |
| **结论** | 交付达成；**本模块贡献了全项目唯一的 HIGH 级静默故障**（消费者组恢复路径从不生效） |

---

## 一、需求拆解

| spec 出处 | 要求 | 落地 |
|---|---|---|
| §7.2 | **SSE 流式对话**，必须处理 `ctx.Done()` 防止 goroutine 泄漏 | `handler.Preconsult.Chat` + `service.Preconsult.Chat`（回调式） |
| §7.2 | 结构化总结 + **jsonschema 校验** | `service/summary.SummaryValidator` |
| §7.2 | **降级策略**：校验失败重试 1 次；仍失败则存原文、`structured_json=NULL`、`fallback=true` | `summarizeReport` 的两段式 |
| §7.2 | **Prompt 版本化**，版本号入库 | `prompts` 包（`go:embed`）+ `ai_pre_reports.prompt_version` |
| **决策 #8** | 总结**异步**（真实场景里用户提问后会刷新；LLM 有限流时需排队） | Redis Stream：`XADD` 入队 → 立即 202 |
| §9.2 | `llm_calls_total` 指标 | `metrics` + `classifyLLMError` 分类 |

**spec 在此没有需要修正的缺陷** —— 反倒是 spec 的降级要求在实现中被证明**必须补一道自检**（见 §四第 3 条）。

---

## 二、方案选型

| 决策点 | 被否决的方案 | 选定 | 理由 |
|---|---|---|---|
| 流式接口形态 | `chan string` | **回调 `onDelta func(string) error`** | channel 会重新引入 goroutine 泄漏风险；回调的**返回错误天然表达"客户端已断开"**，不必再起一个 goroutine 去监听 |
| 前端如何收流 | `EventSource` | **`fetch` + POST + `text/event-stream`** | `EventSource` 只支持 GET，**无法携带消息体与 Authorization 头** |
| 总结的执行时机 | 同步等 LLM 返回 | **异步（Redis Stream）** | 见上表决策 #8。同步会让 submit 接口的 P99 完全取决于 LLM |
| 队列消费的投递语义 | 至多一次 | **至少一次 + 幂等抢占 + 死信** | 崩溃时消息必须能重投；用 `MarkProcessing` 的带状态 UPDATE 作幂等闸门 |
| 对话存哪 | 直接写报告行 | **Redis** | 对话是**进行中**的状态，写 MySQL 会让每轮对话都产生一次写入；提交时才落库成报告 |
| 校验失败怎么办 | 重试到底 / 直接失败 | **重试 1 次 → 降级** | spec §7.2；降级后医生端展示原始对话 |
| 上游配置为真实厂商时 | 回退到 mock | **明确返回错误** | 生产里"以为接了真模型、其实返回的是假数据"**比直接挂掉危险得多** |
| mock 的确定性 | 随机回复 | **按 user 消息数量推断轮次** | 否则测试没法写断言 |

---

## 三、§10.2 检查清单

### ① 分层与架构

- **`llm.Client` 是使用方定义的接口**：`service` 声明它需要的能力，`llm/mock` 与未来的真实适配器都隐式满足。
- `prompts` 用 `go:embed` 把提示词编进二进制 —— **单二进制不会漏文件，提示词与代码同版本**。
- `worker` 只做"消费 → 调 service → ACK"，业务逻辑在 `service/summary`，
  **worker 不碰 GORM**。
- 对话存储抽象成 `conversationStore`，`service` 不知道底层是 Redis。

**判定：通过。**

### ② 错误与并发

| 检查项 | 情况 |
|---|---|
| goroutine 退出路径 | **回调式**设计消除了 channel 的泄漏面；SSE 的 `ctx.Done()` 被显式处理（客户端断开时不再尝试写响应） |
| 客户端断开的语义 | `errors.Is(streamErr, context.Canceled) && ctx.Err() != nil` → 记 Info 而非 Warn，**不当作服务端故障** |
| 共享状态 | worker 的并发度受 `consumeBatch` 限制；报告抢占用带状态条件的 UPDATE 作幂等闸门 |
| **恢复路径的 ctx** | ❌ **曾经有一个 HIGH 级缺陷**：见 §四第 1 条 |

**判定：曾存在一个真实的 HIGH 级并发缺陷，已修并有反向验证。**

### ③ 性能与 SQL

| 查询 | 选用索引 | 实际读取行数 | 备注 |
|---|---|---|---|
| Q23 预问诊报告·按预约 | `uk_appointment` | ~1（**const**） | 一次预约一份报告，唯一键直接命中 |
| 报告写回 | `PRIMARY` | ~1 | `SaveOutcome` 按主键更新 |
| 报告抢占 | `PRIMARY` | ~1 | `MarkProcessing` 带状态条件 |

- **报告表设计的一处关键点**：`uk_appointment` 唯一键让 `submit` **天然幂等** ——
  用户重复点击不会产生第二份报告，也不会重复入队（`CreatePending` 用 `OnConflict{DoNothing}`，
  `RowsAffected==0` 时返回已存在的那一份）。
- **无 N+1**；`ListPrescriptions` 用 `idx_record (record_id, sort_order)` 覆盖索引（见 M6 篇）。
- **事务内无网络调用**：LLM 调用与 Redis 操作都在事务外。

### ④ KISS 原则

| 出现的"多余" | 判定 |
|---|---|
| 回调式而非 channel | **必要**（见 §二） |
| `SummaryValidator` 独立成类型而非一个函数 | **必要** —— 它要承载编译后的 schema 缓存与 `SelfCheck()` |
| 故障注入配置（`summary_mode` / `stream_error_at`） | **必要且有充分理由**：spec G3 的验收标准是"**100% 通过校验 _或_ 优雅降级**"，没有故障注入，降级分支就是**永远未被执行过的代码** |
| mock 按**轮次**而非随机回复 | **必要** —— 确定性才能写断言 |

### ⑤ 基础卫生

| 检查项 | 情况 |
|---|---|
| 输入校验 | 单条消息有长度上限（按 **rune** 计，不是 byte）；空消息拒绝 |
| 敏感数据脱敏 | 对话内容含病情描述，**报告端点做了归属校验**（三分支：admin 直通 / patient 本人 / doctor 接诊医生，见 M6 篇 §6.3） |
| 硬编码 | 超时、重试次数、批量大小均为命名常量并注明理由；无密钥（LLM key 走配置，且真实厂商当前**故意不可用**） |

### ⑥ AI 幻觉

本次新补查（方法见 `README.md`）。M5 深度使用了 Redis Stream 与 JSON Schema，
这两处是最容易"记错语义"的地方：

| API | 存在性 | 语义核对结果 |
|---|---|---|
| `XGroupCreateMkStream` | ✅ | `MkStream` 后缀表示**流不存在时自动创建** —— 少了它，`NOGROUP` 会持续报错 |
| `XReadGroup` 的 `>` 与 `0` | ✅ | `>` = 新消息；`0` = 读本消费者**已投递未确认**的（PEL）。本项目两者都用，语义正确 |
| `XAutoClaim` | ✅ | 抢占**超时未 ACK** 的消息（崩溃恢复）。返回的 `messages` 可能为空但有 `next` 游标 —— 必须继续迭代，代码处理正确 |
| `XAck` | ✅ | 必须显式 ACK，否则消息留在 PEL 里被反复重投 |
| **`{}` 是合法的 JSON Schema** | ✅（jsonschema/v5 行为正确） | **这是本模块最重要的语义发现**：`{}` 表示"任何内容都通过"。**库的行为没错，错的是我们的假设** |
| `Compiler.Compile` 的返回 | ✅ | 编译一次可复用，本项目缓存编译结果 |

**结论：0 个幻觉符号；1 个"库行为与直觉相反"的语义发现（`{}`），它催生了 `SelfCheck()`。**

---

## 四、本模块暴露的真实缺陷

### 1（HIGH）· 消费者组重建被自己的 context 掐死

```go
ctx, cancel := context.WithTimeout(...)
msgs, err := w.queue.Read(ctx, ...)
cancel()                              // ← 已经取消
if repository.IsNoGroupError(err) {
    w.queue.EnsureGroup(ctx)          // ← 永远返回 context.Canceled
```

**后果**：Redis 被 flush 后"自动重建消费者组"这条恢复路径**从来没能成功过**，
循环只会反复记同一条错误日志 —— **而这正是该分支的注释所声称要避免的
"所有总结任务静默卡死"**。

修复：重建用**独立的短超时 context**（`recoveryTimeout`）。

**它此前测不出来，是因为测试里的 fake 忽略了 ctx。**
修 fake（让它在 ctx 失效时返回错误并计数）后，把 bug 改回去**用例确实变红** —— 已做反向验证。

> **两条教训**：
> ① **mock/fake 忽略参数，等于放弃对该参数的验证。**
>    凡是实现会把参数透传给下游的，fake 至少要检查它是否有效。
> ② **注释声称的行为必须有测试守护**。这段代码的注释写得完全正确，
>    而实现与它相反 —— 注释不会失败，测试才会。

### 2 · `FailReport` 无条件 `return nil`

落库失败只写日志，**worker 以为已标记失败**，报告永远留在 `processing`。
修复：返回并向上传播错误。

> 这与第 1 条是同一类：**错误被吞掉，恢复路径看起来"处理过了"**。

### 3（测试错，但暴露真风险）· `{}` 是合法的 JSON Schema

我原本写测试断言 `{}` 应该编译失败。**测试失败了 —— 因为它是合法的**，含义是"任何内容都通过"。

**这才是真正危险的**：Schema 被误改成空对象（或必填字段被删光）时，
**编译成功、校验永远通过、降级路径永不触发**，
垃圾数据被当成结构化结果入库，**而且没有任何报错**。

修复：加 `SummaryValidator.SelfCheck()` —— **用空对象做反向验证**，
把这个陷阱变成**启动即失败**。

> **这是全项目"反向验证"思想最漂亮的一次应用**：
> 我们不是测试"正确的 Schema 能通过"，而是测试"**退化的 Schema 不能通过**"。
> 前者只能覆盖想到的情况，后者把一整类退化都变成启动失败。

### 4 · Mock 按 byte 分片会截断中文

必须按 **rune** 切分，否则前端显示乱码。有专门测试断言每个分片都是合法 UTF-8。

### 5 · 202 响应漏掉 `trace_id`

直接构造 `response.Body{...}` 忘了填 `TraceID` → 该请求在日志与审计里**断链**。
已加 `response.Accepted` helper。

### 6（我的断言错了 3 次，不是代码缺陷）

| 断言 | 为什么错 |
|---|---|
| "JSON 载荷能字节级往返" | JWT 载荷是 JSON，**JSON 字符串无法承载非法 UTF-8**，字节级往返本就不成立 |
| "输出必须不等于输入" | 反例 `Name("0***")` —— 输入已经脱敏，输出等于输入是**正确**的 |
| "长度按 rune 守恒" | `Phone` 对超长输入会归一到 11 位，**本就不守恒** |

> **fuzz 报失败时，第一件事不是改代码，而是问"这条性质真的必须成立吗"。**
> 定性的方法是回实现读一遍，而不是看到红就直接改。

---

## 五、反向验证记录

| 被测性质 | 反向验证方式 | 结果 |
|---|---|---|
| **消费者组重建用独立 ctx** | 重建改回用已取消的 ctx → `TestLoop_RecreatesMissingConsumerGroup` | **变红 ✅**（断言"重建时 ctx 不应已失效"） |
| **Schema 自检** | `SelfCheck()` 用空对象做反向验证 | 空对象 → **启动失败** ✅ |
| 降级路径真的被执行过 | 故障注入 `summary_mode: invalid_json` 实测 | ✅ `attempts=2, fallback=true, structured_json IS NULL`，`XPENDING=0`，死信=0 |
| SSE 客户端断开 | `stream_error_at: N` 故障注入 | ✅ 流中断被识别为客户端断开，不写响应 |
| 分片 UTF-8 合法 | mock 测试逐分片断言 | ✅ |
| flaky 用例 | 修完**在 CPU 负载下**连跑 20 次 | ✅ 全绿 |

**降级路径的实测数据（这是 spec G3 的验收证据）**：

```
attempts=2, fallback=true, structured_json IS NULL
conversation 2 轮保留
XPENDING=0, 死信队列=0
llm_calls_total{purpose="summary",result="success"}=2   ← 两次调用都成功，只是输出不合法
```

最后一行值得注意：**`result="success"` 是对的** —— LLM 调用本身成功了，
不合法的是它的输出。把"上游调用失败"与"输出不符合 Schema"分成两个维度，
才能在告警时区分"模型挂了"和"模型不听话"。

---

## 六、遗留与未验证

| 项 | 说明 |
|---|---|
| **真实 LLM 接入** | `llm.NewClient` 对 `openai`/`dashscope`/`deepseek` **明确返回错误**（刻意设计）。当前只有 mock。**这是 G3 之外的未完成项**：真实厂商的限流、超时、流式分片边界都没有被真实验证过 |
| `llm` 流式分片解析无 fuzz 目标 | 已列为待办。这是**解析不可信输入**的典型场景（上游返回的分片可能在任何字节位置切断） |
| 死信队列的**人工处理入口** | 死信只被记录，没有"重放死信"的管理端点。需要人工介入时只能直接操作 Redis |
| 消费者组的**消息上限** | `XADD` 未设 `MAXLEN`，流会无限增长。当前报告量小，长期运行需要加 `MAXLEN ~` 或定期 trim |
| 对话的 Redis TTL | 对话有 TTL，但**提交后是否清理**未逐一验证；TTL 到期时未提交的对话会静默消失（用户看到"对话没了"） |

---

## 交叉引用

- 结构与决策：`docs/MODULES.md` §5（含 SSE 要点、故障注入表）
- 缺陷账：`docs/FIXES.md` H1
- 原始记录：`docs/HANDOVER.md` §2.7-A（HIGH）、§2.7-D（`FailReport`）、§2.9-C（fuzz 断言）
- 易错点：`docs/PITFALLS.md` §6（断言恒为真）、§2（ctx 复用）
- 接手规则：`AGENTS.md` §5.6（`{}` 合法）、§5.12（fuzz 报失败先怀疑断言）
- **运行期提示词**：`docs/ai-vibe-coding/prompts/preconsult_v1.md`（指向 `backend/prompts/`）

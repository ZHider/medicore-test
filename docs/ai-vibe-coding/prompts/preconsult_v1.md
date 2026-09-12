# AI 预问诊 System Prompt v1（`prompts/preconsult_v1.md`）

> **这个文件是指针，不是内容。**
> 实际文件在 **`backend/prompts/preconsult_v1.md`**。
> 原因不是懒，是**它必须在 Go module 目录里才能被编译进二进制**。

## 为什么不能放这里

`backend/prompts/prompts.go` 用 `go:embed` 把提示词编进二进制：

```go
//go:embed *.md *.json
var files embed.FS
```

`go:embed` 的硬约束：**只能嵌入本包目录（含子目录）下的文件**，
不允许用 `..` 跳出包目录。所以提示词必须待在 `backend/prompts/`，
放进 `docs/` 就编不进去，运行时只能改成读文件系统 ——
而那是**更差的设计**：

| | `go:embed`（实际） | 运行时读文件 |
|---|---|---|
| 部署 | 单二进制，不会漏文件 | 必须记得把 prompts 目录一起拷进去 |
| 一致性 | 提示词与代码**同提交、同版本** | 二进制与提示词可能版本错配 |
| 失败形态 | 编译期就报错 | 运行时 `no such file`，且可能只在某个分支触发 |

## 这个目录里有什么

| 文件 | 用途 |
|---|---|
| `preconsult_v1.md` | **对话**阶段的 system prompt |
| `preconsult_summary_v1.md` | **总结**阶段的 system prompt（要求输出 JSON） |
| `preconsult_summary_v1.json` | 总结输出的 **JSON Schema**，用于结构校验 |
| `prompts.go` | 版本化读取（`readVersioned`，`version` 传空串取当前推荐版） |
| `prompts_test.go` | 守护提示词与 Schema 的存在性、版本可解析性、Schema 非空 |

## 版本化是怎么落地的（spec §7.2 要求）

1. 文件名自带版本：`preconsult_v1.md`；
2. 读取时按版本号拼路径（`readVersioned("preconsult", "md", version)`）；
3. **实际使用的版本写进数据库**：`ai_pre_reports.prompt_version`。
   于是拿到一份历史报告时，能确切知道它是被哪一版提示词生成的 ——
   排查"同一份对话为什么两次总结结果不同"时，这是唯一的抓手。

## 与 spec §10.3 的差异

spec 期望的是：

```text
docs/ai-vibe-coding/prompts/
├── system-rules.md      # 全局规则提示词
└── preconsult_v1.md     # AI预问诊 system prompt
```

实际是：

| spec 期望 | 实际位置 | 差异原因 |
|---|---|---|
| `system-rules.md` | `AGENTS.md`（本目录留指针） | 换成仓库文件，可版本化、自动加载（详见 `system-rules.md`） |
| `preconsult_v1.md` | `backend/prompts/preconsult_v1.md`（本目录留指针） | **`go:embed` 不允许跳出包目录**，它必须与代码同目录才能编进二进制 |

**这是刻意偏离 spec §10.3 的一处**（同上表第 6 条）。
完整的 6 处偏离清单在 `docs/HANDOVER.md` §4 —— 那里是唯一事实来源，本文件不复述编号。

留指针而不是复制，是为了让"提示词只有一个副本"这个性质保持成立。

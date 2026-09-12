# 前后端契约同步方案（待做，未采纳）

> **状态：方案存档，尚未实施。**
> 本文记录"如何让前端动态、同步、可靠地拿到**接口**与**数据结构**"这一问题的
> 候选方案、实测证据、利害权衡与下一步。
> **等需要时再拿出来照做**——现在不必读全文，直接看 §0 与 §6。

---

## 0. 一分钟结论（如果你只读一段）

**问题的答案不是"用哪个工具"，而是三件事分开做：**

| 你要的 | 机制 | 靠什么 |
|---|---|---|
| **动态**（不手抄第二遍） | **生成** | `go/ast` 抽取，零依赖 |
| **同步**（后端改前端跟着改） | 生成 + **产物提交进仓库** | 契约变更出现在 code review 的 diff 里 |
| **可靠**（万一没同步上，要炸） | **检测**，不是生成 | 契约金文件 + 双向测试 |

> **生成解决"省事"，测试解决"可信"，两者不能互相替代。**
> 只有检测没有生成 → 手写但不会烂，只是累；
> 只有生成没有检测 → 生成器一个映射 bug 就静默产出错类型。
> **检测是底线，生成是加速。**

**建议的落点：先做 L1（契约金文件 + 双向测试），零新依赖，约 270 行。**
跑通后再决定要不要 L2（从同一抽取器生成 TS 骨架）。

---

## 1. 为什么这个问题在本项目特别值得做

因为**最容易出错的地方，恰好是代码生成最容易生成错的地方**。

Go 的 `omitempty` 有三种语义，TS 里只有一个 `?`：

| Go 写法（本项目真实存在） | 实际 JSON | 天真生成器会写成 | 对不对 |
|---|---|---|---|
| `NextCursor uint64 \`json:"next_cursor,omitempty"\`` | **字段整个消失** | `next_cursor: number` | ❌ 前端当必填用会崩 |
| `QueueNo *int \`json:"queue_no,omitempty"\`` | 消失 / 数字 | `queue_no: number` | ❌ 漏了 `undefined` |
| `CheckedInAt *string \`json:"checked_in_at"\`` | **`null`** / 字符串 | `checked_in_at: string` | ❌ 漏了 `null` |

这三条已经写进 `frontend/AGENTS.md` §2.10 作为警告。
**所以生成必须配明确的映射规则 + 对规则本身的表驱动测试**，不能"反射一把梭"。

---

## 2. 实测证据（不是推测）

用一个临时探针（纯标准库 `go/ast`，已删除）在 `backend/` 上跑出：

| 项 | 结果 |
|---|---|
| DTO 类型数（含 `json` 标签的导出 struct） | **26** |
| DTO 字段总数 | **155** |
| 路由 | **29 条（GET 18 + POST 11）** |
| 新依赖 | **0** |

抽出的路由与已知端点数**完全一致**，样例也对：

```
GrabRequest:  ScheduleID -> schedule_id
              IdempotencyKey -> idempotency_key
QueueItem:    ID -> appointment_id      ← 注意 Go 名与 JSON 名**不同**
              CheckedInAt -> checked_in_at
```

> `QueueItem.ID` 映射到 `appointment_id` 这一条，正是"手抄必错"的典型：
> 光看 Go 结构体名字根本猜不到 JSON 字段名。

### 2.1 能力边界（诚实清单）

**`go/ast` 能抽出来的：**

| 能 | 依据 |
|---|---|
| DTO 类型名 + 字段名 + `json` 名 | 实测 26 / 155 |
| `omitempty` 与指针类型（决定 TS 该不该带 `?`） | tag + `*ast.StarExpr` |
| 全部路由 method + path | 实测 29 条 |
| 中间件挂载（`RequireRole` 等） | `ast.CallExpr` 实参可读 |

**抽不出来的（必须显式声明，别指望推导）：**

| 不能 | 为什么 |
|---|---|
| 每个路由**返回哪个 DTO** | 藏在 `response.OK(c, page)` 的函数体里，静态推导不稳 |
| 错误码 → HTTP 状态码 | `errcode.httpStatus` 是**私有 map**，要额外解析 map 字面量 |
| SSE 事件协议（`delta`/`done`/`error`） | 藏在 handler 闭包里 |
| **路由组的 URL 前缀** | 实测时就得**手写**一张映射表：`{r:"", g:"/api/v1", auth:"/api/v1/auth", admin:"/api/v1/admin"}` |

最后一条要特别留意：**它本身就是一处会漂移的手写映射**。
所以 `router.go` 里加新 group 时，抽取器必须同步——这要有测试守。

---

## 3. 候选方案

### L1 · 契约金文件 + 双向测试（**建议先做这一层**）

这是"可靠"的落点，也是 L2 的前置（抽取器是同一份）。

```
backend/internal/contract/
├── contract.go          # go/ast 抽取，产出**确定性** JSON（排序！）
└── contract_test.go     # 断言 committed 的金文件 == 现场生成的
frontend/src/types/api.contract.json      # 生成的金文件，提交进仓库
frontend/src/types/api.contract.test.ts   # 断言手写的 api.ts 覆盖契约里每个字段
```

**确定性是硬要求**：map 遍历在 Go 里是随机的，不排序就会每次生成都 diff。
`ast.Inspect` 按文件顺序稳定，但**跨文件的类型要按名字排序**。

**它抓得住 5 类漂移，每类都会让 CI 变红：**

| 漂移 | 谁抓到 |
|---|---|
| DTO 加/删/改字段 | 后端契约测试（金文件 diff） |
| `json` 标签改名 | 同上 |
| 路由 path / method 变了 | 同上 |
| 新增端点，前端没跟上 | 前端契约测试（契约里有、`api.ts` 里无） |
| 枚举加了新值 | 同上（枚举值也进契约） |

**关键性质：金文件提交进 git。** 于是契约变更**必然出现在 code review 的 diff 里** ——
这是给人看的最后一层网，也是"同步"真正落地的地方。

**成本**（估）：

| 件 | 规模 |
|---|---|
| `internal/contract` 抽取器 | ~200 行 |
| 契约金文件 | 生成，约 400 行 JSON |
| 后端契约测试 | ~30 行 |
| 前端契约测试 | ~40 行 |
| **合计** | **约 270 行手写 + 生成物** |

**代价**：
- 后端多一个包，前端的 `api.ts` **仍需手写**（这是有意的，见下）
- 抽出器里的路由前缀映射表是新的漂移点，需测试守护

**为什么保留手写 `api.ts`**：可读性、可加手写映射（如把 `"am"/"pm"` 变成中文）、
以及**给人一个不看代码就能读懂契约的地方**。L1 只是保证它**不会静默漂移**。

---

### L2 · 从同一抽取器生成 TS 骨架

L1 的抽取器加一个 `-format=ts`：

```ts
// 由 backend/internal/contract 生成 —— 请勿手改，改 model 后跑 nub run contract:gen
export interface GrabRequest {
  schedule_id: number
  idempotency_key?: string        // omitempty 且有指针 → 可选
}
export interface AppointmentItem {
  queue_no?: number               // *int + omitempty → 可选且可能 undefined
  expire_at?: string
}
export interface QueueItem {
  checked_in_at: string | null    // 无 omitempty → 字段必在，但值可能 null
}
```

**注意最后三行**：正确性取决于**映射规则**，不取决于生成器。
所以必须给映射规则配**表驱动测试**（每个 Go 类型形态 → 期望 TS 片段），
就像 `internal/model/pagination_test.go` 那样。

**代价**：
- 生成产物与手写类型**并存时的取舍**要定（建议：生成物只进 `api.contract.ts`，
  业务层可在此基础上手写扩展类型）
- 前端会多一个"生成代码"的概念，需要 `nub run contract:gen` 入口与文档

---

### L3 · OpenAPI（**现在不做**）

只有当需要**给外部消费者**、或就是想要那套 Swagger UI 时才值得。

换来的独有价值是 **HTTP 语义**：路径参数、状态码、请求体绑定、SSE 的 `text/event-stream`。
而这些用"显式映射表 + 覆盖性测试"也能覆盖到八成。

**若要做，选型结论**（星数为实测）：

| 方案 | Stars | 路线 | 评价 |
|---|---|---|---|
| `grpc-gateway` | 20,002 | proto-first | Go 生态第一，但要**换协议栈**，对本项目是重写 |
| `swaggo/swag` | 13,017 | **注解式** | ❌ **漂移风险最高**：注解与实现是两份东西 |
| `oapi-codegen` | 8,570 | 契约先行 | 需先手写 yaml 再生成，与"代码是 SSOT"冲突 |
| `goadesign/goa` | 6,100 | DSL 先行 | 零漂移，但要换框架 |
| `danielgtaylor/huma` | 4,391 | **类型反射式** | ✅ 若重来会选它：从 Go 类型出 schema，无注解 |
| `getkin/kin-openapi` | 3,294 | 解析/校验库 | 不是生成器，可用于"契约测试" |

**若将来真要 OpenAPI，正确组合是**：
手写 `docs/openapi.yaml` + **一个断言"路由集合 == yaml path 集合"的测试** +
用 redocly 静态托管。**零 Go 依赖、零 handler 改动、且契约受测试守护。**

---

## 4. 明确**不要**做的

### ❌ 运行时拉契约生成类型

```
后端开 /api/v1/contract，前端构建时 fetch 它生成 .d.ts
```

三个理由：

1. **构建顺序耦合** —— 后端没起，前端构建不了。CI 里要么多起一个服务，
   要么就跳过生成 —— 而"跳过生成"意味着契约失效。
2. **没有编译期保障** —— 类型是构建时才知道的，IDE 里无法补全、无法跳转。
3. **离线开发答不上来** —— 面试官一定会问"后端没起你怎么开发"。

**契约应该是编译期/提交期的产物，不是运行时的。**

### ❌ 只做生成不做检测

生成器里任何一个映射 bug（比如把 `*string` 生成成 `string`）都会
**静默**产出错的类型，而且**没有任何东西会变红**。
这比手写更危险：手写至少有人在看。

### ❌ 用 `swaggo` 注解

注释与实现是两份东西。本项目已经因为"手写路由清单"**错过两次**
（`AGENTS.md` §5.7），再加一层手写注解就是第三次。

---

## 5. 行业参照（做决策时的背景）

2025 Go Developer Survey（5,379 名受访者）报告的**三大困扰**里：
"符合 Go 惯例/最佳实践" 33%、"缺别的语言特性" 28%、"找到可信模块" 26%。
**API 文档工具不在榜上**，而 55% 的受访者用 Go 写 API 服务。

Go 的文档文化是 **godoc-first**：标准库不产 OpenAPI（靠 `go doc` / pkg.go.dev），
Kubernetes 从 Go 类型**反向**生成 OpenAPI。
**Swagger 在 Go 里是可选项，在 Java/TS 里接近默认项** —— 这个认知差是很多人的焦虑来源。

本项目已有的三层"文档"，与本文方案的关系：

| 现有 | 覆盖什么 | 本文方案补什么 |
|---|---|---|
| `frontend/AGENTS.md` §3 | HTTP 语义（路径/角色/限流/SSE/分页） | —— 人读，**手写会漂移** |
| `backend/test/integration/`（60 例） | 端点的**真实行为** | —— 是"活契约"，但不覆盖 DTO 字段全集 |
| `go doc` / godoc | Go 侧类型与注释（**含结构体 tag**） | —— 读 Go 的人用，前端用不上 |
| **本文 L1/L2** | **机器可读的 DTO+路由契约** | ✅ 补上"不会漂移"这一维 |

> 附：`go doc` 实测结论——
> 结构体 tag **会**显示（`json:`/`binding:`/`gorm:` 都能看到），
> `go doc -http` 起的是**本地 pkg.go.dev UI**（`internal/` 包也能渲染）。
> 但两个坑：`-http` 是**布尔开关不能指定端口**（随机挑）；
> **首次运行会下载 `golang.org/x/pkgsite` 等约 7 个模块**（真实网络出口）。

---

## 6. 下一步（需要时照此执行）

**建议顺序：先 L1，跑通再决定 L2。**

- [ ] **① 写抽取器** `backend/internal/contract/contract.go`
  - 用 `go/parser` + `go/ast`（**零新依赖**）
  - 输出**确定性** JSON：所有切片按名字排序
  - 覆盖范围：`internal/model` 的导出 struct（有 `json` 标签者）+ `internal/errcode` 的
    码/文案/HTTP 状态 + `internal/router` 的 method/path
  - 路由前缀映射表写在代码里，**并加注释说明这是手工维护的**
- [ ] **② 生成金文件** `frontend/src/types/api.contract.json` 并提交
- [ ] **③ 后端测试** `contract_test.go`：现场生成 == 金文件
  - **反向验证**：改一个 `json` 标签，确认测试变红（本项目一贯要求，见 `AGENTS.md` §7.2）
- [ ] **④ 前端测试** `api.contract.test.ts`：契约里每个类型/字段都能在 `api.ts` 找到
- [ ] **⑤ 加 `nub run contract:gen`** 到 `package.json`（重新生成金文件）
- [ ] **⑥ 决定是否做 L2**：把 TS 生成接到同一抽取器上
- [ ] **⑦ 若做了 L2**：映射规则配表驱动测试（覆盖 `omitempty` / 指针 / 枚举三种形态）

**同时要改的文档**：
- `frontend/AGENTS.md` §5.4（类型定义放哪）改为指向契约流程
- `AGENTS.md` §4 命令表加 `contract:gen`
- `docs/FIXES.md` 记录这次引入

**验收判据**：故意改一个 `json` 标签 → **CI 必须变红**，且错误信息要指出是哪个字段。

---

## 7. 风险与注意

| 风险 | 说明 |
|---|---|
| **抽取器自身的漂移** | 路由前缀映射表是手工的；新增 group 忘了加就漏路由。**要有"路由条数 == 29"这类断言** |
| **`omitempty` 判定规则** | 必须显式定义（有 `omitempty` → 可选；无 `omitempty` 但字段可能为 `null` → `T \| null`）。**规则要测** |
| **金文件的 diff 噪音** | 不排序就会每次全文件 diff。**确定性是硬要求** |
| **过度生成** | 只生成 DTO 与路由，**不要**试图推导返回类型绑定（§2.1 已说明不可靠） |
| **给已完成的后端加代码** | 这属于**新增功能**，不在"只写交接文档"的范围内。做之前应确认优先级 |

---

## 8. 相关文件

| 文件 | 关系 |
|---|---|
| `frontend/AGENTS.md` §2.10 / §5.4 | 本文要解决的漂移，在那两节有具体案例与字段约定 |
| `AGENTS.md` §5.7 | "路由清单以代码为准，已错过两次"——本文的动机 |
| `AGENTS.md` §2.8 | 引入新依赖前必须先问（**本方案 L1/L2 零依赖，不受此限**） |
| `docs/ai-vibe-coding/reviews/06-m6-records-sync.md` §三⑥ | `go doc` 全量核验第三方 API 的方法（同一套思路） |
| `backend/internal/router/router.go` | 路由的权威来源 |
| `backend/internal/model/*.go` | DTO 的权威来源 |

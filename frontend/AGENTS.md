# frontend/AGENTS.md —— 前端交接文档

> **给接手前端的 agent：这是第一份要读的文件。**
> 读完这份就能开工；需要细节时按文中指引跳转。
>
> **后端已全部完成**（29 个端点、M1–M6、实测通过）。你要做的是 spec §8 的页面。
> 项目级背景见仓库根的 `AGENTS.md`；后端模块细节见 `docs/MODULES.md`。

---

## 0. 三十秒了解现状

| 项 | 状态 |
|---|---|
| 后端 | ✅ 29 个端点全部可用，接口契约已冻结（本文档 §2/§3 是权威来源） |
| 前端 | ⚠️ **只有 create-vue 脚手架**：`App.vue` 里是 `HelloWorld.vue`，没有路由、没有 Pinia、没有 axios 封装、没有页面 |
| 依赖 | 已装好：`vue` 3.5 / `vue-router` 5.3 / `pinia` 4.0 / `axios` 1.20 / `element-plus` 2.14 / `echarts` 6.1 |
| 构建 | `vite` 8.3 / `typescript` **5.9.3（锁死）** / `vue-tsc` 3.3 / `eslint` 10 |
| 包管理 | **`nub`**（不是 npm / pnpm / yarn） |
| 未完成 | 全部页面、axios 封装、路由与角色分流、ECharts 看板 |

---

## 1. 立刻能跑起来

### 1.1 ⛔ 第一件事：配 Vite 代理（**不配就一定失败**）

**后端没有任何 CORS 中间件**（`go.mod` 里也没有 cors 依赖，已核实）。
浏览器的同源策略会让前端直连 `http://127.0.0.1:8080` 的请求**全部被拦**，
而且后端**无法通过配置解决**。

**正确做法：用 Vite 的开发代理，让浏览器看到的是同源请求。**

```ts
// frontend/vite.config.ts —— 当前这个文件里没有任何 proxy 配置，必须加
import vue from '@vitejs/plugin-vue'
import { defineConfig } from 'vite'

export default defineConfig({
  plugins: [vue()],
  server: {
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', changeOrigin: true },
      // /metrics 与探针不在 /api 下，管理看板要用
      '/metrics': { target: 'http://127.0.0.1:8080', changeOrigin: true },
      '/healthz': { target: 'http://127.0.0.1:8080', changeOrigin: true },
      '/readyz': { target: 'http://127.0.0.1:8080', changeOrigin: true },
    },
  },
})
```

于是前端代码里 **baseURL 一律写相对路径**（`/api/v1`），不要写 `http://127.0.0.1:8080`：

```ts
const api = axios.create({ baseURL: '/api/v1' })
```

生产环境同理 —— spec §11.2 的部署方案是 nginx 同时托管静态资源与反代 `/api`，
**从头到尾都是同源**，所以"相对路径"这个写法在开发与生产都成立。
（若将来确实要前后端分域部署，需要**后端**加 CORS 中间件，那是后端改动，别在前端绕。）

### 1.2 起服务

```powershell
# 1) 依赖服务（MySQL + Redis）
nub run deps:up

# 2) 首次需要生成本地密钥（仓库里不含任何密钥）
cd backend; go run ./cmd/genlocal; cd ..

# 3) 启动后端（默认监听 127.0.0.1:8080）
$env:MEDICORE_MYSQL_PASSWORD='medicore123'
nub run backend:dev

# 4) 另一个终端：前端
nub run frontend:dev
```

**确认后端活着**：`curl http://127.0.0.1:8080/healthz` →
`{"code":0,"message":"ok","data":{"status":"ok"},"trace_id":"..."}`

### 1.3 演示账号（`nub run backend:seed` 之后）

**先跑一次 `nub run backend:seed`**（幂等，可重复跑）。它创建的账号：

| 角色 | 手机号 | 口令 | 数量 |
|---|---|---|---|
| **admin** | `13800000000` | 环境变量 `MEDICORE_ADMIN_PASSWORD`；**未设置时用 `Admin@12345` 并打印警告** | 1 |
| **doctor** | `13900000001` … `13900000012` | 全部是 `Doctor@123` | 12 |
| **patient** | — | — | **0**（seed 不建患者，自己注册） |

**其余 seed 数据**（`backend/cmd/seed/main.go`）：

- **12 位医生**，每人**只属于一个科室**；科室分布 IM 5 / SURG 3 / PED 2 / OBGY 2，
  职称按 `doctorPlan` 的 `titles` 轮转。
- **168 个排班** = 12 医生 × 1 科室 × **7 天** × 2 个半天，全部 `total = 20`、`remaining = 20`、`status = open`。
  日期从**本地时区的今天**起算。

**没有患者账号**（`POST /api/v1/auth/register` 自己注册，**只会得到 patient 角色**）。
医生与管理员**无法自助注册**（刻意的防提权设计），只能用 seed 数据。

#### ⚠️ 当前开发库里还有额外的数据

实测（`medicore` 库）：**18 位医生 / 178 个排班（`total` 在 15–30 之间）/ 200 个患者**。
多出来的部分来自：

| 来源 | 产生什么 |
|---|---|
| `POST /admin/sync/mockhis`（或 cron 的 HIS 同步） | 额外医生与排班，**号源数与 seed 不同**（15–30） |
| `nub run backend:loadtest`（压测） | 200 个压测患者，手机号 `13700000001` 起 |

**所以如果你要一份可预期的数据，先重置**：

```powershell
nub run deps:reset          # ⚠️ 会删数据卷，MySQL/Redis 全部清空
nub run deps:up
cd backend; go run ./cmd/genlocal; cd ..   # 若 config.local.yaml 也没了
nub run backend:seed        # 得到上表那份干净数据
```

> **做页面时别依赖"数据库里有 200 个患者"这件事** —— 那是压测残留，重置后就没了。

### 1.4 可用命令

| 命令 | 作用 |
|---|---|
| `nub run frontend:dev` | 开发服务器 |
| `nub run frontend:build` | **`vue-tsc -b && vite build`**（类型错误会**中断构建**） |
| `nub run frontend:typecheck` | 只做类型检查 |
| `nub run frontend:lint` | ESLint（flat config） |
| `nub --cwd frontend add <pkg>` | 加依赖（**不用 npm install**） |
| `nub run backend:seed` | 灌演示数据（幂等） |

---

## 2. 后端契约（不可协商）

这一节是**接口的硬约定**。改不了，只能适配。

### 2.1 统一响应信封

**所有** `/api/*` 响应（包括错误）都是这个形状：

```jsonc
// 成功
{ "code": 0, "message": "ok", "data": { /* 实际数据 */ }, "trace_id": "3f2a…" }
// 失败
{ "code": 40002, "message": "号源不足", "data": null, "trace_id": "3f2a…" }
```

三条必须记住的：

1. **业务数据在 `data` 里**，不要直接读顶层。
2. **HTTP 状态码与 `code` 是两套东西**。成功不一定是 200（异步接口返回 **202**，
   但 `code` 仍是 0）；失败时 HTTP 状态由错误码映射（见 §2.2）。
   **判定成功请用 `code === 0`，不要用 `status === 200`。**
3. **`trace_id` 一定要用起来**。它是排障的唯一抓手：把它显示在错误提示里
   （例如"操作失败（追踪号 3f2a…）"），用户截图即可定位到 `audit_logs` 与后端日志。
   响应头里也有 `X-Trace-Id`，值相同。

**axios 拦截器的正确写法**（HTTP 4xx/5xx 时 axios 会 throw，信封在 `error.response.data`）：

```ts
api.interceptors.response.use(
  (res) => res.data,                                  // 统一剥掉一层
  (err) => {
    const body = err.response?.data                  // 后端给的信封
    return Promise.reject(new ApiError({
      code: body?.code ?? -1,
      message: body?.message ?? '网络异常，请稍后重试',
      traceId: body?.trace_id ?? err.response?.headers['x-trace-id'] ?? '',
      httpStatus: err.response?.status ?? 0,
    }))
  },
)
```

### 2.2 错误码全表

`400xx` 业务校验 / `401xx` 认证 / `403xx` 权限 / `429xx` 限流 / `500xx` 服务端 / `502xx` LLM 上游。

| code | HTTP | 含义 | 前端该做什么 |
|---|---|---|---|
| `0` | 200/202 | 成功 | — |
| `40001` | 400 | 参数校验失败 | 表单错误提示（`message` 里可能有具体原因） |
| `40002` | **409** | 号源不足 | 提示并**刷新排班列表**（`remaining` 已变） |
| `40003` | 404 | 资源不存在 | 空状态页 / 返回上一页 |
| `40004` | 409 | 状态冲突 | 提示 + 重新拉取该资源 |
| `40005` | 409 | 操作已完成，请勿重复提交 | **按幂等成功处理**（见 §2.7） |
| `40006` | 405 | 方法不支持 | 前端 bug，不该出现 |
| `40101` | 401 | 未认证或凭证无效 | 清 token → 跳登录 |
| `40102` | 401 | **登录已过期** | **先尝试 refresh，失败才跳登录**（见 §2.3） |
| `40103` | 401 | 刷新令牌无效 | 清 token → 跳登录 |
| `40301` | 403 | 无权限 | 提示"无权限"，不要跳登录 |
| `40302` | 403 | **无权访问他人资源** | 同上。**这是资源归属校验失败**，不是角色不够 |
| `42901` | 429 | 限流 | 读 `Retry-After` 响应头（秒）并倒计时 |
| `50001` | 500 | 服务器内部错误 | 通用错误提示 + trace_id |
| `50002` | 503 | 依赖未就绪 | 后端刚启动或 MySQL/Redis 挂了 |
| `50201` | 502 | AI 服务暂时不可用 | 在 AI 对话/报告场景下展示"AI 暂不可用，请稍后重试" |

> **`40101` 与 `40102` 必须分开处理**：前者是"没令牌 / 令牌坏"，后者是"过期"。
> 混成一个分支会让"过期的用户可以自动续期"这个体验丢失。

### 2.3 认证：双令牌

| 令牌 | 形式 | 有效期 | 存哪 |
|---|---|---|---|
| `access_token` | JWT（HS256） | **2 小时** | 内存 / localStorage，随 `Authorization: Bearer <token>` 发送 |
| `refresh_token` | 不透明随机串 | **7 天** | 需持久化（建议 localStorage） |

**登录/刷新/登出的响应形态**：

```jsonc
// POST /auth/login、POST /auth/refresh 的 data
{ "access_token": "…", "refresh_token": "…", "token_type": "Bearer", "expires_in": 7200 }
// POST /auth/logout 的 data
{ "status": "logged_out" }
```

**刷新流程的三个硬约束**：

1. `refresh_token` **是一次性的**：服务端用 Redis `GETDEL` 原子消费。
   刷新成功后**必须用返回的新 `refresh_token` 覆盖旧的**，否则下一次刷新会失败。
2. **并发请求同时 401 时只能触发一次刷新**。多个请求各刷一次会让第二个用已消费的令牌
   → 拿到 `40103` → 被登出。**必须用一个共享的 Promise 做去重**（经典写法：模块级
   `let refreshing: Promise<TokenPair> | null`）。
3. **不要用 `401` 做刷新触发器，要用 `code === 40102`**。`401` 在"刷新令牌也失效"时同样出现，
   用 HTTP 状态做判据会陷入无限刷新循环。

登出要**把 `refresh_token` 一起发给后端**（`POST /auth/logout` body 是
`{"refresh_token": "…"}`）—— 只在前端删 token 不会撤销服务端的那一份。

### 2.4 分页：游标，**禁止页码**

所有列表接口统一返回：

```jsonc
{
  "items": [ /* … */ ],        // 永远是数组，空时是 [] 而不是 null（后端已保证）
  "next_cursor": 12345,        // 数字；没有下一页时**整个字段省略**
  "has_more": true
}
```

| 查询参数 | 说明 |
|---|---|
| `cursor` | 上一页返回的 `next_cursor`。**首页不传**（或传空串） |
| `size` | 每页条数，默认 **20**，上限 **100**（超出会被后端静默截到 100，不报错） |

**为什么不能用页码**：后端**根本不支持** `page`/`offset`，这是 spec §5.2 的强制要求。
实测深页差 3 个数量级（`docs/sql-optimization.md` §3.1：游标读 21 行 / 0.022ms
vs `OFFSET 299980` 读 300,000 行 / 94.2ms）。

**无限滚动的正确写法**（spec §8.1 要求 Intersection Observer）：

```
首次：GET /appointments?size=20
下一页：GET /appointments?size=20&cursor=<上次的 next_cursor>
停止：has_more === false（或 next_cursor 为空）
```

**三个容易写错的地方**：

- 判断"还有没有下一页"要用 `has_more`，**不要**用 `items.length === size`
  （最后一页恰好满页时这个判据会出错）。
- `next_cursor` 缺失时它会**从 JSON 里消失**（`omitempty`），不要假设它恒存在。
- **不要在前端做去重后再拼接**——游标天然不重复；如果出现重复，说明把 `cursor` 传错了。

### 2.5 时间格式：**不是 ISO 8601**

后端所有时间字段统一是 **`YYYY-MM-DD HH:MM:SS`**（本地时区，无时区后缀）。

```
"created_at": "2026-09-13 14:05:00"      ← 长这样
```

**不要直接 `new Date("2026-09-13 14:05:00")`**：Safari 与部分环境下这种格式会被判为
invalid date。要么用 `dayjs(str)`（宽松解析），要么手工替换成 ISO：

```ts
const parse = (s: string) => new Date(s.replace(' ', 'T'))   // 本地时区语义
```

**例外**：`sched_date` 是**纯日期** `"2026-09-13"`（就诊日，没有时间部分）。

后端为什么这么做：曾经有一个字段直接序列化了 `time.Time`，结果是 RFC3339
（`2026-09-13T00:52:00+08:00`），前端得为**那一个字段**单独写解析分支。
现在全接口强制统一（`model.APITimeLayout`），不要再引入特例。

### 2.6 SSE 流式对话：**POST + fetch，不是 EventSource**

`POST /appointments/:id/preconsult/chat`

| 项 | 值 |
|---|---|
| 请求头 | `Authorization: Bearer …`、`Content-Type: application/json` |
| 请求体 | `{ "message": "我发烧三天了" }` |
| 成功响应头 | `Content-Type: text/event-stream`、`Cache-Control: no-cache`、`X-Accel-Buffering: no` |

**为什么不能用 `EventSource`**：它只支持 GET，**无法携带请求体与 `Authorization` 头**。
必须用 `fetch` + `ReadableStream`（spec §8.1 也是这么要求的）。

**事件协议**（三种 `event`）：

```
event: delta
data: {"delta":"您"}

event: delta
data: {"delta":"好，"}

event: done
data: {"status":"ok"}

event: error
data: {"code":50201,"message":"AI 服务暂时不可用"}
```

**错误有两种形态，必须都处理**：

| 时机 | 形态 | 怎么判断 |
|---|---|---|
| **流还没开始**（前置校验失败：预约不存在、不属于你、状态不允许） | **普通 JSON 信封**，HTTP 4xx/5xx | 看响应的 `content-type`，不是 `text/event-stream` 就按 §2.1 解析 |
| **流已经开始**（上游出错、中途断开） | 一个 `event: error`，HTTP 状态**早已是 200** | 收到 `error` 事件后按 `code` 处理 |

这就是后端 `startStream()` 延迟到"第一次真要吐数据"时才发响应头的原因 ——
**为了让前置校验失败还能返回统一信封**。前端必须配合这个设计。

**实现要点**：

- `fetch` 不会因为 4xx 抛异常，要自己检查 `res.ok` 与 `content-type`。
- 解析 SSE 要自己按 `\n\n` 切分事件块、按行取 `event:` 与 `data:`。
  **不要用 `EventSource`**，也不要用任何假设 `data:` 只有一行的库（这里确实只有一行，但别写死）。
- **组件卸载时 `AbortController.abort()`**（spec §8.1 明确要求）。
  后端会检测到客户端断开并停止向上游请求。
- 对话历史可恢复：`GET /appointments/:id/preconsult/history` → `{"items":[{"role","content"}]}`。
  `role` 取值 `user` / `assistant` / `system`（历史里只会有前两种）。

### 2.7 幂等键：抢号必须带

`POST /appointments`（抢号）的请求体：

```jsonc
{
  "schedule_id": 123,
  "idempotency_key": "550e8400-e29b-41d4-a716-446655440000"   // 可选，但强烈建议带
}
```

| 规则 | 说明 |
|---|---|
| 长度 | 若提供，必须是 **8–64** 字符（服务端 binding 校验，不满足返回 `40001`） |
| 生成 | 用 `crypto.randomUUID()`，**每次"用户点击抢号"生成一个** |
| 复用 | **同一次提交的重试必须复用同一个 key**；用户重新点击应生成新的 |
| 不带 | 服务端会自己补一个，仍能防"同一次请求被重放"，但防不住"用户连点两次" |

**遇到 `40005`（操作已完成，请勿重复提交）**：不要当错误弹红。
它表示"这个 key 正在处理中"或"已经处理过"，正确反应是**提示"请稍候"并稍后查询结果**。
另外，**幂等重放会返回首次创建的那条预约**（结构完全一致），所以重试是安全的。

### 2.8 限流：读 `Retry-After`

| 端点 | 限制 | 维度 |
|---|---|---|
| `POST /auth/login` | **5 次/分钟** | 按 IP |
| `POST /appointments`（抢号） | **10 次/分钟** | **按用户**（不是 IP，所以同一 NAT 下互不影响） |

被限流时返回 `429` + `code=42901` + **`Retry-After: <秒>` 响应头**。
前端应读这个头做倒计时，而不是写死等待时间。

> ⚠️ **登录被限流时不要提示"密码错误"** —— 用户会去改密码，越改越锁。

### 2.9 脱敏：**只有手机号被脱敏**

这一点容易想反，说清楚：

| 字段 | 是否脱敏 | 说明 |
|---|---|---|
| `UserProfile.phone`（`GET /auth/me`） | ✅ `138****5678` | 后端唯一实际调用脱敏的地方 |
| 医生姓名（科室医生列表、医生详情） | ❌ 原文 | 医生信息公开 |
| **患者姓名**（医生接诊列表、候诊队列、病历、报告） | ❌ **原文** | 医生必须能识别面前的患者，打成"张*"会让队列失去意义 —— 这是 spec §2.2 的**有意例外** |
| `patient_id` / `user_id` | ❌ 原文 | 数字 ID |
| 身份证 | — | **从不返回**（只有 `RegisterRequest` 收，`UserProfile` 里没有这个字段） |

**结论**：前端**不需要**做二次脱敏，也不该自作主张把患者姓名打码 ——
那会破坏医生工作台的核心用途。这些字段由后端的**资源层归属校验**保护
（医生只能看到自己接诊的患者），前端只需正确带着 token 调用。

### 2.10 其它字段级约定

| 约定 | 说明 |
|---|---|
| `consult_fee` 是 **字符串** | `"50.00"`（后端用 string 承载 DECIMAL 以避免浮点误差）。**不要直接参与算术**，需要计算时用 `Number()` 且注意展示精度 |
| `queue_no` 可能**缺失** | `omitempty`，未签到时字段不存在（拿到后注意 `undefined`） |
| `checked_in_at` 可能**是 null** | 未签到时为 `null` |
| `expire_at` / `error_message` / `finished_at` 等 | 同样是 `omitempty`，可能不出现 |
| 列表内层字段 | 只有**顶层**分页字段保证存在，条目内的可选字段按上表处理 |
| 数字 ID | 全部是 `uint64`，**在 JS 里可能超过 `Number.MAX_SAFE_INTEGER`**。当前演示数据远小于该值，但做 `Map` 的 key 时建议用字符串 |

---

## 3. 端点全表（29 条）

`base = /api/v1`。**这张表以 `backend/internal/router/router.go` 为准**，
文档与代码不一致时**信代码**（本项目已经因为手写路由清单错过两次，见 `AGENTS.md` §5.7）。

### 3.1 运维（免认证，**不在 `/api/v1` 下**）

| # | Method | Path | 说明 |
|---|---|---|---|
| 1 | GET | `/healthz` | 存活探针。`data: {status:"ok"}` |
| 2 | GET | `/readyz` | 就绪探针。任一依赖不可用时 **503** + `data` 是**以依赖名为 key 的对象**（见 §3.9） |
| 3 | GET | `/metrics` | **Prometheus 文本格式**，不是 JSON。见 §3.10 |

### 3.2 认证

| # | Method | Path | 角色 | 请求体 / 查询 | 响应 `data` |
|---|---|---|---|---|---|
| 4 | POST | `/auth/register` | 公开 | `{phone, name, password, id_card?}` | `UserProfile` |
| 5 | POST | `/auth/login` | 公开（限流 5/min） | `{phone, password}` | `TokenPair` |
| 6 | POST | `/auth/refresh` | 公开 | `{refresh_token}` | `TokenPair`（**新的 refresh_token**） |
| 7 | POST | `/auth/logout` | 登录 | `{refresh_token}` | `{status:"logged_out"}` |
| 8 | GET | `/auth/me` | 登录 | — | `UserProfile` |

**注册校验规则**（前端应做同样的即时校验，别等后端报 40001）：

| 字段 | 规则 |
|---|---|
| `phone` | 必填，**恰好 11 位，全数字** |
| `name` | 必填，2–32 字符 |
| `password` | 必填，**8–64 字符**（注意：后端 bcrypt 上限 72 **字节**，中文口令会提前触顶） |
| `id_card` | 可选，若填必须**恰好 18 位** |

`UserProfile`：`{ "id": 1, "role": "patient", "name": "张三", "phone": "138****8888" }`
`role` 取值：**`admin` / `doctor` / `patient`**。

### 3.3 目录查询（登录即可）

| # | Method | Path | 查询参数 | 响应 `data` |
|---|---|---|---|---|
| 9 | GET | `/departments` | — | `{items: DepartmentBrief[]}` |
| 10 | GET | `/departments/:id/doctors` | — | `{items: DoctorListItem[]}`（**服务端有 5 分钟 Redis 缓存**） |
| 11 | GET | `/departments/:id/schedules` | `date`(必填, `YYYY-MM-DD`) `cursor` `size` | `CursorPage<ScheduleListItem>` |
| 12 | GET | `/doctors/:id` | — | `DoctorDetail`（含 `departments[]`） |
| 13 | GET | `/doctors/:id/schedules` | `from` `to`(必填, `YYYY-MM-DD`) `cursor` `size` | `CursorPage<ScheduleListItem>` |

**注意**：

- **`/departments/:id/schedules` 的 `date` 是必填的**，不传会怎样？——后端会把它当空串，
  于是按"日期为空"查询。**请始终传**，并在 UI 上默认选中今天。
- 科室不存在 → **404（`40003`）而不是空列表**。这是刻意的：让客户端能区分
  "科室不存在"与"该科室暂无医生"。前端应据此展示不同的空状态。
- `from > to` → **400**。前端应在日期选择器上直接禁止。

```jsonc
// DepartmentBrief
{ "id": 1, "code": "IM", "name": "内科", "description": "…" }
// DoctorListItem
{ "id": 3, "name": "王医生", "title": "主任医师", "specialty": "…",
  "consult_fee": "50.00", "department_id": 1, "is_primary": true }
// DoctorDetail —— 比 ListItem 多 intro 与 departments
{ "id": 3, "name": "王医生", "title": "主任医师", "specialty": "…", "intro": "…",
  "consult_fee": "50.00", "departments": [ DepartmentBrief, … ] }
// ScheduleListItem
{ "id": 88, "doctor_id": 3, "department_id": 1, "sched_date": "2026-09-20",
  "time_slot": "am", "total": 50, "remaining": 37, "status": "open" }
```

`time_slot`：**`am` / `pm`**（前端映射成"上午/下午"）。
`status`：`open` / `closed` / `cancelled`。**只展示 `remaining > 0 && status === 'open'` 的可抢号**。

### 3.4 预约与抢号

| # | Method | Path | 角色 | 请求 / 查询 | 响应 `data` |
|---|---|---|---|---|---|
| 14 | POST | `/appointments` | patient（限流 10/min） | `{schedule_id, idempotency_key?}` | `AppointmentItem` |
| 15 | GET | `/appointments` | patient | `cursor` `size` | `CursorPage<AppointmentItem>` |
| 16 | POST | `/appointments/:id/cancel` | patient | `{reason?}`（**body 可为空**） | `{status:"cancelled"}` |
| 17 | POST | `/appointments/:id/checkin` | patient | — | `{id, queue_no, checked_in_at, status}` |
| 18 | GET | `/doctor/appointments` | **doctor / admin** | `date` `status` `cursor` `size` | `CursorPage<DoctorAppointmentItem>` |
| 19 | GET | `/schedules/:id/queue` | **doctor / admin** | — | `{items: QueueItem[]}` |

```jsonc
// AppointmentItem（患者视角）
{ "id": 501, "schedule_id": 88, "doctor_id": 3, "doctor_name": "王医生",
  "department_id": 1, "department_name": "内科", "sched_date": "2026-09-20",
  "time_slot": "am", "queue_no": 7, "status": "confirmed",
  "expire_at": "2026-09-13 14:20:00", "created_at": "2026-09-13 14:05:00" }

// DoctorAppointmentItem（医生视角，字段完全不同！）
{ "id": 501, "schedule_id": 88, "patient_id": 12, "patient_name": "张三",
  "department_id": 1, "sched_date": "2026-09-20", "time_slot": "am",
  "queue_no": 7, "status": "confirmed", "has_record": false,
  "created_at": "2026-09-13 14:05:00" }

// QueueItem（候诊队列，只含已签到的）
{ "appointment_id": 501, "queue_no": 7, "patient_id": 12,
  "patient_name": "张三", "checked_in_at": "2026-09-20 08:12:00", "status": "confirmed" }
```

**`status` 取值**：`pending`（待确认，15 分钟内有效）/ `confirmed` / `cancelled` / `expired`。
只有 `pending` / `confirmed` 占用号源。

**`has_record` 是医生端最该用起来的字段**：`false` = 还没写病历。
这是后端特意 JOIN 出来的，用来让医生一眼看出"还有谁没处理"。

**`/doctor/appointments` 的 `status` 参数要传白名单内的值**，
传别的会返回 **400**（刻意的：拼错的状态若被静默忽略会返回空列表，
让人误以为"今天没有预约"）。

> **一个极易踩的坑**：`GET /schedules/:id/queue` 返回的 `patient_name` 是**明文**，
> 而且这个端点做了**资源层归属校验** —— 医生只能查**自己**排班的队列，
> 换别人的排班 ID 会拿到 `40302`。前端不要把它当作"传任意排班 ID 都能查"。

### 3.5 AI 预问诊

| # | Method | Path | 角色 | 说明 |
|---|---|---|---|---|
| 20 | POST | `/appointments/:id/preconsult/chat` | patient | **SSE**，见 §2.6 |
| 21 | GET | `/appointments/:id/preconsult/history` | patient | `{items: [{role, content}]}` |
| 22 | POST | `/appointments/:id/preconsult/submit` | patient | **202 Accepted**，见下 |
| 23 | GET | `/appointments/:id/preconsult/report` | patient / doctor / admin | 报告，见下 |

**`submit` 返回 202**（HTTP 202，`code` 仍是 0）：

```jsonc
// data
{ "report_id": 42, "status": "pending", "already_submitted": false }
```

`already_submitted: true` 表示此前已提交过、**本次未重复入队** —— 这是幂等，
不是错误。前端应直接进入轮询。

**报告轮询**（`GET .../report`）：

```jsonc
{
  "id": 42, "appointment_id": 501,
  "status": "pending" | "processing" | "done" | "failed",
  "fallback": false,
  "structured": { /* 见下，fallback 或未完成时缺失 */ },
  "conversation": [ { "role": "user", "content": "…" } ],
  "llm_model": "mock-llm-v1", "prompt_version": "preconsult_v1",
  "error_message": "", "created_at": "2026-09-13 14:20:00", "finished_at": ""
}
```

**前端必须处理的三种终态**：

| 情况 | 判据 | 展示 |
|---|---|---|
| 正常 | `status === 'done'` 且 `fallback === false` | 渲染 `structured` |
| **降级** | `status === 'done'` 且 **`fallback === true`** | `structured` 为 null/缺失，**改为展示 `conversation` 原始对话**，并标注"AI 未能结构化总结" |
| 失败 | `status === 'failed'` | 展示 `error_message`，提供重试入口 |

`pending` / `processing` 时**继续轮询**（建议 1–2 秒间隔，加上"最长轮询时间"与超时提示）。
`structured` 的字段结构由后端 JSON Schema 定义（`backend/prompts/preconsult_summary_v1.json`）：

```jsonc
{
  "chief_complaint": "发热伴咳嗽 3 天",        // string, 2–200
  "present_illness": "…",                     // string, 2–2000
  "past_history": "无",                       // string, ≤2000
  "medications": "无",                        // string, ≤1000
  "allergies": "否认药物过敏史",                // string, ≤500
  "red_flags": [],                            // string[], ≤10 项
  "possible_departments": ["呼吸内科"],         // string[], 1–5 项
  "urgency": "routine"                        // "routine" | "urgent" | "emergency"
}
```

**八个字段全部必填**（Schema 要求），所以 `done && !fallback` 时可以放心渲染。
`urgency` 建议做颜色区分：`routine` 普通 / `urgent` 橙 / `emergency` 红。
`red_flags` 非空时**必须高亮**——它们是可能危及生命的症状。

### 3.6 病历与处方

| # | Method | Path | 角色 | 说明 |
|---|---|---|---|---|
| 24 | POST | `/appointments/:id/record` | doctor | 写病历，**只有该预约的接诊医生**能写 |
| 25 | GET | `/appointments/:id/record` | any | 患者本人 / 接诊医生 / 管理员可读 |
| 26 | GET | `/records` | any | 按身份返回不同的列表 |

```jsonc
// POST body（WriteRecordRequest）
{
  "chief_complaint": "…",     // ≤512，可选
  "present_illness": "…",     // ≤2000，可选
  "diagnosis": "…",           // 必填，2–512
  "treatment_advice": "…",    // ≤2000，可选
  "prescriptions": [          // ≤20 项，可选
    { "drug_name": "…",       // 必填 ≤128
      "spec": "…",            // ≤64
      "quantity": 2,          // 必填 1–999
      "usage_desc": "…",      // ≤255
      "days": 3 }             // 1–365，可选
  ]
}
```

**重复写病历会返回 `40004`（409）** —— 一次就诊只有一份病历。
前端应在进入写病历页面前先 `GET` 一次，已有则改为只读展示。

`GET /records` 的返回**按角色不同**（后端自动分流，前端不用传任何参数）：

| 角色 | 看到什么 |
|---|---|
| `patient` | 自己的病历 |
| `doctor` | 自己**书写**的病历 |
| `admin` | 全部 |

`MedicalRecordView`（详情）含 `patient_name` / `doctor_name` / `department_name` /
`sched_date` / `chief_complaint` / `present_illness` / `diagnosis` /
`treatment_advice` / `created_at` / `prescriptions[]`。
`MedicalRecordListItem`（列表）**故意不含**主诉/现病史/医嘱三段长文本 —— 列表页别指望有。

### 3.7 管理端（**只有 admin**）

| # | Method | Path | 说明 |
|---|---|---|---|
| 27 | GET | `/admin/audit-logs` | 查询参数：`resource` `action` `from` `to` `user_id` `cursor` `size` |
| 28 | POST | `/admin/sync/:source` | `:source` 目前**只接受 `mockhis`**，其它值返回 404 |
| 29 | GET | `/admin/sync-logs` | 查询参数：`source` `cursor` `size` |

**审计查询的 `from` / `to`** 接受两种格式：`YYYY-MM-DD` 或 `YYYY-MM-DD HH:MM:SS`。
格式不对返回 `40001` 且 `message` 说明期望格式。
**`user_id` 写错会被静默忽略**（不报错，就是不加这个条件）—— 前端应自己做数字校验，
否则"筛选了却没生效"很难察觉。

```jsonc
// AuditLogView
{ "id": 9001, "trace_id": "3f2a…", "user_id": 12, "role": "patient",
  "method": "POST", "path": "/api/v1/appointments", "resource": "appointments",
  "resource_id": 501, "action": "create", "status_code": 200, "latency_ms": 42,
  "client_ip": "127.0.0.1", "created_at": "2026-09-13 14:05:00" }

// SyncLogView
{ "id": 7, "source": "mockhis", "trigger_type": "manual",
  "total": 30, "succeeded": 30, "failed": 0, "status": "success",
  "error_message": "", "started_at": "…", "finished_at": "…" }
```

`sync_logs.status`：`running` / `success` / `partial` / `failed`。
`trigger_type`：`manual` / `cron`。

> **`POST /admin/sync/:source` 是同步返回的**（不是 202）—— 模拟数据源很快，
> 管理员点完就能看到结果。接真实 HIS 后若变慢，后端会改成异步 + 轮询。
> 前端**不需要**为它写轮询。

### 3.8 角色 → 端点速查

| 端点组 | patient | doctor | admin |
|---|---|---|---|
| 认证（4–8） | ✅ | ✅ | ✅ |
| 目录（9–13） | ✅ | ✅ | ✅ |
| 预约患者侧（14–17） | ✅ | ❌ | ❌ |
| 医生侧（18–19） | ❌ | ✅ | ✅ |
| 预问诊对话/历史/提交（20–22） | ✅ | ❌ | ❌ |
| 预问诊报告（23） | ✅ 本人 | ✅ 接诊的 | ✅ |
| 病历写（24） | ❌ | ✅ 接诊的 | ❌ |
| 病历读（25–26） | ✅ 本人 | ✅ 自己的 | ✅ |
| 管理端（27–29） | ❌ | ❌ | ✅ |

**两个必须理解的"双层校验"**：

- 上表只说明**角色够不够**（路由层 RBAC）。
- 具体**是不是你的资源**由 service 层判定，失败返回 **`40302`**。
  例如医生访问**非本人接诊**的预约报告 → `40302`，**不是** `40301`。
  前端对这两个码可以给不同的提示文案。

### 3.9 探针与健康

```
GET /readyz → data 是**以依赖名为 key 的对象**（不是数组）：

{ "mysql": { "status": "ok" },
  "redis": { "status": "ok" } }

依赖不可用时该分支变成：
{ "mysql": { "status": "down", "error": "dial tcp …: connect: connection refused" },
  "redis": { "status": "ok" } }
```

任一 `status !== "ok"` 时整体返回 **503**，但 `data` 仍是完整明细
（便于运维直接定位是哪个依赖挂了）。前端做健康面板时：
**只有 `ok` 与 `down` 两种 `status`**，`error` 字段仅在下线时出现。
注意这是 **object 不是 array**，`Object.entries(data)` 才是正确的遍历方式。

### 3.10 `/metrics`（管理看板的系统健康度）

**返回 Prometheus 文本格式，不是 JSON**，且**免认证**。

```
# HELP http_requests_total …
# TYPE http_requests_total counter
http_requests_total{method="GET",path="/api/v1/departments",status="200"} 42
```

已注册的指标（`docs/MODULES.md` §1 / `internal/metrics`）：

| 指标 | 类型 | 标签 |
|---|---|---|
| `http_requests_total` | counter | `method`, `path`, **`status`** |
| `http_request_duration_seconds` | histogram | `method`, `path` |
| `appointment_grab_total` | counter | `result` |
| `llm_calls_total` | counter | `purpose`, `result` |
| `summary_tasks_total` | counter | `result`（`done` / `retry` / `dead_letter` / `malformed`） |
| `stock_reconcile_total` | counter | `result` |
| `audit_entries_total` | counter | `result` |
| `ratelimit_errors_total` | counter | — |

**实现提示**：浏览器里解析 Prometheus 文本需要自己写一个小解析器
（跳过 `#` 注释行，按 `指标名{标签} 值` 切分）。**不要为此引入新依赖** ——
本项目对依赖面很敏感（见 §5.2），一个 30 行的解析函数足够。
另外 `path` 标签用的是**路由模板**（`/api/v1/departments/:id/doctors`），
所以时间序列不会因 ID 而爆炸 —— 前端做 TOP 榜时要按模板聚合。

---

## 4. 要做的页面（spec §8）

| 页面 | 角色 | 必须体现的后端能力 |
|---|---|---|
| **登录 / 注册** | 公开 | 双令牌、`40102` 自动刷新、登录限流倒计时、防用户枚举（失败文案不区分"账号不存在"与"密码错误"） |
| **挂号**（科室 → 医生 → 排班 → 抢号） | patient | 科室列表、医生列表（缓存 5 分钟）、按日期查排班、`remaining` 展示、**抢号带幂等键** |
| **我的预约** | patient | 游标分页**无限滚动**、取消、签到（拿 `queue_no`）、`pending` 的 15 分钟倒计时 |
| **AI 预问诊对话** | patient | **SSE 打字机**、组件卸载 `abort()`、断线提示、历史恢复 |
| **预问诊报告** | patient / doctor | 轮询到终态、**`fallback` 时展示原始对话**、`red_flags` 高亮、`urgency` 配色 |
| **医生工作台** | doctor | `GET /doctor/appointments`（含 `has_record` 筛选"未处理"）、候诊队列、写病历 + 处方 |
| **管理看板** | admin | **ECharts**：预约趋势 / 科室 TOP / 系统健康度（拉 `/metrics`）；审计日志查询；HIS 同步与同步记录 |

**spec §8.2 的工程规范要求**（不是建议）：

1. **Axios 封装**：请求拦截注入 token **与幂等键（uuid）**；401 自动 refresh 重试；
   响应拦截统一错误提示。
2. **状态与路由**：Pinia 管 auth / appointment；**登录后按 role 动态注册路由**。
3. **TypeScript**：所有 API 响应定义 `interface`，**与后端 DTO 严格对齐**
   （字段名/可选性照抄 §3 的表）。

---

## 5. 前端工程约定

### 5.1 `nub`，不是 npm

```powershell
nub run frontend:dev          # 开发
nub run frontend:build        # 构建（含类型检查）
nub --cwd frontend add xxx    # 加依赖
nub --cwd frontend install    # 安装
```

**不要用 `npm install` / `pnpm` / `yarn`**，会产生第二份 lockfile 与不一致的依赖树。

### 5.2 ⛔ 不要引入新依赖（硬约束）

**加任何依赖前先问。** 原因不是保守，是**这个动作会出网**：

- 机主的 `HTTPS_PROXY` 指向一个**计费**的代理；
- 容器侧一律不出网（`GOPROXY=off`），新依赖在容器里拉不到。

这个约束同样适用于前端。**已装好的依赖已经覆盖了 spec §8 的全部需求**
（axios / element-plus / pinia / vue-router / echarts），
剩下的（SSE 解析、Prometheus 文本解析、日期格式化、uuid）**都应该手写**：
`crypto.randomUUID()` 是浏览器内置的，日期格式化用 `Intl.DateTimeFormat` 或几十行工具函数。
详见仓库根 `AGENTS.md` §2.8。

### 5.3 ⛔ TypeScript 锁在 5.9.3

**不要升级到 TypeScript 7。** TS 7 是 Go 原生重写版，**不再提供 JavaScript 编译器 API**：

- `vue-tsc` 会崩溃（`ERR_PACKAGE_PATH_NOT_EXPORTED: './lib/tsc'`）
- `typescript-eslint` 直接抛 `does not support TS 7.0.`

`package.json` 里写的是 `"typescript": "~5.9.3"`（波浪号，只允许补丁级），**保持它**。

> **另一个坑**：`nub` 的 isolated store 里 peer 变体是**粘性**的 ——
> 若哪天真的升了 TS 大版本，**必须先删掉 `node_modules` 重装**，否则会解析到旧变体，
> 出现"看起来能用"的假象（本项目曾因此得出过错误结论）。

### 5.4 类型定义放哪

建议 `src/types/api.ts`：**一个后端 DTO 一个 interface**，字段名与可选性严格照抄 §3。
`next_cursor` 在 Go 里是 `omitempty`，对应 TS 就是 `next_cursor?: number`
（**不要写成 `number | null`** —— 后端不会给 null，是字段消失）。

枚举一律用**字符串字面量联合**而不是 TS `enum`
（`tsconfig.app.json` 开了 `erasableSyntaxOnly`，`enum` 会编译失败）：

```ts
export type Role = 'admin' | 'doctor' | 'patient'
export type AppointmentStatus = 'pending' | 'confirmed' | 'cancelled' | 'expired'
export type TimeSlot = 'am' | 'pm'
export type Urgency = 'routine' | 'urgent' | 'emergency'
```

### 5.5 目录结构建议

现状是脚手架（`App.vue` 直接用 `HelloWorld.vue`）。建议：

```
frontend/src/
├── api/            # 每个后端模块一个文件：auth.ts appointment.ts catalog.ts …
│   └── client.ts   # axios 实例 + 拦截器（含刷新去重）
├── stores/         # Pinia：auth.ts appointment.ts
├── router/         # index.ts + 按角色的路由守卫
├── types/api.ts    # 后端 DTO 的 TS 镜像
├── composables/    # useSSE.ts useCursorList.ts useCountdown.ts
├── views/          # 页面
├── components/     # 可复用组件
└── utils/          # 日期、Prometheus 解析、脱敏展示等
```

删掉脚手架残留：`components/HelloWorld.vue`、`assets/vue.svg` / `vite.svg` / `hero.png`、
`public/` 下的示例图（`favicon.svg` / `icons.svg` 可留）。

### 5.6 路由与角色

**登录后按 role 动态注册路由**（spec §8.2 要求）。三角色的入口互斥：

| role | 首页 |
|---|---|
| `patient` | 挂号 / 我的预约 |
| `doctor` | 医生工作台 |
| `admin` | 管理看板 |

**路由守卫要用 `GET /auth/me` 校验，不能只信 localStorage 里的 role** ——
localStorage 可以被改，而且 token 可能已过期或被撤销。

### 5.7 本地开发小抄

| 事 | 做法 |
|---|---|
| 后端 401 但 token 看着没过期 | 检查 `expires_in`（2 小时）与系统时间 |
| 改了后端代码 | 后端没有热重载，要重启 `nub run backend:dev` |
| 想看某个接口的原始响应 | 浏览器 Network 面板；或 `curl -H "Authorization: Bearer …"` |
| 想看后端日志 | `backend:dev` 那个终端；日志是 JSON，含 `trace_id` |
| 排障 | 拿响应里的 `trace_id` 去查 `GET /admin/audit-logs`（admin 账号） |
| 数据乱了 | `nub run deps:reset`（**会删数据卷**）+ `nub run deps:up` + `nub run backend:seed` |

---

## 6. 陷阱清单（都是本项目的真实教训）

| # | 陷阱 | 后果 | 正确做法 |
|---|---|---|---|
| 1 | 不配 Vite proxy，直接请求 `127.0.0.1:8080` | **所有请求被 CORS 拦掉**，且后端**无法配置**解决 | 配 `server.proxy`，前端写相对路径（§1.1） |
| 2 | 用 `EventSource` 做流式对话 | **无法带请求体与 Authorization 头**，根本发不出消息 | `fetch` + `ReadableStream`（§2.6） |
| 3 | 用 `status === 200` 判成功 | 漏掉 **202**（submit 与所有异步接口） | 用 `code === 0`（§2.1） |
| 4 | 用 `page`/`offset` 分页 | 后端不支持，深页性能差 3 个数量级 | 游标分页（§2.4） |
| 5 | `new Date("2026-09-13 14:05:00")` | Safari 下 invalid date | 手工转 ISO 或用宽松解析（§2.5） |
| 6 | 用 HTTP `401` 触发刷新 | 刷新令牌也失效时会**无限循环** | 用 `code === 40102`（§2.3） |
| 7 | 并发 401 各刷一次 token | 第二个用已消费的令牌 → 被登出 | 共享 Promise 去重（§2.3） |
| 8 | 刷新后不换 `refresh_token` | 下次刷新必失败（旧的已被 `GETDEL` 消费） | 两个令牌都要覆盖（§2.3） |
| 9 | `items.length === size` 判有无下一页 | 最后一页恰好满页时永远多请求一次 | 用 `has_more`（§2.4） |
| 10 | 把 `40005` 当错误弹红 | 用户以为抢号失败，其实已成功 | 按幂等提示"请稍候"（§2.7） |
| 11 | 抢号重试时换新幂等键 | 幂等失效，可能重复占号 | 同一次提交复用同一个 key（§2.7） |
| 12 | 前端自己给患者姓名打码 | **破坏医生工作台的核心用途** | 后端只在 `phone` 上脱敏，其余是刻意不脱敏（§2.9） |
| 13 | 把 `consult_fee` 当 number 算 | `"50.00"` 是字符串；浮点会有舍入误差 | 需要时 `Number()`，展示按字符串处理（§2.10） |
| 14 | 假设 `next_cursor` / `queue_no` 恒存在 | `omitempty` 字段会消失 | 按可选处理（§2.10） |
| 15 | 组件卸载不停 SSE | 后端一直往上游请求，**浪费 LLM 配额** | `AbortController.abort()`（§2.6） |
| 16 | 报告 `fallback: true` 时白屏 | `structured` 是 null | 改展示 `conversation`（§3.5） |
| 17 | 用 `npm install` | 第二份 lockfile，依赖树不一致 | 用 `nub`（§5.1） |
| 18 | 升级 TypeScript 到 7 | `vue-tsc` 崩溃，构建全挂 | 锁 5.9.3（§5.3） |
| 19 | 自己 `add` 一个日期/SSE/Prometheus 解析库 | 触发计费代理出网，且与项目约束冲突 | **先问**；多数应手写（§5.2） |
| 20 | 前端写死 `http://127.0.0.1:8080` | 生产环境失效（nginx 同源部署） | baseURL 用 `/api/v1`（§1.1） |
| 21 | 医生工作台不展示 `has_record` | 医生看不出"还有谁没处理"，工作台失去意义 | 用 `has_record` 做筛选/标记（§3.4） |
| 22 | 以为 `GET /schedules/:id/queue` 任意排班都能查 | 换成别人的排班 → **`40302`** | 只查自己排班的；把 40302 当正常业务分支处理 |

---

## 7. 验收标准（spec G1）

> **G1：三角色全流程闭环** —— 这是项目唯一还没达成的后端相关验收项，
> 而它**只差前端**（后端接口 100% 就绪）。

必须能完整走通这三条：

```
患者：注册 → 登录 → 选科室/医生/排班 → 抢号（幂等）→ 我的预约看到它
      → AI 预问诊对话（SSE 打字机）→ 提交 → 轮询到报告 → 签到拿号

医生：登录 → 工作台看到该患者（has_record=false）→ 候诊队列
      → 查看 AI 报告 → 写病历 + 处方 → has_record 变 true

管理员：登录 → 看板（ECharts：预约趋势 / 科室 TOP / 系统健康度）
        → 审计日志（能按 trace_id 查到刚才那几次请求）→ 触发 HIS 同步 → 看同步记录
```

**同时要满足的工程质量门槛**：

- [ ] `nub run frontend:typecheck` 通过（**零错误**，`build` 也会跑它）
- [ ] `nub run frontend:lint` 通过
- [ ] 所有 API 响应都有 TS `interface`，且与 §3 的字段严格对齐
- [ ] 无新增依赖（或新增前已征得同意）
- [ ] 无 CORS 报错（即 proxy 配对了）
- [ ] SSE 在组件卸载时确实 `abort()`

---

## 8. 明确**不要**做的事

| 不要 | 为什么 |
|---|---|
| 改后端接口来迁就前端 | 后端契约已冻结且被 60 个集成测试 + 251 个单元测试守护。要改先与后端确认，那会是一轮独立改动 |
| 在前端做脱敏 | §2.9：患者姓名**刻意不脱敏**，打码会破坏医生工作台 |
| 引入 UI 之外的依赖（图表库已有 echarts、组件库已有 element-plus） | §5.2 的硬约束 |
| 用 `npm`/`pnpm` 装依赖 | §5.1 |
| 升级 TypeScript / Vue 大版本 | §5.3 与 `package.json` 的 `~` 约束 |
| 在前端拼 SQL 语义的筛选（比如自己算"号源是否够"） | `remaining` 由后端返回，且有并发扣减；前端的判断只能当 UI 提示，最终以接口返回为准 |
| 假设"接口返回 200 就一定成功" | §2.1：判定用 `code === 0` |

---

## 9. 遇到问题时去哪查

| 想知道什么 | 看哪里 |
|---|---|
| **接口契约**（权威） | `backend/internal/router/router.go` + `backend/internal/model/*.go` + `backend/internal/handler/*.go` |
| 端点行为与错误码的实测记录 | `backend/test/integration/`（60 个用例，是行为的活文档） |
| 某个模块为什么这么设计 | `docs/MODULES.md` 对应章节 |
| 后端有哪些坑、怎么诊断 | `docs/PITFALLS.md`（先看 §0 的判据表） |
| 项目整体状态与未完成项 | `docs/FIXES.md` §1、`docs/HANDOVER.md` §3 |
| 后端开发历程与教训 | `docs/HANDOVER.md` §2 |
| 项目级硬规则（对前端同样适用） | 仓库根 `AGENTS.md` §2 |

> **接口清单以代码为准。** 本项目的手写路由清单**已经错过两次**，
> 所以 §3 的表格也标注了它来源于 `router.go`。
> 有疑问时启动一次后端，gin 会逐条打印真实路由。

---

## 10. 一句话交接

**后端是完备的、契约是冻结的、陷阱是已知的。**
你要做的是：**先配 Vite proxy**（否则一步都走不动），
**按 §3 的表写 TS interface 与 api 层**，
**把 §6 的 22 条陷阱当成必读清单**，
然后才有资格开始写页面。别引入新依赖，别改后端。

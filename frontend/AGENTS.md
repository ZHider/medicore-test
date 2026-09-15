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
| 前端 | ✅ **已完成并实测**：三角色全部页面 + axios 封装 + 按角色动态路由 + ECharts 看板 |
| 依赖 | 运行时：`vue` 3.5 / `vue-router` 5.3 / `pinia` 4.0 / `axios` 1.20 / `element-plus` 2.14 / `echarts` 6.1（**运行时依赖未新增**）。开发期新增 `vitest` 5.0 + `jsdom` 30 + `@vue/test-utils` 2.5 + `axios-mock-adapter` 2.1（测试）与 `mockjs` 1.1（**已装但刻意未用**，理由见 §5.8）以及 `concurrently`（在仓库根，用于 `nub run dev`） |
| 构建 | `vite` 8.3 / `typescript` **5.9.3（锁死）** / `vue-tsc` 3.3 / `eslint` 10 / `vitest` 5.0 |
| 测试 | ✅ **202 个用例 / 12 个文件**（`nub run frontend:test`，约 2.9s）。含组件测试、composable 状态机与 **API 契约测试**；关键用例都做过反向验证 |
| 包管理 | **`nub`**（不是 npm / pnpm / yarn） |
| 未完成 | ~~全栈 compose 编排~~ ✅ 已完成（`deploy/compose.yml`，见 `deploy/README.md`）；剩余是**浏览器人工验收** |

### 0.1 本次交付与实测结论

`typecheck` / `lint` / `build` 三者均**零错误零告警**，并对着**真实后端**跑通了三条主流程。

| 项 | 结论 |
|---|---|
| 响应式 | 移动优先 CSS（断点 768 / 1024），<768 用抽屉导航，宽屏用固定侧栏；看板图表走 ResizeObserver |
| 代理 | `/api` `/metrics` `/healthz` `/readyz` 四条代理规则均实测 200 |
| 空列表 | 后端确实把空切片序列化成 `[]`，前端无需判 `null` |
| 分页 | 最后一页 `has_more=false` 且 **`next_cursor` 字段消失**（`omitempty`），实测确认 |
| SSE | 响应头 `text/event-stream`，事件为 `delta`（12 个）+ `done`（1 个），实测解析正常 |
| 异步接口 | `POST .../preconsult/submit` 返回 **HTTP 202 且 `code=0`**，`already_submitted=false` |
| 报告 | `done && fallback=false`，8 个结构化字段全在（`allergies,chief_complaint,medications,past_history,possible_departments,present_illness,red_flags,urgency`） |
| 限流 | 429 + `code=42901` + **`retry-after: 12`** 响应头确实存在（用 `curl -i` 抓原始头确认） |
| 归属校验 | 医生 2 访问医生 1 的报告 / 队列 / 写病历，三者均**实测 `40302`** |

> **实测中值得记下的一件事**：`GET /appointments/:id/record` 对**不存在**的记录返回 `40003`（而非 `40302`），
> 即"先查存在性、再判归属"。前端把 `40003` 当作"该病历不存在"处理是正确的，不要误以为是权限问题。

---

## 1. 立刻能跑起来

### 1.1 ✅ Vite 代理（**已配置**，但必须理解它为什么存在）

**后端没有任何 CORS 中间件**（`go.mod` 里也没有 cors 依赖，已核实）。
浏览器的同源策略会让前端直连 `http://127.0.0.1:8080` 的请求**全部被拦**，
而且后端**无法通过配置解决**。

**做法：用 Vite 的开发代理，让浏览器看到的是同源请求。**
`frontend/vite.config.ts` 已配好（含 `@` → `src` 别名）：

```ts
// frontend/vite.config.ts（当前实际内容，已实测四条代理规则均 200）
const backend = 'http://127.0.0.1:8080'

export default defineConfig({
  plugins: [vue()],
  resolve: { alias: { '@': fileURLToPath(new URL('./src', import.meta.url)) } },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: backend, changeOrigin: true },
      // /metrics 与探针不在 /api 下，管理看板要用
      '/metrics': { target: backend, changeOrigin: true },
      '/healthz': { target: backend, changeOrigin: true },
      '/readyz': { target: backend, changeOrigin: true },
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

> **若你改动了 `vite.config.ts`，必须重启 `frontend:dev`** —— Vite 只对源码热更新，
> 配置变更不会热重载。
>
> **另一个实测细节**：Vite 8 默认只监听 `localhost`，用 `127.0.0.1:5173` 访问会**连接被拒**。
> 本机验证时请用 `http://localhost:5173/`，或给 dev server 加 `host: true`。

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
| **`nub run frontend:test`** | **vitest**（202 个用例 / 12 个文件，约 2.9s）。默认 node 环境；组件/SSE 测试在文件首行用 `// @vitest-environment jsdom` 单独切换 |
| **`nub --cwd frontend run test:watch`** | 监听模式（改代码自动重跑） |
| `nub --cwd frontend add <pkg>` | 加依赖（**不用 npm install**） |
| `nub run backend:seed` | 灌演示数据（幂等） |

> 根目录的 **`nub run test`** 会依次跑后端（268 用例）与前端（202 用例）；
> `nub run lint` 会跑 `backend:vet` + `frontend:lint` + `frontend:typecheck`。

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
   （例如"操作失败（追踪号 3f2a…）"），用户截图即可定位到**那一条**请求。
   响应头里也有 `X-Trace-Id`，值相同。**具体怎么查见 §5.7.1。**

> **一个已经发生过的事故，值得记住**：这一节原本写着"用户截图即可定位到 `audit_logs`"，
> 但当时**根本没有任何按追踪号查询的入口** —— `/admin/audit-logs` 只认
> `resource`/`action`/`from`/`to`/`user_id`，`audit_logs` 上那个 `idx_trace` 索引
> 在 Go 代码里从未被引用过。文档承诺了一个不存在的功能。
>
> 教训不是"写文档要小心"，而是：**凡是在文档里写出一个操作步骤，
> 就应该在同一次改动里把它跑一遍**。这条承诺在对齐时才发现是空的。

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
| 27 | GET | `/admin/audit-logs` | 查询参数：`resource` `action` **`trace_id`** `from` `to` `user_id` `cursor` `size` |
| 28 | POST | `/admin/sync/:source` | `:source` 目前**只接受 `mockhis`**，其它值返回 404 |
| 29 | GET | `/admin/sync-logs` | 查询参数：`source` `cursor` `size` |

**`trace_id` 必须传 32 位十六进制**（大小写均可，后端归一为小写）。
格式不对返回 `40001` —— 这一点与 `user_id` **刻意相反**：

| 参数 | 格式错时 | 为什么 |
|---|---|---|
| `user_id` | **静默忽略** | 忽略后返回的是不加该条件的列表，语义上还能接受 |
| `trace_id` | **40001** | 忽略后返回的是**全部日志**，管理员会以为"这个追踪号关联了成百上千条记录" —— 一个静默且高度误导的结果 |

所以前端对 `trace_id` **不要**自己写一套格式校验去拦请求（那会与后端规则漂移），
只做去空白、转小写这类宽松归一即可。列排序规则是 `ascii_bin`（逐字节比较），
不归一大写的追踪号查不到。

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

### 5.5 实际目录结构（脚手架残留已清空）

```
frontend/src/
├── api/                   # client.ts 是唯一的 axios 实例
│   ├── client.ts          # 信封剥离 + 刷新去重 + 40102 重放 + Retry-After
│   ├── error.ts           # ApiError 与 ErrorCode 常量
│   ├── tokens.ts          # 双令牌读写（localStorage 不可用时退化为内存）
│   ├── auth.ts  catalog.ts  appointment.ts
│   ├── preconsult.ts  record.ts  admin.ts
│   └── system.ts          # /healthz /readyz /metrics（刻意绕开拦截器，见下方注释）
├── stores/                # auth.ts（登录态）/ booking.ts（挂号草稿）
├── router/index.ts        # 静态路由 + 按角色 addRoute + 守卫
├── types/
│   ├── api.ts             # 后端 DTO 的 TS 镜像（一 DTO 一 interface）
│   └── ui.ts              # 前端内部类型
├── composables/
│   ├── client 无关的：useCursorList.ts（游标分页 + IntersectionObserver）
│   ├── useCountdown.ts    # 15 分钟确认窗口 / Retry-After 倒计时
│   ├── useMediaQuery.ts   # 需要 JS 决策的响应式分支
│   └── usePreconsultChat.ts  # SSE 打字机 + abort + 历史恢复
├── layouts/AppLayout.vue  # 响应式外壳：≥1024 固定侧栏，<1024 抽屉
├── views/                 # LoginView / RegisterView / NotFoundView
│   ├── patient/           # BookingView / MyAppointmentsView / PreconsultChatView
│   ├── doctor/            # WorkbenchView / WriteRecordView
│   ├── admin/             # DashboardView / AuditLogsView / SyncView
│   ├── ReportView.vue     # 三角色共用
│   └── RecordsView.vue    # 三角色共用
├── components/            # AppIcon(内联 SVG) / AppNav / PageHeader / StatusTag
│                          # EmptyState / ReportPanel / EChart
└── utils/                 # datetime.ts prometheus.ts sse.ts id.ts
```

**两处刻意的设计决定，改动前请先理解：**

1. **`api/system.ts` 不走 `client.ts` 的拦截器。** `/readyz` 依赖下线时返回 503，
   但 `data` 里仍带着逐项明细（运维真正要看的东西）；走统一拦截器会把这次响应
   压成"只有错误码、没有明细"的 ApiError。`/metrics` 则是纯文本而非 JSON 信封。
2. **`AppIcon` 是内联 SVG 路径表**，不是图标库。引入 `@element-plus/icons-vue`
   会新增依赖（见 §5.2），而这里只需要十几个 24×24 描边路径。

删掉的脚手架残留：`components/HelloWorld.vue`、`assets/{vue.svg,vite.svg,hero.png}`、
`public/icons.svg`（`public/favicon.svg` 保留，`index.html` 引用了它）。

### 5.6 路由与角色

**登录后按 role 动态注册路由**（spec §8.2 要求）。三角色的入口互斥：

| role | 首页 |
|---|---|
| `patient` | 挂号 / 我的预约 |
| `doctor` | 医生工作台 |
| `admin` | 管理看板 |

**路由守卫要用 `GET /auth/me` 校验，不能只信 localStorage 里的 role** ——
localStorage 可以被改，而且 token 可能已过期或被撤销。

#### 5.6.1 动态注册的两个必踩之坑（**都真实发生过**）

**坑一：守卫的判断顺序 —— 刷新页面必 404。**

角色路由是登录后才注册的，所以**刷新时任何深链接都会被 `/:pathMatch(.*)*`（catchAll）抢走匹配**。
而 catchAll 带着 `public: true`。若把 `if (to.meta.public) return true` 写在注册之前：

1. 匹配到 catchAll → `to.meta.public` 为真 → 直接放行；
2. `installRoleRoutes()` **永远不会执行**；
3. 用户看到「页面不存在」。

原本用来救这个场景的那段 `justInstalled && to.name === 'not-found'` 因此是**永远走不到的死代码**。
**正确顺序：已登录 → 先装角色路由 → 再判 public。**

**坑二：按 name 跳转前，目标 name 必须先注册。**

守卫里有好几处 `return { name: homeRouteName(role) }`（首页、角色不匹配、已登录访问登录页）。
如果目标路由还没注册，vue-router 会以"无匹配"**中断本次导航** —— 表现是"停在原地 / 点了没反应"，
不是报错。`LoginView` 里那段注释（"先把角色路由注册进去，否则按 name 跳转会找不到目标"）
说的就是这个坑，而**守卫自己当时又犯了一次**。

> 同一个教训的两种形态：**"注册"必须发生在任何"消费路由表"的动作之前** ——
> 无论是路径匹配还是按 name 解析。

#### 5.6.2 注册集合必须从 `meta.roles` 推导（不变量已由测试守护）

路由用数组分组（`patientRoutes` / `doctorRoutes`…）便于阅读，但**注册集合不能按分组来**：

```ts
// ❌ 曾经：手写映射 —— 会与路由自己声明的 meta.roles 漂移
const ROUTES_BY_ROLE = { patient: patientRoutes, doctor: doctorRoutes, admin: adminRoutes }

// ✅ 现在：从 meta.roles 推导
export function routesForRole(role: Role): RouteRecordRaw[] {
  return ALL_ROLE_ROUTES.filter((r) => !r.meta?.roles || r.meta.roles.includes(role))
}
```

漂移的后果是**"界面里点了没反应"** —— 既不报错、也不容易联想到路由：

| 路由 | `meta.roles` 声明 | 曾经只在哪一组 | 症状 |
|---|---|---|---|
| `preconsult-report` | patient / doctor / admin | patient 组 | **医生工作台的「AI 报告」按钮点不动** |
| `workbench` | doctor / admin | doctor 组 | **管理员侧栏的「医生工作台」点不动** |

`router.spec.ts` 里有一条**不变量**用例守着这件事：凡 `meta.roles` 声明允许某角色访问的路由，
那个角色登录后必须真的 `hasRoute`。新增路由写错分组会立刻变红。

### 5.7 本地开发小抄

| 事 | 做法 |
|---|---|
| 后端 401 但 token 看着没过期 | 检查 `expires_in`（2 小时）与系统时间 |
| 改了后端代码 | 后端没有热重载，要重启 `nub run backend:dev` |
| 想看某个接口的原始响应 | 浏览器 Network 面板；或 `curl -H "Authorization: Bearer …"` |
| 想看后端日志 | `backend:dev` 那个终端；日志是 JSON，含 `trace_id` |
| 排障（按追踪号定位） | 见下方 §5.7.1 |
| 数据乱了 | `nub run deps:reset`（**会删数据卷**）+ `nub run deps:up` + `nub run backend:seed` |

### 5.7.1 怎么用追踪号排障

`trace_id` 是 **32 位十六进制**，同时出现在三个地方：响应头 `X-Trace-Id`、
响应信封的 `trace_id`、以及 `audit_logs.trace_id`。界面把它拼进错误提示
（`号源不足（追踪号 3f2a…）`），所以用户截图就够定位。**实测过的三条路**：

**① 界面上直接查（最快，需要 admin）**

管理看板 → 审计日志 → **追踪号**输入框（支持粘贴；大小写都行）。
表格每行的追踪号旁边还有一个「筛选」，点一下就用它筛当前列表。

**② 接口直查**

```powershell
curl -H "Authorization: Bearer <admin-token>" `
  "http://127.0.0.1:8080/api/v1/admin/audit-logs?trace_id=3f2a1b…"
```

格式不对会返回 `40001`（不是静默忽略，见 §3.7）。

**③ 直接查库（不依赖后端在跑，适合后端挂了的时候）**

```powershell
node deploy/db.mjs mysql "SELECT id,user_id,method,path,status_code,latency_ms,created_at FROM audit_logs WHERE trace_id='3f2a1b…'"
```

实测 `EXPLAIN` 走 `idx_trace`、`type=ref`、`rows=1`，是索引查找。

**④ grep 后端日志**（日志是 JSON、输出到 **stderr**，没有落盘）

```powershell
# 开发：直接在 nub run dev 的 [backend] 输出里搜
nub run backend:dev 2>&1 | Select-String "3f2a1b…"
# 编排
docker logs medicore-backend 2>&1 | Select-String "3f2a1b…"
```

> ⚠️ **上游传进来的 `X-Trace-Id` 会被校验**：只有 32 位十六进制才沿用（归一为小写），
> 其它一律丢弃并生成新的。所以**响应的 `X-Trace-Id` 才是权威值** ——
> 若你在反向代理或客户端里塞了一个不合规的值，它不会被采纳，也不会写进审计表。

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
| 23 | 把追踪号当 UUID 传（带连字符、大写、超长） | 后端返回 **`40001`**；而 `X-Trace-Id` 里的同类值会被**丢弃重生成** | 追踪号是 **32 位十六进制**，以响应头为准；界面上直接粘贴即可（§3.7 / §5.7.1） |
| 24 | 以为"显示了追踪号"就等于"能按它查" | 曾经确实查不了 —— 没有任何查询入口，`idx_trace` 是死索引 | 写完排障步骤要**真的跑一遍**（§2.1 记的那次事故） |
| 25 | 把 `public` 路由的判断写在**注册角色路由之前** | **刷新任何页面都变成「页面不存在」**（catchAll 抢走匹配后直接放行） | 已登录 → **先装路由** → 再判 public；且任何"按 name 跳转"之前目标必须已注册（§5.6.1） |
| 26 | 按数组分组决定注册哪些路由，而不是按 `meta.roles` | 声明与实现漂移 → **医生工作台「AI 报告」、管理员「医生工作台」点了没反应** | 注册集合从 `meta.roles` 推导；`router.spec.ts` 有不变用例守着（§5.6.2） |
| 27 | 三个角色共用同一个视图，却不区分谁能调哪个接口 | `ReportView` 让医生/管理员调用**患者专属**的 `submit` / `listMine`，并渲染跳 `preconsult-chat` 的按钮 → 403 或静默无响应 | 共用视图里**患者专属**动作要按 `role` 收起（§3.8 的表 + `ReportView.spec.ts`） |

---

## 7. 验收标准（spec G1）

> **G1：三角色全流程闭环** —— ✅ **已达成**（2026-09-13 对真实后端实测，逐条见 §0.1）。

必须能完整走通这三条（括号内是实测结果）：

```
患者：注册 → 登录 → 选科室/医生/排班 → 抢号（幂等）→ 我的预约看到它
      → AI 预问诊对话（SSE 打字机）→ 提交 → 轮询到报告 → 签到拿号
      （实测：注册 200 / 抢号 200 / 同幂等键重放返回**同一条** id=31
       / 签到拿到 queue_no=1 / SSE 13 个事件 / submit 202 / 报告 1 次轮询即 done）

医生：登录 → 工作台看到该患者（has_record=false）→ 候诊队列
      → 查看 AI 报告 → 写病历 + 处方 → has_record 变 true
      （实测：has_record false→true；队列 1 人；写病历含 2 项处方；
       重复写病历返回 40004）

管理员：登录 → 看板（ECharts：预约趋势 / 科室 TOP / 系统健康度）
        → 审计日志 → 触发 HIS 同步 → 看同步记录
        （实测：审计 10 条可读；resource=appointments 筛出 5 条；
         sync total=21 succeeded=21；sync-logs 4 条）
```

**工程质量门槛（全部已满足）**：

- [x] `nub run frontend:typecheck` 通过（**零错误零告警**）
- [x] `nub run frontend:lint` 通过（**零错误零告警**）
- [x] `nub run frontend:build` 通过（`vue-tsc -b && vite build`）
- [x] `nub run frontend:test` 通过（**202 用例**；关键用例做过反向验证，见 §5.8）
- [x] 所有 API 响应都有 TS `interface`，且与 §3 的字段严格对齐（`src/types/api.ts`）
- [x] 运行时依赖未新增（仅开发期加了 `vitest` / `jsdom` / `@vue/test-utils` / `axios-mock-adapter`）
- [x] 无 CORS 报错（四条代理规则实测 200）
- [x] SSE 在组件卸载时确实 `abort()`（`usePreconsultChat` 的 `onUnmounted(abort)`）

### 5.8 前端测试（vitest）

```powershell
nub run frontend:test              # 一次性跑完（202 用例，约 2.9s）
nub --cwd frontend run test:watch  # 监听模式
```

**默认环境是 `node`**（`vite.config.ts` 里配的）。组件测试在文件首行加
`// @vitest-environment jsdom` 单独切换，不必把整个套件都拖进 DOM 环境。

| 测试文件 | 用例 | 锁住的坑 |
|---|---|---|
| `src/utils/sse.spec.ts` | 14 | SSE 事件块被 TCP 切成任意分片；`\r\n` **跨分片**；多字节 UTF-8 切半；结尾无空行的补发 |
| `src/utils/prometheus.spec.ts` | 14 | 标签值里含 `}`；转义引号与逗号；NaN/±Inf 归零；无值行被跳过 |
| `src/utils/datetime.spec.ts` | 17 | `YYYY-MM-DD HH:MM:SS` 必须按**本地时区**解析（Safari 兼容）；跨月/跨年/闰年；倒计时归零不为负 |
| `src/api/error.spec.ts` | 12 | `trace_id` 必须带进展示文案；`40302` 与 `40301` 分开识别 |
| **`src/api/client.spec.ts`** | **22** | **信封剥离**；202 也按 `code` 判成功；**40102 才刷新**（40101 不刷）；**并发 401 只刷一次**；刷新后**两个令牌都覆盖**；`_retried` 防死循环；认证端点不参与重试；`Retry-After` 解析；`cleanParams` 保留 `0`/`false` |
| **`src/components/ReportPanel.spec.ts`** | **14** | **三种终态分支**（正常 / 降级 / 失败）；**降级时不得去渲染 `structured`**；`red_flags` 高亮与空态；`urgency` 三档；`report=null` 的加载态 vs 空态 |
| **`src/api/contract.spec.ts`** | **31** | HTTP 层的**契约形状**：成功判定用 `code === 0`（**202 也算成功**）；`next_cursor` 缺失而非 `null`；空列表是 `[]` 不是 `null`；`queue_no` / `checked_in_at` 的可选性；`consult_fee` 是字符串；时间格式 `YYYY-MM-DD HH:MM:SS`；`40005` / `40002` / `40302` 的 HTTP→code 映射 |
| **`src/composables/useCursorList.spec.ts`** | **13** | 用 **`has_more` 而非 `items.length === size`** 判有无下一页；`cursor` 只在非首页带上；并发 `loadMore` 不得重复请求；错误后 `loading` 必须复位；`reset()` 清空列表与游标；卸载时断开 `IntersectionObserver` |
| **`src/composables/usePreconsultChat.spec.ts`** | **14** | SSE 打字机把 `delta` **追加**而非替换；`error` 事件（流已 200）也要变成可见错误；**流未开始时的 JSON 信封**（非 `text/event-stream`）按 §2.1 解析；`onUnmounted` 必须 `abort()`；历史恢复的顺序与 `role` 映射 |
| **`src/stores/auth.spec.ts`** | **18** | 登录态的边界：**必须真的打 `/auth/me`**（不能只信本地存的角色）；`/auth/me` 失败要**清掉令牌**而不是留着；`logout` 服务端撤销失败也要清本地（`finally` 分支）；令牌**两个都覆盖**；注册失败不接着登录；`reset` 与 `logout` 的区别（前者不发请求） |
| **`src/router/router.spec.ts`** | **22** | **刷新即 404**（catchAll 带 `public`，把角色路由注册挡在门外）；**按 name 跳转前目标必须先注册**；不变量：**`meta.roles` 声明允许的角色必须真的注册到那条路由**；未登录深链接去登录而不是 404；登出后角色路由确实消失 |
| **`src/views/ReportView.spec.ts`** | **11** | 三角色共用页里的**患者专属动作**：医生/管理员不得调 `submit` / `listMine`，也不得渲染「返回对话」；患者侧三个动作照常可用 |

`client.spec.ts` 用 `vi.mock('axios')` 把拦截器函数**捕获下来直接调用** ——
拦截器本身就是纯函数，比搭假 HTTP 服务器更贴近被测逻辑。
注意 `vi.mock` 会被提升到 import 之前，共享状态必须用 `vi.hoisted` 声明。

`contract.spec.ts` 反过来：它**真的要发请求**，所以用 `axios-mock-adapter` 挂在
真实的 axios 实例上。两者互补 —— 前者测"拦截器逻辑"，后者测"我们的 TS interface
与后端实际吐出来的 JSON 是否对得上"。
`axios-mock-adapter` 与 `mockjs` 都是开发期依赖；**`mockjs` 目前刻意未使用** ——
契约测试要的是**确定性夹具**，随机数据会让断言失去意义。需要它时再说。

**写新用例时请遵守三条**（都来自本项目的实际教训）：

1. **断言基于字段值，不要基于绝对时间戳** —— 否则结果随机器时区变化
   （CI 是 UTC，本机是 Asia/Shanghai）。
2. **加了关键用例要做反向验证**：故意改坏实现，确认用例真的变红。
   本轮验证记录（每次都**只**命中预期的那几条）：
   - 去掉 SSE「尾部补发」→ `sse.spec.ts` 1 条失败
   - `lastIndexOf('}')` → `indexOf('}')` → `prometheus.spec.ts` 1 条失败
   - **去掉刷新去重**（`if (!refreshing)` → `if (true)`）→ 「并发只刷一次」1 条失败
   - **恢复 `status ?? 'pending'` 并去掉 null 分支** → 2 条 null 报告用例失败
   - `has_more` → `items.length === size` → `useCursorList.spec.ts` 2 条分页用例失败
   - **去掉 `onUnmounted(abort)`** → `usePreconsultChat.spec.ts` 1 条卸载用例失败
   - **丢掉 `date` 查询参数** → `contract.spec.ts` 3 条排班用例失败
   - **`bootstrap` 改成读 localStorage 里的角色（不打 `/auth/me`）** → `auth.spec.ts` 2 条失败
   - **`bootstrap` 的 catch 里去掉 `clearTokens()`** → `auth.spec.ts` 1 条失败
   - **`logout` 去掉 `try/finally`** → `auth.spec.ts`「服务端撤销失败也要清干净本地」1 条失败
   - **把 `public` 判断挪回注册之前** → `router.spec.ts` **15 条失败**（含患者/医生/管理员三侧刷新）
   - **`routesForRole` 改回按数组分组** → `router.spec.ts` 4 条失败（不变用例直接点名 `preconsult-report` 声明了 doctor 却没注册）
   - **去掉 ReportView 的角色判断** → `ReportView.spec.ts` 6 条失败（"医生/管理员不该调用患者专属的 submit"）
   **一条不会失败的测试等于没有测试。**
3. **失败时先问"是我的期望错了吗"**。本轮有 4 次失败，其中 **2 次是测试本身写错**
   （把消息总数写成患者发言数；用 `toThrow` 断言一个返回 Promise 的拦截器）。
   写 `auth.spec.ts` 时又踩了一次同类的：`beforeEach` 里写 `localStorage.clear()`，
   但 `api/tokens.ts` 有一份**模块级内存副本**（localStorage 不可用时的退化路径），
   它跨用例存活 —— **必须调 `clearTokens()`** 才能清干净。模块级状态不会随 DOM 重置。

> 测试文件放在 `src/**/*.spec.ts`（与源码同目录）。
> 它们会被 `vue-tsc` 与 `eslint` 覆盖（这是好事），但**不会进生产产物** ——
> Vite 只打包被 import 的模块，已实测 `dist/assets` 里没有测试代码。

### 5.9 测试抓到的一个真实缺陷（值得一读）

写 `ReportPanel.spec.ts` 时暴露了两件事，都不是"测试写错"，而是组件真的有问题：

1. **`loading` 是个死参数** —— 它声明在 `defineProps` 里，但模板从头到尾没用过。
2. **`report === null` 被显示成"AI 正在生成结构化总结"** ——
   因为 `status` 写的是 `props.report?.status ?? 'pending'`。
   后果：若那次请求其实**失败了**，用户会一直等一个永远不会来的结果。

**修法**：`status` 不再默认成 `'pending'`；模板最前面加
`v-if="!report"`，并真正用上 `loading` 来区分「正在加载」与「确实没有」。

**为什么值得单独记一条**：这两个问题在浏览器里**都看不出来** ——
正常流程下 `report` 很快就有值，`loading` 也一直是 `true`。
只有把"空 + 加载完"这个组合显式构造出来才会暴露。

### 7.1 本次**没有**验证的部分（下一个接手的人请注意）

- **渲染层没有在浏览器里打开过**（路由与角色这一层已由 `router.spec.ts` / `ReportView.spec.ts` 覆盖 —— 那是踩过一次真实事故之后补的）：以上结论来自 HTTP 层（经 Vite 代理 **与**
  生产编排的 nginx）+ 静态检查，**没有**验证过渲染结果、移动端实际布局、
  ECharts 绘制是否正常。这些需要在浏览器里人工走一遍。
  最快的方式是起生产编排：`nub run stack:init && nub run stack:up && nub run stack:seed`。
  > **本机浏览器事实（实测，省得再查一遍）**：只装了 **Microsoft Edge**
  > （`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`，v153），**没有 Chrome**；
  > 没有 playwright / puppeteer 的任何缓存。**Node 24 自带全局 `WebSocket`** ——
  > 也就是说"CDP 驱动浏览器"这件事**零依赖**就能做（CDP = WebSocket + JSON）。
  > 想给 agent 接浏览器时，官方 `chrome-devtools-mcp` 的文档明确支持 Edge
  > （`--executablePath` 指上面那个 exe 即可）。**但任何 npm 下载都会走
  > `HTTPS_PROXY=127.0.0.1:2333` 那个计费代理，装之前先问。**
  >
  > **一个重要的事实**：`http://127.0.0.1:3080`（DSH Web GUI）对**没有凭据的浏览器**
  > 只会返回一行 `dsh web authentication required; reopen the URL printed by dsh web.`
  > 所以"让 agent 截个图看界面"这条路，对 **3080 这个 GUI 本身不成立**；
  > 对 `5173` / `8090` 的前端**没有这个限制**。
- **`fallback=true` 的降级分支未经活体触发**：本项目的 LLM 是 mock，实测恒为
  `fallback=false`。该分支的代码路径（改展示 `conversation` 原始对话）是照 §3.5 的契约写的，
  但**没有被真实数据走到过** —— 现在它由 `ReportPanel.spec.ts` 按契约形状覆盖（3 条用例），
  算是有了防线，但**仍然没有真实样本验证过后端的实际输出**。
- **`/readyz` 的 503 分支未经活体触发**：MySQL/Redis 一直是健康的。
  要验证可以 `nub run deps:down` 后再看管理看板的健康面板。
- **`40002`（号源不足）未经活体触发**：需要把某个排班的 `remaining` 抢到 0 才能复现。

> **一个工具链上的坑（实测踩到）**：`frontend:dev` 运行期间，如果编辑工具用
> "临时文件 + 重命名"的方式原子写 `frontend/` 下的文件，Vite 的 FSWatcher 可能对那个
> 临时文件抛 `EBUSY` 并**让 dev server 直接退出**。改文档时尤其容易撞上。
> 遇到时重启 dev server 即可，与代码无关。

---

## 8. 明确**不要**做的事

| 不要 | 为什么 |
|---|---|
| 改后端接口来迁就前端 | 后端契约已冻结且被 67 个集成测试 + 268 个单元测试守护。要改先与后端确认，那会是一轮独立改动 |
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
| 端点行为与错误码的实测记录 | `backend/test/integration/`（67 个用例，是行为的活文档） |
| 某个模块为什么这么设计 | `docs/MODULES.md` 对应章节 |
| 后端有哪些坑、怎么诊断 | `docs/PITFALLS.md`（先看 §0 的判据表） |
| 项目整体状态与未完成项 | `docs/FIXES.md` §1（指标唯一台账）、§7（未完成与风险的唯一清单） |
| 文档全集与阅读顺序 | `docs/README.md`（唯一入口） |
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
**把 §6 的 27 条陷阱当成必读清单**，
然后才有资格开始写页面。别引入新依赖，别改后端。

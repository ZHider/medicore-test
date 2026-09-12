# 01 · M1 —— 基础设施（配置 / 日志 / 中间件链 / 响应信封 / 可观测性）

| | |
|---|---|
| **对应提交** | `28cb2a3`（骨架，与准备阶段同提交）；后续缺陷修正在 `6b23e57`、`ecaa04c` |
| **规模** | 16 文件 / 951 行（`MODULES.md` §1） |
| **涉及包** | `config` `logger` `middleware` `response` `errcode` `apperr` `metrics` `traceid` `router` |
| **本篇性质** | **事后整理**。材料来源：`docs/HANDOVER.md` §2.1 A–G、`docs/MODULES.md` §1.3–1.5、`docs/FIXES.md`、git 提交历史。§三⑥ 为**本次新补查**（`go doc` 全量核验，方法见 `README.md`） |
| **结论** | 交付达成；**遗留 1 项**（`errcode` 三表一致性无自动化守护，见 §六） |

---

## 一、需求拆解（spec §10.1 第 1 步）

从 spec 摘出的 M1 需求：

| spec 出处 | 要求 | 落地位置 |
|---|---|---|
| §3.1 | 中间件链顺序：`TraceID → Recovery → RequestLog → Metrics → [RateLimit → JWT → RBAC] → Audit` | `internal/middleware` + `internal/router` |
| §3.3 | 统一响应与错误码 | `internal/response` + `internal/errcode` |
| §9.2 | Zap JSON 日志（`trace_id/user_id/method/path/status/latency`）；TraceID 贯穿请求→日志→审计表 | `internal/logger` + `internal/traceid` + `middleware.RequestLog` |
| §9.2 | Prometheus 暴露 `http_requests_total` / `http_request_duration_seconds` / `appointment_grab_total` / `llm_calls_total` | `internal/metrics` |
| §9.3 | 敏感配置走环境变量 | `config.envBindings` |

**这一步**spec 本身没有缺陷需要修正（M1 的四项 spec 修正都在 §5 数据库与 §7 流程里）。

---

## 二、方案选型（spec §10.1 第 2 步）

| 决策点 | 被否决的方案 | 选定 | 理由（写在代码注释里） |
|---|---|---|---|
| Prometheus 注册表放哪 | 放 `middleware` | **独立成 `internal/metrics`** | 业务指标要由 **service** 注册（抢号、对账、LLM）。放 middleware 会让 service 反向依赖 middleware |
| 用默认注册表还是独立 registry | `prometheus.DefaultRegisterer` | **独立 registry** | 避免测试重复注册 panic；`/metrics` 只暴露本项目指标 |
| 指标的 `path` 标签取值 | 真实请求路径 | **路由模板（`FullPath`）** | 真实路径让每个 ID 产生独立时间序列 → **基数爆炸** |
| 未匹配路由 | 用 gin 默认 404 | **`NoRoute` + 统一信封** | 否则客户端拿到纯文本，无法按 `code` 分支 |
| 方法不匹配 | 不管 | **`HandleMethodNotAllowed = true` + `NoMethod`** | gin 默认返回 404 而非 405 |
| 密钥校验方式 | 「已知坏值黑名单」 | **按特征判断** | 黑名单只能拦住预先想到的值，**而且黑名单本身会把密钥字面量带回仓库** |

---

## 三、§10.2 检查清单

### ① 分层与架构

- `handler` 无 GORM、`service` 无 `gin.Context` —— 自查命令写在 `AGENTS.md` §2.1，本轮复核仍为 0 命中。
- **本模块的关键分层决策是把 metrics 下沉**：`metrics` 只依赖 `prometheus`，被所有层依赖，因此不产生环。
- `apperr` 承担"错误码 + 上下文"的载体，让 `service` 不必 import `errcode` 的 map 结构。

**判定：通过。**

### ② 错误与并发

| 检查项 | 情况 |
|---|---|
| 错误是否包装 | `apperr.Wrap(code, msg, err)` 保留 `errors.Is/As` 链；`Recovery` 中间件把 panic 转 500 且**不泄漏堆栈给客户端** |
| goroutine 退出路径 | `middleware` 本身不起 goroutine；`audit` 的异步写入器有 `Stop()`，**且 `Stop()` 幂等**（启动失败回滚路径与 `main` 的 defer 都可能调用；重复调用会 close 已关闭 channel 而 panic —— 恰恰在最需要优雅退出的时刻） |
| 共享状态加锁 | 指标计数走 prometheus 原子量；`audit` 缓冲用 mutex |

**判定：通过。** 一处曾经出错：`service/health.go` 的 `Ready()` 最初用
channel → `close` → `range` → 起 goroutine → 再用另一个 channel 收结果来表达
「并发跑两个 probe」，绕了三个数据结构。改成 `sync.WaitGroup` 后长度减半。
**这是我自己写的时候就觉得别扭、但还是写完的一处**（`HANDOVER.md` §2.1-E）。

### ③ 性能与 SQL

- 本模块不碰 SQL。唯一相关的是**指标基数**（见 §二，已用路由模板解决）。
- `RequestLog` 只记录必要字段，不落库（审计是另一条链路，且异步）。

**判定：不适用 SQL 部分；基数问题已处理。**

### ④ KISS 原则

| 出现的"多余" | 判定 |
|---|---|
| `MIDDLEWARE` 链固定顺序 + 注释解释为什么 | **必要**：顺序是可观测性正确性的前提 |
| `errcode` 用三张 map（常量 / message / httpStatus） | **必要但脆弱**，见 §六 |
| `metrics` 独立成包 | **必要**（见 §二） |
| `service/health.go` 的 channel 编排 | ❌ 曾经存在，**已删除** —— 典型的过度设计 |

### ⑤ 基础卫生

| 检查项 | 情况 |
|---|---|
| 输入校验 | 参数校验在 handler；`config.Validate()` 在启动前按**特征**拒绝可预测密钥 |
| 敏感数据脱敏 | M1 本身不输出个人数据；`Recovery` 不回传堆栈 |
| 硬编码 | **仓库内无任何密钥**（连开发用的也没有），本地密钥由 `cmd/genlocal` 生成到 gitignore 的 `config.local.yaml`；**无代理地址**（本轮复检仍为 0） |

### ⑥ AI 幻觉

**本次新补查**。全量抽取源码里的第三方「包.符号」引用（110 个组合）逐个 `go doc` 验证：
**存在 101 个、真实幻觉 0 个**，其余 9 条经人工核实为正则假阳性
（8 条是 `v.SetDefault("mysql.host", …)` 这类**配置键字符串**，1 条是 import 路径里的 `gorm.io/gorm`）。

M1 涉及的高风险 API（`viper.SetDefault`/`BindEnv`、`zap`、`promhttp.Handler`、
`collectors.NewGoCollector`、`gin.WrapH`、`gin.Context.Stream`）**全部存在**。

> **但这个结果不等于"AI 没编过 API"**：不存在的符号在编译期就被拦住，活不到提交。
> 真正需要防的是**符号存在但语义记错**。M1 里有一个真实的语义级坑：
> **`viper.AutomaticEnv` 对嵌套键的 `Unmarshal` 支持不完整** ——
> 新增配置项忘了手工加 `envBindings` 时，程序**不报错，只是读到零值**。
> 这类问题只能靠"显式绑定 + 启动校验"防，`go doc` 查不出来。

---

## 四、本模块暴露的真实缺陷

| # | 缺陷 | 症状 | 修法 |
|---|---|---|---|
| 1 | 未知路由返回纯文本 404 | `curl /api/v1/nope` → `404 page not found`，违反 spec §3.3 | 补 `NoRoute` → 40003 |
| 2 | 方法不允许返回 404 而非 405 | gin 默认行为；`POST /healthz` 得不到 405 | `HandleMethodNotAllowed = true` + `NoMethod` → 40006 |
| 3 | **202 响应漏 `trace_id`** | handler 手拼 `response.Body{...}` 忘了填 `TraceID` → 该请求在日志与审计里**断链**，违反 spec §9.2 | 加 `response.Accepted(c, msg, data)` helper，**目的是不让调用方手拼 Body** |
| 4 | **限流 fail-open 没有指标** | 代码注释写着"故障会被 metrics 捕获"，**而当时根本没有这个指标** —— 限流已静默失效却没有告警 | 补 `ratelimit_errors_total` |
| 5 | `config.Validate()` 用黑名单 | 只能拦住预先想到的值；**黑名单本身把密钥字面量带回仓库** | 改为按特征判断（单字符重复 / 短周期重复 / 连续升序 / 含占位词） |

> **第 3、4 条属于同一类**：**注释描述了一个尚不存在的实现**。
> 它比"没写注释"更糟 —— 读者会以为已经处理了。已写进 `HANDOVER.md` §2.1-C。

---

## 五、反向验证记录

| 被测性质 | 反向验证方式 | 结果 |
|---|---|---|
| `config.Validate()` 按特征拒绝可预测密钥 | `internal/config/validate_test.go`：构造单字符重复、短周期重复、连续升序、含占位词的密钥 | 全部被拒 ✅ |
| JWT / RBAC 中间件 | `internal/middleware/auth_test.go` 5 例：缺头、畸形头、过期（返回**独立错误码**而非笼统 401）、有效令牌注入身份、`RequireRole` 及无令牌时的 401 | ✅ |
| 限流 | `test/integration/ratelimit_test.go` 4 例（真实 Redis）：容量耗尽后被限、限流响应用**统一信封**、清桶后恢复、**抢号按用户而非按 IP** | ✅ |

**诚实说明（M1 的验证强度弱于其他模块）**：
`response` / `traceid` / `metrics` / `apperr` / `logger` **没有单独的测试文件**，
它们由集成测试间接覆盖（29 个端点的统一信封、TraceID 贯穿）。
**没有做过"故意改坏 `NoRoute` 看集成测试是否变红"这样的反向验证** ——
这是 M1 相对 M4/M5 的真实差距，记录在这里而不是含糊过去。

---

## 六、遗留与未验证

| 项 | 说明 |
|---|---|
| **`errcode` 三表一致性无自动化守护** | `errcode.go` 有**三张表**（常量 15 个 / `messages` / `httpStatus`），注释明确要求"必须同步改，漏改会让错误码落到『未知错误 / 500』"。但 `internal/errcode/` **没有测试文件** —— 这条规则目前**只靠人记**。建议补一个测试：遍历三张表的 key 集合，断言三者相等。这是本模块唯一的实质缺口 |
| 5 个薄包装包无单测 | `response` / `traceid` / `metrics` / `apperr` / `logger` 由集成测试间接覆盖，可接受 |
| 中间件链顺序无守护测试 | 顺序错误不会让任何现有用例变红。集成测试的 TraceID 贯穿用例能间接发现部分顺序问题 |

---

## 交叉引用

- 结构与决策：`docs/MODULES.md` §1
- 原始错误记录：`docs/HANDOVER.md` §2.1（A–G 类）
- 易错点：`docs/PITFALLS.md` §4（响应信封）、§8（工具链）
- 接手规则：`AGENTS.md` §2.1、§3.2、§5.5

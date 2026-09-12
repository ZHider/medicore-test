# MediCore Backend

Go 1.27 + Gin 单体后端。分层架构由 spec §3.2 强制约束。

## 目录结构

```
backend/
├── cmd/server/main.go        # 装配依赖、启动、优雅关闭
├── configs/config.yaml       # 配置（敏感项用环境变量覆盖）
├── internal/
│   ├── config/               # 配置加载与启动前校验（Viper）
│   │
│   ├── handler/              # 参数绑定+校验；禁止 SQL/GORM
│   ├── service/              # 业务逻辑、事务边界、归属校验；禁止 http.Context
│   ├── repository/           # 唯一允许 GORM 的层
│   ├── model/                # PO / DTO / 枚举
│   ├── middleware/           # TraceID / Recovery / RequestLog / Metrics / JWT / RBAC / RateLimit
│   ├── router/               # 路由与中间件链组装
│   │
│   └── apperr/ authctx/ errcode/ jwt/ logger/
│       mask/ response/ secret/ traceid/          # 无状态工具包
```

**依赖方向只能向下**：`handler → service → repository → model`。

> 说明：工具包直接平铺在 `internal/` 下，没有采用 `internal/pkg/` 分组。
> `internal` 本身已阻止外部导入，再加一层 `pkg` 是冗余层级。
> 这一处刻意偏离 spec §3.2，采用 Go 社区惯例。

**接口由使用方定义**：`service` 在 `deps.go` 中声明它所需的**最小**能力接口
（`userStore` / `tokenStore` / `healthChecker`），而不是依赖 `repository` 的具体类型。
因此 service 层单测可注入内存实现，完全不依赖 MySQL / Redis。

## 本地运行

```bash
# 1. 生成本地密钥（仅首次）
#    仓库中不含任何密钥，因此必须先执行这一步
go run ./cmd/genlocal

# 2. 起依赖（mysql + redis，首次自动执行 docs/schema.sql）
nub run deps:up

# 3. 起后端
MEDICORE_MYSQL_PASSWORD=medicore123 go run ./cmd/server
# Windows PowerShell: $env:MEDICORE_MYSQL_PASSWORD='medicore123'; go run ./cmd/server
```

## 密钥管理

**仓库中不保存任何密钥**，包括开发用密钥——密钥扫描工具不会区分"仅开发"与否。

| 环境 | 密钥来源 |
|---|---|
| 本地开发 | `go run ./cmd/genlocal` 生成 `configs/config.local.yaml`（已被 `.gitignore` 排除，启动时自动合并） |
| 生产 | 环境变量 `MEDICORE_SECURITY_AES_KEY` / `_PHONE_PEPPER` / `_JWT_SECRET` |

`cmd/genlocal` 默认拒绝覆盖已有文件：重新生成会换掉 `aes_key`，
导致数据库中已加密的手机号/身份证**永久无法解密**，需要显式加 `-force`。

启动时会校验密钥（`internal/config/validate.go`），按**特征**而非黑名单判断可预测性：
单一字符重复、短周期重复、连续升序序列、含 `change-me`/`example` 等占位词，一律拒绝启动。
用特征而非黑名单，是因为黑名单既拦不全，本身又会把密钥字面量带回仓库。

> 注意：`.gitignore` 只能防住"未来"，防不住"已经提交过"。
> 若密钥曾进入历史，必须改写历史（本仓库用 `git commit --amend` 处理过），
> 且**已泄露的密钥应视为失效**，即使历史被清理也要轮换。

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `MEDICORE_CONFIG` | `configs/config.yaml` | 配置文件路径 |
| `MEDICORE_APP_ENV` | `dev` | `prod` 时 gin 切换 ReleaseMode |
| `MEDICORE_APP_PORT` | `8080` | 监听端口 |
| `MEDICORE_LOG_LEVEL` | `info` | `debug`/`info`/`warn`/`error` |
| `MEDICORE_MYSQL_HOST` / `_PORT` / `_USER` / `_PASSWORD` / `_DATABASE` | — | 数据库连接 |
| `MEDICORE_REDIS_ADDR` / `_PASSWORD` / `_DB` | — | Redis 连接 |
| `MEDICORE_SECURITY_AES_KEY` | — | 32 字节 hex，AES-256-GCM 主密钥 |
| `MEDICORE_SECURITY_PHONE_PEPPER` | — | 手机号/身份证摘要的 HMAC pepper |
| `MEDICORE_SECURITY_JWT_SECRET` | — | JWT 签名密钥 |

## 端点

### 运维（M1）

| Method | Path | 说明 |
|---|---|---|
| GET | `/healthz` | 存活探针，不探测外部依赖 |
| GET | `/readyz` | 就绪探针，探测 mysql + redis，任一故障返回 503 |
| GET | `/metrics` | Prometheus 指标 |

### 认证（M2）

| Method | Path | 鉴权 | 说明 |
|---|---|---|---|
| POST | `/api/v1/auth/register` | 公开 | 患者注册（仅 patient 角色，防止自助提权） |
| POST | `/api/v1/auth/login` | 公开 + **限流 5/分钟** | 返回双令牌 |
| POST | `/api/v1/auth/refresh` | 公开 | 刷新令牌，**一次性轮换** |
| POST | `/api/v1/auth/logout` | 需登录 | 撤销刷新令牌 |
| GET | `/api/v1/auth/me` | 需登录 | 返回本人脱敏资料 |

### RBAC

`middleware.RequireRole(model.RoleAdmin, ...)` 挂在 JWT 之后使用。
注意这只是**路由层**校验；资源归属仍必须由 service 层校验（spec §2.2 双层校验）。

## 安全设计要点（M2）

| 关注点 | 做法 |
|---|---|
| 手机号存储 | AES-256-GCM，密文 `nonce(12B)‖ct‖tag`；接口输出统一 `138****5678` |
| 手机号查找 | HMAC-SHA256(pepper, phone) **确定性**摘要，命中 `uk_phone_hash` 索引 |
| 口令 | bcrypt cost≥10；显式拒绝 >72 字节输入（避免静默截断造成的安全错觉） |
| 防用户枚举 | 登录失败统一返回"手机号或密码错误"，不区分账号是否存在 |
| 防时序枚举 | 账号不存在时执行**等价 bcrypt 比对**，避免用响应时间区分账号是否存在 |
| 生产密钥守卫 | `APP_ENV=prod` 时若密钥为空或仍是开发默认值，**拒绝启动** |
| access_token | JWT HS256 / 2h，显式限定算法（防 `alg=none` 与算法混淆） |
| refresh_token | 不透明随机串 256 位 / 7d 存 Redis；**必须可撤销**，故不用 JWT |
| 刷新轮换 | `GETDEL` 原子消费，旧令牌立即失效，杜绝重放窗口 |
| 限流 | Redis 令牌桶 Lua 脚本（原子）；限流器故障时 fail-open 并留痕 |
| 错误包装 | `apperr` 携带错误码向上传递，handler 统一转换（spec §3.2 铁律 2） |

## 模块进度

- [x] **M1** 配置 / 日志 / 中间件链 / 健康检查 / 分层骨架 / dev compose
- [x] **M2** 认证鉴权：AES-GCM + HMAC 摘要 + bcrypt + JWT 双令牌 + Redis 刷新轮换 + RBAC + 令牌桶限流
- [ ] M3 科室 / 医生（多对多）/ 排班 + 游标分页 + 缓存
- [ ] M4 抢号（幂等 + Lua + 乐观锁 + cron 释放 + 签到队列）
- [ ] M5 AI 预问诊（Mock LLM + SSE + Stream 异步总结 + Schema 降级）
- [ ] M6 病历 / 处方 / 审计 / HIS 同步

## 测试

```bash
nub run backend:test      # go test ./...
```

41 个用例，service 层不依赖任何外部服务（注入内存实现）：

| 包 | 覆盖内容 |
|---|---|
| `secret` | 加解密往返、随机 nonce、篡改检测、摘要确定性、bcrypt 边界 |
| `jwt` | 签发解析、过期、错密钥、错签发方、**alg=none 伪造拒绝** |
| `mask` | 手机号/身份证/姓名脱敏，且结果不等于原文 |
| `middleware` | JWT 缺失/非法/过期三态、RBAC 放行与拒绝、无令牌优先 401 |
| `service` | 注册角色固定为 patient、唯一键冲突转 40004、登录三态、**时序防护**、刷新轮换与重放拒绝、登出幂等、脱敏与密文落盘 |

时序防护用例经过反向验证：临时移除防护后该用例失败（比值 0.00），确认其确实能捕获回归。


## 中间件链顺序（spec §3.1，固定）

```
TraceID → Recovery → RequestLog → Metrics → [RateLimit → JWT → RBAC]
```

方括号内三段在后续模块接入，且 **JWT/RBAC 挂在业务路由组而非全局**——探针与 `/metrics` 必须免认证。

## 已实现的行为约定

- **统一响应信封**：`{code, message, data, trace_id}`，成功 `code=0`。
  未匹配路由返回 40003，方法不支持返回 40006，二者同样是 JSON 信封（非 gin 默认纯文本）。
- **TraceID**：入口生成 32 位十六进制；上游带 `X-Trace-Id` 则沿用；同时写响应头与响应体。
- **日志**：Zap JSON，字段 `trace_id/method/path/status/latency_ms/client_ip`；5xx 走 Error 级别便于告警。
- **指标**：`http_requests_total{method,path,status}`、`http_request_duration_seconds{method,path}`。
  `path` 用路由模板而非真实路径，避免高基数。
- **优雅关闭**：SIGINT/SIGTERM 触发，10 秒超时。

业务模块可用 `middleware.Registry()` 注册自定义指标（如 `appointment_grab_total`、`llm_calls_total`）。


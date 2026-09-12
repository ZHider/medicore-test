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

### 业务查询（M3）

| Method | Path | 说明 |
|---|---|---|
| GET | `/api/v1/departments` | 科室列表（启用中） |
| GET | `/api/v1/departments/:id/doctors` | 科室下医生列表，**Redis 缓存** |
| GET | `/api/v1/departments/:id/schedules?date=&cursor=&size=` | 按科室+就诊日查询号源，游标分页 |
| GET | `/api/v1/doctors/:id` | 医生详情（含所属科室，多对多） |
| GET | `/api/v1/doctors/:id/schedules?from=&to=&cursor=&size=` | 医生排班，游标分页 |

全部需要登录（院内数据不应匿名可见）。

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
- [x] **M3** 科室 / 医生（多对多）/ 排班查询 + 游标分页 + Redis 缓存（含缓存击穿防护）
- [ ] M4 抢号（幂等 + Lua + 乐观锁 + cron 释放 + 签到队列）
- [ ] M5 AI 预问诊（Mock LLM + SSE + Stream 异步总结 + Schema 降级）
- [ ] M6 病历 / 处方 / 审计 / HIS 同步

## 演示数据

```bash
go run ./cmd/seed      # 幂等，可反复执行
```

写入 4 个科室、12 名医生（含科室关联）、每人 7 天 × 上午/下午的排班（共约 168 条）。
执行完会**清除科室医生缓存**，否则此前缓存下的空列表会掩盖新数据直到 TTL 过期。

## 游标分页

所有列表接口统一使用游标分页，**禁止 OFFSET**（spec §5.2）：

```
GET /api/v1/departments/1/schedules?date=2026-09-12&size=2
→ { "items": [...], "next_cursor": 57, "has_more": true }
GET ...&cursor=57
```

- 仓储层固定查询 `limit+1` 条，多出的一条只用于判断 `has_more`，**不发额外 COUNT**。
- `size` 上限 100：否则 `size=999999` 会被当作合法请求，等价于一次全表扫描。
- 实测对比（23 万行）：`OFFSET 100000` 耗时 137ms、扫描 20 万行；游标 0.483ms、恒定 20 行。

## 缓存设计

科室医生列表走 Redis 缓存（spec §6.1），键 `cache:dept_doctors:{id}`，TTL 5 分钟。

- **缓存击穿防护**：`repository.Load` 用 `singleflight` 合并并发回源。
  热点键失效瞬间若有大量请求同时未命中，各自回源会把压力全打到 MySQL。
- **降级不静默**：缓存读写失败都不阻断请求（退化为每次回源），但一律记日志，
  否则缓存长期失效也无人察觉。
- **脏数据自愈**：反序列化失败按未命中处理并删除该键，避免错误反复出现。
- **显式失效**：写路径调用 `InvalidateDepartmentCache`，不只依赖 TTL。

## 测试

```bash
nub run backend:test      # go test ./...
```

67 个用例，service 层不依赖任何外部服务（注入内存实现）：

| 包 | 覆盖内容 |
|---|---|
| `secret` | 加解密往返、随机 nonce、篡改检测、摘要确定性、bcrypt 边界 |
| `jwt` | 签发解析、过期、错密钥、错签发方、**alg=none 伪造拒绝** |
| `mask` | 手机号/身份证/姓名脱敏，且结果不等于原文 |
| `config` | 密钥非空/长度/可预测性（单一字符、短周期、升序、占位词）、生产守卫 |
| `model` | 游标解析、size 收敛、分页标志与探测行裁剪 |
| `middleware` | JWT 缺失/非法/过期三态、RBAC 放行与拒绝、无令牌优先 401 |
| `service` | 认证全流程 + 时序防护；科室不存在返回 404、多对多详情、分页传递、错误包装为 500 且不泄漏内部细节 |

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


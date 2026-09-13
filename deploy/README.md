# deploy/ —— 全栈编排（spec §11.2）

一次起 mysql + redis + backend + nginx（含前端静态资源），宿主机只暴露一个端口。

---

## 快速开始

```powershell
nub run stack:init     # 1) 生成 deploy/.env（密钥 / 端口 / GOMODCACHE），幂等且默认拒绝覆盖
nub run stack:up       # 2) 构建前端产物 + 构建镜像 + 起全栈
nub run stack:seed     # 3) 灌演示数据（幂等，可重复跑）
```

然后打开 `nub run stack:init` 输出里给出的地址（默认 `http://localhost:80`，
本机 80 被占用时会自动选 `8080`）。

| 账号 | 手机号 | 口令 |
|---|---|---|
| 管理员 | `13800000000` | 见 `deploy/.env` 的 `STACK_ADMIN_PASSWORD`（`node deploy/init-env.mjs --print` 可打印） |
| 医生 | `13900000001` … `13900000012` | `Doctor@123` |
| 患者 | — | 自行注册（注册只开放患者角色） |

### 日常命令

| 命令 | 作用 |
|---|---|
| `nub run stack:up` | 重新构建并启动（改完代码就跑这个） |
| `nub run stack:ps` | 查看容器与健康状态 |
| `nub run stack:logs` | 跟踪日志 |
| `nub run stack:seed` | 灌演示数据（幂等） |
| `nub run stack:down` | 停止（保留数据卷） |
| `nub run stack:reset` | 停止并**删除数据卷**（清空数据库） |

---

## 拓扑

```
                       ┌──────────────────────────────┐
   宿主 :80 / :8080 ──►│  frontend (nginx)            │
   （唯一入口）         │  · /            SPA 静态资源  │
                       │  · /api/    ──► backend:8080 │
                       │  · /healthz /readyz /metrics │
                       └──────────────┬───────────────┘
                                      │  medicore 网络（bridge）
                    ┌─────────────────┼──────────────────┐
                    ▼                 ▼                  ▼
            ┌──────────────┐  ┌──────────────┐  ┌──────────────┐
            │  backend     │  │  mysql:3306  │  │  redis:6379  │
            │  :8080       │  │  （仅内部）   │  │  （仅内部）   │
            │  （仅内部）   │  └──────────────┘  └──────────────┘
            └──────────────┘
```

**端口暴露策略**（spec §11.2）：`mysql` / `redis` / `backend` 一律**不映射**宿主机端口，
`docker port` 对它们返回空。唯一的入口是 nginx，容器内监听 80，
宿主机端口由 `STACK_HTTP_PORT` 决定。

> 注意别和 `deploy/compose.dev.yml` 混淆：那个只起依赖服务并**故意**把
> 3306/6379 映射到宿主机，方便本机 `go run` 直连。两套编排的 project name
> 不同（`medicore` / `medicore-dev`），数据卷与网络互不影响，可以同时运行。

---

## 密钥管理

**仓库里不含任何密钥**（项目硬规则）。后端启动时 `config.Validate()` 会拒绝空密钥，
所以 `stack:init` 是必经步骤。

`deploy/init-env.mjs` 用 `node:crypto` 的 `randomBytes` 生成：

| 变量 | 用途 | 泄露后果 |
|---|---|---|
| `STACK_AES_KEY` | AES-256-GCM，加密手机号/身份证 | 全部病历与手机号明文泄露 |
| `STACK_PHONE_PEPPER` | 手机号/身份证 HMAC | 可用彩虹表反推手机号 |
| `STACK_JWT_SECRET` | JWT HS256 签名 | 可伪造任意用户（含管理员）令牌 |

> **为什么是 `STACK_` 前缀而不是 `MEDICORE_`？**（实测踩过的坑，改动前务必先读）
>
> 根目录还有一份**开发用**的 `.env`（含 `MEDICORE_MYSQL_PASSWORD=medicore123`），
> 而 `nub` 会自动加载它、Docker Compose 又遵循 **「shell 环境 > `.env` 文件」** 的优先级。
> 两个文件一旦用同名变量，`nub run stack:*` 就会拿**开发口令**去起编排 ——
> 与数据卷里初始化的口令不匹配，后端直接 `Access denied`。
>
> 实测证据（修复前）：
>
> ```text
> $ nub -e "... docker compose -f deploy/compose.yml config ..."
>   MEDICORE_MYSQL_PASSWORD: medicore123          ← 错！应该是 deploy/.env 里的随机值
> ```
>
> 现在 `compose.yml` 里"容器内的键名"仍是 `MEDICORE_*`（那是后端读的），
> 只有"插值来源"改成 `STACK_*` —— 两套配置从此不可能撞名。

两个刻意的设计：

1. **默认拒绝覆盖**。重新生成会换掉 `aes_key`，导致库里已加密的数据永久无法解密、
   现有账号全部登不上。这与 `cmd/genlocal` 的立场一致，必须 `--force` 显式确认。
2. **复刻了后端的强度判据**。生成器内置了 `internal/config/validate.go` 里的
   `looksPredictable`（占位词、单一字符重复、短周期重复、连续升序），
   生成时就把不合格的值重摇 —— 否则问题会推迟到容器启动时，
   表现为一句与真实原因无关的 `invalid config`。

`deploy/.env` 已被 `.gitignore` 的 `.env` 规则覆盖（`git check-ignore deploy/.env` 可验证）。
`deploy/.env.example` 只是变量清单，**复制它不可用**（里面全是会被后端拒绝的占位值）。

---

## 两个镜像各自的设计取舍

### 后端：构建期零网络

`backend/Dockerfile` 把宿主机的 `GOMODCACHE` 作为 **BuildKit 命名构建上下文**
挂进来，配合 `GOPROXY=off` 编译：

```yaml
build:
  additional_contexts:
    gomodcache: ${GOMODCACHE:?未探测到 GOMODCACHE...}
```

实测数据（本机）：

| 指标 | 值 |
|---|---|
| 首次构建 | 传输模块缓存 ~1.05 GB / 79s，之后 Go 编译 34.5s |
| 缓存命中后重建 | **5.4s** |
| 运行镜像体积 | **86.3 MB**（`golang:1.27.1` 基础镜像为 1.31 GB） |

1 GB 的上下文传递是一次性的，BuildKit 会按内容哈希缓存。若不想付这个代价，
把 `deploy/.env` 里的 `GOMODCACHE` 清空即可回退到容器内联网下载
（Dockerfile 里有这条分支，判据是 `cache/download` 是否非空而不是目录是否存在 ——
空目录也会挂载成功，那时 `GOPROXY=off` 会报一个指向 GOPROXY 的误导性错误）。

`apk add tzdata` 是**唯一**需要网络的构建步骤。它解决一个真实问题：
容器默认 UTC，而本项目 `sched_date` 全部按本地时区计算（seed 的"今天起 7 天"、
DSN 的 `loc=Local`），时区不对会让排班日期整体偏移一天。

### 前端：产物在宿主机构建，镜像只做打包

`frontend/Dockerfile` 只做两件事：装 nginx 配置、`COPY frontend/dist`。
**不在容器里构建前端**，这是一个刻意的决定：

1. `nub` 是独立二进制（`~/.nub/bin/nub-*`），**不在 npm 上**，容器里拿不到；
2. 退而用 pnpm/npm 在容器内装依赖，等于把几百个 npm 包重新下载一遍 ——
   容器内的 store 与宿主机的 nub store 不共享，而本机 `HTTPS_PROXY` 指向**计费**代理；
3. 这条路径同时会违背项目"容器一律不出网"的硬约束。

代价：构建镜像前必须先跑 `nub --cwd frontend run build`（`nub run stack:up` 已包含这一步）。
好处：前端镜像构建是**零网络、1 秒级**的，且产物与开发时看到的完全一致。

前端镜像的构建上下文是**仓库根目录**（需要同时取到 `frontend/dist` 与
`deploy/nginx/default.conf`），根 `.dockerignore` 用白名单把它压到几 MB。

---

## nginx 配置里三个容易改错的地方

| 位置 | 为什么这么写 |
|---|---|
| `proxy_buffering off`（`/api/`） | 预问诊对话是 `text/event-stream`。开着缓冲时 nginx 会把整段响应攒完再一次性下发，"打字机"会退化成"转圈很久然后全文出现"。 |
| `proxy_set_header X-Forwarded-For $remote_addr` | **覆盖**而不是 `$proxy_add_x_forwarded_for` 追加。后端按客户端 IP 做登录限流（5 次/分钟），而 gin 默认信任所有代理并取 XFF 的第一个值 —— 追加的话客户端自带一个 `X-Forwarded-For: 1.2.3.4` 就能绕开限流。 |
| `include /etc/nginx/snippets/security-headers.conf` | nginx 的 `add_header` 在 location 里是**替换**而非继承：只要 location 自己写了任何一条 `add_header`，server 级的那几条就全部失效。因此 `/assets/` 与 `/index.html` 必须显式 include，否则会**静默**丢掉安全头。 |

另外 `location /assets/` 刻意不用 `expires 1y`（它会**另外**再发一条
`Cache-Control: max-age=...`，导致同一条响应里出现两个 `Cache-Control` 头），
也不用 `always`（否则 `try_files $uri =404` 的 404 会被缓存一年）。

---

## 调试：怎么访问数据库 / Redis

编排按 spec §11.2 **刻意不把 mysql / redis 映射到宿主机**，
所以没有 `localhost:3306` 这种直连入口。这是设计而非疏漏 ——
数据服务不该对着宿主机开放。

> **如果你在改代码（跑本机 `go run` + `vite dev`），请直接看下面 §0 ——
> 那是"两套编排同时在跑"的场景，也是本项目推荐的日常开发方式。**

### 0. 两套编排的口令与端口完全不同，别连错

这是最容易出的一类调试事故：**查了半天，发现查的是另一个数据库**。

| | `stack`（全栈编排） | `dev`（开发依赖） |
|---|---|---|
| 起法 | `nub run stack:up` | `nub run deps:up` |
| 用途 | 演示、验收、接近生产 | **本机改代码** |
| 容器名 | `medicore-mysql` / `medicore-redis` | `medicore-dev-mysql` / `medicore-dev-redis` |
| 宿主机端口 | **无**（仅容器内） | `127.0.0.1:3306` / `:6379` |
| 口令 | `deploy/.env` 里随机生成 | 固定 `medicore123` |
| project name | `medicore` | `medicore-dev` |
| 数据卷 | `medicore_mysql-data` | `medicore-dev_mysql-data` |

两套**互不影响，可以同时运行**（端口、卷、网络都不冲突）。

`deploy/db.mjs` 会自动识别该连哪一套：**只有一套在跑就用那套；
两套都在跑时默认用 `stack`，并打印提示**。可以用 `--dev` / `--stack` 强制指定。

```powershell
node deploy/db.mjs url          # 先看它连的是哪一套，再动手
```

### 1. 本机开发（`go run` + `vite dev` + `deps` 容器）—— 最常用

这就是"前后端同时运行 + 调试数据库"的标准姿势：

```powershell
# 终端 1：依赖
nub run deps:up

# 终端 2：后端（配置默认就指向 127.0.0.1:3306 / 127.0.0.1:6379）
$env:MEDICORE_MYSQL_PASSWORD='medicore123'; nub run backend:dev

# 终端 3：前端
nub run frontend:dev        # http://localhost:5173，经 Vite 代理打到 127.0.0.1:8080
```

链路是 `浏览器 → Vite(5173) → Go 后端(8080) → MySQL(3306)/Redis(6379)`，
**每一段都在宿主机上可达**，所以数据库既能用命令行查、也能用 GUI 客户端直连、
还能直接挂 IDE 调试器到后端。

```powershell
nub run stack:db -- "SELECT * FROM appointments ORDER BY id DESC LIMIT 5;"
nub run stack:redis -- "KEYS stock:*"

# GUI 客户端直连（dev 已映射端口，不需要任何转发）
#   host=127.0.0.1  port=3306  user=medicore  password=medicore123  db=medicore
```

> ⚠️ **这两套的数据是不通的**：`deps:up` 的 3306 上是 `medicore-dev_mysql-data`，
> 不是编排那套。`.env` 里 `deploy/compose.dev.yml` 用的是**明文口令 `medicore123`** ——
> 那是本地开发依赖，它本来就在仓库里、只监听 `127.0.0.1`。

### 2. 用现成的小工具

`deploy/db.mjs` 会自己挑对编排、把口令读出来再交给 docker，
**口令不会出现在你的 shell 历史里**：

```powershell
nub run stack:db                          # 进 MySQL 交互式 shell
nub run stack:db -- "SELECT id,name FROM departments;"   # 执行单条 SQL
nub run stack:redis                       # 进 redis-cli
nub run stack:redis -- "KEYS stock:*"     # 执行单条命令
nub run stack:dump                        # 导出到 deploy/backup.sql（已被 git 忽略）

node deploy/db.mjs url                    # 打印连接信息（含"连的是哪一套"）
node deploy/db.mjs port 13306             # 起临时端口转发，给 Navicat/DBeaver 用
node deploy/db.mjs restore backup.sql     # 导入
node deploy/db.mjs --dev mysql "SELECT 1;"  # 强制连开发那套
```

> PowerShell 里给 `nub run` 传参要写成 `nub run stack:db -- "SQL"`（`--` 之后的才是脚本参数）。

实测输出：

```text
$ nub run stack:db -- "SELECT code,name FROM departments ORDER BY sort_order;"
code    name
IM      内科
SURG    外科
PED     儿科
OBGY    妇产科
```

### 4. 直接 `docker exec`（等价的原生命令）

容器里自带客户端，不需要在宿主机装任何东西：

```powershell
# 开发那套（口令固定）
docker exec -i medicore-dev-mysql mysql --default-character-set=utf8mb4 -umedicore -pmedicore123 medicore
docker exec -i medicore-dev-redis redis-cli

# 编排那套（口令从 .env 读，避免明文进历史）
$P = (Select-String -Path deploy\.env -Pattern '^STACK_MYSQL_PASSWORD=(.*)$').Matches[0].Groups[1].Value
docker exec -i medicore-mysql mysql --default-character-set=utf8mb4 -umedicore "-p$P" medicore -e "SELECT id,name FROM departments;"
```

> **`-i` 与 `-it`**：`-i` 在管道/脚本里也能用；`-it` 需要真实终端，
> 在 Codex/CI 这类没有 TTY 的环境里会报
> `cannot attach stdin to a TTY-enabled container because stdin is not a terminal`。
>
> **中文显示成 `??`** 时加 `--default-character-set=utf8mb4`。
> 这是 Windows 控制台代码页的问题，**数据本身是好的** ——
> 表与库都是 `utf8mb4`，在 Navicat 一类客户端里正常显示。

### 5. 查数据时要知道的两个字段约定

直接看表很容易被这两点绊住（都是**有意**的设计，不是 bug）：

| 你以为的 | 实际列名 | 说明 |
|---|---|---|
| `phone` | **`phone_enc`**（`varbinary(128)`）+ `phone_hash`（`char(64)`） | 手机号是 **AES-256-GCM 密文**，不可直接读；唯一性与登录靠 `phone_hash`（HMAC 摘要，有唯一索引）。要还原明文只能走应用层的解密 |
| 明文手机号 | 同上 | 所以 `SELECT * FROM users` 里**看不到手机号**，这是 spec §2.2 的加密存储要求 |

同理 `id_card_enc` / `id_card_hash`。`password_hash` 是 bcrypt（`$2a$10$` 前缀）。

实测确认（注册 → 从库里查回来）：

```text
id    role     name       enc_hex(前 60 字符)                                  hash_prefix
251   patient  调试验证   13F44C9750C86ECB278AB6126EBF9EA78B594A3AC6C7C8BB…   e8160d569648f226
```

要看"某个手机号是谁"，用应用层而不是 SQL：

```powershell
# 登录成功就说明 phone_hash 匹配（比在 SQL 里试摘要靠谱）
curl.exe -s -X POST http://127.0.0.1:8080/api/v1/auth/login `
  -H "Content-Type: application/json" `
  --data '{\"phone\":\"13900000001\",\"password\":\"Doctor@123\"}'
```

### 6. 常用排查查询

```sql
-- 各状态预约数（抢号/取消/过期的分布）
SELECT status, COUNT(*) FROM appointments WHERE deleted_at IS NULL GROUP BY status;

-- 号源是否与预约对得上（对账逻辑的人工复核）
SELECT s.id, s.sched_date, s.time_slot, s.total, s.remaining,
       (SELECT COUNT(*) FROM appointments a
         WHERE a.schedule_id = s.id
           AND a.status IN ('pending','confirmed')
           AND a.deleted_at IS NULL) AS occupied
FROM schedules s WHERE s.deleted_at IS NULL ORDER BY s.id DESC LIMIT 10;

-- 最近失败的预问诊报告（含 error_msg 与 LLM 调用次数）
SELECT id, appointment_id, status, attempt_count, fallback, error_msg, created_at
FROM ai_pre_reports WHERE deleted_at IS NULL ORDER BY id DESC LIMIT 10;

-- 按 trace_id 反查一次请求（与前端错误提示里的"追踪号"对应）
SELECT trace_id, user_id, role, method, path, status_code, latency_ms, created_at
FROM audit_logs WHERE trace_id = '<粘贴追踪号>';
```

Redis 侧：

```powershell
nub run stack:redis -- "KEYS stock:*"        # 号源键（抢号时才创建）
nub run stack:redis -- "GET stock:sched:14"  # 某个排班的剩余号源
nub run stack:redis -- "KEYS rl:*"           # 限流令牌桶
nub run stack:redis -- "XLEN preconsult:summary"   # 总结任务队列长度
```

### 备份 / 恢复

```powershell
nub run stack:dump                # -> deploy/backup.sql（已被 .gitignore 排除）
node deploy/db.mjs restore deploy/backup.sql
```

> 导出文件里含**口令散列与加密后的手机号/身份证**，所以 `.gitignore` 里专门
> 忽略了 `deploy/backup.sql`。历史上"密钥进过 git"的教训说明，
> 这类文件的忽略规则不能靠自觉 —— 见仓库根 `AGENTS.md` §2.6。

### ⚠️ 一条最容易踩的坑：改了密钥/口令后必须清数据卷

MySQL 的 `MYSQL_PASSWORD` / `MYSQL_ROOT_PASSWORD` **只在数据卷为空时生效一次**
（`docker-entrypoint-initdb.d` 的机制同理）。所以：

```
改 deploy/.env 里的口令
  → 容器环境变量变了，但卷里还是旧口令
  → 后端连不上（Access denied），或你 docker exec 连不上
```

**这条在本项目里不是理论风险，是实测踩到的**：先跑了一次 `stack:up`（生成口令 A），
后来又 `stack:init` 重新生成了 `.env`（口令 B），结果：

```
ERROR 1045 (28000): Access denied for user 'medicore'@'localhost' (using password: YES)
```

判别方法 —— 对比"卷创建时间"与"`.env` 修改时间"：

```powershell
docker volume inspect medicore_mysql-data --format "{{.CreatedAt}}"
(Get-Item deploy\.env).LastWriteTime
```

卷更早 ⇒ 口令已经不匹配。修法是**清卷重建**（会清空数据）：

```powershell
nub run stack:reset    # down -v，删数据卷
nub run stack:up
nub run stack:seed     # 重新灌演示数据
```

> 同一类问题也适用于 `docs/schema.sql`：**它只在卷为空时执行一次**。
> 改了表结构必须 `stack:reset` 才会生效。

---

## 排障

| 现象 | 原因与处理 |
|---|---|
| `Access denied for user 'medicore'` | 口令与数据卷里的不一致 —— 见上面「改了密钥/口令后必须清数据卷」 |
| 表结构改动没生效 | 同上：`docs/schema.sql` 只在卷为空时执行。`nub run stack:reset` |
| `port is already allocated` | 宿主机端口被占用。`stack:init` 会自动挑空闲端口（并刻意避开 8080，那是本机 go run 后端的端口），但那只在生成时判断一次；后来占用了就改 `deploy/.env` 的 `STACK_HTTP_PORT` 再 `stack:up`。 |
| 构建报 `未探测到 GOMODCACHE` | `go` 不在 PATH 上。装上 Go 1.27.1 后重新 `stack:init`，或手动在 `deploy/.env` 填 `go env GOMODCACHE`（**用正斜杠**）。 |
| 后端起不来，日志说 `invalid config: security.xxx 未设置` | `deploy/.env` 缺密钥。跑 `nub run stack:init`。 |
| 后端日志说密钥"含占位词 / 短周期重复" | 手动编辑 `.env` 时写了弱值。用 `node deploy/init-env.mjs --force` 重新生成（**会换掉 aes_key**，需同时 `stack:reset`）。 |
| 反复 `stack:up` 后浏览器 502 | nginx 缓存了旧的后端 IP。本配置用 Docker 内置 DNS（`resolver 127.0.0.11`）+ 变量形式的 `proxy_pass` 自动重新解析，正常情况下不会出现；若仍出现，`docker compose -f deploy/compose.yml restart frontend`。 |
| 中文显示成 `??` | Windows 控制台代码页问题，不是数据问题。加 `--default-character-set=utf8mb4`，或用 GUI 客户端。 |
| 想确认谁占了 3306 | `docker ps --format "{{.Names}}\t{{.Ports}}"` —— 有 `127.0.0.1:3306->` 的是 `medicore-dev-mysql`（开发依赖），与编排无关。 |

---

## 已验证 / 未验证

对着真实编排实测通过（2026-09-13）：

| 项 | 结果 |
|---|---|
| `stack:up` 一键起全栈 | 四个容器全部 healthy |
| 端口暴露 | `docker port` 对 mysql/redis/backend 返回空；仅 frontend 发布 |
| 同源入口 | `/` 返回 index.html；`/appointments` 深链接正确回退到 index.html |
| 运维端点反代 | `/healthz` `/readyz` `/metrics` 均 200 |
| 三角色登录 | 管理员、医生均 `code=0` |
| 业务读写 | 科室列表、审计日志、抢号、预问诊历史、`submit`(202)、报告(done) 全部正常 |
| **SSE 未被缓冲** | 首字节 **0.003s** / 结束 0.247s，12 个 `delta` + 1 个 `done` |
| 缓存与安全头 | index.html `no-store`；8 个哈希资源 `immutable` 长缓存；404 不带缓存；安全头齐全 |
| 镜像体积 | backend 86.3 MB / frontend 105 MB |

**未验证**：

- **没有在浏览器里打开过**。以上全部结论来自 HTTP 层；页面渲染、图表绘制、
  移动端布局仍需人工过一遍。
- **没有在 Linux / macOS 上跑过**。`GOMODCACHE` 的正斜杠归一化与端口探测都是
  跨平台写的，但只在 Windows 上实测过。
- **没有测试「依赖挂掉」的表现**。可以 `docker compose -f deploy/compose.yml stop mysql`
  后观察 `readyz` 是否转为 503、backend 是否变为 unhealthy。

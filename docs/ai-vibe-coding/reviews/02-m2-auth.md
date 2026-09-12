# 02 · M2 —— 认证鉴权（AES-GCM / bcrypt / 双令牌 / RBAC / 限流）

| | |
|---|---|
| **对应提交** | `28cb2a3`；缺陷修正在 `6b23e57`（包注释）、`ecaa04c`（fuzz 目标） |
| **规模** | 14 文件 / 930 行（`MODULES.md` §2） |
| **涉及包** | `secret` `jwt` `authctx` `mask` `model/user`+`dto_auth` `repository/user`+`token` `service/auth` `handler/auth` |
| **本篇性质** | **事后整理**。来源：`docs/HANDOVER.md` §2.1-D、§2.2、§2.7-D、§2.9-C、`MODULES.md` §2、`FIXES.md`；§三⑥ 为本次新补查 |
| **结论** | 交付达成；**2 处真实缺陷**已修（fuzz 抓出脱敏 `IDCard`；本次补查抓出 bcrypt 注释的语义记错，见 §三⑥）；遗留见 §六 |

---

## 一、需求拆解

| spec 出处 | 要求 | 落地 |
|---|---|---|
| §6.1 | `POST /auth/login` 返回**双 token** | `service/auth.Login` → access(JWT HS256, 2h) + refresh(不透明随机串, 7d) |
| §2.2 | 敏感数据 **AES-GCM 加密存储**，DTO 层**统一脱敏** | `secret.Cipher`（AES-256-GCM，nonce 前置 12 字节）；`mask` 包 |
| §9.3 | 登录 **5 次/分钟**限流 | `middleware.RateLimit` 挂在 `/auth/login` |
| §2.1 | 三角色 RBAC | `middleware.RequireRole`；**注册永远产生 `patient`** |

**spec 有缺陷需修正的部分**：§5.1 的 `phone_hash = SHA256(phone + salt)` 概念上不成立
（逐行随机盐无法反查用户，登录会彻底不可用）—— 见 §二第 1 条与 `HANDOVER.md` §4。

---

## 二、方案选型

| 决策点 | 被否决的方案 | 选定 | 理由 |
|---|---|---|---|
| `phone_hash` 算法 | `SHA256(phone + salt)`（spec 原文） | **HMAC-SHA256(pepper, phone)** | keyed hash 不会因拼接方式引入长度扩展问题。**关键性质仍是"确定性"** —— 用逐行随机盐就没法由手机号反查用户（**刻意偏离 spec 第 1 处**） |
| refresh_token 形式 | JWT（自包含、无状态） | **不透明随机串 + Redis** | 刷新令牌**必须可撤销**，无状态做不到（除非再加黑名单，等于绕回 Redis）。既然要存 Redis，JWT 的自包含性毫无收益 |
| 刷新消费方式 | `GET` + `DEL` 两步 | **`GETDEL` 原子消费** | 两步写法在并发刷新时同一令牌会被消费两次 → **重放窗口** |
| 登录失败提示 | 区分"账号不存在"/"密码错误" | **不区分** | 区分就是用户枚举漏洞 |
| 账号不存在时的耗时 | 直接返回 | **执行一次等价比对** | 否则响应时间差约一个 bcrypt 周期（cost=10 约 60ms），可据此枚举手机号 |
| 占位散列的 cost | 固定 cost | **与真实口令相同的 cost 预生成** | 若占位用固定 cost 而配置改了，时间差会重新出现 |
| 超长口令 | 信任 bcrypt | **显式拒绝 >72 字节** | bcrypt 会**静默截断**，长密码给出虚假安全感 |
| 限流器故障 | fail-closed（拒绝所有） | **fail-open** | 让登录整体不可用比短暂放开限流更严重 —— **但必须有指标**（否则就是 M1 坑 4） |

---

## 三、§10.2 检查清单

### ① 分层与架构

- 加密/摘要/令牌生成下沉到 `secret`（无状态工具包），`service` 不碰 crypto 原语。
- `authctx` 统一 `gin.Context` 里认证信息的读写键，**避免各层各写字符串字面量**
  （否则一个层写 `"userID"`、另一个写 `"user_id"`，编译期发现不了）。
- `repository/token.go` 是唯一碰 Redis 令牌键的地方。

**判定：通过。**

### ② 错误与并发

| 检查项 | 情况 |
|---|---|
| 错误包装 | 全部走 `apperr`，登录失败统一 `ErrUnauthorized`（不泄漏是账号还是口令错） |
| 时序攻击 | **登录失败也执行一次等价比对** —— 有专门测试守护，且**做过反向验证**（见 §五） |
| 并发 | `GETDEL` 原子消费刷新令牌；注册依赖 `uk_phone_hash` 唯一索引判定重复，**而不是"先查再插"** |
| 为什么不用"先查再插" | 软删除行会被 GORM 的查询过滤掉，但唯一索引依然会拦住插入 —— 先查再插会给出**与事实相反**的提示（"该手机号可用"然后插入失败） |

**判定：通过。** 这是全项目唯一一处**主动做时序防护**的地方，且防护有测试与反向验证。

### ③ 性能与 SQL

- 登录是"按 `phone_hash` 等值查唯一键" —— 实测 `type=const`，**优化期即出结果**
  （见 `docs/sql-optimization.md` Q21）。
- 无 N+1；无事务内网络调用（注册的加密与散列是纯 CPU，Redis 操作在事务外）。
- **限流参数实测**：`capacity` 允许瞬时突发、`refill` 决定稳态速率；
  登录 5/分钟、抢号 10/分钟（spec §9.3）。集成测试验证了"容量耗尽后被限"与"清桶后恢复"。

### ④ KISS 原则

| 出现的"多余" | 判定 |
|---|---|
| `secret.DummyPassword()` | **必要** —— 时序防护的载体，且 cost 与真实口令一致 |
| `authctx` 独立成包 | **必要** —— 消除字符串字面量漂移 |
| `mask` 独立成包 | **必要** —— 脱敏规则集中一处，避免每个 DTO 自己写 |
| `Package crypto` 的包注释 | ❌ 曾经存在：包名是 `secret` 而注释写 `Package crypto`，godoc 不挂载（ST1000）。**已改正** |

### ⑤ 基础卫生

| 检查项 | 情况 |
|---|---|
| 输入校验 | 手机号 `len=11,numeric`、身份证 `len=18` 由 binding tag 校验；注册角色**由服务端强制为 patient** |
| 敏感数据脱敏 | 手机号/身份证**加密存储**（密文），DTO 层再脱敏；`mask` 有专门 fuzz 目标断言**绝不原样返回** |
| 硬编码 | **仓库内无任何密钥**；`config.Validate()` 按特征拒绝可预测密钥；占位散列由 `genlocal` 生成 |

### ⑥ AI 幻觉

本次新补查（方法见 `README.md`）：M2 涉及的 `golang.org/x/crypto/bcrypt`
（`GenerateFromPassword` / `CompareHashAndPassword`）、`golang-jwt/jwt/v5`
（`ParseWithClaims` / `RegisteredClaims`）、`crypto/aes`+`cipher`（标准库）
**全部存在**。

**语义级核对（比"存不存在"更重要）**。这一项本轮**真的抓到了东西** ——
`secret/password.go` 的注释写着"bcrypt 超出 72 字节**会被静默截断**"，
而实测当前依赖（`x/crypto v0.48.0`）的源码：

```go
// bcrypt.go:93-97
// GenerateFromPassword does not accept passwords longer than 72 bytes ...
if len(password) > 72 {
    return nil, ErrPasswordTooLong     // ← 报错，不是截断
}
```

**注释陈述的理由是错的。** 继续核对还发现一个更关键的事实：

```go
// 比对路径 bcrypt.CompareHashAndPassword → bcrypt() → expensiveBlowfishSetup
// 全程**没有长度检查**，会照常算完一整轮 Blowfish 扩展
```

于是原注释里"必须显式拒绝"的说法虽然结论对，**但真正的理由被写反了**：
生成侧库自己会拦，**比对侧才是没有守卫的那一侧**。
`VerifyPassword` 的提前返回不是"防止截断"，而是**短路一次白算**。

| API | 容易记错的点 | 本轮核实结果 |
|---|---|---|
| `bcrypt.GenerateFromPassword` | 超 72 字节是**截断**还是**报错**？ | **报错**（`ErrPasswordTooLong`）。原注释写"静默截断"是**过时/错误**的陈述 |
| `bcrypt.CompareHashAndPassword` | 是否同样拒绝超长输入？ | **不检查**。本项目 `VerifyPassword` 的提前返回因此是**真守卫** |
| `jwt.ParseWithClaims` | 需要显式限定签名算法（防 alg 混淆） | **未踩到** —— `jwt.Manager` 既校验 `*jwt.SigningMethodHMAC` 又 `WithValidMethods(["HS256"])`，双保险 |
| `jwt.ParseWithClaims` 的 `WithIssuer` | 不设则任意签发者的令牌都被接受 | **未踩到** —— 已设 `WithIssuer(m.issuer)` |

**处理**：两处不准确的注释已改正（`password.go` 与 `secret_test.go`），
并把"比对侧无长度检查"这个真实理由写了进去。**行为零改动**（注释-only）。

> **这正是"AI 幻觉"检查该有的产出形态**：不是"符号不存在"（那编译期就死了），
> 而是**"符号存在、结论对、但理由记错了"** ——
> 它不会让任何测试变红，却会让下一位读者基于错误前提做判断。
> 例如有人读到"库会截断"，就可能认为"我们这道检查是多余的"而删掉它 ——
> 删掉后生成侧仍会报错，但**比对侧的守卫会一起消失**。

---

## 四、本模块暴露的真实缺陷

| # | 缺陷 | 定性 | 来源 |
|---|---|---|---|
| 1 | **`mask.IDCard` 在长度恰为 10 时一个字符都没屏蔽** | ✅ **真缺陷** | **fuzz 抓出**（`FuzzMaskNeverLeaksOriginal`） |
| 2 | **`secret/password.go` 注释把 bcrypt 的超长行为说成"静默截断"**，实际是报错；且真正的理由（**比对侧无长度检查**）被写反了 | ✅ **真缺陷（文档级）** | **本篇 §三⑥ 本轮补查抓出** |
| 3 | `secret/cipher.go` 包注释写 `Package crypto` | 文档缺陷（ST1000） | 全量审视 |
| 4 | `assert.Empty(t, u.PasswordHash)` —— 本意是"散列非空"，**语义写反了** | ❌ 测试错 | `HANDOVER.md` §2.1-D |
| 5 | 用 `strings.Repeat("a1b2c3d4", 8)` 当"强密钥" | ❌ 夹具错：它本身就是**短周期重复**，被正确的校验拒绝了。**实现的判断是对的，测试的夹具错了** | 同上 |

### 第 1 条值得展开

`mask.IDCard` 的屏蔽逻辑用 `strings.Repeat("*", n)` 计算要打几个星号。
当输入长度恰为 10 时，算出的 `n == 0` —— **返回原样**，一个字符都没屏蔽。

**这个缺陷手写测试几乎抓不到**：要恰好想到"长度 = 10"这个边界。
fuzz 在一次随机输入里就命中了 `FuzzMaskNeverLeaksOriginal`
（性质："输出绝不能包含原输入"）。修法是把 `len < 10` 的守卫改成 `len < 11`。

> **教训**：脱敏函数的不变量应该是**可被证伪的性质**（"绝不原样返回"），
> 而不是"几个固定输入得到几个固定输出"。前者能被 fuzz 覆盖，后者只能覆盖你想到的情况。

---

## 五、反向验证记录

| 被测性质 | 反向验证方式 | 结果 |
|---|---|---|
| **登录时序防护** | `TestLogin_UnknownUserTimingIsEqualized`：去掉假比对后，两次登录的耗时比值降到 0.00 | **失败 ✅**（说明测试真的在测时序，而不是在看返回值） |
| 脱敏绝不泄漏原值 | `FuzzMaskNeverLeaksOriginal` | 抓出真实缺陷（§四第 1 条）✅ |
| AES-GCM 往返 | `secret/secret_test.go` + `FuzzEncryptDecryptRoundTrip` | ✅ |
| JWT 解析 | `jwt/jwt_test.go` + `FuzzIssueParseRoundTrip` | ✅（**但我最初写的往返断言是错的**，见下） |
| 包注释与包名一致 | `staticcheck` ST1000 | 抓出 §四第 2 条 ✅ |

> **诚实记录一处"我的断言错了"**：`FuzzIssueParseRoundTrip` 最初断言 JWT 载荷能**字节级往返**。
> 第一次跑就报失败 —— 但**不是代码缺陷**：JWT 载荷是 JSON，而 JSON 字符串
> **无法承载非法 UTF-8**，字节级往返本就不成立。
> **fuzz 报失败时第一件事是问"这条性质真的必须成立吗"，而不是看到红就改代码。**

---

## 六、遗留与未验证

| 项 | 说明 |
|---|---|
| refresh token 轮换的**重放检测** | 当前 `GETDEL` 保证同一令牌只能消费一次；但**若旧令牌被重放，不会撤销整个令牌族**（RFC 6819 的 refresh token rotation + reuse detection）。当前规模下未实现，属**有意取舍**，非疏漏 |
| bcrypt cost 取值 | 由 `config` 提供（默认 10）。**未测过 cost 与登录延迟的曲线**，只在时序防护测试里间接依赖它 |
| `secret.Cipher` 的密钥轮换 | 单一密钥，无 key id / 无多版本解密能力。生产环境轮换密钥需要数据迁移 —— 当前未设计 |
| 账号锁定 | 只有限流（5 次/分钟），**无连续失败锁定**。spec 未要求 |

---

## 交叉引用

- 结构与决策：`docs/MODULES.md` §2
- 原始错误记录：`docs/HANDOVER.md` §2.1-D、§2.2（spec 缺陷 1）
- 易错点：`docs/PITFALLS.md` §6（断言恒为真）
- 接手规则：`AGENTS.md` §2.6（密钥绝不进仓库）

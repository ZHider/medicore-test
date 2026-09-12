# 06 · M6 —— 病历处方 / HIS 同步 / 审计 / 医生工作台补全

| | |
|---|---|
| **对应提交** | `8eca3e2`（病历/处方/HIS 同步）、`0bc436d`（审计 + 抽取 `internal/app`）、`3f0207c`（HTTP 集成测试层）、`e3072ab`（**医生工作台补全**）；缺陷修正在 `6b23e57`、`dddd194`、`ecaa04c` |
| **涉及包** | `audit` `middleware/audit` `repository/audit` `service/audit` `handler/audit` `service/record` `repository/record` `service/sync` `repository/sync`+`synclog` `mockhis` `his` `handler/record` `handler/sync` |
| **本篇性质** | **事后整理**。来源：`MODULES.md` §6、`HANDOVER.md` §2.7-B/C/E/F、`FIXES.md` H2/H3、`docs/PITFALLS.md` §9/§10；§三⑥ 为本次新补查 |
| **结论** | 交付达成；引入**契约包 `internal/his`** 修正了一处依赖倒置；新增 29 个用例（单元 +18 / 集成 +11）；遗留见 §六 |

---

## 一、需求拆解

| spec 出处 | 要求 | 落地 |
|---|---|---|
| §6.1 | 病历与处方 | `service/record` + `repository/record`（**事务内同写**，见 §二） |
| §7.3 | 模拟 HIS：`upsert by external_id` + **`errgroup` 限并发 5** + 记 `sync_logs` | `service/sync`（**三阶段**，见 §二） |
| §2.2 | **双层校验**：路由 RBAC + 资源层归属 | `service/record.authorize` 三分支 |
| §9.2 | 审计日志：`trace_id` 贯穿 | `audit` 异步写入器 + `middleware/audit` |
| §2.1 | 管理员全部读权限 | 各 `authorize` 的 admin 分支 |
| §2.1 | **医生查看本人预约 / 患者预问诊报告** | ⚠️ **规格缺口** —— 见 §四第 1 条 |

---

## 二、方案选型

| 决策点 | 被否决的方案 | 选定 | 理由 |
|---|---|---|---|
| 审计写入 | 同步写（永不丢） | **异步批量 + 有界缓冲（2048）** | 审计是**事后追溯**用途，不在业务关键路径。代价（崩溃丢一个批次）被限制在 batch(100) / flush(500ms) 之内，**且丢弃与失败都有指标可告警** |
| 缓冲满时 | 阻塞 | **丢弃** | 审计价值再高，也不值得让一次 DB 抖动演变成**全站请求堆积** |
| 审计存什么 | 含请求体 | **刻意不含请求体** | 请求体可能含手机号、口令、病情描述 —— 复制进审计表**等于把脱敏工作全部作废** |
| 资源名从哪来 | 真实请求路径 | **`FullPath`（路由模板）推导** | 真实路径让每个 ID 产生不同"资源名"，审计查询无法按资源聚合 |
| 病历与处方 | 分两次提交 | **同一事务** | 分开提交会出现"病历已存但处方没存"的中间态，医生看到一份缺了用药的诊疗记录 —— **临床上是危险的** |
| HIS 同步 | 一把梭并发全部实体 | **三阶段**（科室 → 医生 → 排班） | 排班依赖医生、医生依赖科室。并发处理会让排班因医生未落库而**大量失败** |
| 同步来的排班 `remaining` | 按数据源覆盖 | **绝不重置** | 覆盖意味着患者已抢到的号被"复活" → **超卖** |
| HIS 契约放哪 | `pkg/mockhis`（spec 原文） | **`internal/his` 契约 + `mockhis` 适配器** | 见 §四第 4 条 |

---

## 三、§10.2 检查清单

### ① 分层与架构

**本模块修正了一处依赖倒置**：`service/sync.go` 的 `SyncSource.Load` 曾经返回
`*mockhis.Dataset` —— **业务逻辑包依赖了一个名字里带 mock 的实现包**，
真实 HIS 的适配器将不得不 import 一个 mock 来拿类型。那个"抽象"什么也没抽象。

修复：新增 `internal/his` 契约包（**只放数据类型，不放接口**，遵循本项目"接口由使用方声明"的惯例）；
`mockhis` 变成它的一个适配器，并把 "相对今天 N 天" 这种数据文件写法**在适配器内部换算成绝对日期**，
不再泄漏到业务层。

其他：

- `Access` 被收敛到 `service/access.go` 顶层。最初叫 `RecordAccess` 并定义在 `record.go`，
  但预问诊与预约列表也需要同一身份 —— 放在任一模块下都会让别的模块产生"要 import 病历模块"的错觉依赖。
- `FindIDByUserID` 从 `MedicalRecordRepo` 迁到 `DoctorRepo`（同理由）。
- 审计中间件在链尾，**被 RBAC 拒绝的请求同样留下记录** —— 而那恰恰是审计最关心的内容之一。

**判定：通过（含一处重要修正）。**

### ② 错误与并发

| 检查项 | 情况 |
|---|---|
| 错误包装 | ✅ 含 `ErrRecordExists` 的语义化转译（`gorm.ErrDuplicatedKey` → 唯一键冲突 → 409） |
| goroutine 退出路径 | 审计写入器的 `Stop()` **幂等** —— 启动失败回滚路径与 `main` 的 defer 都可能调用它，重复调用会 close 已关闭 channel 而 panic，**恰恰在最需要优雅退出的时刻** |
| 共享状态 | 审计缓冲用 mutex；HIS 同步用 `errgroup.SetLimit(5)` 限并发 |
| 不可达的错误通道 | ✅ **已删**：`runPhase`/`run` 的 errgroup 错误分支**永远走不到**（三个 upsert 都返回 nil），**读起来却像"失败会中止整批"** —— 让代码形状匹配真实语义 |

**判定：通过（含一处已清的形状不一致）。**

### ③ 性能与 SQL

| 查询 | 选用索引 | 实际读取行数 | 热态耗时 |
|---|---|---|---|
| Q8 病历列表（患者） | `idx_patient_cursor` | ~5 | — |
| Q9 病历列表（管理员全量） | `PRIMARY`（反向） | 21 | 0.183 ms |
| Q10 病历详情 | `uk_appointment` | ~1（**const**） | — |
| Q11 处方明细 | `idx_record (record_id, sort_order)` | ~2 | — |
| Q17 审计日志·游标（无筛选） | `PRIMARY`（反向） | 21 | **0.022 ms** |
| Q18 审计日志·按用户 | `idx_user_cursor` | 7 | 0.039 ms |
| Q19 审计日志·按时间范围 | `PRIMARY`（反向） | 1,830 | 0.772 ms |
| Q20 同步日志 | `PRIMARY`（反向） | 21 | 0.020 ms |

**三处值得记的判定**：

- **Q11 是"覆盖索引"的正确示范**：`idx_record (record_id, sort_order)`
  同时满足等值查找与排序，**不需要回表**（查询列 `drug_name/spec/quantity/...` 不在索引里，
  但行数只有 2，回表代价可忽略 —— 真正的收益是**免排序**）。
- **Q17 是"游标分页"的最佳样本**：`type=index, key=PRIMARY, rows=21, Backward index scan`，
  **无 filesort、无 filter**，代价与页码完全无关。对照组 OFFSET 扫了 30 万行（94.2 ms）。
- **Q19 走 PRIMARY 而非 `idx_created`**：因为 `ORDER BY id DESC`。
  这**成立的前提是 `created_at` 与 `id` 同向增长**（审计只追加不更新）。
  若将来引入回填/乱序写入，这个前提会被打破 —— 记在这里作为一个**隐含假设**。

### ④ KISS 原则

| 出现的"多余" | 判定 |
|---|---|
| `trailingActions` 映射表 | **必要** —— 覆盖"末段是动作而非资源"的路由（`/appointments/:id/cancel` → `action=cancel`），否则记成笼统的 `create` |
| `scopeSegments` 跳过权限域 | **必要** —— 见 §四第 3 条 |
| 审计的"资源 / 动作"从路径推导 | **必要且有取舍**（见 §二）；代价是新增"末段是动作"的路由时要维护映射表 |
| ~~`SyncDeps.Concurrency`~~ | ❌ **已删** —— spec 定死 5，生产从不设，测试设的也是默认值。"为将来保留的旋钮" |
| ~~`UpsertDoctor` 的医生 ID / `UpsertSchedule` 的 `created`~~ | ❌ **已删** —— 调用方直接丢弃的返回值 |
| ~~`mockhis.Dataset.Summary`、`SyncLogRepo.LastFinished`、`MedicalRecordRepo.FindByID`~~ | ❌ **已删** —— 死代码 |
| ~~`audit_logs.detail` 列~~ | ❌ **已删列** —— 从没写过也没读过，而审计的设计前提正是"不存请求体"。**留一个空的 JSON 列只会诱使后人往里塞敏感数据** |
| `sync_logs.trace_id` | ✅ **接上而非删** —— 它符合项目"TraceID 贯通"的设计，手动触发同步时能把 `sync_logs` 与同一次请求的审计记录串起来 |

### ⑤ 基础卫生

| 检查项 | 情况 |
|---|---|
| 输入校验 | 病历写入校验诊断非空；处方数量/天数有下界；同步的 source 参数走白名单 |
| 敏感数据脱敏 | 病历/报告的**读取入口全部经过 `authorize`**；审计**不存请求体** |
| 硬编码 | 阈值均为命名常量；无密钥；同步账号的口令由 seed/genlocal 生成 |

### ⑥ AI 幻觉

本次新补查（方法见 `README.md`）。M6 是"用 GORM 高级特性最多"的模块：

| API | 存在性 | 语义核对结果 |
|---|---|---|
| `gorm.ErrDuplicatedKey` + `Config.TranslateError` | ✅ | **两者必须同时用**：不开 `TranslateError` 时 GORM 不会把驱动错误翻译成 `ErrDuplicatedKey`，`errors.Is` 永远为假。代码两处都配了，正确 |
| `clause.OnConflict{DoNothing: true}` | ✅ | 配合 `RowsAffected==0` 判断"已存在" —— **这是 MySQL `INSERT IGNORE` 语义**，`DoNothing` 正确对应 |
| `DB.CreateInBatches(entries, len(entries))` | ✅ | 第二个参数是**批大小**。传 `len(entries)` 表示"一次全插"，**这正是这里想要的**（审计批量插入，避免 GORM 默认分批带来的多次往返） |
| `DB.Pluck("id", &doctorID)` | ✅ | 只取一列到标量。注意 `doctorID==0` 才判"没找到" —— 因为 `Pluck` **不返回 `ErrRecordNotFound`**，代码处理正确 |
| `DB.UpdateColumn` vs `Update` | ✅ | `UpdateColumn` **跳过 Hook 与 `UpdatedAt` 自动更新** —— `TouchLastLogin` 用它正是为了"只更新单列，避免整行写放大"，正确 |
| `errgroup.SetLimit(5)` | ✅ | 必须在 `Go` 之前调用；超出上限的 `Go` 会**阻塞**直到有空位。语义符合"限并发"的意图 |
| `clause.Locking{Strength: "UPDATE"}` | ✅ | 生成 `FOR UPDATE`。注意它**必须在事务内**才有效，代码都在 `Transaction` 里，正确 |

**结论：0 个幻觉符号。多个 API 的"细节语义"（`TranslateError` 必须开、`Pluck` 不报 NotFound、
`UpdateColumn` 跳过 Hook、`SetLimit` 在 `Go` 前调用）均核对无误。**

---

## 四、本模块暴露的真实缺陷

### 1 · 规格缺口：医生工作台的两个接口根本不存在

M1–M6 全部标记"完成"**之后**，把 spec §2.1 的角色能力矩阵**横过来**查了一遍，
发现医生侧有**两处真实缺口** —— 不是文档漏写，而是**接口根本不存在**：

| spec §2.1 要求 | 补全前 | 补全后 |
|---|---|---|
| 医生查看**本人**预约列表 | ❌ 只有患者视角的 `/appointments` | ✅ `GET /doctor/appointments` |
| 医生查看患者**预问诊报告** | ❌ 报告接口只对 patient 开放 | ✅ 开放给 patient / doctor / admin |

**这不是锦上添花，是规格缺口**：spec §8 的医生工作台没有这两个接口就搭不起来。

> **教训：模块维度全部打勾 ≠ 角色维度没有缺口。**
> 此前验收是**逐模块自测**的，所以 M4 自测通过、M5 自测通过，
> 但"医生坐下来用一遍"这条路径**从来没有被完整走过**。
>
> 这条直接催生了后来独立的"**按角色横切复查**"一轮
> （医生角色发现 2 处缺口；患者/管理员角色复查后无新缺口）。

**报告的归属校验三分支**（本轮**唯一有真实隐私后果**的一处）：

```
admin   → 直接放行（spec §2.1 全部读权限）
patient → appt.user_id   必须等于自己
doctor  → appt.doctor_id 必须等于本人 doctors.id   ← 不是 users.id！
```

### 2 · 审计资源名：`/admin/*` 的三个端点全塌成 `admin`

`Describe()` 直接取路径第 3 段作为资源名，而 `/api/v1/admin/*` 的第 3 段是
**权限域而不是资源** —— 于是三个管理端点的 `resource` 全变成 `admin`，**按资源聚合时混成一条**。
修复：`scopeSegments` 跳过权限域。

> ⚠️ **这个 bug 曾被两个测试同时固化**：`audit_test.go` 断言 resource 是 `admin`，
> `http_test.go` 也查 `resource = 'admin'`。**测试全绿，问题却一直在。**
> 教训：**测试里出现"当前实现恰好返回的值"时，要回代码确认那是不是正确值。**

### 3 · `audit.truncate` 按字节硬切

UA 含中文时切出**非法 UTF-8**，utf8mb4 列在严格模式下直接报错 ——
**整批 100 条审计一起插入失败**。修复：回退到字符边界。

> **这个缺陷是 fuzz 抓出来的，而且它暴露了更深的一层**：
> 修完字符边界后，`FuzzTruncate` 仍然报失败 —— 因为**输入本身就是非法 UTF-8** 时，
> "回退到字符边界"依然会原样返回非法字节。最终修法是加 `strings.ToValidUTF8`。
> 另外 fuzz 还发现 `max <= 0` 时会 panic（切片越界）。
>
> **一次 fuzz 命中，牵出两个真实缺陷 + 一个补丁不完整。**

### 4 · `SyncDeps.Concurrency` 与一批"无人使用的通用性"

| 类别 | 项 |
|---|---|
| 从不设置的旋钮 | `SyncDeps.Concurrency`（spec 定死 5，生产从不设） |
| 无人使用的返回值 | `UpsertDoctor` 的医生 ID、`UpsertSchedule` 的 `created` |
| 不可达分支 | `runPhase`/`run` 的 errgroup 错误通道 |
| 死函数 | `mockhis.Dataset.Summary`、`SyncLogRepo.LastFinished`、`MedicalRecordRepo.FindByID` |
| 从不写入的字段 | `sync_logs.trace_id`（**改为真正写入**）、`audit_logs.detail`（**连列删除**） |

**通用判据（已上升为规则）：抽出或保留之前问一句 —— "删掉它会怎样？"
如果答案是"什么都不会发生"，就删。**

### 5 · 注释与实现相反

`conversation.List` 的注释说"**跳过**损坏条目"，代码其实是**整体失败**。
保留"失败"行为并改正注释，说明为什么不静默跳过（静默跳过会让对话历史出现空洞，
而模型看到缺失的上下文会给出更差的回复）。

---

## 五、反向验证记录

| 被测性质 | 反向验证方式 | 结果 |
|---|---|---|
| **审计不存请求体** | 类型层面保证（`Entry` 无该字段） | ✅ 编译期 |
| **同步不重置 `remaining`** | `TestSync_DoesNotResetRemaining` | ✅ |
| 同步日期滚动到未来 | `TestSync_ScheduleDatesAreFresh` | ✅ |
| 医生口令不能人人相同 | `TestSync_DoctorPasswordsAreUniqueAndHashed` | ✅ |
| 中文编码往返 | `TestSync_ChineseRoundTrips` | ✅ |
| **非接诊医生拿不到报告** | `test/integration/doctor_test.go`：非接诊医生 → **40302**，**且其接诊列表中看不到该患者的预约**（两个端点交叉验证，避免只修一个漏一个） | ✅ |
| **`truncate` 不产生非法 UTF-8** | `FuzzTruncate` | **抓出 2 个真实缺陷** ✅ |
| 脱敏绝不泄漏原值 | `FuzzMaskNeverLeaksOriginal`（脱敏属 M2，但审计路径同样使用） | **抓出 1 个真实缺陷** ✅ |
| HTTP 层的越权与信封 | `test/integration/` 60 个用例，复用 `app.Build()` 的真实装配 | ✅ |

**新增用例数（医生工作台补全这一轮）：单元 +18、集成 +11。**
两处缺口都做了交叉验证 —— **只修一个漏一个**是这类改动最常见的收尾错误。

---

## 六、遗留与未验证

| 项 | 说明 |
|---|---|
| **软删除后的"复活"逻辑未实现** | `database-review.md` §4.2 曾把"由 service 复活 `deleted_at = NULL`"写成已完成，**实际上代码里没有任何 `Unscoped()`/`deleted_at = NULL` 调用**。当前没有任何入口会软删这些表，所以**缺口不可达**；但**加管理端删除功能之前必须先实现复活**，否则 HIS 同步会永久失败 |
| 审计的**零丢失模式** | 当前是有界缓冲 + 丢弃。若合规要求零丢失，把 `Record` 改成直接落库即可，其余代码不变 —— 已在 `MODULES.md` §6.2 写明改法 |
| `audit_logs` 分区 / 归档 | 写入最频繁的表，长期运行需要 TTL 或分区 |
| Q19 的隐含假设 | "审计日志按时间范围查询走 PRIMARY"依赖 `created_at` 与 `id` 同向增长。**引入回填或乱序写入会打破它** |
| HIS 同步**不失效科室医生缓存** | 本轮核实：`DepartmentDoctorsCacheKeys` 只在 `cmd/seed` 与测试里被调用，`service/sync.go` 对 cache/redis **零引用**。同步更新医生/关联后，科室医生列表**最坏 5 分钟内是旧的**（由 TTL 兜底）。属已知取舍（`MODULES.md` §3.6 已写"写路径要调它失效，别只依赖 TTL"），但同步路径尚未调用 |
| 真实 HIS 适配器 | `his` 契约已就位，但只有 `mockhis` 一个适配器。契约是否真的能容纳真实 HIS（分页、增量、删除语义）**未被验证** |

---

## 交叉引用

- 结构与决策：`docs/MODULES.md` §6（含审计取舍、三阶段同步、医生工作台补全）
- **性能实测**：`docs/sql-optimization.md` 的 Q8–Q11、Q17–Q20
- 缺陷账：`docs/FIXES.md` H2（队列越权，与报告越权同类）、H3
- 原始记录：`docs/HANDOVER.md` §2.7-B/C/E/F、§2.9-C
- 易错点：`docs/PITFALLS.md` §9（事实来源）、§10（重复与抽象）
- 接手规则：`AGENTS.md` §5.8（LEFT JOIN + `IS NOT NULL`）

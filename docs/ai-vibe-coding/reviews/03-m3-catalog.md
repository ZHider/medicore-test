# 03 · M3 —— 目录查询与游标分页（含 Redis 缓存防击穿）

| | |
|---|---|
| **对应提交** | `e94b93a`；缺陷修正在 `6b23e57`（软删除条件）、`430c77d`（缓存技术债）、`edffa4e`（EXPLAIN 实测） |
| **规模** | 9 文件 / 898 行（`MODULES.md` §3） |
| **涉及包** | `model/pagination` `repository/cache` `repository/doctor` `repository/schedule` `repository/department` `service/catalog` `handler/catalog` |
| **本篇性质** | **事后整理**。来源：`MODULES.md` §3、`HANDOVER.md` §2.2、`docs/sql-optimization.md`、`docs/benchmark/`；§三⑥ 为本次新补查 |
| **结论** | 交付达成；本模块产出了**全项目最值得记住的一条并发陷阱**（缓存 context 传染，见 §四） |

---

## 一、需求拆解

| spec 出处 | 要求 | 落地 |
|---|---|---|
| §6.1 | `GET /departments/:id/doctors`（**Redis 缓存**） | `DoctorRepo.ListByDepartment` → `repository.Load` |
| §6.1 | 科室/医生/排班查询 | `CatalogService` 5 个只读方法 |
| §5.2 | **游标分页强制**，禁止 OFFSET，`size` 有上限 | `model.CursorPage` + `NormalizePageSize` |
| §5.1 / 决策 | 医生与科室**多对多** | `doctor_departments` |

**spec 有缺陷需修正的部分**：§5.1 原 DDL 的 `schedules` 表 `KEY idx_date_status (sched_date, status)`
**引用了不存在的列** —— **建表必然失败**。已补齐 `status` 列。

---

## 二、方案选型

| 决策点 | 被否决的方案 | 选定 | 理由 |
|---|---|---|---|
| 分页方式 | OFFSET | **游标 `id < cursor` + 查 `limit+1` 条** | spec §5.2 强制；实测深页差 38×~4300×（口径见 `sql-optimization.md` §0） |
| 是否需要 `COUNT(*)` | 查总数给前端 | **不查** —— 取 `limit+1` 条判断有无下一页 | `COUNT` 在大表上代价与分页深度无关但绝对值高，而前端只需要"有没有下一页" |
| 手写 JOIN 取医生姓名 | INNER JOIN | **LEFT JOIN** | 医生即便已软删，历史预约仍应显示当时姓名而非空白 |
| 软删除过滤 | 各处手写 | **`Model(&Struct{})` 让 GORM 自动加**；手写 `Table(...)` 时**必须显式写** | GORM 的作用域只在 `Model` 生效（**刻意偏离 spec 第 3 处**：PO 用 `gorm.DeletedAt`） |
| `consult_fee` 类型 | `float64` | **`string`** | 金额用浮点有舍入误差；DECIMAL 可直接扫进 string，JSON 输出字符串，两端都不丢精度 |
| 科室不存在时 | 返回空列表 | **404** | 否则客户端无法区分"科室不存在"与"该科室暂无医生" |
| 日期区间反向 (`from > to`) | 静默返回空 | **400** | 否则用户以为"没有排班"，实际是参数写反 |

---

## 三、§10.2 检查清单

### ① 分层与架构

- 缓存逻辑下沉到 `repository/cache.go`，**`service` 只知道"调一个方法拿数据"**，不知道有缓存。
- 写路径失效用导出的 `DepartmentDoctorsCacheKeys()`，**而不是让调用方复制键前缀**
  （复制键前缀意味着前缀一改就静默失效）。
- `service/catalog.go` 只做编排与 404/400 的语义判定，无 SQL。

**判定：通过。**

### ② 错误与并发

| 检查项 | 情况 |
|---|---|
| 错误包装 | `repository` 把 `gorm.ErrRecordNotFound` 转 `ErrNotFound`，**GORM 细节不泄漏到上层** |
| goroutine 退出路径 | `singleflight` 的跟随者会等 leader；leader 的 context 若被取消会**传染给所有跟随者** —— 这是本模块最大的一处坑，见 §四第 1 条 |
| 共享状态 | 缓存回源用 `singleflight.Group.Do` 合并，无自有共享状态 |

**判定：曾存在一个真实的并发语义缺陷（context 传染），已修并有反向验证。**

### ③ 性能与 SQL

本模块是 `docs/sql-optimization.md` 覆盖最密集的地方。**本节数字全部来自该文档的实测**：

| 查询 | 选用索引 | type | 实际读取行数 | 热态耗时 |
|---|---|---|---|---|
| Q12 科室医生列表 | `idx_dept_doctor` + filesort | ref | 13 | 0.314 ms |
| Q13 医生详情 | `PRIMARY` | **const** | ~1 | — |
| Q14 科室排班（科室+日期） | `idx_dept_date` | ref | 20 | 0.042 ms |
| Q15 医生排班（日期区间） | `uk_doctor_dept_date_slot` + filesort | ref | 60 | 0.060 ms |
| Q2 患者预约列表·深页 | `idx_user_cursor` | range | 1 | 0.017 ms |

**两项判定**：

- **`idx_dept_date (department_id, sched_date, status)` 是这份 schema 里最漂亮的一条索引**：
  `key_len=12` 表示**三个条件全部进了索引查找**，一个都没退化成过滤；
  `(reverse)` 还顺带满足了 `ORDER BY id DESC`（InnoDB 二级索引尾部隐含主键），连 filesort 都省了。
  这是"等值列在前、范围列在后"的正面样板。
- **Q15 的索引只吃到前缀**：`uk_doctor_dept_date_slot (doctor_id, department_id, sched_date, time_slot)`
  里 `department_id` 夹在中间且无条件，B+ 树在 `doctor_id` 层就"断"了，
  `sched_date` 的范围只能靠 **ICP** 过滤，`ORDER BY id` 只能 filesort。
  **实测 60 行 / 0.060 ms，判定不改**（每加一条索引都要为 HIS 全量同步的每次 UPSERT 付费），
  触发条件已写明：单医生排班过万或该查询 P99 > 5ms 时再加。

**缓存命中不是靠"Redis 里有键"来验证的**（那只能说明写进去了），
而是用**哨兵值证伪**：往 `cache:dept_doctors:2` 写一个假医生 → 接口返回了哨兵
→ 证明真的在读缓存；删键后恢复真实数据 → 证明控制组有效。

### ④ KISS 原则

| 出现的"多余" | 判定 |
|---|---|
| `model.CursorPage[T]` 泛型 + `NonNil()` | **必要** —— 统一分页语义与 `[]` 而非 `null` 的序列化 |
| `repository.Cache` 独立封装 | **必要** —— 防击穿与降级只写一次 |
| `Cache` 的 `loader` 参数是函数 | **必要** —— 让缓存与具体查询解耦 |
| `applyCursor(q, cursor)` 小工具 | **必要** —— 首页不加条件、后续页加 `id < cursor`，避免各处重复判断 |

### ⑤ 基础卫生

| 检查项 | 情况 |
|---|---|
| 输入校验 | `size` 经 `NormalizePageSize` 收敛到 `[1, 100]`（有 fuzz 目标守护：**不允许超过上限**，否则一次请求就能拉爆内存）；`cursor` 解析失败给 400 |
| 敏感数据脱敏 | 目录类 DTO **不含 phone / id_card**（本轮三角色复查再次确认），无需脱敏 |
| 硬编码 | 缓存 TTL 是常量 `deptDoctorsTTL = 5min`，有注释说明为何允许短暂脏读；无密钥 |

### ⑥ AI 幻觉

本次新补查（方法见 `README.md`）。M3 涉及的高风险 API：

| API | 存在性 | 语义核对结果 |
|---|---|---|
| `singleflight.Group.Do(key, fn)` | ✅ 存在 | **语义坑**：跟随者**共享 leader 的 context**。文档不会把这个风险标红，是本模块踩过才知道的 |
| `singleflight.Group.Do` 的返回值顺序 | ✅ | 是 `(v, err, shared)`，`shared` 表示"结果是别人算的"。本项目未使用 `shared`，无误用 |
| `gorm.DB.Model(&S{})` 的软删除作用域 | ✅ | **语义坑**：`Table("...")` 时**完全不生效** —— 这是本模块与 M6 共同踩过的坑 |
| `gorm.DB.Scan(&items)` 到 DTO | ✅ 存在 | `Scan` 不做 `First` 那套主键推断，适合投影查询，用法正确 |
| `clause.Locking{Strength: "UPDATE"}` | ✅ 存在 | 见 M4 篇 |

**结论：0 个幻觉符号；2 个语义坑（singleflight context 传染、GORM 软删除作用域），两个都已被踩到并记录成硬规则。**

---

## 四、本模块暴露的真实缺陷

### 1（最有价值）· 缓存回源的 context 传染

`singleflight` 的**跟随者共享 leader 的 context**。
leader 的请求被取消（用户关页面、连接超时）时，
**所有健康的跟随者会一起收到 `context.Canceled`** —— 拿到一个本不该属于它们的错误。

修复：回源用 `context.WithoutCancel(ctx)` + 独立超时上界。

**已做反向验证**：把 `loader(loadCtx)` 改回 `loader(ctx)`，
`TestCacheLoad_LoaderContextSurvivesCallerCancellation` 与
`TestCacheLoad_LeaderCancellationDoesNotFailFollower` **都失败**
（`回源 context 已被取消: context canceled`）。

> 这条已上升为项目硬规则（`AGENTS.md` §2.3）。
> **它的隐蔽性在于：单请求测试永远发现不了** —— 必须有"leader 被取消 + 跟随者仍在等"这个并发场景。

### 2 · 手写 JOIN 漏了软删除条件

已软删除的医生照常出现在列表里。**这是 GORM 最著名的陷阱，也是本项目最值得记的一条**
（`AGENTS.md` §2.5）。修法：手写 JOIN 时逐个检查每张表的 `deleted_at IS NULL`。

> 注意它与"决策"里的第 4 条是**同一枚硬币的两面**：
> 用 `Model(&Struct{})` 就自动带上，用 `Table(...)` 就得手写。
> 项目选择让 PO 带 `gorm.DeletedAt`（偏离 spec 第 3 处）正是为了**让默认路径安全**。

### 3 · 小数据量下的 EXPLAIN 误导

**2 行数据的表上**，游标分页查询显示 `Using filesort` —— 让人一度以为索引设计有问题。
灌到 **3 万行**后计划自动变优。

> **结论：索引决策必须用有代表性的数据量验证。**
> 这条直接催生了后来的 `docs/sql-optimization.md` —— 那份文档的第一节就写明
> "在几十行的开发库上跑 EXPLAIN，优化器一律全表扫描，那不是发现，是把没有数据误读成结论"。

### 4 · 部分索引的"看起来没用"

`docs/sql-optimization.md` §7 记录：试图用 `sys.schema_unused_indexes` 判断哪些索引冗余，
结果**与 EXPLAIN 直接矛盾**（把明说用到的索引记为 0 次使用），
**因此没有据此得出任何删索引的结论**。这条也属于本模块的性能域。

---

## 五、反向验证记录

| 被测性质 | 反向验证方式 | 结果 |
|---|---|---|
| **缓存回源脱离调用方取消** | 改回 `loader(ctx)` | 两个用例**变红** ✅ |
| `size` 上限 | `FuzzNormalizePageSize`：断言结果**永不超过 `MaxPageSize`** | ✅（性质可被证伪，非固定输入输出） |
| `cursor` 解析 | `model/pagination_test.go` + `FuzzParseCursor` | ✅ |
| 缓存命中 | **哨兵值证伪**（写入假医生 → 看接口是否返回它） | ✅ 证明真的读缓存 |
| 索引不可替代 | `IGNORE INDEX` + `EXPLAIN ANALYZE` 前后对照（见 `sql-optimization.md` §5） | ✅ 拿掉 `idx_dept_date` 后退到 `idx_date_status`，读 20 行 → 400 行（7×） |

> **哨兵值这条值得单独学**：验证"缓存生效"最直觉的做法是查 Redis 里有没有键，
> 但那只能证明**写入**生效，不能证明**读取**走了缓存。
> 把脏数据塞进去、看它有没有被读出来，才是能证伪的做法。

---

## 六、遗留与未验证

| 项 | 说明 |
|---|---|
| `schedules` 缺 `(doctor_id, sched_date, status)` | Q15 只用到索引前缀（60 行 / 0.060 ms）。**判定不改**，触发条件已写明（见 §三③） |
| `repository` 缓存命中路径的**直测**覆盖率 0% | 缓存逻辑由 `repository/cache_test.go` 覆盖，但"缓存命中时不回源"这条路径在直测里为 0%；集成口径整体 67.5% |
| 目录只读查询的直测 0% | 同上。风险由 `sql-optimization.md` 的实测补上：Q12–Q15 都有执行计划证据 |
| 缓存与写路径的一致性 | 写路径靠 `DepartmentDoctorsCacheKeys` 主动失效，**但当前没有写入目录数据的入口**（HIS 同步不失效该缓存）—— 见下 |
| **HIS 同步不失效科室医生缓存** | `DoctorRepo.ListByDepartment` 缓存 5 分钟。HIS 同步更新医生/关联后，**最坏 5 分钟内的列表是旧的**。当前由 TTL 兜底（`MODULES.md` §3.6 的注意事项里写了"写路径要调 `DepartmentDoctorsCacheKeys` 失效，别只依赖 TTL"），但**同步路径尚未调用它**。属于已知取舍，不是疏漏 |

---

## 交叉引用

- 结构与决策：`docs/MODULES.md` §3
- **性能实测**：`docs/sql-optimization.md` 的 Q2 / Q12–Q15 与 §4.2（`idx_dept_date` 正面样板）
- 原始错误记录：`docs/HANDOVER.md` §2.2（spec 缺陷 1）
- 接手规则：`AGENTS.md` §2.3（缓存 context）、§2.5（软删除 JOIN）、§3.4

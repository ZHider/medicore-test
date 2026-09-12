# MediCore · SQL 与索引优化实测

> **口径声明**：本文档的每一条结论都来自 `EXPLAIN` / `EXPLAIN ANALYZE` 在**真实行数**上的实测输出，
> 没有一条是"照着 schema 推断出来的"。
> 数据来源：`backend/scripts/sqlopt-seed.sql` 灌入的一次性数据集（见 §1）。
> 复现命令、原始输出、以及一条**失败的方法学尝试**都在文中如实记录。

---

## 0. 本文档回答什么

| 问题 | 在哪一节 |
|---|---|
| 每条业务查询实际走了哪个索引、读了多少行、多快？ | §3 |
| 哪些查询的索引不完美？严重吗？该不该动 schema？ | §4 |
| 这些索引**真的有用吗**（拿掉会怎样）？ | §5 |
| 写放大有多大？ | §6 |
| 一次失败的方法学尝试 | §7 |

**与 `docs/benchmark/` 的关系**：两者都是性能证据，但**口径不同，数字不可互相替代**。

| | `docs/benchmark/` | 本文档 |
|---|---|---|
| 数据量 | `audit_logs` 200,000 行 | `audit_logs` 300,000 行 |
| 路径 | 经 GORM + 连接池（应用真实路径） | `mysql` 客户端直连（隔离掉驱动与网络） |
| 冷热 | 基准内多轮 | 明确区分冷态首跑与热态重跑 |
| 分页对照 | 1.01 ms vs 38.81 ms（**38 倍**） | 0.022 ms vs 94.2 ms（**约 4300 倍**） |

两个数字都对，**都不该拿去覆盖对方**。跨环境可迁移的结论只有一条：
**游标分页是常数代价，OFFSET 是线性代价**。倍数随数据量、OFFSET 深度、冷热态而变。

---

## 1. 复现步骤

EXPLAIN 的结论**只在有真实行数与新鲜统计信息时才成立**。在几十行的开发库上跑 EXPLAIN，
优化器一律全表扫描 —— 那不是"发现索引没用"，而是把"没有数据"误读成了结论。

```bash
# 1) 依赖服务
nub run deps:up

# 2) 重建一次性测试库并应用真实 DDL
docker exec -i -e MYSQL_PWD=medicore123 medicore-dev-mysql \
  mysql -umedicore -e "DROP DATABASE IF EXISTS medicore_test;
                       CREATE DATABASE medicore_test CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;"
docker exec -i -e MYSQL_PWD=medicore123 medicore-dev-mysql \
  mysql -umedicore medicore_test < docs/schema.sql

# 3) 灌入真实量级数据（本机实测 25 秒）
docker exec -i -e MYSQL_PWD=medicore123 medicore-dev-mysql \
  mysql -umedicore medicore_test < backend/scripts/sqlopt-seed.sql
```

**为什么用 `medicore_test` 而不是开发库**：`medicore` 账号只对这个库有全部权限
（`deploy/mysql/init-test-db.sql` 授权的），而且集成测试本来就每轮 `DROP` 重建它——
灌进这里不需要任何额外授权，也不会碰开发库的演示数据。**代价**：跑一次集成测试就会清掉它，
要重看先重跑第 2、3 步。

> ⚠️ **三步一步都不能省，顺序也不能变。** 目标库必须是**全新**的：
> `schema.sql` 里有不带 id 的演示科室行（重复建表报 `Table 'departments' already exists`），
> 而播种脚本第 1 步要 `DELETE FROM departments` 腾出 id 1..20
> （在已有业务数据的库上会被外键拦住）。
> 这两个坑我都亲自踩过一次，所以写在这里。

**数据是确定性的**：除 `created_at` 以 `NOW()` 为基准外全部由行号推导，
实测跑两次行数完全一致（`departments 20 / appointments 240000 / audit_logs 300000`）。

### 1.1 播种脚本的三处刻意设计

不是随手写的，改动前请先理解（细节见脚本内注释）：

1. **构造性唯一，而不是随机取值。** `appointments` 的 `uk_active` 是 `(user_id, schedule_id)`
   的部分唯一索引（生成列）。随机生成必然撞键（生日悖论），撞了要重试、结果还不确定。
   脚本用 `1 + ((u*5 + b) mod 12000)` 这个**可证明两两不同**的映射，一次插入成功、分布均匀。
2. **不用递归 CTE。** `cte_max_recursion_depth` 默认 1000，且是**会话级**变量——
   `SET SESSION` 之后下一条语句可能落到连接池里的另一条连接，于是时好时坏。
   改用普通表 `sqlopt_digits` 的 6 路交叉连接（**不能是 TEMPORARY**：
   MySQL 不允许同一查询里多次引用临时表，会报 `ERROR 1137: Can't reopen table`）。
3. **`queue_no` 用 `ROW_NUMBER() OVER (PARTITION BY schedule_id)`。**
   `uk_sched_queue (schedule_id, queue_no)` 要求排班内序号不重复，窗口函数构造性满足约束。

---

## 2. 数据量级与体积

实测行数（脚本末尾会自己打印，避免"以为灌了 30 万其实是 3 万"）：

| 表 | 行数 | 表 | 行数 |
|---|---|---|---|
| `users` | 48,200（200 医生 + 48,000 患者） | `medical_records` | 160,000 |
| `schedules` | 12,000（200 医生 × 30 天 × 2 半天） | `prescription_items` | 320,000 |
| `appointments` | 240,000（48,000 患者 × 5 次） | `audit_logs` | 300,000 |
| `ai_pre_reports` | 40,000 | `sync_logs` | 500 |
| `departments` | 20 | `doctors` | 200 |

业务分布：预约 `confirmed` 70% / `cancelled` 15% / `expired` 10% / `pending` 5%；
`pending` 的 `expire_at` 落在过去一小时（正是 cron 要扫的"超时未确认"）；
`audit_logs` 覆盖最近 90 天。

体积（`information_schema.tables`）：

| 表 | 数据 | 索引 | 索引/数据 |
|---|---|---|---|
| `appointments` | 57.6 MB | **97.3 MB** | **1.69×** |
| `audit_logs` | 53.6 MB | 46.2 MB | 0.86× |
| `medical_records` | 44.6 MB | 17.5 MB | 0.39× |
| `prescription_items` | 38.6 MB | 9.5 MB | 0.25× |

`appointments` 的索引比数据本身还大 —— 它身上有 **11 条索引**，详见 §6。

---

## 3. 查询清单与执行计划总表

25 条查询全部抄自 `backend/internal/repository/*.go`（含 GORM 自动追加的 `deleted_at IS NULL`），
只把参数替换成具体常量。分页 `size=20`（`limit+1=21`）、cron 批 200、对账批 500。

**约定**：`实际读取行数` 与 `耗时` 来自 `EXPLAIN ANALYZE`（热态第二跑）；
标 `~` 的是优化器估计值 —— 即只跑了 `EXPLAIN`、没做实测，**不要当结论用**。

| # | 查询 | 选用索引 | type | 实际读取行数 | 热态耗时 |
|---|---|---|---|---|---|
| Q1 | 患者预约列表·首页 | `uk_user_idem` + filesort | ref | 5 | 0.088 ms |
| Q2 | 患者预约列表·深页 `id<100` | `idx_user_cursor` | range | 1 | 0.017 ms |
| Q3 | 医生接诊列表 | `idx_doctor_cursor` | ref | 21 | 0.595 ms |
| Q4 | 医生接诊列表 + 状态筛选 | `idx_doctor_cursor` | ref | ~1200 | — |
| Q5 | 管理员全部预约 + 就诊日筛选 | `PRIMARY`（反向） | index | **2,341** | 0.936 ms |
| Q6 | cron 扫描超时 pending | `PRIMARY`（正向） | index | **4,000** | 1.48 ms |
| Q7 | 候诊队列 | `uk_sched_queue` | range | ~20 | — |
| Q8 | 病历列表（患者） | `idx_patient_cursor` | ref | ~5 | — |
| Q9 | 病历列表（管理员全量） | `PRIMARY`（反向） | index | 21 | 0.183 ms |
| Q10 | 病历详情 | `uk_appointment` | **const** | ~1 | — |
| Q11 | 处方明细 | `idx_record` | ref | ~2 | — |
| Q12 | 科室医生列表 | `idx_dept_doctor` + filesort | ref | 13 | 0.314 ms |
| Q13 | 医生详情 | `PRIMARY` | **const** | ~1 | — |
| Q14 | 科室排班（科室+日期） | `idx_dept_date` | ref | 20 | 0.042 ms |
| Q15 | 医生排班（日期区间） | `uk_doctor_dept_date_slot` + filesort | ref | 60 | 0.060 ms |
| Q16 | 号源对账·分批扫描 | `PRIMARY` | range | 500 | 0.293 ms |
| Q17 | 审计日志·游标（无筛选） | `PRIMARY`（反向） | index | 21 | **0.022 ms** |
| Q18 | 审计日志·按用户 | `idx_user_cursor` | ref | 7 | 0.039 ms |
| Q19 | 审计日志·按时间范围 | `PRIMARY`（反向） | index | 1,830 | 0.772 ms |
| Q20 | 同步日志 | `PRIMARY`（反向） | range | 21 | 0.020 ms |
| Q21 | 登录·按手机号摘要 | `uk_phone_hash` | **const** | ~1 | — |
| Q22 | 由用户解析医生档案 ID | `uk_user` | **const** | ~1 | — |
| Q23 | 预问诊报告·按预约 | `uk_appointment` | **const** | ~1 | — |
| Q24 | 抢号·号源扣减 `UPDATE` | `PRIMARY` | range | ~1 | — |
| Q25 | **对照组**：OFFSET 深翻 | `PRIMARY`（反向） | index | **300,000** | **94.2 ms** |

**一句话**：25 条里 24 条都命中了一条合适的索引，没有一条是"全表扫描被迫"的。
唯一逐行读完整表的，是 §3.1 那个**刻意用来对照的 OFFSET 写法** —— 而项目里根本不存在这种写法。

### 3.1 游标 vs OFFSET：这份文档最该记住的一组数字

```
Q17  游标分页（项目实际写法）   Index scan on a using PRIMARY (reverse)   rows=21        0.022 ms
Q25  OFFSET 299980（对照）      Index scan on audit_logs using PRIMARY (reverse) rows=300000  94.2 ms
```

两者用的是**同一条索引、同一个扫描方向**，差别只在"扫多少行"：
游标让它扫 21 行就停，OFFSET 必须**先吐出前 299,980 行再丢掉**。

- 冷态首跑：Q17 `0.0246 ms`、Q25 `162 ms`（缓冲池未预热，绝对值为环境相关的量，**看比例不看绝对值**）。
- 这就是 spec §5.2 第 1 条"游标分页强制"的全部理由。它不是风格偏好，是 3 个数量级的差别。

---

## 4. 逐条判定：哪些索引不完美

前 24 条都能用，但**不是每一条都"用得漂亮"**。下面把需要解释的挑出来，逐条给结论。

### 4.1 唯一键点查全部落在 `const`（Q10 / Q13 / Q21 / Q22 / Q23）

`EXPLAIN` 的 `type=const` 意味着**优化期就把结果算出来了**，执行期不碰 B+ 树。
登录（`uk_phone_hash`）、病历详情（`uk_appointment`）、报告（`uk_appointment`）、
医生档案解析（`uk_user`）全在这一档 —— 都是等值命中唯一键，没有改进空间。

### 4.2 `idx_dept_date` 是这套 schema 里最漂亮的一条索引（Q14）

```
Index lookup on schedules using idx_dept_date
  (department_id=1, sched_date=curdate(), status='open')  (reverse)
```

`key_len=12` = `department_id(8) + sched_date(3) + status(1)` —— **三个条件全部进了索引查找，
一个都没退化成过滤**。`(reverse)` 还顺带满足了 `ORDER BY id DESC`（InnoDB 二级索引尾部隐含主键），
所以连 filesort 都省了。读 20 行、0.042 ms。

这正是 `idx_dept_date (department_id, sched_date, status)` 的列顺序被设计成这样的回报：
把**等值列放在范围列之前**，等值列越多，能"走进去"的层数越多。

### 4.3 医生排班区间：索引只吃到前缀，剩下一截靠 ICP（Q15）—— 记录，但**不改**

```sql
WHERE doctor_id = 1 AND sched_date BETWEEN CURDATE() AND CURDATE()+7 DAY AND status = 'open'
ORDER BY id DESC
```

```
Index lookup on schedules using uk_doctor_dept_date_slot (doctor_id=1),
  with index condition: (sched_date between ...)      ← ICP，不是索引查找
Sort: schedules.id DESC, limit input to 21
```

原因在看唯一键的**列顺序**：`uk_doctor_dept_date_slot (doctor_id, department_id, sched_date, time_slot)`。
`department_id` 夹在 `doctor_id` 和 `sched_date` 中间且**没有条件**，
所以 B+ 树在 `doctor_id` 这一层就"断"了 —— `sched_date` 的范围只能靠
**索引条件下推（ICP）在引擎层过滤**，`ORDER BY id` 也只能靠 filesort。

**代价实测**：读 60 行、排 16 行、0.060 ms。

**要不要加一条 `(doctor_id, sched_date, status)`？——不加。** 理由是算过的：

- 单医生的排班量 = 年数 × 365 × 2。即使门诊排到 **5 年**也只有 ~3,650 行，
  排序仍是微秒级；要到"单医生排班过万"才算病态。
- `schedules` 每多一条索引，每次 HIS 同步的 UPSERT 就多维护一棵 B+ 树，
  而 HIS 同步是全量遍历（§7.3）。**为 0.06 ms 加一条写路径成本，不划算。**

**触发条件**（将来复现时据此判断）：`ListByDoctorRange` 的 P99 > 5 ms，或单医生排班量过万。

### 4.4 cron 扫描**没有**用 `idx_status_expire`（Q6）—— 记下风险与阈值

```sql
WHERE status='pending' AND expire_at < NOW() ORDER BY id ASC LIMIT 200
```

```
possible_keys: idx_status_expire
key:           PRIMARY                    ← 没用那条为它建的索引
Index scan on appointments using PRIMARY   rows=4000  → 0.0518..11.7 ms（冷）/ 1.48 ms（热）
```

**为什么没用**：`idx_status_expire (status, expire_at)` 能服务条件，
但**服务不了 `ORDER BY id`**。优化器在"用索引过滤 + filesort"和
"按主键顺序扫、命中即取"之间选了后者 —— 因为主键扫描天然有序，凑够 200 条就能停。

**量化风险**：实测为了拿 200 条读了 **4,000 行**，即约 20 行换 1 条
（数据里 pending 占比 5%）。代价 ≈ `LIMIT ÷ pending占比`：

| pending 占比 | 需要扫描的行数 |
|---|---|
| 5%（当前） | 4,000 |
| 1% | 20,000 |
| 0.1% | **200,000** |

**结论：现在的代价可控（1.48 ms），但它是随"数据分布"而非"数据量"恶化的** ——
极端情况下（例如引入自动确认把 pending 清零）会退化成近全表扫描。

**改法（若将来触发）**：把 `ORDER BY id ASC` 改成 `ORDER BY expire_at ASC`，
`idx_status_expire (status, expire_at)` 就能变成 range 扫描，精确读 200 行、无 filesort。
顺序语义也合理 —— "超时最久的先释放"。**本次未改**：当前代价可接受，
而改排序会改变 cron 的处理次序，属于需要单独验证的行为变更。

### 4.5 管理员全部预约 + 就诊日筛选：读 2,341 行拿 21 条（Q5）

```
possible_keys: idx_date_status
key:           PRIMARY（Backward index scan）      actual rows=2341 → 21
```

`idx_date_status (sched_date, status)` 被评估了但没被选中，优化器改按主键倒序扫。
**这一条能成立，依赖于 `sched_date` 与 `id` 是同向增长的**（新排班的 id 大、日期也靠后）。
本项目里这个前提成立（排班按日期顺序批量生成），所以倒着扫很快碰到今天的排班。

**值得留意的是估计偏差**：优化器估 `rows=352`，实际读了 **2,341 行（6.6 倍）**。
这也是本文档坚持用 `EXPLAIN ANALYZE` 而不是只看 `EXPLAIN` 的原因 ——
`rows` 是估算，**不能当结论**。

### 4.6 首页没走 `idx_user_cursor`（Q1）—— 不是问题，但要知道

```
possible_keys: uk_user_idem, idx_user_cursor
key:           uk_user_idem        Extra: Using where; Using filesort
```

两条索引都能做 `user_id` 等值查找（估计都是 5 行），优化器平局时选了 `uk_user_idem`，
代价是多了个 filesort。**5 行数据上这不是问题**（实测 0.088 ms）。

**深页就不同了**：`AND id < 100` 时它切到了 `idx_user_cursor`（`range`，无 filesort，读 1 行）——
因为此时 `(user_id, id DESC)` 的"有序"才体现出价值。

**结论**：不调整。但这条提醒了一个真实规律：**只有一条可用索引时它才会被选中，
有多条平局时优化器不保证挑到"带排序"的那条**。若将来出现"单用户预约量大 + 首页慢"，
第一反应应该是确认它走的是哪条索引。

### 4.7 科室医生列表有 `Using temporary; Using filesort`（Q12）—— 13 行，不值得动

`ORDER BY dd.is_primary DESC, dd.sort_order ASC, d.id ASC` 跨越了 `doctor_departments`
和 `doctors` 两张表，`idx_dept_doctor (department_id, doctor_id)` 服务不了。
但实测**只有 13 行**参与排序、0.314 ms。科室医生列表还是全站唯一挂了 Redis 缓存的接口
（`deptDoctorsTTL = 5min`），实际请求大多不落到 MySQL。**结论：不动。**

### 4.8 手写 JOIN 的软删除条件有没有拖累计划？

`AGENTS.md` §2.5 要求手写 JOIN 显式带 `deleted_at IS NULL`。这确实会阻止某些"覆盖索引"路径，
但从 §3 的表看，**它没有让任何一条查询失去索引** —— 软删除列不在任何索引里，
所以它只是引擎层的行过滤（`Using where`）。

**回报是实打实的**：Q3 的 `(mr.id IS NOT NULL) AS has_record` 必须是 `LEFT JOIN`
且判定列必须是 `mr.id`（见 §5.8）—— 这类"语义正确"的代价是 0.595 ms 读 21 行。

---

## 5. 反向验证：这些索引**真的有用吗**

一个索引"被 EXPLAIN 选中"不等于"它必不可少"。所以做了一组对照：
**模拟索引不存在，看同一条查询会退化成什么样。**

**方法**：用 `IGNORE INDEX (...)` 而不是 `DROP INDEX`。两个原因：
① `appointments.idx_doctor_cursor` **同时是外键 `fk_appt_doctor` 的支撑索引**，
`DROP` 会被 MySQL 直接拒绝：

```
ERROR 1553 (HY000): Cannot drop index 'idx_doctor_cursor': needed in a foreign key constraint
```

② 不动 schema，就不存在"验证完忘了还原"的风险。

| 查询 | 有索引 | 模拟无索引 | 倍数 |
|---|---|---|---|
| Q18 审计日志·按用户 | `idx_user_cursor`，读 **7** 行，0.039 ms | `PRIMARY` 反向**扫完 300,000 行**，**116 ms** | **≈ 3,000×** |
| Q3 医生接诊列表 | `idx_doctor_cursor`，读 **21** 行，0.595 ms | `PRIMARY` 反向扫 **192,840 行**，**346 ms** | **≈ 580×** |
| Q14 科室排班 | `idx_dept_date`，读 **20** 行，0.042 ms | 退到 `idx_date_status (sched_date,status)`，读 400 行，0.294 ms | 7× |

三条结论：

1. **前两条索引是不可替代的。** 少了它们不是"慢一点"，而是**把点查变成了全表扫描**。
2. **第三条展示了"降级"而非"崩溃"。** `schedules` 上还有一条部分覆盖的 `idx_date_status`，
   所以去掉 `idx_dept_date` 只是从"精确走三列"退到"走两列 + 过滤"，代价 7 倍但仍然快。
   **这解释了 §4.3 为什么可以不动 schema** —— 冗余索引的存在让局部设计不完美不至于变成故障。
3. **`IGNORE INDEX` 这条路径值得保留在工具箱里**：它是"这条索引值不值"的最低成本判据，
   比"删了再建"安全得多。

### 5.1 反向验证里最值得看的一处：优化器估错了 4 个数量级

```
Q18 有索引：  Index lookup on a using idx_user_cursor (user_id=201)  rows=7
Q18 无索引：  Index scan on a using PRIMARY (reverse)  (cost=10328 rows=21) (actual ... rows=300000)
                                                        ~~~~~~~~~~~~~          ~~~~~~~~~~~~~~~
                                                        估计 21 行             实际 30 万行
```

`EXPLAIN` 说"扫 21 行"，实际扫了 30 万行。**只看 `EXPLAIN` 会得出完全错误的成本判断**——
这也是本文档的实测数字全部来自 `EXPLAIN ANALYZE` 的原因。

---

## 6. 写放大：`appointments` 的 11 条索引

`appointments` 单表 **11 条索引**，索引体积（97.3 MB）是数据（57.6 MB）的 **1.69 倍**：

| 索引 | 列 | 服务谁 |
|---|---|---|
| `PRIMARY` | `id` | 全部游标分页的排序基准 |
| `uk_user_idem` | `user_id, idempotency_key` | 幂等重放（Q1 也顺手用了它） |
| `uk_active` | `active_uk`（生成列） | **进行中预约唯一**（"同一排班不能重复挂号"） |
| `uk_sched_queue` | `schedule_id, queue_no` | 候诊队列序号唯一（Q7） |
| `idx_user_cursor` | `user_id, id DESC` | 患者列表（Q2） |
| `idx_doctor_cursor` | `doctor_id, id DESC` | 医生列表（Q3）**兼 `fk_appt_doctor` 支撑索引** |
| `idx_status_expire` | `status, expire_at` | cron 超时扫描（Q6 未选中，见 §4.4） |
| `idx_sched_checkin` | `schedule_id, checked_in_at` | 签到队列排序 |
| `idx_schedule` | `schedule_id` | **兼 `fk_appt_schedule` 支撑索引** |
| `idx_date_status` | `sched_date, status` | 看板趋势统计（**当前 25 条查询无一条选中它**） |
| `fk_appt_dept` | `department_id` | **MySQL 为 `fk_appt_dept` 自动补的** |

两条值得记住的事实：

1. **外键会"占用"索引。** `appointments` 上 4 个外键：3 个复用了已有的业务索引
   （`idx_user_cursor` / `idx_doctor_cursor` / `idx_schedule`），只有 `fk_appt_dept`
   因为找不到以 `department_id` 打头的索引，被 MySQL **自动创建**了一条同名索引。
   **所以"我的 schema 里只有 10 条索引"很可能是错觉** —— 数一遍
   `information_schema.statistics` 才是真的。
2. **`idx_date_status` 目前没有任何查询用到。** 它服务的是 spec §8.1 的看板趋势统计，
   而看板属于尚未实现的前端部分。**不删**：删了将来要加回来，而 12,000 行的表上
   维护它的成本可以忽略。但如果将来确认不做趋势统计，它就是第一个该删的候选。

**每次 `INSERT` 要向 11 棵 B+ 树写一遍**（其中 `uk_active` 还要先算生成列）。
这是"用数据库约束保证并发正确性"的价格 —— 换来的是**零超卖**。
`docs/benchmark/` 的抢号基准证明这笔交易划算：扣减 419 µs ≈ 裸 Redis `PING` 422 µs，
瓶颈在传输而不在 MySQL。

---

## 7. 一次失败的方法学尝试（记录下来，免得下一个人重踩）

我试图用 MySQL 自带的索引使用统计来回答"哪些索引是冗余的"，即
`sys.schema_unused_indexes` / `performance_schema.table_io_waits_summary_by_index_usage`。

**结果自相矛盾，因此本文档没有据此得出任何结论。**

| 尝试 | 现象 |
|---|---|
| 跑完 25 条查询后查 `count_star = 0` 的索引 | 报了 28 条 —— 其中**包含刚才 EXPLAIN 明说用到的** `idx_doctor_cursor`、`audit_logs.idx_user_cursor` |
| 换单会话：`TRUNCATE` 计数 → 跑 1 条查询 → 立刻读 | **成功**：`idx_user_cursor=7`、`PRIMARY=300000` ✓ |
| 单会话里 `TRUNCATE` → 跑全部 25 条 → 读 | 又变成几乎全 0，包括 `appointments.PRIMARY`（而 Q1–Q5 全都碰它） |

**判据**：`EXPLAIN` 与这份计数**互相矛盾**，且矛盾方向不一致（既漏报也误报）。
instrument 本身是启用的（`wait/io/table/sql/handler` = ENABLED/TIMED = YES），
所以不是"没采集"，而是**这个计数表的语义不是"索引被读取的次数"**。

**结论**：**不要用 `sys.schema_unused_indexes` 判断本项目的索引该不该删。**
本文档所有"某条索引有没有用"的判断，依据都是 §5 的 `EXPLAIN` + `IGNORE INDEX` 对照实测。

---

## 8. 结论

1. **25 条业务查询，24 条命中合适索引，0 条被迫全表扫描。** 唯一逐行读完整表的
   （Q25，扫 300,000 行）是刻意构造的 OFFSET 对照，项目里不存在这种写法。
2. **游标分页是常数代价，OFFSET 是线性代价**：同为审计日志列表，
   0.022 ms / 读 21 行 vs 94.2 ms / 读 300,000 行。
3. **`idx_dept_date` 的列顺序是这份 schema 里设计得最好的一条**：三列全部用于索引查找，
   连排序都省了（§4.2）。它是"等值列在前、范围列在后"这条规则的正面样板。
4. **三处不完美，均已量化，均判定为"不改"**：医生排班区间（§4.3）、
   cron 扫描（§4.4）、科室医生列表的 filesort（§4.7）。
   共同理由：实测代价都在微秒~毫秒级，而**每条新索引都要为 HIS 全量同步的每次 UPSERT 付费**。
   每一处都写明了触发条件，将来复现时据此判断，而不是拍脑袋。
5. **反向验证证明索引不是装饰**：拿掉 `idx_user_cursor` 后，按用户查审计从读 7 行
   变成扫完 300,000 行（3,000 倍）；拿掉 `idx_doctor_cursor` 后，医生接诊列表
   从读 21 行变成扫 192,840 行（580 倍）。
6. **`EXPLAIN` 的 `rows` 是估算，会错得很离谱**（§5.1 里估 21 行、实际 30 万行）。
   任何成本判断都必须基于 `EXPLAIN ANALYZE` 的实际行数与耗时。
7. **索引使用统计表不可信**（§7），不要拿它做删索引的依据。

# MediCore 数据库设计与评审 v2

> 交付物：`docs/schema.sql`（完整 DDL v2，可直接执行）
> 验证环境：**MySQL 8.0.46**（真实容器，非静态检查）
> 验证方式：建表 + 22 项约束/性能实测，全部有输出证据

---

## 1. 结论

`docs/schema.sql` v2 已在 MySQL 8.0.46 上**完整执行通过**：**11 张表**全部创建成功，无语法或语义错误。

包含三类修正（v1）与三项设计变更（v2）：

| 版本 | 内容 |
|---|---|
| v1 | 修正 spec 硬伤、补 6 张缺失表、修正索引方向 |
| **v2** | **医生-科室多对多**、**软删除**、**就诊序号按签到时间分配** |

---

## 2. spec.md 原始 DDL 的问题

### 2.1 硬伤：索引引用了不存在的列

`spec.md:168`：

```sql
KEY idx_date_status (sched_date, status)   -- ❌ schedules 表中没有 status 列
```

→ **该建表语句必然报 `ERROR 1072: Key column 'status' doesn't exist in table`**。

**修正**：补 `status ENUM('open','closed','cancelled') NOT NULL DEFAULT 'open'`。

### 2.2 缺失 6 张表

| 表 | 依据 |
|---|---|
| `departments` | §6.1 `GET /departments/:id/doctors`；§2.1 "科室管理" |
| `doctors` | 职称/擅长/挂号费无处存放 |
| `medical_records` | §2.1 医生"书写诊断/处方" |
| `prescription_items` | 处方需明细 |
| `audit_logs` | §2.2 "审计日志查询"；§9.2 "TraceID 贯穿审计表" |
| `sync_logs` | §7.3 明确要求 |

### 2.3 索引方向与游标分页策略不匹配

spec 用 `(user_id, created_at DESC)`，但 §5.2 强制查询是 `WHERE id < :last_id ORDER BY id DESC` → **索引列与排序键不一致，无法支撑游标分页**。修正为 `(user_id, id DESC)`。

---

## 3. 最终表清单（11 张）

| 表 | 职责 | 关键约束 |
|---|---|---|
| `departments` | 科室 | `uk_code`、`uk_external` |
| `users` | 三角色共用账号 | `uk_phone_hash`、AES-GCM 列 |
| `doctors` | 医生档案（1:1 users） | `uk_user`、`uk_external` |
| **`doctor_departments`** | **医生-科室关联（多对多）** | `uk_doctor_dept`、`idx_dept_doctor` |
| `schedules` | 排班/号源 | `uk_doctor_dept_date_slot`、`ck_remaining` |
| `appointments` | 预约 | `uk_user_idem`、`uk_active`、`uk_sched_queue` |
| `ai_pre_reports` | AI 预问诊报告 | `uk_appointment`、`status` |
| `medical_records` | 病历 | `uk_appointment` |
| `prescription_items` | 处方明细 | FK→medical_records |
| `audit_logs` | 审计日志 | 刻意**无** user_id 外键 |
| `sync_logs` | HIS 同步记录 | `idx_source_cursor` |

---

## 4. v2 三项设计变更

### 4.1 医生-科室：多对多

**变更**：新增 `doctor_departments` 中间表，`doctors` 移除 `department_id`。

```sql
CREATE TABLE doctor_departments (
  doctor_id     BIGINT UNSIGNED NOT NULL,
  department_id BIGINT UNSIGNED NOT NULL,
  is_primary    TINYINT(1) NOT NULL DEFAULT 0 COMMENT '1=主诊科室',
  deleted_at    DATETIME(3) NULL,
  UNIQUE KEY uk_doctor_dept (doctor_id, department_id),
  KEY idx_dept_doctor (department_id, doctor_id)
)
```

**连带影响（关键）**：医生可跨科室出诊 ⇒ **同一医生在同一天同一午别可以有两个科室的排班**。因此排班唯一键必须从 `(doctor_id, sched_date, time_slot)` 改为：

```sql
UNIQUE KEY uk_doctor_dept_date_slot (doctor_id, department_id, sched_date, time_slot)
```

**已实测**：同医生同日同午别在不同科室可并存（T2），同科室重复则被拒（T3）。

### 4.2 软删除

**方案**：统一 `deleted_at DATETIME(3) NULL`，配合 GORM 的 `gorm.DeletedAt` 类型 —— **GORM 会自动为所有查询追加 `WHERE deleted_at IS NULL`**，无需在每处手写过滤条件。

覆盖表：`users` / `departments` / `doctors` / `doctor_departments` / `schedules` / `appointments` / `ai_pre_reports` / `medical_records`。

**不软删**：`audit_logs`（审计不可删）、`sync_logs`、`prescription_items`（随病历整体处理）。

**与唯一键的冲突处理（重要设计决策）**：

唯一键（如 `uk_phone_hash`、`uk_external`、`uk_code`）**保持全局唯一，不加 `deleted_at`**。理由：

- 若把 `deleted_at` 并入唯一键，MySQL 中多行 NULL 互不冲突，反而**会让唯一性失效**。
- 保持全局唯一，意味着"删除后重建同名记录"必须由 service 层**复活（resurrect）**原行
  （`UPDATE ... SET deleted_at = NULL`），而不是插入新行。
  对 HIS 同步尤其重要：`external_id` 是 upsert 的锚点，重新同步应复活而非插入重复行。

> ⚠️ **复活逻辑目前并未实现 —— 这是一处已知缺口，不是"已经做好了"。**
>
> 全量审视时逐项核对过：代码里**没有任何** `Unscoped()` / `deleted_at = NULL` / `.Delete()` 调用。
> 也就是说：
>
> - 当前**没有任何入口会软删这些表**，所以复活路径不可达，缺口暂时不会暴露；
> - 但若将来加上管理端的删除功能，`UpsertSchedule` 会命中 `uk_external` 冲突而**永久同步失败**
>   （它按带软删作用域的查询找不到已删行 → 走 Create → 唯一键冲突）；
>   `UpsertDepartment` 的 `ON DUPLICATE KEY UPDATE` 会更新到已删行但**不清 `deleted_at`**，
>   于是数据"更新成功"却依然在列表里看不见。
>
> **加删除功能之前，必须先实现复活。** 已记入 `HANDOVER.md` §3.3。

**唯一的例外是 `appointments.active_uk`**，其生成列表达式已包含软删除判断，使软删除同样释放号源占位：

```sql
active_uk VARCHAR(48) GENERATED ALWAYS AS (
  IF(status IN ('pending','confirmed') AND deleted_at IS NULL,
     CONCAT(user_id, ':', schedule_id), NULL)
) STORED
```

**既有的 `uk_active` 生成列方案**：MySQL 没有 PostgreSQL 式部分唯一索引；用生成列 + 唯一索引 + NULL 可重复，等效实现"同一用户不能重复占用同一号源"。取消、过期、软删除三种情况都会释放。

### 4.3 就诊序号：按实际签到时间分配

**变更**：`appointments` 新增两个字段：

| 字段 | 说明 |
|---|---|
| `checked_in_at DATETIME(3) NULL` | 实际签到时间，NULL=未签到 |
| `queue_no INT NULL` | 就诊序号，签到瞬间分配 |

**分配规则**：序号 = 该排班内**已签到人数 + 1**，即严格按签到先后排序。未签到者为 NULL，不参与排序。

**并发安全**：两步保障。

1. **事务串行化**：锁定排班行，使同一排班的签到串行执行

```sql
START TRANSACTION;
  SELECT id FROM schedules WHERE id = ? FOR UPDATE;         -- 锁住排班，串行化
  SELECT COALESCE(MAX(queue_no), 0) + 1 INTO @n
    FROM appointments WHERE schedule_id = ?;
  UPDATE appointments SET checked_in_at = NOW(3), queue_no = @n
    WHERE id = ? AND checked_in_at IS NULL;                 -- 幂等：重复签到不重排
COMMIT;
```

2. **唯一键兜底**：`UNIQUE KEY uk_sched_queue (schedule_id, queue_no)` —— 即便并发失控也不可能出现同号。`queue_no` 为 NULL 时不冲突，故未签到者不受约束。

**新增索引**：`KEY idx_sched_checkin (schedule_id, checked_in_at)` 支撑队列排序查询。

**已实测**：签到顺序 患者丙→1、患者甲→2（T6）；重复序号被拒（T7）。

**新需要的接口**（并入 API 设计）：

- `POST /api/v1/appointments/:id/checkin` —— 患者签到，分配序号
- `GET /api/v1/schedules/:id/queue` —— 医生端查看候诊队列（按 `queue_no` 排序）

---

## 5. 实测验证结果

全部在真实 MySQL 8.0.46 上执行。

### 5.1 v1 约束有效性（预期失败即通过）

| # | 测试 | 结果 |
|---|---|---|
| T3 | 重复预约同一号源 | ✅ `Duplicate entry '2:1' for key 'appointments.uk_active'` |
| T4 | 取消后重新预约 | ✅ 成功 |
| T5 | `remaining = -1` | ✅ `Check constraint 'ck_remaining' is violated` |
| T6 | `remaining > total` | ✅ 同上被拒 |
| T7 | 引用不存在的科室 | ✅ `ERROR 1452` 外键拦截 |
| T8 | 引用不存在的用户 | ✅ `ERROR 1452` 外键拦截 |
| T17 | 同一预约插入两份报告 | ✅ `Duplicate entry '3' for key 'ai_pre_reports.uk_appointment'` |

生成列实际取值：

```
id | status    | active_uk
 1 | cancelled | NULL        ← 取消后释放唯一性
 3 | pending   | 2:1         ← 进行中占用
```

### 5.2 v2 新增特性

| # | 测试 | 结果 |
|---|---|---|
| T1 | 医生归属内科 + 儿科 | ✅ `内科,儿科` |
| T2 | 同医生同日同午别、**不同科室** | ✅ 两张排班并存（id 1、2） |
| T3 | 同医生同日同午别、**同科室** | ✅ `Duplicate entry '1-1-2026-10-01-am' for key 'schedules.uk_doctor_dept_date_slot'` |
| T5 | 未签到者 `queue_no` 均为 NULL | ✅ 3 行 NULL 并存 |
| T6 | 按签到顺序分配序号 | ✅ 患者丙=1、患者甲=2 |
| T7 | 同排班序号重复 | ✅ `Duplicate entry '1-1' for key 'appointments.uk_sched_queue'` |
| T8 | 软删除后 `active_uk` 变 NULL | ✅ 已释放 |
| T9 | 软删除后同一用户重新预约 | ✅ 新行 `active_uk='2:1'` 创建成功 |
| T10 | 软删除过滤 | ✅ 可见 3 行 / 总计 4 行 |
| T11 | 医生跨科室排班统计 | ✅ 2 张排班 |

### 5.3 索引命中

| 查询 | 计划 |
|---|---|
| cron 扫过期订单 `status='pending' AND expire_at < NOW()` | ✅ `idx_status_expire`，`Using index`（覆盖索引，免回表） |
| 稀有用户游标分页（23 万行中该用户仅 3 行） | ✅ 走索引查找 0.028ms |
| 强制 `idx_user_cursor` | ✅ 区间扫描 0.0148ms（省一次排序） |

> 说明：**数据量只有个位数行时优化器会给出 `Using filesort`**，那是统计信息不足的假象，非索引设计问题——我在 2 行的表上复现过，灌到 3 万行后计划自动变优。

### 5.4 性能基准：游标分页 vs OFFSET（230,005 行）

| 方式 | 实际耗时 | 扫描行数 |
|---|---|---|
| `LIMIT 100000, 20`（OFFSET 深翻） | **137 ms** | 203,334 行索引查找 + 排序 100,020 行 |
| `WHERE id < 132771 ORDER BY id DESC LIMIT 20`（游标） | **0.483 ms** | **20 行** |

**相差约 284 倍**，且游标分页代价与页码无关（恒定 20 行），OFFSET 随页码线性增长。

→ 直接支撑 spec §5.2「游标分页强制」与 §12.2「`docs/sql-optimization.md`（EXPLAIN 对比）」。

---

## 6. 待确认项

1. **`status` 枚举是否够用？** 当前沿用 spec 的 `pending/confirmed/cancelled/expired`。接入签到后，真实流程通常是 `待确认 → 已确认 → 已签到 → 已完成`。我目前用 `checked_in_at`/`queue_no` 表示签到，未改枚举。是否需要补 `checked_in`/`completed` 两个状态？
2. **`audit_logs` 增长策略**：写入最频繁的表。是否需要按 `created_at` 月度分区或定期归档？
3. **"复活"语义确认**：删除后用同一手机号重新注册，是复活原账号（保留历史病历），还是要求换号？当前设计倾向复活。
4. **HIS 同步范围**：`departments`/`doctors`/`schedules` 均带 `external_id`。是否还需同步患者基础信息？
5. **初始管理员**：建议由 Go seed 程序读 `ADMIN_INIT_PASSWORD` 环境变量生成 bcrypt，不把 hash 硬编码进 SQL。

---

*验证容器与全部临时 SQL 已清理。`mysql:8.0` 镜像已缓存，后续启动真实实例无需再次拉取。*

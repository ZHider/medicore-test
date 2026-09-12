-- =============================================================================
-- sqlopt-seed.sql —— 为 docs/sql-optimization.md 灌入**真实量级**的数据。
--
-- 为什么需要它：EXPLAIN 的结论**只在有真实行数与统计信息时才成立**。
-- 在几十行的开发库上跑 EXPLAIN，优化器一律选择全表扫描 —— 那不是"发现"，
-- 而是把"没有数据"误读成"索引没用"。所以这份脚本的目标只有一个：
-- 让优化器面对与生产同量级的选择性，从而给出**可被信任**的执行计划。
--
-- ⚠️ 它灌的是模拟数据，**不要跑在 medicore 开发库上**。
--    灌进 medicore_test（集成测试专用的一次性库，`medicore` 账号已被授权，
--    且集成测试本来每轮就会 DROP 重建它，所以用它不需要任何额外授权）。
--
-- ⚠️ **目标库必须是全新的**：本脚本第 1 步会 `DELETE FROM departments`
--    （原因见那里），而 schema.sql 里有不带 id 的演示科室行 —— 重复建表会报
--    `Table 'departments' already exists`，在已有数据的库上 DELETE 又会撞外键。
--    所以三步一步都不能省，顺序也不能变：
--
--      mysql -e "DROP DATABASE IF EXISTS medicore_test; CREATE DATABASE medicore_test
--                CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;"
--      mysql medicore_test < docs/schema.sql
--      mysql medicore_test < backend/scripts/sqlopt-seed.sql
--
--    完整复现命令见 docs/sql-optimization.md §1。
--    跑一次集成测试（它会 DROP 掉这个库）之后要重看数据，就从第一步重来。
--
-- 全部数据都是确定性的（除 created_at 以 NOW() 为基准），跑两次行数一致 ——
-- 实测两次都是 departments 20 / appointments 240000 / audit_logs 300000。
--
-- 三个刻意为之的设计（不是随手写的，改动前请先理解）：
--
--  1. **构造性唯一**而不是随机取值。appointments 的 uk_active 是
--     (user_id, schedule_id) 的部分唯一索引；随机生成必然撞键（生日悖论），
--     撞了就得重试、结果还不确定。这里用 `(u*5 + b) mod 12000` 这种
--     **可证明两两不同**的映射，一次插入成功，且分布均匀。
--  2. **不用递归 CTE**。MySQL 的 cte_max_recursion_depth 默认 1000，
--     且它是会话级变量 —— SET SESSION 之后下一条语句可能落到连接池里的
--     另一条连接，于是时好时坏。这里用 6 路 digits 交叉连接得到 0..999999，
--     与连接、与 MySQL 版本都无关。
--  3. **window function 生成 queue_no**。uk_sched_queue (schedule_id, queue_no)
--     要求同一排班内序号不重复；ROW_NUMBER() 按 schedule_id 分区，
--     构造性地满足约束，而不是靠试。
-- =============================================================================

SET NAMES utf8mb4;

-- -----------------------------------------------------------------------------
-- 0. 数字序列：0 .. 999999
--
-- ⚠️ sqlopt_digits 必须是**普通表**而非 TEMPORARY：MySQL 不允许在同一条查询里
--    多次引用同一个临时表（报 `ERROR 1137: Can't reopen table`），
--    而下面的 6 路交叉连接正需要引用它 6 次。
--    一次性库里放这张辅助表没有代价，末尾会删掉。
-- -----------------------------------------------------------------------------
DROP TABLE IF EXISTS sqlopt_digits;
CREATE TABLE sqlopt_digits (d INT NOT NULL);
INSERT INTO sqlopt_digits VALUES (0),(1),(2),(3),(4),(5),(6),(7),(8),(9);

DROP TEMPORARY TABLE IF EXISTS sqlopt_nums;
CREATE TEMPORARY TABLE sqlopt_nums (n INT NOT NULL, PRIMARY KEY (n));
INSERT INTO sqlopt_nums (n)
SELECT a.d + 10*b.d + 100*c.d + 1000*d.d + 10000*e.d + 100000*f.d
FROM sqlopt_digits a, sqlopt_digits b, sqlopt_digits c,
     sqlopt_digits d, sqlopt_digits e, sqlopt_digits f;

-- -----------------------------------------------------------------------------
-- 1. departments：20 个
--
-- 先清空：docs/schema.sql 结尾会插入 4 条演示科室（id 1..4，无显式 id）。
-- 本脚本要用显式 id 1..20，不清掉就会主键冲突。
-- 此刻尚无任何表引用 departments，DELETE 不会触发外键错误。
-- -----------------------------------------------------------------------------
DELETE FROM departments;

INSERT INTO departments (id, code, name, description, sort_order, status)
SELECT n + 1, CONCAT('DEPT', LPAD(n + 1, 3, '0')), CONCAT('科室', n + 1),
       CONCAT('用于性能验证的科室 ', n + 1), n * 10, 1
FROM sqlopt_nums WHERE n < 20;

-- -----------------------------------------------------------------------------
-- 2. users：200 名医生（id 1..200）+ 48000 名患者（id 201..48200）
--
-- id_card_hash 刻意留 NULL：uk_id_card_hash 是唯一索引，而 MySQL 的唯一索引
-- 允许任意多个 NULL —— 这正是"未采集身份证"能共存于唯一约束下的原因。
-- -----------------------------------------------------------------------------
INSERT INTO users (id, role, name, phone_enc, phone_hash, password_hash, status)
SELECT
  n + 1,
  IF(n < 200, 'doctor', 'patient'),
  IF(n < 200, CONCAT('医生', n + 1), CONCAT('患者', n + 1)),
  UNHEX(SHA2(CONCAT('enc:', n), 256)),
  SHA2(CONCAT('phone:', n), 256),
  -- bcrypt 密文长度固定 60：'$2a$10$'(7) + 53 位
  CONCAT('$2a$10$', LPAD(n + 1, 53, '0')),
  1
FROM sqlopt_nums WHERE n < 48200;

-- -----------------------------------------------------------------------------
-- 3. doctors：200 名，user_id 与 id 一一对应
-- -----------------------------------------------------------------------------
INSERT INTO doctors (id, user_id, external_id, title, specialty, intro, consult_fee, status)
SELECT n + 1, n + 1, CONCAT('EXT-D-', n + 1), '主任医师',
       CONCAT('擅长领域 ', n + 1), CONCAT('简介 ', n + 1), 50.00, 1
FROM sqlopt_nums WHERE n < 200;

-- -----------------------------------------------------------------------------
-- 4. doctor_departments：每人一个主诊科室，每 3 人再加一个跨科出诊
--
-- 科室取值必须与第 5 步 schedules 推导出的 department_id 一致
-- （schedules.department_id = 1 + (s0 % 20)，而 doctors.id = s0 % 200 + 1），
-- 否则"医生在其所属科室下出诊"这一语义就断了。
-- -----------------------------------------------------------------------------
INSERT INTO doctor_departments (doctor_id, department_id, is_primary, sort_order)
SELECT n + 1, 1 + (n % 20), 1, 0 FROM sqlopt_nums WHERE n < 200;

-- (n+7) % 20 ≠ n % 20（7 不是 20 的倍数），因此不会撞 uk_doctor_dept
INSERT INTO doctor_departments (doctor_id, department_id, is_primary, sort_order)
SELECT n + 1, 1 + ((n + 7) % 20), 0, 1
FROM sqlopt_nums WHERE n < 200 AND n % 3 = 0;

-- -----------------------------------------------------------------------------
-- 5. schedules：12000 条 = 200 名医生 × 30 天 × 2 个半天
--
-- 这是一个**双射**：s0 -> (doctor, day, slot) 一一对应，所以
-- uk_doctor_dept_date_slot (doctor_id, department_id, sched_date, time_slot)
-- 必然不冲突 —— 不需要任何去重或重试。
--   doctor = 1 + (s0 % 200)
--   rest   = s0 DIV 200  in [0, 60)  ->  day = rest DIV 2, slot = rest % 2
-- department_id 由 doctor 推导，故四元组同样唯一。
-- -----------------------------------------------------------------------------
INSERT INTO schedules
  (id, doctor_id, department_id, external_id, sched_date,
   time_slot, total, remaining, version, status)
SELECT
  s0 + 1,
  1 + (s0 % 200),
  1 + (s0 % 20),
  CONCAT('EXT-S-', s0 + 1),
  DATE_ADD(CURDATE(), INTERVAL ((s0 DIV 200) DIV 2) DAY),
  IF((s0 DIV 200) % 2 = 0, 'am', 'pm'),
  50,
  1 + ((s0 + 1) % 40),
  0,
  'open'
FROM (SELECT n AS s0 FROM sqlopt_nums WHERE n < 12000) t;

-- -----------------------------------------------------------------------------
-- 6. appointments：240000 条 = 48000 名患者 × 5 次就诊
--
-- key 的构造（这是整份脚本最关键的一处）：
--   n     -> u = n % 48000（患者编号）, b = n DIV 48000（就诊次序 in 0..4）
--   user_id     = 201 + u
--   schedule_id = 1 + ((u*5 + b) mod 12000)
--
-- 为什么 (user_id, schedule_id) 唯一：对固定的 u，b 取 0..4 时 u*5+b 是
-- 5 个**连续**整数，模 12000 后仍然两两不同（5 < 12000）。因此同一患者
-- 的 5 次预约落在 5 个不同排班上 —— uk_active 构造性满足。
-- 又因为 (u,b) -> u*5+b 是 [0,240000) 上的双射，12000 个排班被均匀覆盖，
-- 每个恰好 20 条。
--
-- status 分布（合计 100%）：
--   confirmed 70% / cancelled 15% / expired 10% / pending 5%
-- pending 的 expire_at 落在过去一小时 —— 那正是 cron 要扫的"超时未确认"。
--
-- queue_no 用 ROW_NUMBER() 按 schedule_id 分区，保证 uk_sched_queue 不冲突。
-- -----------------------------------------------------------------------------
INSERT INTO appointments
  (id, user_id, schedule_id, doctor_id, department_id, sched_date, time_slot,
   queue_no, checked_in_at, status, idempotency_key, expire_at, confirmed_at,
   cancelled_at, deleted_at, created_at)
SELECT
  x.id, x.user_id, x.schedule_id,
  s.doctor_id, s.department_id, s.sched_date, s.time_slot,
  CASE WHEN x.status = 'confirmed' THEN x.rn END,
  CASE WHEN x.status = 'confirmed' THEN DATE_ADD(x.created_at, INTERVAL 11 MINUTE) END,
  x.status,
  x.idem,
  x.expire_at,
  CASE WHEN x.status = 'confirmed' THEN DATE_ADD(x.created_at, INTERVAL 10 MINUTE) END,
  CASE WHEN x.status = 'cancelled' THEN DATE_ADD(x.created_at, INTERVAL 1 DAY) END,
  NULL,
  x.created_at
FROM (
  SELECT
    t.n + 1                                        AS id,
    201 + (t.n % 48000)                            AS user_id,
    1 + (((t.n % 48000) * 5 + (t.n DIV 48000)) % 12000) AS schedule_id,
    CASE
      WHEN t.n % 100 < 70 THEN 'confirmed'
      WHEN t.n % 100 < 85 THEN 'cancelled'
      WHEN t.n % 100 < 95 THEN 'expired'
      ELSE 'pending'
    END                                            AS status,
    LPAD(t.n + 1, 36, '0')                         AS idem,
    CASE WHEN t.n % 100 >= 95
         THEN NOW(3) - INTERVAL 1 HOUR
         ELSE NOW(3) + INTERVAL 15 MINUTE END      AS expire_at,
    NOW(3) - INTERVAL (t.n % 20) DAY               AS created_at,
    ROW_NUMBER() OVER (
      PARTITION BY 1 + (((t.n % 48000) * 5 + (t.n DIV 48000)) % 12000)
      ORDER BY t.n
    )                                              AS rn
  FROM (SELECT n FROM sqlopt_nums WHERE n < 240000) t
) x
JOIN schedules s ON s.id = x.schedule_id;

-- -----------------------------------------------------------------------------
-- 7. medical_records：为最早的 160000 条 confirmed 预约写病历
--
-- 只取 confirmed 是语义要求：未确认/已取消的预约不该有诊疗记录。
-- 因此实际行数 = MIN(160000, confirmed 总数)。
-- -----------------------------------------------------------------------------
INSERT INTO medical_records
  (appointment_id, patient_id, doctor_id,
   chief_complaint, present_illness, diagnosis, treatment_advice, created_at)
SELECT
  a.id, a.user_id, a.doctor_id,
  '主诉：发热伴咳嗽 3 天',
  '现病史：3 天前受凉后出现发热，最高 38.5℃，伴咽痛、咳白色粘痰。',
  CONCAT('诊断', a.id % 50),
  '医嘱：多饮水，注意休息，如持续高热及时复诊。',
  a.created_at
FROM appointments a
WHERE a.status = 'confirmed'
ORDER BY a.id
LIMIT 160000;

-- -----------------------------------------------------------------------------
-- 8. prescription_items：每份病历 2 条
-- -----------------------------------------------------------------------------
INSERT INTO prescription_items
  (record_id, drug_name, spec, quantity, usage_desc, days, sort_order)
SELECT r.id, CONCAT('药品', (r.id + k.n) % 200), '0.25g×24片',
       1 + (r.id % 3), '口服，一日三次，饭后服用', 3, k.n
FROM medical_records r
CROSS JOIN (SELECT 0 AS n UNION ALL SELECT 1) k;

-- -----------------------------------------------------------------------------
-- 9. ai_pre_reports：为最早的 40000 条预约建报告
-- -----------------------------------------------------------------------------
INSERT INTO ai_pre_reports
  (appointment_id, user_id, status, structured_json, conversation,
   llm_model, prompt_version, fallback, attempt_count, created_at, finished_at)
SELECT
  a.id, a.user_id, 'done',
  JSON_OBJECT('chief_complaint', '发热伴咳嗽', 'duration', '3天',
              'severity', 'moderate', 'allergy', '无'),
  JSON_ARRAY(
    JSON_OBJECT('role', 'user',      'content', '我发烧三天了，还咳嗽'),
    JSON_OBJECT('role', 'assistant', 'content', '请问最高体温多少？有无药物过敏？')
  ),
  'mock-llm-v1', 'preconsult_v1', 0, 1, a.created_at, a.created_at
FROM appointments a
ORDER BY a.id
LIMIT 40000;

-- -----------------------------------------------------------------------------
-- 10. audit_logs：300000 条，覆盖最近 90 天
--
-- 写入最频繁的表，也是"游标分页 vs OFFSET"对照实验的载体。
-- trace_id 由行号派生，天然唯一（真实系统里是随机 32 位 hex）。
-- -----------------------------------------------------------------------------
INSERT INTO audit_logs
  (trace_id, user_id, role, method, path, resource, resource_id, action,
   status_code, latency_ms, client_ip, user_agent, created_at)
SELECT
  LPAD(HEX(t.n + 1), 32, '0'),
  201 + (t.n % 48000),
  'patient',
  IF(t.n % 7 = 0, 'POST', 'GET'),
  ELT(1 + (t.n % 5),
      '/api/v1/appointments',
      '/api/v1/departments',
      '/api/v1/records',
      '/api/v1/auth/me',
      '/api/v1/doctors/1'),
  ELT(1 + (t.n % 4), 'appointment', 'department', 'record', 'auth'),
  t.n % 100000,
  ELT(1 + (t.n % 4), 'create', 'view', 'cancel', 'checkin'),
  IF(t.n % 11 = 0, 400, 200),
  1 + (t.n % 300),
  '127.0.0.1',
  'Mozilla/5.0 (sqlopt seed)',
  NOW(3) - INTERVAL (t.n % 90) DAY - INTERVAL (t.n % 86400) SECOND
FROM (SELECT n FROM sqlopt_nums WHERE n < 300000) t;

-- -----------------------------------------------------------------------------
-- 11. sync_logs：500 条
-- -----------------------------------------------------------------------------
INSERT INTO sync_logs
  (source, trigger_type, total, succeeded, failed, status, started_at, finished_at, trace_id)
SELECT
  ELT(1 + (t.n % 2), 'mockhis', 'mockhis-backup'),
  IF(t.n % 4 = 0, 'manual', 'cron'),
  100, 100, 0, 'success',
  NOW(3) - INTERVAL (t.n % 30) DAY,
  NOW(3) - INTERVAL (t.n % 30) DAY,
  LPAD(HEX(t.n + 1), 32, '0')
FROM (SELECT n FROM sqlopt_nums WHERE n < 500) t;

-- -----------------------------------------------------------------------------
-- 12. 清理辅助表（sqlopt_nums 是临时表，会话结束自动消失）
-- -----------------------------------------------------------------------------
DROP TABLE IF EXISTS sqlopt_digits;

-- -----------------------------------------------------------------------------
-- 13. 刷新统计信息
--
-- 必须显式 ANALYZE：MySQL 8 默认 innodb_stats_on_metadata=OFF，
-- 且 InnoDB 的持久化统计信息只在特定时机更新。不刷新就 EXPLAIN，
-- 优化器可能拿着"表只有 4 行"的旧统计做决策，前面所有的功夫都白费。
-- -----------------------------------------------------------------------------
ANALYZE TABLE departments, users, doctors, doctor_departments, schedules,
  appointments, ai_pre_reports, medical_records, prescription_items,
  audit_logs, sync_logs;

-- -----------------------------------------------------------------------------
-- 14. 实际行数 —— 记进文档，避免"以为灌了 30 万其实是 3 万"
-- -----------------------------------------------------------------------------
SELECT 'departments' AS tbl, COUNT(*) AS rows_ FROM departments
UNION ALL SELECT 'users',              COUNT(*) FROM users
UNION ALL SELECT 'doctors',            COUNT(*) FROM doctors
UNION ALL SELECT 'doctor_departments', COUNT(*) FROM doctor_departments
UNION ALL SELECT 'schedules',          COUNT(*) FROM schedules
UNION ALL SELECT 'appointments',       COUNT(*) FROM appointments
UNION ALL SELECT 'medical_records',    COUNT(*) FROM medical_records
UNION ALL SELECT 'prescription_items', COUNT(*) FROM prescription_items
UNION ALL SELECT 'ai_pre_reports',     COUNT(*) FROM ai_pre_reports
UNION ALL SELECT 'audit_logs',         COUNT(*) FROM audit_logs
UNION ALL SELECT 'sync_logs',          COUNT(*) FROM sync_logs;

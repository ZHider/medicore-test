-- =============================================================================
-- MediCore · 完整 DDL v2
-- 目标: MySQL 8.0+  |  字符集: utf8mb4 / utf8mb4_0900_ai_ci  |  引擎: InnoDB
--
-- v2 相对 v1 的三项设计变更（用户决策）:
--   1. 医生与科室改为 **多对多** → 新增 doctor_departments，doctors 去掉 department_id
--   2. 引入 **软删除** → 统一 deleted_at + GORM gorm.DeletedAt（自动过滤）
--   3. 就诊序号按 **实际签到时间** 分配 → 新增 checked_in_at / queue_no + uk_sched_queue
--
-- 以及 spec.md §5.1 的原始硬伤修正:
--   schedules 补 status 列（原 DDL 的 idx_date_status 引用了不存在的列 → 建表必失败）
-- =============================================================================

SET NAMES utf8mb4;
SET time_zone = '+08:00';

-- -----------------------------------------------------------------------------
-- 1. departments 科室
-- -----------------------------------------------------------------------------
CREATE TABLE departments (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  external_id VARCHAR(64)     NULL COMMENT 'HIS 外部ID，upsert 锚点',
  code        VARCHAR(32)     NOT NULL COMMENT '科室编码',
  name        VARCHAR(64)     NOT NULL COMMENT '科室名称',
  description VARCHAR(255)    NULL,
  sort_order  INT             NOT NULL DEFAULT 0,
  status      TINYINT         NOT NULL DEFAULT 1 COMMENT '1=启用 0=停用',
  deleted_at  DATETIME(3)     NULL COMMENT '软删除时间，NULL=未删除（GORM gorm.DeletedAt）',
  created_at  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_code (code),
  UNIQUE KEY uk_external (external_id) COMMENT '保持全局唯一：HIS 重新同步时"复活"被软删的行',
  KEY idx_status_sort (status, sort_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='科室';

-- -----------------------------------------------------------------------------
-- 2. users 用户（三角色共用）
--    phone_enc/id_card_enc : AES-256-GCM 密文，前 12 字节为 nonce
--    phone_hash            : SHA256(phone + 全局 pepper)，**必须确定性**，否则无法登录查找
-- -----------------------------------------------------------------------------
CREATE TABLE users (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  role          ENUM('admin','doctor','patient') NOT NULL,
  name          VARCHAR(64)     NOT NULL,
  phone_enc     VARBINARY(128)  NOT NULL COMMENT 'AES-GCM(phone)，nonce(12B)+ct+tag(16B)',
  phone_hash    CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL COMMENT 'SHA256(phone+pepper)',
  id_card_enc   VARBINARY(128)  NULL COMMENT 'AES-GCM(id_card)',
  id_card_hash  CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NULL,
  password_hash VARCHAR(100)    NOT NULL COMMENT 'bcrypt 60 字符；预留到 100 便于将来换 argon2',
  status        TINYINT         NOT NULL DEFAULT 1 COMMENT '1=正常 0=禁用',
  last_login_at DATETIME(3)     NULL,
  deleted_at    DATETIME(3)     NULL,
  created_at    DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at    DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_phone_hash (phone_hash) COMMENT '全局唯一；重新注册同一手机号需"复活"原行',
  UNIQUE KEY uk_id_card_hash (id_card_hash),
  KEY idx_role_status (role, status),
  KEY idx_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='用户(三角色)';

-- -----------------------------------------------------------------------------
-- 3. doctors 医生档案（与 users 1:1，仅 role=doctor 者有记录）
--    注意：科室归属在 doctor_departments（多对多），本表不再有 department_id
-- -----------------------------------------------------------------------------
CREATE TABLE doctors (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id     BIGINT UNSIGNED NOT NULL,
  external_id VARCHAR(64)     NULL,
  title       VARCHAR(32)     NOT NULL DEFAULT '' COMMENT '职称，如 主任医师',
  specialty   VARCHAR(255)    NULL COMMENT '擅长领域',
  intro       TEXT            NULL COMMENT '简介',
  consult_fee DECIMAL(10,2)   NOT NULL DEFAULT 0.00 COMMENT '挂号费',
  status      TINYINT         NOT NULL DEFAULT 1,
  deleted_at  DATETIME(3)     NULL,
  created_at  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_user (user_id),
  UNIQUE KEY uk_external (external_id),
  CONSTRAINT fk_doctors_user FOREIGN KEY (user_id) REFERENCES users(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='医生档案';

-- -----------------------------------------------------------------------------
-- 4. doctor_departments 医生-科室关联（多对多）
--    is_primary 标记主诊科室；一名医生可在多个科室出诊（如专家跨科）
-- -----------------------------------------------------------------------------
CREATE TABLE doctor_departments (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  doctor_id     BIGINT UNSIGNED NOT NULL,
  department_id BIGINT UNSIGNED NOT NULL,
  is_primary    TINYINT(1)      NOT NULL DEFAULT 0 COMMENT '1=主诊科室',
  sort_order    INT             NOT NULL DEFAULT 0,
  deleted_at    DATETIME(3)     NULL,
  created_at    DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at    DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_doctor_dept (doctor_id, department_id),
  KEY idx_dept_doctor (department_id, doctor_id) COMMENT '按科室列医生',
  KEY idx_doctor_primary (doctor_id, is_primary),
  CONSTRAINT fk_dd_doctor FOREIGN KEY (doctor_id)     REFERENCES doctors(id)     ON DELETE RESTRICT,
  CONSTRAINT fk_dd_dept   FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='医生-科室关联(多对多)';

-- -----------------------------------------------------------------------------
-- 5. schedules 排班（号源载体）
--    因医生可跨科室出诊，唯一键为 (doctor_id, department_id, sched_date, time_slot)
--    remaining 由 Redis Lua 原子扣减，MySQL 侧用 version 乐观锁兜底
-- -----------------------------------------------------------------------------
CREATE TABLE schedules (
  id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  doctor_id     BIGINT UNSIGNED NOT NULL,
  department_id BIGINT UNSIGNED NOT NULL COMMENT '该排班所属科室',
  external_id   VARCHAR(64)     NULL,
  sched_date    DATE            NOT NULL,
  time_slot     ENUM('am','pm') NOT NULL,
  total         INT             NOT NULL COMMENT '总号源',
  remaining     INT             NOT NULL COMMENT '剩余号源',
  version       INT             NOT NULL DEFAULT 0 COMMENT '乐观锁版本号',
  status        ENUM('open','closed','cancelled') NOT NULL DEFAULT 'open',
  deleted_at    DATETIME(3)     NULL,
  created_at    DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at    DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_doctor_dept_date_slot (doctor_id, department_id, sched_date, time_slot),
  UNIQUE KEY uk_external (external_id),
  KEY idx_date_status (sched_date, status),
  KEY idx_dept_date (department_id, sched_date, status),
  CONSTRAINT fk_sched_doctor FOREIGN KEY (doctor_id)     REFERENCES doctors(id)     ON DELETE RESTRICT,
  CONSTRAINT fk_sched_dept   FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE RESTRICT,
  CONSTRAINT ck_remaining CHECK (remaining >= 0 AND remaining <= total)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='排班/号源';

-- -----------------------------------------------------------------------------
-- 6. appointments 预约
--    queue_no : **按实际签到时间** 分配（签到在前，序号在前）
--    checked_in_at : 实际签到时间，NULL=未签到
--    uk_sched_queue : 同一排班内序号不重复（NULL 可重复 → 未签到者不受约束）
--    active_uk : 生成列实现部分唯一索引；取消/过期/软删后释放，用户可重新预约
-- -----------------------------------------------------------------------------
CREATE TABLE appointments (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  user_id         BIGINT UNSIGNED NOT NULL,
  schedule_id     BIGINT UNSIGNED NOT NULL,
  doctor_id       BIGINT UNSIGNED NOT NULL COMMENT '冗余：医生端"我的预约"游标分页',
  department_id   BIGINT UNSIGNED NOT NULL COMMENT '冗余：科室维度统计',
  sched_date      DATE            NOT NULL COMMENT '冗余：按就诊日排序',
  time_slot       ENUM('am','pm') NOT NULL,
  queue_no        INT             NULL COMMENT '就诊序号，签到瞬间按签到顺序分配',
  checked_in_at   DATETIME(3)     NULL COMMENT '实际签到时间',
  status          ENUM('pending','confirmed','cancelled','expired') NOT NULL DEFAULT 'pending',
  idempotency_key CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  expire_at       DATETIME(3)     NOT NULL COMMENT '15 分钟确认截止',
  confirmed_at    DATETIME(3)     NULL,
  cancelled_at    DATETIME(3)     NULL,
  cancel_reason   VARCHAR(255)    NULL,
  active_uk       VARCHAR(48) CHARACTER SET ascii COLLATE ascii_bin
                  GENERATED ALWAYS AS (
                    IF(status IN ('pending','confirmed') AND deleted_at IS NULL,
                       CONCAT(user_id, ':', schedule_id), NULL)
                  ) STORED COMMENT '进行中预约唯一键（软删除后同样释放）',
  deleted_at      DATETIME(3)     NULL,
  created_at      DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at      DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_user_idem (user_id, idempotency_key) COMMENT '幂等键按用户隔离',
  UNIQUE KEY uk_active (active_uk),
  UNIQUE KEY uk_sched_queue (schedule_id, queue_no) COMMENT '同一排班内序号唯一',
  KEY idx_user_cursor   (user_id, id DESC)   COMMENT '患者列表游标分页',
  KEY idx_doctor_cursor (doctor_id, id DESC) COMMENT '医生列表游标分页',
  KEY idx_status_expire (status, expire_at)  COMMENT 'cron 扫描超时订单',
  KEY idx_sched_checkin (schedule_id, checked_in_at) COMMENT '签到队列排序',
  KEY idx_schedule (schedule_id),
  KEY idx_date_status (sched_date, status)   COMMENT '看板趋势统计',
  CONSTRAINT fk_appt_user     FOREIGN KEY (user_id)       REFERENCES users(id)       ON DELETE RESTRICT,
  CONSTRAINT fk_appt_schedule FOREIGN KEY (schedule_id)   REFERENCES schedules(id)   ON DELETE RESTRICT,
  CONSTRAINT fk_appt_doctor   FOREIGN KEY (doctor_id)     REFERENCES doctors(id)     ON DELETE RESTRICT,
  CONSTRAINT fk_appt_dept     FOREIGN KEY (department_id) REFERENCES departments(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='预约';

-- -----------------------------------------------------------------------------
-- 7. ai_pre_reports AI 预问诊报告（决策 #8：Stream 异步生成）
-- -----------------------------------------------------------------------------
CREATE TABLE ai_pre_reports (
  id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  appointment_id  BIGINT UNSIGNED NOT NULL,
  user_id         BIGINT UNSIGNED NOT NULL,
  status          ENUM('pending','processing','done','failed') NOT NULL DEFAULT 'pending',
  structured_json JSON            NULL COMMENT '经 JSON Schema 校验的结构化输出；降级时为 NULL',
  conversation    JSON            NOT NULL COMMENT '完整对话记录（降级时医生端展示此内容）',
  llm_model       VARCHAR(64)     NOT NULL DEFAULT '',
  prompt_version  VARCHAR(16)     NOT NULL DEFAULT '' COMMENT '如 preconsult_v1',
  fallback        TINYINT(1)      NOT NULL DEFAULT 0,
  attempt_count   INT             NOT NULL DEFAULT 0,
  error_msg       VARCHAR(512)    NULL,
  started_at      DATETIME(3)     NULL,
  finished_at     DATETIME(3)     NULL,
  deleted_at      DATETIME(3)     NULL,
  created_at      DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at      DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_appointment (appointment_id) COMMENT '一次预约一份报告，保证 submit 幂等',
  KEY idx_user_cursor (user_id, id DESC),
  KEY idx_status_created (status, created_at) COMMENT 'Worker 补偿扫描',
  CONSTRAINT fk_report_appt FOREIGN KEY (appointment_id) REFERENCES appointments(id) ON DELETE RESTRICT,
  CONSTRAINT fk_report_user FOREIGN KEY (user_id)        REFERENCES users(id)        ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='AI预问诊报告';

-- -----------------------------------------------------------------------------
-- 8. medical_records 病历（医疗数据，只软删不物理删）
-- -----------------------------------------------------------------------------
CREATE TABLE medical_records (
  id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  appointment_id   BIGINT UNSIGNED NOT NULL,
  patient_id       BIGINT UNSIGNED NOT NULL,
  doctor_id        BIGINT UNSIGNED NOT NULL,
  chief_complaint  VARCHAR(512)    NULL COMMENT '主诉',
  present_illness  TEXT            NULL COMMENT '现病史',
  diagnosis        VARCHAR(512)    NOT NULL COMMENT '诊断',
  treatment_advice TEXT            NULL COMMENT '医嘱',
  deleted_at       DATETIME(3)     NULL,
  created_at       DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at       DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  UNIQUE KEY uk_appointment (appointment_id) COMMENT '一次就诊一份病历',
  KEY idx_patient_cursor (patient_id, id DESC),
  KEY idx_doctor_cursor  (doctor_id, id DESC),
  CONSTRAINT fk_mr_appt    FOREIGN KEY (appointment_id) REFERENCES appointments(id) ON DELETE RESTRICT,
  CONSTRAINT fk_mr_patient FOREIGN KEY (patient_id)     REFERENCES users(id)        ON DELETE RESTRICT,
  CONSTRAINT fk_mr_doctor  FOREIGN KEY (doctor_id)      REFERENCES doctors(id)      ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='病历';

-- -----------------------------------------------------------------------------
-- 9. prescription_items 处方明细
-- -----------------------------------------------------------------------------
CREATE TABLE prescription_items (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  record_id   BIGINT UNSIGNED NOT NULL,
  drug_name   VARCHAR(128)    NOT NULL,
  spec        VARCHAR(64)     NULL COMMENT '规格',
  quantity    INT             NOT NULL DEFAULT 1,
  usage_desc  VARCHAR(255)    NULL COMMENT '用法用量',
  days        INT             NULL COMMENT '用药天数',
  sort_order  INT             NOT NULL DEFAULT 0,
  created_at  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_record (record_id, sort_order),
  CONSTRAINT fk_pi_record FOREIGN KEY (record_id) REFERENCES medical_records(id) ON DELETE RESTRICT
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='处方明细';

-- -----------------------------------------------------------------------------
-- 10. audit_logs 审计日志（不软删、可加 TTL/分区）
--     刻意不加 user_id 外键：审计记录必须独立于业务数据存活
-- -----------------------------------------------------------------------------
CREATE TABLE audit_logs (
  id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  trace_id    CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  user_id     BIGINT UNSIGNED NULL,
  role        VARCHAR(16)     NULL,
  method      VARCHAR(8)      NOT NULL,
  path        VARCHAR(255)    NOT NULL,
  resource    VARCHAR(64)     NULL COMMENT '资源类型，如 appointment',
  resource_id BIGINT UNSIGNED NULL,
  action      VARCHAR(32)     NULL COMMENT 'create/cancel/checkin/view',
  status_code INT             NOT NULL,
  latency_ms  INT             NOT NULL,
  client_ip   VARCHAR(45)     NULL,
  user_agent  VARCHAR(255)    NULL,
  detail      JSON            NULL,
  created_at  DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (id),
  KEY idx_trace (trace_id),
  KEY idx_user_cursor (user_id, id DESC),
  KEY idx_created (created_at),
  KEY idx_resource (resource, resource_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='审计日志';

-- -----------------------------------------------------------------------------
-- 11. sync_logs 模拟 HIS 同步记录（spec §7.3）
-- -----------------------------------------------------------------------------
CREATE TABLE sync_logs (
  id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  source       VARCHAR(32)     NOT NULL COMMENT '数据源，如 mockhis',
  trigger_type ENUM('manual','cron') NOT NULL,
  total        INT             NOT NULL DEFAULT 0,
  succeeded    INT             NOT NULL DEFAULT 0,
  failed       INT             NOT NULL DEFAULT 0,
  status       ENUM('running','success','partial','failed') NOT NULL DEFAULT 'running',
  error_msg    VARCHAR(512)    NULL,
  trace_id     CHAR(32) CHARACTER SET ascii COLLATE ascii_bin NULL,
  started_at   DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  finished_at  DATETIME(3)     NULL,
  PRIMARY KEY (id),
  KEY idx_source_cursor (source, id DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci COMMENT='HIS同步日志';

-- =============================================================================
-- 初始数据
-- =============================================================================
INSERT INTO departments (code, name, sort_order) VALUES
  ('IM',   '内科',   10),
  ('SURG', '外科',   20),
  ('PED',  '儿科',   30),
  ('OBGY', '妇产科', 40);

-- 初始管理员：建议由 Go seed 程序读取 ADMIN_INIT_PASSWORD 环境变量动态生成 bcrypt，
-- 不要把 hash 硬编码进 SQL。

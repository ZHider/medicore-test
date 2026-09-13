/**
 * 后端 DTO 的 TypeScript 镜像。
 *
 * 字段名与可选性严格照抄 `backend/internal/model/*.go` 的 json tag：
 * - Go 侧带 `omitempty` 的字段 → TS 用 `?`（**不是** `| null`，后端是"字段消失"而非给 null）
 * - Go 侧 `*T` 且无 omitempty → 真的会给 `null`，TS 写 `| null`
 *
 * 枚举一律用字符串字面量联合：`tsconfig.app.json` 开了 `erasableSyntaxOnly`，
 * TS `enum` 会直接编译失败。
 */

// ---------------------------------------------------------------------------
// 通用信封与分页
// ---------------------------------------------------------------------------

/** 所有 /api/* 响应的统一信封（spec §3.3）。 */
export interface ApiEnvelope<T> {
  code: number
  message: string
  data: T
  trace_id: string
}

/**
 * 游标分页响应。禁止页码分页（spec §5.2）。
 * `next_cursor` 为 0 时后端不序列化该字段，所以是可选而非 null。
 */
export interface CursorPage<T> {
  items: T[]
  next_cursor?: number
  has_more: boolean
}

/** 不分页的列表包装（后端统一返回 {"items": [...]}）。 */
export interface ItemsWrapper<T> {
  items: T[]
}

/** 列表查询的公共参数。size 后端上限 100，超出静默截断。 */
export interface CursorQuery {
  cursor?: number | string
  size?: number
}

// ---------------------------------------------------------------------------
// 认证（/auth/*）
// ---------------------------------------------------------------------------

export type Role = 'admin' | 'doctor' | 'patient'

export interface RegisterRequest {
  /** 恰好 11 位全数字 */
  phone: string
  /** 2–32 字符 */
  name: string
  /** 8–64 字符 */
  password: string
  /** 可选，若填必须恰好 18 位 */
  id_card?: string
}

export interface LoginRequest {
  phone: string
  password: string
}

export interface TokenPair {
  access_token: string
  refresh_token: string
  token_type: string
  /** 秒 */
  expires_in: number
}

/** 返回给客户端的用户信息，phone 已脱敏（138****8888）。 */
export interface UserProfile {
  id: number
  role: Role
  name: string
  phone: string
}

export interface LogoutResult {
  status: string
}

// ---------------------------------------------------------------------------
// 目录：科室 / 医生 / 排班
// ---------------------------------------------------------------------------

export interface DepartmentBrief {
  id: number
  code: string
  name: string
  description?: string
}

export interface DoctorListItem {
  id: number
  name: string
  title: string
  specialty?: string
  /** DECIMAL 用字符串承载，切勿直接参与算术 */
  consult_fee: string
  department_id: number
  is_primary: boolean
}

export interface DoctorDetail {
  id: number
  name: string
  title: string
  specialty?: string
  intro?: string
  consult_fee: string
  departments: DepartmentBrief[]
}

export type TimeSlot = 'am' | 'pm'
export type ScheduleStatus = 'open' | 'closed' | 'cancelled'

export interface ScheduleListItem {
  id: number
  doctor_id: number
  department_id: number
  /** 纯日期 "2026-09-20"，没有时间部分 */
  sched_date: string
  time_slot: TimeSlot
  total: number
  remaining: number
  status: ScheduleStatus
}

// ---------------------------------------------------------------------------
// 预约与抢号
// ---------------------------------------------------------------------------

export type AppointmentStatus = 'pending' | 'confirmed' | 'cancelled' | 'expired'

export interface GrabRequest {
  schedule_id: number
  /** 8–64 字符；同一次提交的重试必须复用同一个 key */
  idempotency_key?: string
}

/** 患者视角的预约列表项。 */
export interface AppointmentItem {
  id: number
  schedule_id: number
  doctor_id: number
  doctor_name: string
  department_id: number
  department_name: string
  sched_date: string
  time_slot: TimeSlot
  /** omitempty：未签到时字段不存在 */
  queue_no?: number
  status: AppointmentStatus
  expire_at?: string
  created_at: string
}

/** 医生视角的预约列表项，与患者视角字段完全不同。 */
export interface DoctorAppointmentItem {
  id: number
  schedule_id: number
  patient_id: number
  patient_name: string
  department_id: number
  sched_date: string
  time_slot: TimeSlot
  queue_no?: number
  status: AppointmentStatus
  /** false = 还没写病历，医生工作台最该用起来的字段 */
  has_record: boolean
  created_at: string
}

/** 候诊队列项，只含已签到的预约。 */
export interface QueueItem {
  appointment_id: number
  queue_no: number
  patient_id: number
  /** 刻意不脱敏（医生必须能识别面前的患者） */
  patient_name: string
  checked_in_at: string
  status: AppointmentStatus
}

export interface CancelRequest {
  reason?: string
}

/** POST /appointments/:id/cancel 的 data */
export interface CancelResult {
  status: AppointmentStatus
}

/**
 * POST /appointments/:id/checkin 的 data。
 * 后端直接塞指针，未签到时为 null（这两个字段**不是** omitempty）。
 */
export interface CheckInResult {
  id: number
  queue_no: number | null
  checked_in_at: string | null
  status: AppointmentStatus
}

// ---------------------------------------------------------------------------
// AI 预问诊
// ---------------------------------------------------------------------------

export type ReportStatus = 'pending' | 'processing' | 'done' | 'failed'

export type Urgency = 'routine' | 'urgent' | 'emergency'

/** 结构化总结，字段结构由 backend/prompts/preconsult_summary_v1.json 定义，八项全必填。 */
export interface StructuredSummary {
  chief_complaint: string
  present_illness: string
  past_history: string
  medications: string
  allergies: string
  red_flags: string[]
  possible_departments: string[]
  urgency: Urgency
}

export type MessageRole = 'user' | 'assistant' | 'system'

export interface ConversationMessage {
  role: MessageRole
  content: string
}

export interface PreconsultReport {
  id: number
  appointment_id: number
  status: ReportStatus
  fallback: boolean
  /** fallback 或未完成时缺失 */
  structured?: StructuredSummary
  /**
   * json.RawMessage 且无 omitempty：字段一定在，但内容可能是 JSON null。
   * 所以是「可选且可为 null」，展示前必须判空。
   */
  conversation?: ConversationMessage[] | null
  llm_model: string
  prompt_version: string
  error_message?: string
  created_at: string
  finished_at?: string
}

/** POST /preconsult/submit 的 data（HTTP 202，code 仍是 0）。 */
export interface SubmitResult {
  report_id: number
  status: ReportStatus
  /** true 表示此前已提交过，本次未重复入队 —— 这是幂等，不是错误 */
  already_submitted: boolean
}

/** SSE 事件的载荷 */
export interface SseDeltaPayload {
  delta: string
}

export interface SseDonePayload {
  status: string
}

export interface SseErrorPayload {
  code: number
  message: string
}

// ---------------------------------------------------------------------------
// 病历与处方
// ---------------------------------------------------------------------------

export interface PrescriptionItemInput {
  drug_name: string
  spec?: string
  /** 1–999 */
  quantity: number
  usage_desc?: string
  /** 1–365 */
  days?: number
}

export interface WriteRecordRequest {
  chief_complaint?: string
  present_illness?: string
  /** 必填，2–512 */
  diagnosis: string
  treatment_advice?: string
  prescriptions?: PrescriptionItemInput[]
}

export interface PrescriptionItemView {
  drug_name: string
  spec?: string
  quantity: number
  usage_desc?: string
  days?: number
}

export interface MedicalRecordView {
  id: number
  appointment_id: number
  patient_id: number
  patient_name: string
  doctor_id: number
  doctor_name: string
  department_name: string
  sched_date: string
  chief_complaint?: string
  present_illness?: string
  diagnosis: string
  treatment_advice?: string
  created_at: string
  prescriptions: PrescriptionItemView[]
}

/** 列表项故意不含主诉/现病史/医嘱三段长文本。 */
export interface MedicalRecordListItem {
  id: number
  appointment_id: number
  patient_id: number
  patient_name: string
  doctor_id: number
  doctor_name: string
  department_name: string
  sched_date: string
  diagnosis: string
  created_at: string
}

// ---------------------------------------------------------------------------
// 管理端
// ---------------------------------------------------------------------------

export interface AuditLogView {
  id: number
  trace_id: string
  user_id?: number
  role?: string
  method: string
  path: string
  resource?: string
  resource_id?: number
  action?: string
  status_code: number
  latency_ms: number
  client_ip?: string
  created_at: string
}

export interface AuditLogQuery extends CursorQuery {
  resource?: string
  action?: string
  /**
   * 链路追踪号：32 位十六进制（大小写均可，后端会归一）。
   *
   * 与 `from` / `to` 一样，**格式错误会返回 40001**，不是静默忽略 ——
   * 忽略之后返回的是全部日志，会让人误以为这个追踪号关联了很多记录。
   */
  trace_id?: string
  /** YYYY-MM-DD 或 YYYY-MM-DD HH:MM:SS */
  from?: string
  to?: string
  /** 后端对非法值静默忽略，前端必须自己校验成数字 */
  user_id?: number
}

export type SyncStatus = 'running' | 'success' | 'partial' | 'failed'
export type SyncTriggerType = 'manual' | 'cron'

export interface SyncLogView {
  id: number
  source: string
  trigger_type: SyncTriggerType
  total: number
  succeeded: number
  failed: number
  status: SyncStatus
  error_message?: string
  started_at: string
  finished_at?: string
}

export interface SyncLogQuery extends CursorQuery {
  source?: string
}

// ---------------------------------------------------------------------------
// 运维探针
// ---------------------------------------------------------------------------

export interface CheckResult {
  status: 'ok' | 'down'
  /** 仅在该依赖下线时出现 */
  error?: string
}

/** GET /readyz 的 data 是**以依赖名为 key 的对象**，不是数组。 */
export type ReadyzData = Record<string, CheckResult>

export interface LivenessData {
  status: string
}

// ---------------------------------------------------------------------------
// 前端内部的展示辅助类型
// ---------------------------------------------------------------------------

/** 解析后的 Prometheus 样本 */
export interface MetricSample {
  name: string
  labels: Record<string, string>
  value: number
}

/** 统一的分页请求选项（配合 useCursorList） */
export interface PageFetchResult<T> {
  items: T[]
  nextCursor?: number
  hasMore: boolean
}

import { cleanParams, http } from './client'
import type {
  AppointmentItem,
  AppointmentStatus,
  CancelResult,
  CheckInResult,
  CursorPage,
  DoctorAppointmentItem,
  GrabRequest,
  ItemsWrapper,
  QueueItem,
} from '@/types/api'

/**
 * POST /appointments —— 抢号，限流 10 次/分钟（按用户，不是 IP）。
 * 幂等键由调用方生成：同一次提交的重试必须复用同一个 key。
 */
export function grab(payload: GrabRequest): Promise<AppointmentItem> {
  return http.post<AppointmentItem>('/appointments', payload)
}

/** GET /appointments —— 患者本人的预约，游标分页。 */
export function listMine(params: {
  cursor?: number
  size?: number
}): Promise<CursorPage<AppointmentItem>> {
  return http.get<CursorPage<AppointmentItem>>('/appointments', cleanParams({ ...params }))
}

/** POST /appointments/:id/cancel —— body 可为空。 */
export function cancel(appointmentId: number, reason?: string): Promise<CancelResult> {
  return http.post<CancelResult>(
    `/appointments/${appointmentId}/cancel`,
    reason ? { reason } : {},
  )
}

/** POST /appointments/:id/checkin —— 拿到 queue_no 与 checked_in_at。 */
export function checkIn(appointmentId: number): Promise<CheckInResult> {
  return http.post<CheckInResult>(`/appointments/${appointmentId}/checkin`)
}

/**
 * GET /doctor/appointments —— 医生 / 管理员视角。
 *
 * `status` 必须传白名单内的值（pending/confirmed/cancelled/expired），
 * 传别的后端会返回 400 —— 刻意的，拼错的状态被静默忽略会返回空列表，
 * 让人误以为"今天没有预约"。
 */
export function listForDoctor(params: {
  date?: string
  status?: AppointmentStatus | ''
  cursor?: number
  size?: number
}): Promise<CursorPage<DoctorAppointmentItem>> {
  return http.get<CursorPage<DoctorAppointmentItem>>(
    '/doctor/appointments',
    cleanParams({ ...params }),
  )
}

/**
 * GET /schedules/:id/queue —— 候诊队列（仅已签到）。
 * 做了资源层归属校验：换别人的排班 ID 会拿到 40302。
 */
export function getQueue(scheduleId: number): Promise<ItemsWrapper<QueueItem>> {
  return http.get<ItemsWrapper<QueueItem>>(`/schedules/${scheduleId}/queue`)
}

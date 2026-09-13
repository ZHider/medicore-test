import { http } from './client'
import type {
  ConversationMessage,
  ItemsWrapper,
  PreconsultReport,
  SubmitResult,
} from '@/types/api'

/** GET /appointments/:id/preconsult/history —— 刷新页面后可恢复上下文。 */
export function getHistory(appointmentId: number): Promise<ItemsWrapper<ConversationMessage>> {
  return http.get<ItemsWrapper<ConversationMessage>>(
    `/appointments/${appointmentId}/preconsult/history`,
  )
}

/**
 * POST /appointments/:id/preconsult/submit
 * HTTP 202，`code` 仍是 0 —— 判定成功请用 code，不要看状态码。
 * `already_submitted: true` 是幂等，不是错误，直接进入轮询。
 */
export function submit(appointmentId: number): Promise<SubmitResult> {
  return http.post<SubmitResult>(`/appointments/${appointmentId}/preconsult/submit`)
}

/** GET /appointments/:id/preconsult/report —— 轮询到终态。 */
export function getReport(appointmentId: number): Promise<PreconsultReport> {
  return http.get<PreconsultReport>(`/appointments/${appointmentId}/preconsult/report`)
}

import { cleanParams, http } from './client'
import type {
  CursorPage,
  MedicalRecordListItem,
  MedicalRecordView,
  WriteRecordRequest,
} from '@/types/api'

/** POST /appointments/:id/record —— 只有该预约的接诊医生能写；重复写返回 40004。 */
export function writeRecord(
  appointmentId: number,
  payload: WriteRecordRequest,
): Promise<MedicalRecordView> {
  return http.post<MedicalRecordView>(`/appointments/${appointmentId}/record`, payload)
}

/** GET /appointments/:id/record —— 患者本人 / 接诊医生 / 管理员可读。 */
export function getRecord(appointmentId: number): Promise<MedicalRecordView> {
  return http.get<MedicalRecordView>(`/appointments/${appointmentId}/record`)
}

/**
 * GET /records —— 后端按角色自动分流（患者看自己的、医生看自己写的、管理员看全部），
 * 前端不传任何身份参数。
 */
export function listRecords(params: {
  cursor?: number
  size?: number
}): Promise<CursorPage<MedicalRecordListItem>> {
  return http.get<CursorPage<MedicalRecordListItem>>('/records', cleanParams({ ...params }))
}

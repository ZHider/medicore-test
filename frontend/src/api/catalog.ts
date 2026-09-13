import { cleanParams, http } from './client'
import type {
  CursorPage,
  DepartmentBrief,
  DoctorDetail,
  DoctorListItem,
  ItemsWrapper,
  ScheduleListItem,
} from '@/types/api'

/** GET /departments */
export function listDepartments(): Promise<ItemsWrapper<DepartmentBrief>> {
  return http.get<ItemsWrapper<DepartmentBrief>>('/departments')
}

/** GET /departments/:id/doctors —— 服务端有 5 分钟 Redis 缓存。科室不存在返回 40003。 */
export function listDoctors(departmentId: number): Promise<ItemsWrapper<DoctorListItem>> {
  return http.get<ItemsWrapper<DoctorListItem>>(`/departments/${departmentId}/doctors`)
}

/**
 * GET /departments/:id/schedules
 * `date` 是必填的，务必始终传（不传后端会按"日期为空"查询）。
 */
export function listSchedulesByDepartment(
  departmentId: number,
  params: { date: string; cursor?: number; size?: number },
): Promise<CursorPage<ScheduleListItem>> {
  return http.get<CursorPage<ScheduleListItem>>(
    `/departments/${departmentId}/schedules`,
    cleanParams({ ...params }),
  )
}

/** GET /doctors/:id —— 含所属科室列表。 */
export function getDoctor(doctorId: number): Promise<DoctorDetail> {
  return http.get<DoctorDetail>(`/doctors/${doctorId}`)
}

/** GET /doctors/:id/schedules —— from/to 必填，且 from <= to（否则 400）。 */
export function listSchedulesByDoctor(
  doctorId: number,
  params: { from: string; to: string; cursor?: number; size?: number },
): Promise<CursorPage<ScheduleListItem>> {
  return http.get<CursorPage<ScheduleListItem>>(
    `/doctors/${doctorId}/schedules`,
    cleanParams({ ...params }),
  )
}

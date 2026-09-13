import { cleanParams, http } from './client'
import type {
  AuditLogQuery,
  AuditLogView,
  CursorPage,
  SyncLogQuery,
  SyncLogView,
} from '@/types/api'

/** GET /admin/audit-logs —— 支持 resource/action/trace_id/from/to/user_id + 游标分页。 */
export function listAuditLogs(query: AuditLogQuery): Promise<CursorPage<AuditLogView>> {
  return http.get<CursorPage<AuditLogView>>('/admin/audit-logs', cleanParams({ ...query }))
}

/**
 * POST /admin/sync/:source —— 同步返回（不是 202），不需要轮询。
 * `:source` 目前只接受 `mockhis`，其它值 404。
 */
export function triggerSync(source: string): Promise<SyncLogView> {
  return http.post<SyncLogView>(`/admin/sync/${encodeURIComponent(source)}`)
}

/** GET /admin/sync-logs */
export function listSyncLogs(query: SyncLogQuery): Promise<CursorPage<SyncLogView>> {
  return http.get<CursorPage<SyncLogView>>('/admin/sync-logs', cleanParams({ ...query }))
}

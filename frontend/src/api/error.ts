/**
 * 业务错误码与前端统一错误类型。
 *
 * 判定成功一律用 `code === 0`，**不要**用 HTTP 状态码：
 * 异步接口（POST .../submit）返回 202 但 code 仍是 0。
 */

export const ErrorCode = {
  Success: 0,

  // 400xx 业务校验
  InvalidParam: 40001,
  NoStock: 40002,
  NotFound: 40003,
  Conflict: 40004,
  AlreadyDone: 40005,
  MethodNotAllowed: 40006,

  // 401xx 认证
  Unauthorized: 40101,
  TokenExpired: 40102,
  RefreshInvalid: 40103,

  // 403xx 权限
  Forbidden: 40301,
  NotOwner: 40302,

  // 429xx 限流
  RateLimited: 42901,

  // 500xx 服务端
  Internal: 50001,
  NotReady: 50002,

  // 502xx LLM 上游
  LLMUpstream: 50201,

  /** 前端自造：请求根本没到后端（断网 / 超时 / 被代理拦掉） */
  Network: -1,
} as const

export type ErrorCodeValue = (typeof ErrorCode)[keyof typeof ErrorCode]

export interface ApiErrorInit {
  code: number
  message: string
  traceId?: string
  httpStatus?: number
  /** 仅 429 限流时存在，来自 Retry-After 响应头（秒） */
  retryAfter?: number
}

export class ApiError extends Error {
  readonly code: number
  readonly traceId: string
  readonly httpStatus: number
  readonly retryAfter?: number

  constructor(init: ApiErrorInit) {
    super(init.message)
    this.name = 'ApiError'
    this.code = init.code
    this.traceId = init.traceId ?? ''
    this.httpStatus = init.httpStatus ?? 0
    this.retryAfter = init.retryAfter
  }

  /** 带上 trace_id 的展示文案 —— 排障时用户截图即可定位到后端日志与 audit_logs。 */
  get displayMessage(): string {
    return this.traceId ? `${this.message}（追踪号 ${this.traceId}）` : this.message
  }
}

export function isApiError(value: unknown): value is ApiError {
  return value instanceof ApiError
}

/** 把任意异常转成可展示的文案。 */
export function errorMessage(value: unknown, fallback = '操作失败，请稍后重试'): string {
  if (isApiError(value)) return value.displayMessage
  if (value instanceof Error && value.message) return value.message
  return fallback
}

/** 把任意异常转成 ApiError，便于统一分支。 */
export function toApiError(value: unknown): ApiError {
  if (isApiError(value)) return value
  return new ApiError({
    code: ErrorCode.Network,
    message: value instanceof Error && value.message ? value.message : '操作失败，请稍后重试',
  })
}

/** 这个错误码代表"资源归属校验失败"而不是"角色不够"，文案要分开。 */
export function isOwnershipFailure(value: unknown): boolean {
  return isApiError(value) && value.code === ErrorCode.NotOwner
}

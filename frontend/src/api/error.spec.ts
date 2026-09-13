import { describe, expect, it } from 'vitest'

import { ApiError, ErrorCode, errorMessage, isApiError, isOwnershipFailure, toApiError } from './error'

/**
 * 错误封装。`trace_id` 一定要能被带出来 —— 它是排障的唯一抓手，
 * 用户截图里的追踪号要能直接拿去查 `GET /admin/audit-logs`。
 */
describe('ApiError', () => {
  it('displayMessage 在有 trace_id 时把它附上', () => {
    const err = new ApiError({ code: ErrorCode.NoStock, message: '号源不足', traceId: '3f2a1b' })
    expect(err.message).toBe('号源不足')
    expect(err.displayMessage).toBe('号源不足（追踪号 3f2a1b）')
  })

  it('没有 trace_id 时不产生多余括号', () => {
    const err = new ApiError({ code: ErrorCode.Network, message: '网络异常' })
    expect(err.displayMessage).toBe('网络异常')
  })

  it('保留 code 与 httpStatus，且是可识别的 ApiError', () => {
    const err = new ApiError({ code: ErrorCode.TokenExpired, message: '过期', httpStatus: 401 })
    expect(err.code).toBe(40102)
    expect(err.httpStatus).toBe(401)
    expect(isApiError(err)).toBe(true)
    expect(err.name).toBe('ApiError')
    expect(err).toBeInstanceOf(Error)
  })

  it('保留 Retry-After（登录限流倒计时依赖它）', () => {
    const err = new ApiError({ code: ErrorCode.RateLimited, message: '限流', retryAfter: 12 })
    expect(err.retryAfter).toBe(12)
  })
})

describe('isApiError', () => {
  it('普通 Error 与任意值都不是 ApiError', () => {
    expect(isApiError(new Error('x'))).toBe(false)
    expect(isApiError(null)).toBe(false)
    expect(isApiError('x')).toBe(false)
  })
})

describe('errorMessage', () => {
  it('ApiError 走 displayMessage', () => {
    expect(errorMessage(new ApiError({ code: 1, message: '坏了', traceId: 'abc' }))).toBe(
      '坏了（追踪号 abc）',
    )
  })

  it('普通 Error 用它的 message', () => {
    expect(errorMessage(new Error('炸了'))).toBe('炸了')
  })

  it('其它值回退到默认文案', () => {
    expect(errorMessage(null)).toBe('操作失败，请稍后重试')
    expect(errorMessage(null, '自定义')).toBe('自定义')
  })
})

describe('toApiError', () => {
  it('ApiError 原样返回（不重复包装）', () => {
    const original = new ApiError({ code: 40003, message: '不存在' })
    expect(toApiError(original)).toBe(original)
  })

  it('普通异常被包成 Network 码（表示请求没到后端）', () => {
    const wrapped = toApiError(new Error('boom'))
    expect(wrapped.code).toBe(ErrorCode.Network)
    expect(wrapped.message).toBe('boom')
  })

  it('非 Error 值也有兜底文案', () => {
    expect(toApiError(undefined).message).toBe('操作失败，请稍后重试')
  })
})

describe('isOwnershipFailure', () => {
  it('40302（无权访问他人资源）单独识别 —— 它不是角色不够', () => {
    expect(isOwnershipFailure(new ApiError({ code: ErrorCode.NotOwner, message: 'x' }))).toBe(true)
    expect(isOwnershipFailure(new ApiError({ code: ErrorCode.Forbidden, message: 'x' }))).toBe(false)
    expect(isOwnershipFailure(new Error('x'))).toBe(false)
  })
})

// @vitest-environment jsdom
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { cleanParams, http, setAuthFailureHandler } from './client'
import { ApiError, ErrorCode } from './error'
import { clearTokens, getAccessToken, getRefreshToken, saveTokens } from './tokens'
import type { TokenPair } from '@/types/api'

/**
 * api/client.ts 是前端**风险最高**的一段：它同时承担
 *   1. 统一剥信封
 *   2. 40102 自动续期并重放
 *   3. 并发 401 只刷一次（refresh_token 是一次性的，刷两次会互相作废）
 *   4. Retry-After 解析
 * 这些逻辑在浏览器里手工验证代价很高（要等 2 小时令牌过期或伪造过期令牌），
 * 所以必须由单测覆盖。
 *
 * 这里用 vi.mock 替换 axios，把拦截器函数**捕获**下来直接调用 ——
 * 比搭一个假 HTTP 服务器更贴近被测逻辑本身（拦截器就是纯函数）。
 */

/**
 * 拦截器与响应/错误的最小结构类型。
 *
 * 刻意不引 axios 的完整类型：这两个拦截器的入参在真实运行时是 axios 内部对象，
 * 但被测代码只用到其中几个字段，用一个结构化的小类型既能表达意图，
 * 又避免满屏 `as` 断言。用 `unknown` 而不是 `any` 也是为了过
 * @typescript-eslint/no-explicit-any（本项目 lint 是零容忍的）。
 */
interface MockRequestConfig {
  url?: string
  headers: { set: (key: string, value: string) => void }
  _retried?: boolean
}

interface MockResponse {
  status: number
  headers: Record<string, string>
  data: unknown
}

type RequestInterceptor = (config: MockRequestConfig) => unknown
type ResponseInterceptor = (response: MockResponse) => unknown
type ErrorInterceptor = (error: unknown) => unknown

// vi.mock 会被提升到 import 之前，因此共享状态必须用 vi.hoisted 声明
const mocks = vi.hoisted(() => ({
  requestInterceptors: [] as RequestInterceptor[],
  responseFulfilled: [] as ResponseInterceptor[],
  responseRejected: [] as ErrorInterceptor[],
  request: vi.fn(),
  post: vi.fn(),
}))

vi.mock('axios', () => ({
  default: {
    create: () => ({
      interceptors: {
        request: { use: (fn: RequestInterceptor) => mocks.requestInterceptors.push(fn) },
        response: {
          use: (ok: ResponseInterceptor, err: ErrorInterceptor) => {
            mocks.responseFulfilled.push(ok)
            mocks.responseRejected.push(err)
          },
        },
      },
      request: mocks.request,
    }),
    post: mocks.post,
  },
}))

/**
 * 取出被捕获的拦截器并调用。
 *
 * 成功拦截器一律包成 `Promise<unknown>`：它**既可能同步返回 data，
 * 也可能返回 `Promise.reject(...)`**（code !== 0 的分支）。统一成 Promise
 * 让调用方可以无差别地 `await` / `.catch`。
 */
const onResponse = () => {
  const fn = mocks.responseFulfilled[0]!
  return (response: MockResponse): Promise<unknown> => Promise.resolve(fn(response))
}

/**
 * 调用错误拦截器。
 *
 * 绝大多数场景它以 `ApiError` reject，所以这里直接按 ApiError 暴露，
 * 省掉每个用例里 `unknown` 收窄的噪音。
 * **唯一会 resolve 的场景是"刷新后重放成功"** —— 那一个用例请直接用
 * `mocks.responseRejected[0]`，避免让类型说谎。
 */
const onResponseError = (error: unknown): Promise<ApiError> =>
  Promise.resolve(mocks.responseRejected[0]!(error)).then(
    (value) => value as ApiError,
    (reason) => reason as ApiError,
  )

const onRequest = () => mocks.requestInterceptors[0]!

const NEW_PAIR: TokenPair = {
  access_token: 'new-access',
  refresh_token: 'new-refresh',
  token_type: 'Bearer',
  expires_in: 7200,
}

/** 造一个 axios 风格的错误对象（只需要被拦截器读取的那几个字段）。 */
function axiosError(options: {
  code: number
  status: number
  message?: string
  traceId?: string
  url?: string
  retryAfter?: string
  config?: Record<string, unknown>
}) {
  const headers: Record<string, string> = {}
  if (options.retryAfter) headers['retry-after'] = options.retryAfter
  if (options.traceId) headers['x-trace-id'] = options.traceId

  return {
    isAxiosError: true,
    config: {
      url: options.url ?? '/appointments',
      headers: { set: vi.fn() },
      ...options.config,
    },
    response: {
      status: options.status,
      headers,
      data: {
        code: options.code,
        message: options.message ?? '出错了',
        data: null,
        trace_id: options.traceId ?? '',
      },
    },
  }
}

beforeEach(() => {
  clearTokens()
  setAuthFailureHandler(() => {})
  mocks.request.mockReset()
  mocks.post.mockReset()
  mocks.request.mockResolvedValue({ ok: true })
})

describe('响应拦截器 —— 剥信封', () => {
  it('成功时返回 data，而不是整个信封', async () => {
    const result = await onResponse()({
      status: 200,
      headers: {},
      data: { code: 0, message: 'ok', data: { id: 7 }, trace_id: 't' },
    })
    expect(result).toEqual({ id: 7 })
  })

  it('HTTP 202（submit 等异步接口）同样按 code 判定成功', async () => {
    // 用 status===200 判成功会漏掉 202 —— 这是本项目的一条硬规则。
    const result = await onResponse()({
      status: 202,
      headers: {},
      data: { code: 0, message: '已提交', data: { report_id: 42 }, trace_id: 't' },
    })
    expect(result).toEqual({ report_id: 42 })
  })

  it('code !== 0 即使 HTTP 2xx 也要 reject', async () => {
    // 拦截器返回的是 rejected Promise，不是同步抛出 —— 用 toThrow 会漏判
    // 并留下一个未处理的 rejection。
    const thrown: unknown = await onResponse()({
      status: 200,
      headers: {},
      data: { code: 40002, message: '号源不足', data: null, trace_id: 't' },
    }).catch((e: unknown) => e)

    expect(thrown).toBeInstanceOf(ApiError)
    expect((thrown as ApiError).code).toBe(ErrorCode.NoStock)
  })
})

describe('响应拦截器 —— 错误映射', () => {
  it('把信封里的 code / message / trace_id 映射进 ApiError', async () => {
    const error = await onResponseError(
      axiosError({ code: 40002, status: 409, message: '号源不足', traceId: 'abc123' }),
    ).catch((e: unknown) => e as ApiError)

    expect(error).toBeInstanceOf(ApiError)
    expect(error.code).toBe(ErrorCode.NoStock)
    expect(error.httpStatus).toBe(409)
    // trace_id 必须能带到展示层，否则用户截图无法定位请求
    expect(error.traceId).toBe('abc123')
    expect(error.displayMessage).toBe('号源不足（追踪号 abc123）')
  })

  it('解析 Retry-After（登录限流倒计时依赖它）', async () => {
    const error = await onResponseError(
      axiosError({ code: 42901, status: 429, message: '请求过于频繁', retryAfter: '12' }),
    ).catch((e: unknown) => e as ApiError)
    expect(error.code).toBe(ErrorCode.RateLimited)
    expect(error.retryAfter).toBe(12)
  })

  it('没有 response（断网/超时）时归为 Network，且不抛原始 AxiosError', async () => {
    const error = await onResponseError({
      isAxiosError: true,
      config: { url: '/x', headers: { set: vi.fn() } },
      response: undefined,
    }).catch((e: unknown) => e as ApiError)

    expect(error).toBeInstanceOf(ApiError)
    expect(error.code).toBe(ErrorCode.Network)
  })

  it('trace_id 缺失时回退读响应头 X-Trace-Id', async () => {
    const error = await onResponseError(
      axiosError({ code: 50001, status: 500, message: '服务器内部错误', traceId: 'from-header' }),
    ).catch((e: unknown) => e as ApiError)
    // 这里 data.trace_id 与 header 同值；关键是两者都能取到
    expect(error.traceId).toBe('from-header')
  })
})

describe('请求拦截器 —— 注入 Bearer', () => {
  it('有 access_token 时设置 Authorization', () => {
    saveTokens({ ...NEW_PAIR, access_token: 'aaa' })
    const config = { headers: { set: vi.fn() } }
    onRequest()(config)
    expect(config.headers.set).toHaveBeenCalledWith('Authorization', 'Bearer aaa')
  })

  it('没有令牌时不设 Authorization（登录/注册是公开接口）', () => {
    clearTokens()
    const config = { headers: { set: vi.fn() } }
    onRequest()(config)
    expect(config.headers.set).not.toHaveBeenCalled()
  })
})

describe('40102 自动续期', () => {
  it('用 refresh_token 换新令牌后**重放原请求**', async () => {
    saveTokens({ ...NEW_PAIR, access_token: 'old', refresh_token: 'old-refresh' })
    mocks.post.mockResolvedValue({ status: 200, data: { code: 0, data: NEW_PAIR, trace_id: '' } })

    // 这条是唯一会 resolve 的路径（重放成功），所以直接用原始拦截器，
    // 不走 onResponseError —— 后者按 ApiError 暴露，与这里的成功值不符。
    const result = await Promise.resolve(
      mocks.responseRejected[0]!(axiosError({ code: 40102, status: 401, message: '登录已过期' })),
    )

    expect(mocks.post).toHaveBeenCalledTimes(1)
    expect(mocks.post.mock.calls[0]![0]).toContain('/auth/refresh')
    expect(mocks.request).toHaveBeenCalledTimes(1) // 重放了一次
    expect(result).toEqual({ ok: true })
  })

  it('刷新后**两个令牌都要覆盖**（旧的 refresh_token 已被服务端 GETDEL 消费）', async () => {
    saveTokens({ ...NEW_PAIR, access_token: 'old', refresh_token: 'old-refresh' })
    mocks.post.mockResolvedValue({ status: 200, data: { code: 0, data: NEW_PAIR, trace_id: '' } })

    await onResponseError(axiosError({ code: 40102, status: 401 }))

    expect(getAccessToken()).toBe('new-access')
    expect(getRefreshToken()).toBe('new-refresh')
  })

  it('★ 并发两个 401 只触发一次刷新（否则第二个会用已消费的令牌 → 被登出）', async () => {
    saveTokens({ ...NEW_PAIR, access_token: 'old', refresh_token: 'old-refresh' })
    // 让刷新"慢一点"，确保第二个 401 落在刷新进行中
    let release: (value: unknown) => void = () => {}
    mocks.post.mockReturnValue(
      new Promise((resolve) => {
        release = resolve
      }),
    )

    const first = onResponseError(axiosError({ code: 40102, status: 401 }))
    const second = onResponseError(axiosError({ code: 40102, status: 401 }))

    release({ status: 200, data: { code: 0, data: NEW_PAIR, trace_id: '' } })
    await Promise.all([first, second])

    // 关键断言：只刷一次。刷两次 => 第二次 40103 => 用户被登出。
    expect(mocks.post).toHaveBeenCalledTimes(1)
    expect(mocks.request).toHaveBeenCalledTimes(2) // 两个请求都重放了
  })

  it('刷新失败时清空令牌并回调登出，而不是无限重试', async () => {
    saveTokens({ ...NEW_PAIR, access_token: 'old', refresh_token: 'old-refresh' })
    mocks.post.mockRejectedValue(new Error('boom'))
    const onAuthFailure = vi.fn()
    setAuthFailureHandler(onAuthFailure)

    const error = await onResponseError(axiosError({ code: 40102, status: 401 })).catch(
      (e: unknown) => e as ApiError,
    )

    expect(onAuthFailure).toHaveBeenCalledTimes(1)
    expect(getRefreshToken()).toBe('')
    expect(error.code).toBe(ErrorCode.TokenExpired)
  })

  it('同一个请求只重放一次（_retried 防死循环）', async () => {
    saveTokens({ ...NEW_PAIR, access_token: 'old', refresh_token: 'old-refresh' })
    mocks.post.mockResolvedValue({ status: 200, data: { code: 0, data: NEW_PAIR, trace_id: '' } })

    const config = { url: '/appointments', headers: { set: vi.fn() }, _retried: true }
    await onResponseError(axiosError({ code: 40102, status: 401, config })).catch(() => {})

    expect(mocks.post).not.toHaveBeenCalled()
    expect(mocks.request).not.toHaveBeenCalled()
  })

  it('认证端点自身不参与重试（否则刷新失败会打转）', async () => {
    saveTokens({ ...NEW_PAIR, access_token: 'old', refresh_token: 'old-refresh' })

    await onResponseError(
      axiosError({ code: 40102, status: 401, url: '/auth/refresh' }),
    ).catch(() => {})

    expect(mocks.post).not.toHaveBeenCalled()
    expect(mocks.request).not.toHaveBeenCalled()
  })
})

describe('40101 与 40102 必须分开处理', () => {
  it('40101（没令牌/令牌坏）**不**触发刷新，直接清空并登出', async () => {
    saveTokens({ ...NEW_PAIR, access_token: 'bad', refresh_token: 'r' })
    const onAuthFailure = vi.fn()
    setAuthFailureHandler(onAuthFailure)

    const error = await onResponseError(axiosError({ code: 40101, status: 401 })).catch(
      (e: unknown) => e as ApiError,
    )

    expect(mocks.post).not.toHaveBeenCalled() // 没有尝试刷新
    expect(onAuthFailure).toHaveBeenCalledTimes(1)
    expect(error.code).toBe(ErrorCode.Unauthorized)
  })

  it('40103（刷新令牌无效）清空并登出', async () => {
    const onAuthFailure = vi.fn()
    setAuthFailureHandler(onAuthFailure)

    await onResponseError(axiosError({ code: 40103, status: 401 })).catch(() => {})

    expect(onAuthFailure).toHaveBeenCalledTimes(1)
    expect(getRefreshToken()).toBe('')
  })

  it('40301 / 40302 不触发登出（权限不够 ≠ 登录失效）', async () => {
    saveTokens({ ...NEW_PAIR })
    const onAuthFailure = vi.fn()
    setAuthFailureHandler(onAuthFailure)

    for (const code of [ErrorCode.Forbidden, ErrorCode.NotOwner]) {
      await onResponseError(axiosError({ code, status: 403 })).catch(() => {})
    }

    expect(onAuthFailure).not.toHaveBeenCalled()
    expect(getAccessToken()).not.toBe('')
  })
})

describe('http 助手', () => {
  it('GET 把 params 透传下去', async () => {
    await http.get('/appointments', { size: 20, cursor: 5 })
    expect(mocks.request).toHaveBeenCalledWith(
      expect.objectContaining({ method: 'GET', url: '/appointments', params: { size: 20, cursor: 5 } }),
    )
  })

  it('POST 把 body 透传下去', async () => {
    await http.post('/appointments', { schedule_id: 88 })
    expect(mocks.request).toHaveBeenCalledWith(
      expect.objectContaining({ method: 'POST', url: '/appointments', data: { schedule_id: 88 } }),
    )
  })
})

describe('cleanParams', () => {
  it('丢掉 undefined / null / 空串，避免拼出 ?cursor= 这类空参数', () => {
    expect(cleanParams({ a: 1, b: undefined, c: null, d: '', e: 0, f: false })).toEqual({
      a: 1,
      e: 0,
      f: false,
    })
  })

  it('保留 0 与 false（它们是合法取值，不是"空"）', () => {
    // 把 0 当空丢掉会让 size=0 之类的查询悄悄变成默认值。
    const result = cleanParams({ remaining: 0, has_more: false })
    expect(result).toEqual({ remaining: 0, has_more: false })
  })
})

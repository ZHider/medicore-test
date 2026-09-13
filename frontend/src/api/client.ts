/**
 * axios 实例与拦截器。
 *
 * 三个硬约束（都来自后端契约，写错会出真问题）：
 *
 * 1. **baseURL 是相对路径** `/api/v1`。后端没有 CORS 中间件，直连 127.0.0.1:8080
 *    会被浏览器同源策略全部拦掉；生产环境是 nginx 同源反代，相对路径两边都成立。
 * 2. **刷新触发器是 `code === 40102`，不是 HTTP 401**。"刷新令牌也失效"时同样是 401，
 *    用 HTTP 状态做判据会陷入无限刷新循环。
 * 3. **刷新必须去重**。refresh_token 是一次性的（服务端 GETDEL），
 *    并发 401 各刷一次会让第二个请求拿到 40103 然后被登出。
 */

import axios from 'axios'
import type { AxiosError, AxiosRequestConfig, InternalAxiosRequestConfig } from 'axios'

import type { ApiEnvelope, TokenPair } from '@/types/api'
import { ApiError, ErrorCode, isApiError } from './error'
import { clearTokens, getAccessToken, getRefreshToken, saveTokens } from './tokens'

export const API_BASE = '/api/v1'

type RetriableConfig = InternalAxiosRequestConfig & { _retried?: boolean }

/** 认证失效时的回调（由 router 注册，避免 api 层反向依赖 router 造成循环引用）。 */
let authFailureHandler: (() => void) | null = null

export function setAuthFailureHandler(handler: () => void): void {
  authFailureHandler = handler
}

function notifyAuthFailure(): void {
  clearTokens()
  authFailureHandler?.()
}

export const instance = axios.create({
  baseURL: API_BASE,
  timeout: 20000,
  headers: { 'Content-Type': 'application/json' },
})

// ---------------------------------------------------------------------------
// 请求拦截：注入 Bearer
// ---------------------------------------------------------------------------

instance.interceptors.request.use((config) => {
  const token = getAccessToken()
  if (token) {
    config.headers.set('Authorization', `Bearer ${token}`)
  }
  return config
})

// ---------------------------------------------------------------------------
// 刷新令牌：模块级共享 Promise 做去重
// ---------------------------------------------------------------------------

let refreshing: Promise<void> | null = null

async function performRefresh(): Promise<void> {
  const refreshToken = getRefreshToken()
  if (!refreshToken) {
    throw new ApiError({
      code: ErrorCode.RefreshInvalid,
      message: '登录状态已失效，请重新登录',
      httpStatus: 401,
    })
  }

  // 用裸 axios：刷新请求自己不能再走这套拦截器，否则失败时会递归刷新
  const res = await axios.post<ApiEnvelope<TokenPair>>(
    `${API_BASE}/auth/refresh`,
    { refresh_token: refreshToken },
    { timeout: 15000 },
  )

  const body = res.data
  if (!body || body.code !== ErrorCode.Success || !body.data) {
    throw new ApiError({
      code: body?.code ?? ErrorCode.RefreshInvalid,
      message: body?.message || '登录已过期，请重新登录',
      traceId: body?.trace_id ?? '',
      httpStatus: res.status,
    })
  }

  saveTokens(body.data)
}

function ensureRefresh(): Promise<void> {
  if (!refreshing) {
    // finally 在微任务里执行，赋值已完成，不存在竞态
    refreshing = performRefresh().finally(() => {
      refreshing = null
    })
  }
  return refreshing
}

/**
 * 供 SSE 等**绕过 axios** 的调用方手动续期。
 *
 * 流式对话用 fetch（EventSource 带不了请求体与 Authorization），
 * 因此享受不到响应拦截器的自动刷新；令牌过期时它需要自己触发一次续期再重试。
 * 复用同一个共享 Promise，保证不会与 axios 侧的刷新各刷一次。
 */
export function refreshAccessToken(): Promise<void> {
  return ensureRefresh()
}

// ---------------------------------------------------------------------------
// 响应拦截
// ---------------------------------------------------------------------------

function parseRetryAfter(headers: unknown): number | undefined {
  const raw = (headers as Record<string, unknown> | undefined)?.['retry-after']
  if (raw === undefined || raw === null) return undefined
  const seconds = Number(raw)
  return Number.isFinite(seconds) && seconds > 0 ? seconds : undefined
}

function toApiError(error: AxiosError<ApiEnvelope<unknown>>): ApiError {
  if (error.code === 'ECONNABORTED' || error.code === 'ETIMEDOUT') {
    return new ApiError({ code: ErrorCode.Network, message: '请求超时，请稍后重试' })
  }

  const response = error.response
  if (!response) {
    return new ApiError({ code: ErrorCode.Network, message: '网络异常，请检查网络后重试' })
  }

  const body = response.data
  const headerTrace = (response.headers as Record<string, unknown> | undefined)?.['x-trace-id']
  const code = typeof body?.code === 'number' ? body.code : ErrorCode.Network

  return new ApiError({
    code,
    message: body?.message || '请求失败，请稍后重试',
    traceId: body?.trace_id || (typeof headerTrace === 'string' ? headerTrace : ''),
    httpStatus: response.status,
    retryAfter: parseRetryAfter(response.headers),
  })
}

/** 认证端点自身不参与"过期重试"，否则刷新失败会打转。 */
function isAuthEndpoint(url?: string): boolean {
  if (!url) return false
  return url.includes('/auth/refresh') || url.includes('/auth/login') || url.includes('/auth/register')
}

instance.interceptors.response.use(
  (response) => {
    const body = response.data as ApiEnvelope<unknown> | undefined

    // 后端失败响应都带非 2xx 状态码，理论上走不到这里；
    // 但信封里 code !== 0 就一定是失败，多判一次比漏判安全。
    if (body && typeof body === 'object' && 'code' in body && body.code !== ErrorCode.Success) {
      return Promise.reject(
        new ApiError({
          code: body.code,
          message: body.message || '请求失败，请稍后重试',
          traceId: body.trace_id,
          httpStatus: response.status,
          retryAfter: parseRetryAfter(response.headers),
        }),
      )
    }

    // 统一剥掉信封，调用方直接拿 data
    return (body ? body.data : undefined) as never
  },
  async (error: unknown) => {
    if (isApiError(error)) return Promise.reject(error)

    const axiosError = error as AxiosError<ApiEnvelope<unknown>>
    const apiError = toApiError(axiosError)
    const config = axiosError.config as RetriableConfig | undefined

    // 只有 40102（登录已过期）才触发刷新。401（凭证无效 / 没令牌）不触发。
    if (
      apiError.code === ErrorCode.TokenExpired &&
      config &&
      !config._retried &&
      !isAuthEndpoint(config.url)
    ) {
      config._retried = true
      try {
        await ensureRefresh()
        // 重放原请求；请求拦截器会注入刷新后的新 access_token
        return await instance.request(config)
      } catch {
        notifyAuthFailure()
        return Promise.reject(
          new ApiError({
            code: ErrorCode.TokenExpired,
            message: '登录已过期，请重新登录',
            httpStatus: 401,
          }),
        )
      }
    }

    // 没令牌 / 令牌坏 / 刷新令牌无效 → 清干净并回登录页
    if (
      apiError.code === ErrorCode.Unauthorized ||
      apiError.code === ErrorCode.RefreshInvalid
    ) {
      notifyAuthFailure()
    }

    return Promise.reject(apiError)
  },
)

// ---------------------------------------------------------------------------
// 类型化的请求助手
// ---------------------------------------------------------------------------

async function request<T>(config: AxiosRequestConfig): Promise<T> {
  return (await instance.request(config)) as unknown as T
}

export const http = {
  get<T>(url: string, params?: Record<string, unknown>, config?: AxiosRequestConfig): Promise<T> {
    return request<T>({ ...config, method: 'GET', url, params })
  },
  post<T>(url: string, data?: unknown, config?: AxiosRequestConfig): Promise<T> {
    return request<T>({ ...config, method: 'POST', url, data })
  },
  put<T>(url: string, data?: unknown, config?: AxiosRequestConfig): Promise<T> {
    return request<T>({ ...config, method: 'PUT', url, data })
  },
  delete<T>(url: string, config?: AxiosRequestConfig): Promise<T> {
    return request<T>({ ...config, method: 'DELETE', url })
  },
}

/** 去掉值为 undefined / 空串的查询参数，避免拼出 `?cursor=` 这种空串参数。 */
export function cleanParams(params: Record<string, unknown>): Record<string, unknown> {
  const result: Record<string, unknown> = {}
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue
    result[key] = value
  }
  return result
}

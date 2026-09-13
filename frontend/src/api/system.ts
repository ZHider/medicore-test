/**
 * 运维端点（免认证，且**不在** `/api/v1` 下）。
 *
 * 这三个端点刻意绕开 `client.ts` 的拦截器：
 * - `/readyz` 依赖下线时返回 503，但 `data` 里仍带着逐项明细（这才是运维要看的），
 *   走统一拦截器会把这次响应变成"只有错误码、没有明细"的 ApiError。
 * - `/metrics` 是 Prometheus **纯文本**，不是 JSON 信封。
 */

import axios from 'axios'

import type { ApiEnvelope, ReadyzData } from '@/types/api'

export interface ReadyzResult {
  code: number
  message: string
  data: ReadyzData
  httpStatus: number
}

/** GET /readyz —— 任一依赖不可用即 503，但明细始终完整。 */
export async function fetchReadyz(): Promise<ReadyzResult> {
  const res = await axios.get<ApiEnvelope<ReadyzData>>('/readyz', {
    timeout: 10000,
    // 503 也要把 body 读出来
    validateStatus: () => true,
  })

  const body = res.data
  return {
    code: typeof body?.code === 'number' ? body.code : -1,
    message: body?.message ?? '未知错误',
    // data 是「以依赖名为 key 的对象」，不是数组
    data: body?.data ?? {},
    httpStatus: res.status,
  }
}

/** GET /healthz —— 存活探针，只判进程是否活着。 */
export async function fetchHealthz(): Promise<boolean> {
  try {
    const res = await axios.get<ApiEnvelope<{ status: string }>>('/healthz', { timeout: 5000 })
    return res.data?.code === 0
  } catch {
    return false
  }
}

/** GET /metrics —— Prometheus 文本格式。 */
export async function fetchMetrics(): Promise<string> {
  const res = await axios.get<string>('/metrics', {
    timeout: 10000,
    responseType: 'text',
    // 关掉 axios 的 JSON 解析尝试，原样拿文本
    transformResponse: [(data: unknown) => data],
  })
  return typeof res.data === 'string' ? res.data : String(res.data)
}

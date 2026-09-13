/**
 * 生成幂等键（spec §8.2：前端 axios 注入 uuid）。
 *
 * `crypto.randomUUID` 只在**安全上下文**（https / localhost）可用，
 * 通过 IP 访问的局域网调试页面里它是 undefined，所以要留回退。
 * 长度必须落在后端要求的 8–64 之间。
 */
export function createIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
    return crypto.randomUUID()
  }
  const random = Math.random().toString(36).slice(2, 12)
  return `idem-${Date.now().toString(36)}-${random}`
}

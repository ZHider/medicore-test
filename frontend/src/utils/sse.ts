/**
 * Server-Sent Events 解析。
 *
 * 为什么不用 `EventSource`：它只支持 GET，**无法携带请求体与 Authorization 头**，
 * 而预问诊对话是 `POST /appointments/:id/preconsult/chat`。
 * 所以必须用 fetch + ReadableStream 自己解析（spec §8.1 也是这么要求的）。
 */

export interface SseEvent {
  event: string
  /** `data:` 可能有多行，按规范用 \n 连接 */
  data: string
}

/** 解析一个事件块（块内不含空行）。 */
export function parseSseBlock(block: string): SseEvent | null {
  const lines = block.split('\n')
  let event = ''
  const dataLines: string[] = []

  for (const line of lines) {
    // 空行是事件分隔符；以 ':' 开头的是注释（可用于心跳）
    if (!line || line.startsWith(':')) continue

    if (line.startsWith('event:')) {
      event = line.slice(6).trim()
    } else if (line.startsWith('data:')) {
      // 规范：冒号后可有一个空格，需要去掉
      dataLines.push(line.slice(5).replace(/^ /, ''))
    }
    // id: / retry: 本项目不使用，忽略
  }

  if (!event && dataLines.length === 0) return null
  return { event: event || 'message', data: dataLines.join('\n') }
}

/**
 * 逐块读取 SSE 流并回调。
 *
 * 按 `\n\n` 切分而不是按行直接处理：一个事件块可能跨多个 chunk 到达，
 * 必须缓冲到出现空行才能确定块已完整。
 */
export async function readSseStream(
  body: ReadableStream<Uint8Array> | null,
  onEvent: (event: SseEvent) => void,
): Promise<void> {
  if (!body) throw new Error('响应正文不可读')

  const reader = body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break

      // 统一换行：\r\n 可能被拆到两个 chunk，所以每次拼接后都规范化一次
      buffer += decoder.decode(value, { stream: true })
      buffer = buffer.replace(/\r\n/g, '\n')

      let separator = buffer.indexOf('\n\n')
      while (separator !== -1) {
        const block = buffer.slice(0, separator)
        buffer = buffer.slice(separator + 2)
        const parsed = parseSseBlock(block)
        if (parsed) onEvent(parsed)
        separator = buffer.indexOf('\n\n')
      }
    }

    // 流以不带空行的最后一个块结尾时要补发一次
    const tail = parseSseBlock(buffer)
    if (tail) onEvent(tail)
  } finally {
    reader.releaseLock()
  }
}

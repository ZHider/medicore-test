import { onUnmounted, ref, shallowRef, triggerRef } from 'vue'
import type { Ref } from 'vue'

import { API_BASE, refreshAccessToken } from '@/api/client'
import { ApiError, ErrorCode } from '@/api/error'
import * as preconsultApi from '@/api/preconsult'
import { getAccessToken } from '@/api/tokens'
import type { ApiEnvelope, ConversationMessage, MessageRole } from '@/types/api'
import { readSseStream } from '@/utils/sse'

export interface ChatMessage {
  role: MessageRole
  content: string
  /** 正在流式接收中 */
  pending?: boolean
  /** 该条消息来自失败的请求，UI 上用不同样式 */
  failed?: boolean
}

/**
 * AI 预问诊流式对话。
 *
 * 三个必须做对的地方：
 * 1. 用 fetch 而不是 EventSource（要带请求体与 Authorization）；
 * 2. 错误有两种形态 —— 流未开始是普通 JSON 信封，流已开始只有 `event: error`，
 *    后端 `startStream()` 延迟发响应头正是为了让前置校验失败还能返回统一信封；
 * 3. 组件卸载必须 `AbortController.abort()`，否则后端会一直往上游 LLM 请求，白烧配额。
 */
export function usePreconsultChat(appointmentId: Ref<number | null>) {
  const messages = shallowRef<ChatMessage[]>([])
  const streaming = ref(false)
  const loadingHistory = ref(false)
  const errorText = ref('')

  let controller: AbortController | null = null

  function patchMessage(index: number, patch: Partial<ChatMessage>): void {
    const current = messages.value[index]
    if (!current) return
    messages.value[index] = { ...current, ...patch }
    // shallowRef：必须手动触发，否则视图不会更新
    triggerRef(messages)
  }

  function appendMessage(message: ChatMessage): number {
    messages.value = [...messages.value, message]
    return messages.value.length - 1
  }

  /** 恢复历史对话（刷新页面后仍能接着聊）。 */
  async function loadHistory(): Promise<void> {
    const id = appointmentId.value
    if (!id) return

    loadingHistory.value = true
    try {
      const result = await preconsultApi.getHistory(id)
      messages.value = (result.items ?? [])
        // 历史里只有 user / assistant，system 只是防御性过滤
        .filter((m: ConversationMessage) => m.role !== 'system')
        .map((m: ConversationMessage) => ({ role: m.role, content: m.content }))
    } catch {
      // 历史拉不到不应该挡住对话，静默降级为空列表
      messages.value = []
    } finally {
      loadingHistory.value = false
    }
  }

  function authHeaders(): Record<string, string> {
    const token = getAccessToken()
    return {
      'Content-Type': 'application/json',
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    }
  }

  async function openStream(text: string, index: number, retried = false): Promise<void> {
    const id = appointmentId.value
    if (!id) return

    controller = new AbortController()

    const response = await fetch(`${API_BASE}/appointments/${id}/preconsult/chat`, {
      method: 'POST',
      headers: authHeaders(),
      body: JSON.stringify({ message: text }),
      signal: controller.signal,
    })

    const contentType = response.headers.get('content-type') ?? ''

    // 流还没开始：前置校验失败（预约不存在 / 不属于你 / 状态不允许），
    // 后端返回的是**普通 JSON 信封**而不是 event-stream。
    if (!response.ok || !contentType.includes('text/event-stream')) {
      const body = (await response.json().catch(() => null)) as ApiEnvelope<unknown> | null
      const code = typeof body?.code === 'number' ? body.code : ErrorCode.Network

      // 令牌在对话途中过期：手动续期一次再重试（fetch 享受不到 axios 拦截器）
      if (code === ErrorCode.TokenExpired && !retried) {
        try {
          await refreshAccessToken()
          return await openStream(text, index, true)
        } catch {
          throw new ApiError({
            code: ErrorCode.TokenExpired,
            message: '登录已过期，请重新登录',
            httpStatus: 401,
          })
        }
      }

      throw new ApiError({
        code,
        message: body?.message || '对话请求失败，请稍后重试',
        traceId: body?.trace_id ?? '',
        httpStatus: response.status,
      })
    }

    await readSseStream(response.body, ({ event, data }) => {
      let payload: unknown
      try {
        payload = JSON.parse(data)
      } catch {
        return
      }

      if (event === 'delta') {
        const delta = (payload as { delta?: string }).delta ?? ''
        const current = messages.value[index]
        patchMessage(index, { content: (current?.content ?? '') + delta, pending: true })
        return
      }

      if (event === 'done') {
        patchMessage(index, { pending: false })
        return
      }

      if (event === 'error') {
        const err = payload as { code?: number; message?: string }
        const detail = err.message || 'AI 服务暂时不可用'
        const current = messages.value[index]
        // 已经吐了一部分内容就保留，把错误挂在末尾
        patchMessage(index, {
          pending: false,
          failed: true,
          content: current?.content ? `${current.content}\n\n[对话中断] ${detail}` : detail,
        })
        errorText.value = detail
      }
    })
  }

  async function send(rawText: string): Promise<void> {
    const text = rawText.trim()
    if (!text || streaming.value || !appointmentId.value) return

    errorText.value = ''
    appendMessage({ role: 'user', content: text })
    const assistantIndex = appendMessage({ role: 'assistant', content: '', pending: true })

    streaming.value = true
    try {
      await openStream(text, assistantIndex)
    } catch (e) {
      // 用户主动中断不算错误，把未完成的占位清掉
      if (e instanceof DOMException && e.name === 'AbortError') {
        const current = messages.value[assistantIndex]
        if (!current?.content) {
          messages.value = messages.value.filter((_, i) => i !== assistantIndex)
        } else {
          patchMessage(assistantIndex, { pending: false })
        }
        return
      }

      const apiError = e instanceof ApiError ? e : null
      const detail = apiError?.message ?? '对话失败，请稍后重试'
      const current = messages.value[assistantIndex]
      patchMessage(assistantIndex, {
        pending: false,
        failed: true,
        content: current?.content ? `${current.content}\n\n[对话中断] ${detail}` : detail,
      })
      errorText.value = detail
    } finally {
      streaming.value = false
      controller = null
    }
  }

  /** 中止当前流。组件卸载时也会调用（spec §8.1 硬要求）。 */
  function abort(): void {
    if (!controller) return
    controller.abort()
    controller = null
    streaming.value = false
    const last = messages.value[messages.value.length - 1]
    if (last?.pending) {
      messages.value[messages.value.length - 1] = { ...last, pending: false }
      triggerRef(messages)
    }
  }

  function clear(): void {
    abort()
    messages.value = []
    errorText.value = ''
  }

  onUnmounted(abort)

  return {
    messages,
    streaming,
    loadingHistory,
    errorText,
    loadHistory,
    send,
    abort,
    clear,
  }
}

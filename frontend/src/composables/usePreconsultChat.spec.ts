// @vitest-environment jsdom
import { mount } from '@vue/test-utils'
import { defineComponent, ref } from 'vue'
import type { Ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { getHistory } from '@/api/preconsult'
import { clearTokens, saveTokens } from '@/api/tokens'
import { usePreconsultChat } from './usePreconsultChat'

vi.mock('@/api/preconsult', () => ({
  getHistory: vi.fn(),
  submit: vi.fn(),
  getReport: vi.fn(),
}))

const getHistoryMock = vi.mocked(getHistory)

/**
 * usePreconsultChat 是 AI 对话页的状态机：历史恢复、SSE 打字机、中止、两种错误形态。
 *
 * 它必须在**组件上下文**里跑（内部有 `onUnmounted(abort)`），所以用 mount 包一层。
 * 这样"卸载即中止流"这条 spec §8.1 的硬要求也能被真正验证 ——
 * 不只是"代码里写了 abort"，而是"卸载时确实调到了"。
 */
function exists(stream: ReadableStream<Uint8Array>, headers: Record<string, string> = {}) {
  return new Response(stream, {
    status: 200,
    headers: { 'content-type': 'text/event-stream', ...headers },
  })
}

/** 把若干字符串当作 SSE 分片依次推送。 */
function sseResponse(chunks: string[]): Response {
  const encoder = new TextEncoder()
  return exists(
    new ReadableStream<Uint8Array>({
      start(controller) {
        for (const chunk of chunks) controller.enqueue(encoder.encode(chunk))
        controller.close()
      },
    }),
  )
}

const delta = (text: string) => `event: delta\ndata: ${JSON.stringify({ delta: text })}\n\n`
const done = 'event: done\ndata: {"status":"ok"}\n\n'
const errorEvent = (code: number, message: string) =>
  `event: error\ndata: ${JSON.stringify({ code, message })}\n\n`

/** 在组件里挂载 composable，返回结果与卸载函数。 */
function setupChat(id: number | null = 501) {
  const appointmentId: Ref<number | null> = ref(id)
  let chat!: ReturnType<typeof usePreconsultChat>

  const wrapper = mount(
    defineComponent({
      setup() {
        chat = usePreconsultChat(appointmentId)
        return () => null
      },
    }),
  )

  return { chat, wrapper, appointmentId }
}

beforeEach(() => {
  clearTokens()
  saveTokens({
    access_token: 'access-1',
    refresh_token: 'refresh-1',
    token_type: 'Bearer',
    expires_in: 7200,
  })
  getHistoryMock.mockReset()
  getHistoryMock.mockResolvedValue({ items: [] })
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('usePreconsultChat —— 历史恢复', () => {
  it('恢复 user / assistant 两种角色', async () => {
    getHistoryMock.mockResolvedValue({
      items: [
        { role: 'user', content: '我发烧三天了' },
        { role: 'assistant', content: '请问最高体温多少？' },
      ],
    })
    const { chat, wrapper } = setupChat()

    await chat.loadHistory()

    expect(chat.messages.value).toEqual([
      { role: 'user', content: '我发烧三天了' },
      { role: 'assistant', content: '请问最高体温多少？' },
    ])
    wrapper.unmount()
  })

  it('过滤掉 system 消息（契约上历史里只有前两种，这是防御）', async () => {
    getHistoryMock.mockResolvedValue({
      items: [
        { role: 'system', content: '你是医疗助手' },
        { role: 'user', content: '你好' },
      ],
    })
    const { chat, wrapper } = setupChat()

    await chat.loadHistory()

    expect(chat.messages.value).toHaveLength(1)
    expect(chat.messages.value[0]?.role).toBe('user')
    wrapper.unmount()
  })

  it('拉历史失败时静默降级为空列表（不能挡住对话）', async () => {
    getHistoryMock.mockRejectedValue(new Error('boom'))
    const { chat, wrapper } = setupChat()

    await chat.loadHistory()

    expect(chat.messages.value).toEqual([])
    expect(chat.loadingHistory.value).toBe(false)
    wrapper.unmount()
  })

  it('没有 appointmentId 时不发请求', async () => {
    const { chat, wrapper } = setupChat(null)
    await chat.loadHistory()
    expect(getHistoryMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})

describe('usePreconsultChat —— 发送与打字机', () => {
  it('把多个 delta 片段拼成一条完整回复', async () => {
    const fetchMock = vi.fn().mockResolvedValue(sseResponse([delta('您'), delta('好，'), delta('请描述症状'), done]))
    vi.stubGlobal('fetch', fetchMock)

    const { chat, wrapper } = setupChat()
    await chat.send('我发烧了')

    expect(chat.messages.value).toHaveLength(2)
    expect(chat.messages.value[0]).toEqual({ role: 'user', content: '我发烧了' })
    expect(chat.messages.value[1]?.role).toBe('assistant')
    expect(chat.messages.value[1]?.content).toBe('您好，请描述症状')
    expect(chat.messages.value[1]?.pending).toBe(false)
    expect(chat.streaming.value).toBe(false)
    wrapper.unmount()
  })

  it('delta 跨分片到达也能正确拼接（真实网络就是这样）', async () => {
    // 一个事件块被切成两段
    const fetchMock = vi.fn().mockResolvedValue(
      sseResponse(['event: del', 'ta\ndata: {"del', 'ta":"好"}\n\n', done]),
    )
    vi.stubGlobal('fetch', fetchMock)

    const { chat, wrapper } = setupChat()
    await chat.send('hi')

    expect(chat.messages.value[1]?.content).toBe('好')
    wrapper.unmount()
  })

  it('请求带 Authorization 与消息体，且打的是 SSE 端点', async () => {
    const fetchMock = vi.fn().mockResolvedValue(sseResponse([done]))
    vi.stubGlobal('fetch', fetchMock)

    const { chat, wrapper } = setupChat(501)
    await chat.send('  我发烧了  ') // 前后空格应被 trim

    const [url, init] = fetchMock.mock.calls[0]!
    expect(url).toBe('/api/v1/appointments/501/preconsult/chat')
    expect(init.method).toBe('POST')
    expect(init.headers.Authorization).toBe('Bearer access-1')
    expect(JSON.parse(init.body)).toEqual({ message: '我发烧了' })
    wrapper.unmount()
  })

  it('空白输入被忽略（不产生气泡）', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    const { chat, wrapper } = setupChat()
    await chat.send('   ')

    expect(chat.messages.value).toEqual([])
    expect(fetchMock).not.toHaveBeenCalled()
    wrapper.unmount()
  })
})

describe('usePreconsultChat —— 两种错误形态', () => {
  it('① 流**还没开始**：普通 JSON 信封（HTTP 4xx）', async () => {
    // 后端把 startStream() 拖到第一次真要吐数据时，就是为了这种场景还能返回统一信封
    const fetchMock = vi.fn().mockResolvedValue(
      new Response(
        JSON.stringify({ code: 40302, message: '无权访问他人资源', data: null, trace_id: 'tid-1' }),
        { status: 403, headers: { 'content-type': 'application/json' } },
      ),
    )
    vi.stubGlobal('fetch', fetchMock)

    const { chat, wrapper } = setupChat()
    await chat.send('你好')

    expect(chat.errorText.value).toContain('无权访问他人资源')
    // 助手气泡应标记为失败，且不留 pending
    const assistant = chat.messages.value[1]
    expect(assistant?.failed).toBe(true)
    expect(assistant?.pending).toBe(false)
    wrapper.unmount()
  })

  it('② 流**已经开始**：HTTP 200 + event: error（上游出错）', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValue(sseResponse([delta('好的'), errorEvent(50201, 'AI 服务暂时不可用')]))
    vi.stubGlobal('fetch', fetchMock)

    const { chat, wrapper } = setupChat()
    await chat.send('你好')

    // 已吐出的内容必须保留，错误追加在后面 —— 否则用户会觉得系统把话吞了
    const assistant = chat.messages.value[1]
    expect(assistant?.content).toContain('好的')
    expect(assistant?.content).toContain('AI 服务暂时不可用')
    expect(assistant?.failed).toBe(true)
    expect(chat.errorText.value).toBe('AI 服务暂时不可用')
    wrapper.unmount()
  })

  it('网络异常（fetch 直接 reject）也要落到气泡上', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')))

    const { chat, wrapper } = setupChat()
    await chat.send('你好')

    expect(chat.messages.value[1]?.failed).toBe(true)
    expect(chat.streaming.value).toBe(false)
    wrapper.unmount()
  })
})

describe('usePreconsultChat —— 中止与卸载', () => {
  it('★ 组件卸载时会 abort（spec §8.1 硬要求，否则后端一直往上游 LLM 请求）', async () => {
    const abortSpy = vi.spyOn(AbortController.prototype, 'abort')
    // 让流永远不结束，模拟"用户中途离开页面"
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        exists(
          new ReadableStream<Uint8Array>({
            start(controller) {
              controller.enqueue(new TextEncoder().encode(delta('你好')))
              // 故意不 close
            },
          }),
        ),
      ),
    )

    const { chat, wrapper } = setupChat()
    void chat.send('hi') // 不 await：流还在进行中

    await vi.waitFor(() => expect(chat.streaming.value).toBe(true))
    wrapper.unmount()

    expect(abortSpy).toHaveBeenCalled()
    abortSpy.mockRestore()
  })

  it('手动 abort 后 streaming 归位，且不留下 pending 气泡', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue(
        exists(
          new ReadableStream<Uint8Array>({
            start(controller) {
              controller.enqueue(new TextEncoder().encode(delta('部分内容')))
              // 不 close
            },
          }),
        ),
      ),
    )

    const { chat, wrapper } = setupChat()
    void chat.send('hi')
    await vi.waitFor(() => expect(chat.streaming.value).toBe(true))

    chat.abort()

    expect(chat.streaming.value).toBe(false)
    expect(chat.messages.value.some((m) => m.pending)).toBe(false)
    wrapper.unmount()
  })

  it('clear 清空消息与错误', async () => {
    getHistoryMock.mockResolvedValue({ items: [{ role: 'user', content: '旧消息' }] })
    const { chat, wrapper } = setupChat()
    await chat.loadHistory()
    expect(chat.messages.value).toHaveLength(1)

    chat.clear()

    expect(chat.messages.value).toEqual([])
    expect(chat.errorText.value).toBe('')
    wrapper.unmount()
  })
})

import { describe, expect, it } from 'vitest'

import { parseSseBlock, readSseStream } from './sse'

/**
 * 把一串字符串按给定顺序喂给解析器，用来模拟**网络分片**。
 *
 * 这是这个模块最值得测的地方：SSE 的一个事件块可能被 TCP 切成任意多段到达，
 * 按行直接处理会在分片边界上出错，而那种错很难在本地复现
 * （本机请求小、几乎总是整块到达）。
 */
function streamOf(chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder()
  let index = 0
  return new ReadableStream<Uint8Array>({
    pull(controller) {
      if (index >= chunks.length) {
        controller.close()
        return
      }
      controller.enqueue(encoder.encode(chunks[index]))
      index += 1
    },
  })
}

async function collect(chunks: string[]): Promise<Array<{ event: string; data: string }>> {
  const events: Array<{ event: string; data: string }> = []
  await readSseStream(streamOf(chunks), (event) => events.push(event))
  return events
}

describe('parseSseBlock', () => {
  it('解析 event 与 data', () => {
    expect(parseSseBlock('event: delta\ndata: {"delta":"您"}')).toEqual({
      event: 'delta',
      data: '{"delta":"您"}',
    })
  })

  it('data 冒号后的一个空格要去掉，但内容里的空格保留', () => {
    const parsed = parseSseBlock('event: delta\ndata: {"delta": " 好"}')
    expect(parsed?.data).toBe('{"delta": " 好"}')
  })

  it('多行 data 按规范用 \\n 连接', () => {
    expect(parseSseBlock('event: msg\ndata: line1\ndata: line2')?.data).toBe('line1\nline2')
  })

  it('忽略注释行（心跳）', () => {
    expect(parseSseBlock(': keep-alive\nevent: done\ndata: {}')).toEqual({
      event: 'done',
      data: '{}',
    })
  })

  it('没有 event 字段时回退为 message', () => {
    expect(parseSseBlock('data: x')?.event).toBe('message')
  })

  it('空块返回 null', () => {
    expect(parseSseBlock('')).toBeNull()
    expect(parseSseBlock('   ')).toBeNull()
    expect(parseSseBlock(': 只有注释')).toBeNull()
  })
})

describe('readSseStream', () => {
  it('整块到达时按顺序回调', async () => {
    const events = await collect([
      'event: delta\ndata: {"delta":"您"}\n\n' + 'event: done\ndata: {"status":"ok"}\n\n',
    ])
    expect(events).toEqual([
      { event: 'delta', data: '{"delta":"您"}' },
      { event: 'done', data: '{"status":"ok"}' },
    ])
  })

  it('事件块被切成多个分片时仍然正确（核心用例）', async () => {
    // 把 event 名、data 行、以及结尾空行分别切开，模拟最坏情况
    const events = await collect([
      'event: del',
      'ta\ndata: {"del',
      'ta":"好"}\n',
      '\n',
    ])
    expect(events).toEqual([{ event: 'delta', data: '{"delta":"好"}' }])
  })

  it('分片恰好落在 \\r\\n 中间也能正确处理', async () => {
    const events = await collect(['event: delta\r', '\ndata: {"delta":"a"}\r\n\r\n'])
    expect(events).toEqual([{ event: 'delta', data: '{"delta":"a"}' }])
  })

  it('兼容 \\r\\n 换行', async () => {
    const events = await collect(['event: done\r\ndata: {"status":"ok"}\r\n\r\n'])
    expect(events).toEqual([{ event: 'done', data: '{"status":"ok"}' }])
  })

  it('流以「没有结尾空行」的块收尾时也要补发', async () => {
    // 后端正常会写 \n\n，但中途断线时最后一块可能没有空行。
    // 丢掉它会让前端的打字机少最后一段文字。
    const events = await collect(['event: delta\ndata: {"delta":"尾巴"}'])
    expect(events).toEqual([{ event: 'delta', data: '{"delta":"尾巴"}' }])
  })

  it('多个事件挤在一个分片里全部回调', async () => {
    const chunk =
      'event: delta\ndata: {"delta":"a"}\n\n' +
      'event: delta\ndata: {"delta":"b"}\n\n' +
      'event: done\ndata: {"status":"ok"}\n\n'
    const events = await collect([chunk])
    expect(events.map((e) => e.event)).toEqual(['delta', 'delta', 'done'])
  })

  it('多字节 UTF-8 字符被切成两半时不产生乱码', async () => {
    // 这是 TextDecoder 必须用 stream:true 的原因。
    // "好" 的 UTF-8 是 3 字节 (E5 A5 BD)，这里故意切在第 1、2 字节之间 ——
    // 逐段独立解码会把两半各自变成替换字符 U+FFFD。
    const bytes = new TextEncoder().encode('event: delta\ndata: {"delta":"好"}\n\n')
    const cut = bytes.indexOf(0xe5) + 1 // 切在"好"的第 1 个字节之后

    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(bytes.slice(0, cut))
        controller.enqueue(bytes.slice(cut))
        controller.close()
      },
    })

    const events: Array<{ event: string; data: string }> = []
    await readSseStream(stream, (e) => events.push(e))

    expect(events).toEqual([{ event: 'delta', data: '{"delta":"好"}' }])
    expect(events[0]?.data).not.toContain('\uFFFD')
  })

  it('body 为 null 时抛错（调用方要能提示"响应不可读"）', async () => {
    await expect(readSseStream(null, () => {})).rejects.toThrow('响应正文不可读')
  })
})

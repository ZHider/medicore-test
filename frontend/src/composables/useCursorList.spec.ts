import { describe, expect, it, vi } from 'vitest'

import { useCursorList } from './useCursorList'
import type { CursorPage } from '@/types/api'

/**
 * useCursorList 是所有列表页的地基（我的预约 / 病历 / 审计日志 / 同步记录）。
 *
 * 它承载着后端契约里最容易写错的一条：
 * **判断"还有没有下一页"必须用 `has_more`，不能用 `items.length === size`。**
 * 最后一页恰好满页时后者会永远多请求一次 —— 这个错误在 UI 上表现为
 * "滚到底又闪了一下"，几乎不可能靠人工发现。
 *
 * 因为 fetcher 是**注入**的（不在内部写死 axios），这里不需要任何 mock：
 * 直接传一个记账的假函数即可 —— 这也是当初把 fetcher 做成参数的好处。
 */

interface Row {
  id: number
}

function page(ids: number[], hasMore: boolean, nextCursor?: number): CursorPage<Row> {
  return {
    items: ids.map((id) => ({ id })),
    has_more: hasMore,
    ...(nextCursor === undefined ? {} : { next_cursor: nextCursor }),
  }
}

describe('useCursorList —— 首页', () => {
  it('immediate=false 时不会自动请求', () => {
    const fetcher = vi.fn()
    const list = useCursorList<Row>(fetcher, { immediate: false })
    expect(fetcher).not.toHaveBeenCalled()
    expect(list.items.value).toEqual([])
    expect(list.loaded.value).toBe(false)
  })

  it('immediate（默认）会自动拉第一页', async () => {
    const fetcher = vi.fn().mockResolvedValue(page([1, 2, 3], false))
    const list = useCursorList<Row>(fetcher)

    await vi.waitFor(() => expect(list.loaded.value).toBe(true))
    expect(fetcher).toHaveBeenCalledTimes(1)
    expect(list.items.value.map((r) => r.id)).toEqual([1, 2, 3])
  })

  it('首页**不带 cursor**（传 undefined 而不是 0）', async () => {
    const fetcher = vi.fn().mockResolvedValue(page([1], false))
    const list = useCursorList<Row>(fetcher, { size: 20, immediate: false })
    await list.loadMore()

    expect(fetcher).toHaveBeenCalledWith({ cursor: undefined, size: 20 })
  })
})

describe('useCursorList —— 翻页与终止判据', () => {
  it('has_more 为 true 时继续拉，并带上上一页的 next_cursor', async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(page([3, 2, 1], true, 1))
      .mockResolvedValueOnce(page([0], false))

    const list = useCursorList<Row>(fetcher, { immediate: false })
    await list.loadMore()
    expect(list.finished.value).toBe(false)

    await list.loadMore()
    expect(fetcher).toHaveBeenLastCalledWith({ cursor: 1, size: 20 })
    expect(list.items.value.map((r) => r.id)).toEqual([3, 2, 1, 0])
    expect(list.finished.value).toBe(true)
  })

  it('★ 最后一页恰好满页时也必须停（这正是不能用 items.length === size 的原因）', async () => {
    // size=3 且后端正好返回 3 条，但 has_more=false —— "满页"不等于"还有下一页"
    const fetcher = vi.fn().mockResolvedValue(page([3, 2, 1], false))
    const list = useCursorList<Row>(fetcher, { size: 3, immediate: false })

    await list.loadMore()

    expect(list.items.value).toHaveLength(3)
    expect(list.finished.value).toBe(true)

    // 再调一次不应该产生请求
    await list.loadMore()
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('has_more 为 true 但 next_cursor 缺失时也停（omitempty 字段会消失）', async () => {
    // 契约上不该出现，但真出现时若不停，会发出 cursor=undefined 的请求并无限循环
    const fetcher = vi.fn().mockResolvedValue(page([1], true))
    const list = useCursorList<Row>(fetcher, { immediate: false })

    await list.loadMore()

    expect(list.finished.value).toBe(true)
    await list.loadMore()
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('加载中重复调用不会并发重复请求', async () => {
    let release: (value: CursorPage<Row>) => void = () => {}
    const fetcher = vi.fn().mockReturnValue(
      new Promise<CursorPage<Row>>((resolve) => {
        release = resolve
      }),
    )

    const list = useCursorList<Row>(fetcher, { immediate: false })
    const first = list.loadMore()
    const second = list.loadMore() // 应被 loading 挡住

    release(page([1], false))
    await Promise.all([first, second])

    expect(fetcher).toHaveBeenCalledTimes(1)
  })
})

describe('useCursorList —— 出错', () => {
  it('出错时记录 error 并**停止继续加载**（否则无限滚动会反复打失败的请求）', async () => {
    const fetcher = vi.fn().mockRejectedValue(new Error('boom'))
    const list = useCursorList<Row>(fetcher, { immediate: false })

    await list.loadMore()

    expect(list.error.value).toBeTruthy()
    expect(list.finished.value).toBe(true)
    expect(list.loading.value).toBe(false)

    await list.loadMore()
    expect(fetcher).toHaveBeenCalledTimes(1)
  })

  it('error 被规范化成 ApiError（展示层依赖 displayMessage）', async () => {
    const fetcher = vi.fn().mockRejectedValue(new Error('network down'))
    const list = useCursorList<Row>(fetcher, { immediate: false })
    await list.loadMore()

    const err = list.error.value as { message?: string } | null
    expect(err?.message).toBe('network down')
  })
})

describe('useCursorList —— refresh 与 reset', () => {
  it('refresh 清空重来（筛选条件变化时用），且首页不带 cursor', async () => {
    const fetcher = vi
      .fn()
      .mockResolvedValueOnce(page([3, 2, 1], true, 1))
      .mockResolvedValueOnce(page([9, 8], false))

    const list = useCursorList<Row>(fetcher, { immediate: false })
    await list.loadMore()
    expect(list.items.value).toHaveLength(3)

    await list.refresh()

    expect(list.items.value.map((r) => r.id)).toEqual([9, 8])
    expect(fetcher).toHaveBeenLastCalledWith({ cursor: undefined, size: 20 })
    expect(list.finished.value).toBe(true)
  })

  it('refresh 会清掉上一次的 error（重试要能恢复）', async () => {
    const fetcher = vi
      .fn()
      .mockRejectedValueOnce(new Error('boom'))
      .mockResolvedValueOnce(page([1], false))

    const list = useCursorList<Row>(fetcher, { immediate: false })
    await list.loadMore()
    expect(list.error.value).toBeTruthy()

    await list.refresh()
    expect(list.error.value).toBeNull()
    expect(list.items.value).toHaveLength(1)
  })

  it('reset 只清状态不发请求', async () => {
    const fetcher = vi.fn().mockResolvedValue(page([1], false))
    const list = useCursorList<Row>(fetcher, { immediate: false })
    await list.loadMore()

    list.reset()

    expect(list.items.value).toEqual([])
    expect(list.loaded.value).toBe(false)
    expect(list.finished.value).toBe(false)
    expect(fetcher).toHaveBeenCalledTimes(1)
  })
})

describe('useCursorList —— 筛选参数', () => {
  it('params() 每次请求都会重新求值（改筛选后 refresh 能生效）', async () => {
    let status = 'pending'
    const fetcher = vi.fn().mockResolvedValue(page([1], false))

    const list = useCursorList<Row, { status: string }>(fetcher, {
      immediate: false,
      params: () => ({ status }),
    })

    await list.loadMore()
    expect(fetcher).toHaveBeenLastCalledWith({ status: 'pending', cursor: undefined, size: 20 })

    status = 'confirmed'
    await list.refresh()
    expect(fetcher).toHaveBeenLastCalledWith({ status: 'confirmed', cursor: undefined, size: 20 })
  })
})

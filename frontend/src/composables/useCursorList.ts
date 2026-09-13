import { ref, shallowRef } from 'vue'
import type { Ref } from 'vue'

import { toApiError } from '@/api/error'
import type { CursorPage } from '@/types/api'

export interface UseCursorListOptions<P> {
  /** 每页条数，后端上限 100（超出静默截断）。 */
  size?: number
  /** 每次请求都要带上的筛选参数（惰性求值，便于改筛选后 refresh）。 */
  params?: () => P
  /** 是否在创建时立刻加载第一页。 */
  immediate?: boolean
}

/**
 * 游标分页列表（后端禁止 page/offset，spec §5.2）。
 *
 * 三个关键点：
 * - 判断"还有下一页"用 `has_more`，**不是** `items.length === size`
 *   （最后一页恰好满页时后者会永远多请求一次）；
 * - `next_cursor` 是 omitempty，缺失时字段直接消失，不要假设它存在；
 * - 不在前端做去重后拼接 —— 游标天然不重复，出现重复说明 cursor 传错了。
 *
 * 约束写成 `P extends object` 而不是 `Record<string, unknown>`：
 * TS 的 interface（如 AuditLogQuery）**没有隐式索引签名**，用后者会让它不满足约束，
 * 类型参数静默回退到默认值，排查起来非常费解。
 */
export function useCursorList<T, P extends object = Record<string, unknown>>(
  fetcher: (query: P & { cursor?: number; size?: number }) => Promise<CursorPage<T>>,
  options: UseCursorListOptions<P> = {},
) {
  const size = options.size ?? 20
  // shallowRef：列表项通常是后端原样返回的对象，没必要做深层响应式代理
  const items = shallowRef<T[]>([])
  const loading = ref(false)
  const finished = ref(false)
  const loaded = ref(false)
  const error = ref<unknown>(null)

  let cursor = 0

  async function loadMore(): Promise<void> {
    if (loading.value || finished.value) return

    loading.value = true
    error.value = null

    try {
      const extra = (options.params?.() ?? {}) as P
      const page = await fetcher({
        ...extra,
        cursor: cursor || undefined,
        size,
      })

      items.value = cursor ? [...items.value, ...page.items] : page.items
      cursor = page.next_cursor ?? 0
      finished.value = !page.has_more || !page.next_cursor
      loaded.value = true
    } catch (e) {
      error.value = toApiError(e)
      // 出错时停下，避免无限滚动反复打同一个失败请求
      finished.value = true
    } finally {
      loading.value = false
    }
  }

  /** 清空并重新拉第一页（筛选条件变化、抢号成功后刷新号源等场景）。 */
  async function refresh(): Promise<void> {
    cursor = 0
    finished.value = false
    loaded.value = false
    items.value = []
    await loadMore()
  }

  function reset(): void {
    cursor = 0
    finished.value = false
    loaded.value = false
    items.value = []
    error.value = null
  }

  if (options.immediate !== false) {
    void loadMore()
  }

  return { items, loading, finished, loaded, error, loadMore, refresh, reset }
}

/**
 * 用 Intersection Observer 做无限滚动（spec §8.1 明确要求）。
 * 传入哨兵元素引用，进入视口时触发 onLoad。
 */
export function useInfiniteScroll(
  target: Ref<HTMLElement | null>,
  onLoad: () => void,
  enabled: Ref<boolean>,
) {
  let observer: IntersectionObserver | null = null

  function disconnect(): void {
    observer?.disconnect()
    observer = null
  }

  function connect(): void {
    disconnect()
    const el = target.value
    if (!el || typeof IntersectionObserver === 'undefined') return

    observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting && enabled.value) onLoad()
        }
      },
      { rootMargin: '160px 0px' },
    )
    observer.observe(el)
  }

  return { connect, disconnect }
}

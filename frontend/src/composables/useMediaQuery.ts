import { onUnmounted, readonly, ref } from 'vue'
import type { Ref } from 'vue'

/**
 * 响应式媒体查询。仅在"布局必须由 JS 决定"的地方使用
 * （例如 Element Plus 表格 vs 卡片列表的取舍、对话框是否全屏）。
 * 纯样式差异一律交给 CSS 媒体查询，不要在 JS 里重复一遍。
 */
export function useMediaQuery(query: string): Readonly<Ref<boolean>> {
  const matches = ref(false)

  if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
    const mql = window.matchMedia(query)
    matches.value = mql.matches

    const handler = (event: MediaQueryListEvent) => {
      matches.value = event.matches
    }
    mql.addEventListener('change', handler)
    onUnmounted(() => mql.removeEventListener('change', handler))
  }

  return readonly(matches)
}

/** 是否手机宽度（< 768px，与 style.css 的断点保持一致）。 */
export function useIsMobile(): Readonly<Ref<boolean>> {
  return useMediaQuery('(max-width: 767.98px)')
}

/** 是否窄屏/平板（< 1024px），用于决定是否用抽屉导航。 */
export function useIsCompact(): Readonly<Ref<boolean>> {
  return useMediaQuery('(max-width: 1023.98px)')
}

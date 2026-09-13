<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'

// 按需引入 echarts：整包 `import * as echarts from 'echarts'` 会把所有图表类型、
// 坐标系与渲染器全部打进产物（实测看板 chunk 超过 1MB）。
// 这里只注册实际用到的三种图与三种组件，CanvasRenderer 是唯一用到的渲染器。
import * as echarts from 'echarts/core'
import { BarChart, LineChart, PieChart } from 'echarts/charts'
import { GridComponent, LegendComponent, TooltipComponent } from 'echarts/components'
import { CanvasRenderer } from 'echarts/renderers'
import type { EChartsType } from 'echarts/core'
import type { EChartsOption } from 'echarts'

echarts.use([
  BarChart,
  LineChart,
  PieChart,
  GridComponent,
  LegendComponent,
  TooltipComponent,
  CanvasRenderer,
])

/**
 * ECharts 容器。
 *
 * 两个必须处理的点：
 * - 容器尺寸变化（侧栏收起、窗口旋转）必须 resize，否则图表会被拉花；
 *   用 ResizeObserver 而不是只监听 window.resize —— 后者漏掉"父容器变化但窗口没变"的情况。
 * - 组件卸载要 dispose，否则实例与监听器泄漏。
 */
const props = withDefaults(
  defineProps<{
    option: EChartsOption
    height?: string
    loading?: boolean
  }>(),
  { height: '', loading: false },
)

const el = ref<HTMLDivElement | null>(null)
const chart = shallowRef<EChartsType | null>(null)
let observer: ResizeObserver | null = null

function render(): void {
  chart.value?.setOption(props.option, true)
}

onMounted(() => {
  if (!el.value) return
  chart.value = echarts.init(el.value)

  if (typeof ResizeObserver !== 'undefined') {
    observer = new ResizeObserver(() => chart.value?.resize())
    observer.observe(el.value)
  }
  render()
})

onBeforeUnmount(() => {
  observer?.disconnect()
  observer = null
  chart.value?.dispose()
  chart.value = null
})

watch(() => props.option, render, { deep: true })

watch(
  () => props.loading,
  (value) => {
    if (value) chart.value?.showLoading('default', { text: '加载中', color: '#0d9488' })
    else chart.value?.hideLoading()
  },
)
</script>

<template>
  <div
    ref="el"
    class="mc-chart"
    :style="height ? { height } : undefined"
  />
</template>

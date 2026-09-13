<script setup lang="ts">
import { computed } from 'vue'

/**
 * 状态标签。把后端各套枚举统一映射成中文 + 配色。
 * 后端状态值一律是字符串字面量，这里集中一处映射，避免各页面各写一套。
 */
type Tone = 'gray' | 'green' | 'teal' | 'blue' | 'orange' | 'red'

const MAP: Record<string, { label: string; tone: Tone }> = {
  // 预约
  pending: { label: '待确认', tone: 'orange' },
  confirmed: { label: '已确认', tone: 'green' },
  cancelled: { label: '已取消', tone: 'gray' },
  expired: { label: '已过期', tone: 'gray' },
  // 排班
  open: { label: '可预约', tone: 'green' },
  closed: { label: '已停诊', tone: 'gray' },
  // AI 报告
  processing: { label: '生成中', tone: 'blue' },
  done: { label: '已完成', tone: 'green' },
  failed: { label: '失败', tone: 'red' },
  // HIS 同步
  running: { label: '进行中', tone: 'blue' },
  success: { label: '成功', tone: 'green' },
  partial: { label: '部分成功', tone: 'orange' },
  // 通用
  ok: { label: '正常', tone: 'green' },
  down: { label: '不可用', tone: 'red' },
}

const props = withDefaults(
  defineProps<{
    value?: string | null
    /** 覆盖文案（例如 fallback: done 但降级时要显示"降级"） */
    label?: string
    tone?: Tone
  }>(),
  {},
)

const entry = computed(() => (props.value ? MAP[props.value] : undefined))
const text = computed(() => props.label ?? entry.value?.label ?? props.value ?? '—')
const tone = computed<Tone>(() => props.tone ?? entry.value?.tone ?? 'gray')
</script>

<template>
  <span
    class="mc-tag"
    :class="`mc-tag--${tone}`"
  >{{ text }}</span>
</template>

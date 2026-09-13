<script setup lang="ts">
import { computed } from 'vue'

import AppIcon from './AppIcon.vue'
import EmptyState from './EmptyState.vue'
import StatusTag from './StatusTag.vue'
import type { PreconsultReport, StructuredSummary, Urgency } from '@/types/api'

/**
 * AI 预问诊报告面板。
 *
 * 必须处理的三种终态（写错任何一种都会白屏或误导）：
 * | 情况 | 判据 | 展示 |
 * | 正常 | status==='done' && !fallback | structured |
 * | 降级 | status==='done' && fallback  | structured 是 null，改展示原始 conversation 并标注 |
 * | 失败 | status==='failed'             | error_message + 重试入口 |
 */
const props = defineProps<{
  report: PreconsultReport | null
  loading?: boolean
}>()

const URGENCY: Record<Urgency, { label: string; tone: 'green' | 'orange' | 'red'; hint: string }> = {
  routine: { label: '常规', tone: 'green', hint: '可常规就诊' },
  urgent: { label: '紧急', tone: 'orange', hint: '建议尽快就诊' },
  emergency: { label: '危重', tone: 'red', hint: '请立即就医或呼叫急救' },
}

// status 不再默认成 'pending'：report 为 null 与"后端说正在生成"是两回事。
// 之前 `?? 'pending'` 会让"报告还没拉到"显示成"AI 正在生成结构化总结" ——
// 若这次请求其实是失败了，用户会一直等一个永远不会来的结果。
const status = computed(() => props.report?.status ?? null)
const isPending = computed(() => status.value === 'pending' || status.value === 'processing')
const isFallback = computed(() => status.value === 'done' && props.report?.fallback === true)
const isDone = computed(() => status.value === 'done' && !props.report?.fallback)
const structured = computed<StructuredSummary | null>(() => props.report?.structured ?? null)
const conversation = computed(() => props.report?.conversation ?? [])
const urgency = computed(() => (structured.value ? URGENCY[structured.value.urgency] : null))

/** 结构化字段的展示顺序与中文标签 */
const FIELDS = [
  { key: 'chief_complaint', label: '主诉' },
  { key: 'present_illness', label: '现病史' },
  { key: 'past_history', label: '既往史' },
  { key: 'medications', label: '用药情况' },
  { key: 'allergies', label: '过敏史' },
] as const

function fieldValue(key: (typeof FIELDS)[number]['key']): string {
  const value = structured.value?.[key]
  return value && value.trim() ? value : '—'
}
</script>

<template>
  <div class="report">
    <!-- 报告还没拿到：区分"正在加载"与"确实没有" -->
    <div
      v-if="!report"
      class="mc-card"
    >
      <el-skeleton
        v-if="loading"
        :rows="5"
        animated
      />
      <EmptyState
        v-else
        title="暂无报告数据"
        description="请先完成预问诊对话并提交"
      />
    </div>

    <!-- 生成中（后端明确说 pending / processing） -->
    <div
      v-else-if="isPending"
      class="mc-card pending"
    >
      <el-skeleton
        :rows="5"
        animated
      />
      <div class="pending-hint">
        <el-icon class="is-loading">
          <AppIcon
            name="clock"
            :size="16"
          />
        </el-icon>
        <span>AI 正在生成结构化总结，通常几秒内完成…</span>
      </div>
    </div>

    <!-- 失败 -->
    <div
      v-else-if="status === 'failed'"
      class="mc-card"
    >
      <EmptyState
        icon="alert"
        title="AI 总结生成失败"
        :description="report?.error_message || '上游服务暂时不可用，可稍后重试'"
      >
        <slot name="retry" />
      </EmptyState>
    </div>

    <!-- 降级：structured 为 null，展示原始对话 -->
    <template v-else-if="isFallback">
      <el-alert
        type="warning"
        show-icon
        :closable="false"
        title="AI 未能结构化总结"
        description="本次总结已降级为原始对话记录，以下内容未经结构化整理，仅供参考。"
      />
      <div class="mc-card">
        <div class="mc-card-title">
          <span>原始对话记录</span>
          <span class="mc-card-sub">{{ conversation.length }} 条</span>
        </div>
        <div
          v-if="conversation.length"
          class="raw-chat"
        >
          <div
            v-for="(msg, index) in conversation"
            :key="index"
            class="raw-row"
          >
            <div
              class="raw-role"
              :class="`raw-role--${msg.role}`"
            >
              {{ msg.role === 'user' ? '患者' : 'AI' }}
            </div>
            <div class="raw-content">
              {{ msg.content }}
            </div>
          </div>
        </div>
        <EmptyState
          v-else
          title="没有可展示的对话记录"
        />
      </div>
    </template>

    <!-- 正常 -->
    <template v-else-if="isDone && structured">
      <div class="mc-card">
        <div class="head">
          <div>
            <div class="head-title">
              AI 结构化预问诊总结
            </div>
            <div class="mc-card-sub">
              {{ report?.llm_model }} · {{ report?.prompt_version }} ·
              {{ report?.finished_at || report?.created_at }}
            </div>
          </div>
          <StatusTag
            v-if="urgency"
            :value="structured.urgency"
            :label="`紧急度：${urgency.label}`"
            :tone="urgency.tone"
          />
        </div>

        <!-- 危急征象必须高亮：它们可能是危及生命的症状 -->
        <div
          v-if="structured.red_flags?.length"
          class="redflags"
        >
          <div class="redflags-title">
            <AppIcon
              name="alert"
              :size="16"
            />
            <span>危险信号（{{ structured.red_flags.length }}）</span>
          </div>
          <div class="redflags-list">
            <span
              v-for="flag in structured.red_flags"
              :key="flag"
              class="redflag"
            >{{ flag }}</span>
          </div>
        </div>
        <div
          v-else
          class="no-redflags"
        >
          <AppIcon
            name="check"
            :size="14"
          />
          <span>未识别到危险信号</span>
        </div>

        <dl class="fields">
          <div
            v-for="field in FIELDS"
            :key="field.key"
            class="field"
          >
            <dt>{{ field.label }}</dt>
            <dd>{{ fieldValue(field.key) }}</dd>
          </div>
        </dl>

        <div class="suggest">
          <div class="suggest-label">
            建议就诊科室
          </div>
          <div class="suggest-tags">
            <span
              v-for="dept in structured.possible_departments"
              :key="dept"
              class="mc-tag mc-tag--teal"
            >
              {{ dept }}
            </span>
            <span
              v-if="!structured.possible_departments?.length"
              class="mc-muted"
            >—</span>
          </div>
        </div>
      </div>
    </template>

    <EmptyState
      v-else
      title="暂无报告数据"
      description="请先完成预问诊对话并提交"
    />
  </div>
</template>

<style scoped>
.report {
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.pending-hint {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 14px;
  font-size: 13px;
  color: var(--mc-text-soft);
}

.head {
  display: flex;
  flex-direction: column;
  gap: 8px;
  margin-bottom: 14px;
}

@media (min-width: 768px) {
  .head {
    flex-direction: row;
    align-items: flex-start;
    justify-content: space-between;
  }
}

.head-title {
  font-size: 16px;
  font-weight: 600;
}

.redflags {
  border: 1px solid #fecaca;
  background: #fef2f2;
  border-radius: var(--mc-radius);
  padding: 12px;
  margin-bottom: 14px;
}

.redflags-title {
  display: flex;
  align-items: center;
  gap: 6px;
  color: #b91c1c;
  font-weight: 600;
  font-size: 13px;
  margin-bottom: 8px;
}

.redflags-list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.redflag {
  background: #fff;
  border: 1px solid #fecaca;
  color: #b91c1c;
  border-radius: 999px;
  padding: 2px 10px;
  font-size: 12px;
}

.no-redflags {
  display: flex;
  align-items: center;
  gap: 6px;
  color: #15803d;
  font-size: 13px;
  background: #f0fdf4;
  border: 1px solid #bbf7d0;
  border-radius: var(--mc-radius);
  padding: 10px 12px;
  margin-bottom: 14px;
}

.fields {
  display: grid;
  grid-template-columns: 1fr;
  gap: 12px;
  margin: 0;
}

@media (min-width: 768px) {
  .fields {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

.field {
  min-width: 0;
}

.field dt {
  font-size: 12px;
  color: var(--mc-text-muted);
  margin-bottom: 4px;
}

.field dd {
  margin: 0;
  font-size: 14px;
  line-height: 1.65;
  color: var(--mc-text);
  white-space: pre-wrap;
  word-break: break-word;
}

.suggest {
  margin-top: 16px;
  padding-top: 14px;
  border-top: 1px dashed var(--mc-border);
}

.suggest-label {
  font-size: 12px;
  color: var(--mc-text-muted);
  margin-bottom: 6px;
}

.suggest-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.raw-chat {
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-height: 460px;
  overflow-y: auto;
}

.raw-row {
  display: flex;
  gap: 8px;
  align-items: flex-start;
}

.raw-role {
  flex-shrink: 0;
  font-size: 12px;
  font-weight: 600;
  padding: 1px 8px;
  border-radius: 999px;
  background: #f1f5f9;
  color: var(--mc-text-soft);
}

.raw-role--assistant {
  background: var(--mc-primary-soft);
  color: var(--mc-primary-dark);
}

.raw-content {
  font-size: 13px;
  line-height: 1.65;
  white-space: pre-wrap;
  word-break: break-word;
}
</style>

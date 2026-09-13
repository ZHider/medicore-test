<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'

import AppIcon from '@/components/AppIcon.vue'
import StatusTag from '@/components/StatusTag.vue'
import { listMine } from '@/api/appointment'
import { toApiError } from '@/api/error'
import { submit } from '@/api/preconsult'
import { usePreconsultChat } from '@/composables/usePreconsultChat'
import type { AppointmentItem } from '@/types/api'
import { timeSlotLabel } from '@/utils/datetime'

const route = useRoute()
const router = useRouter()

const appointmentId = computed(() => Number(route.params.id))
const appointment = ref<AppointmentItem | null>(null)
const submitting = ref(false)

const {
  messages,
  streaming,
  loadingHistory,
  errorText,
  loadHistory,
  send,
  abort,
} = usePreconsultChat(appointmentId)

const draft = ref('')
const scroller = ref<HTMLElement | null>(null)

/** 冷启动时给几个示例，省掉"不知道该说什么"的空白页 */
const QUICK_PROMPTS = [
  '我发烧三天了，还咳嗽',
  '肚子疼了两天，一阵一阵的',
  '最近头晕乏力，睡不好',
]

const canSubmit = computed(() => messages.value.length >= 2 && !streaming.value)

onMounted(async () => {
  await Promise.all([loadHistory(), loadAppointment()])
  await scrollToBottom()
})

async function loadAppointment(): Promise<void> {
  try {
    // 没有 GET /appointments/:id，从列表首页里找（演示数据量下足够）
    const page = await listMine({ size: 50 })
    appointment.value = page.items.find((item) => item.id === appointmentId.value) ?? null
  } catch {
    appointment.value = null
  }
}

async function scrollToBottom(): Promise<void> {
  await nextTick()
  const el = scroller.value
  if (el) el.scrollTop = el.scrollHeight
}

// 流式输出时持续跟随到底部
watch(
  () => messages.value.map((m) => m.content.length).join(','),
  () => void scrollToBottom(),
)

async function handleSend(): Promise<void> {
  const text = draft.value.trim()
  if (!text || streaming.value) return
  draft.value = ''
  await send(text)
}

function usePrompt(text: string): void {
  draft.value = text
}

async function handleSubmit(): Promise<void> {
  if (submitting.value || streaming.value) return
  if (!messages.value.length) {
    ElMessage.warning('请先与 AI 完成几句对话，再提交总结')
    return
  }

  submitting.value = true
  try {
    const result = await submit(appointmentId.value)
    // already_submitted 是幂等，不是错误：直接进入轮询
    ElMessage.success(
      result.already_submitted ? '此前已提交，正在查看生成结果' : '已提交，正在生成结构化总结',
    )
    await router.push({ name: 'preconsult-report', params: { id: String(appointmentId.value) } })
  } catch (e) {
    ElMessage.error(toApiError(e).displayMessage)
  } finally {
    submitting.value = false
  }
}

function handleKeydown(event: KeyboardEvent): void {
  // Enter 发送、Shift+Enter 换行（移动端软键盘通常不触发，靠按钮）
  if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
    event.preventDefault()
    void handleSend()
  }
}
</script>

<template>
  <div class="chat-page">
    <!-- 上下文：医生 / 就诊时间 / 状态 -->
    <div class="ctx mc-card">
      <div class="ctx-left">
        <div class="ctx-avatar">
          <AppIcon
            name="chat"
            :size="18"
          />
        </div>
        <div class="ctx-info">
          <div class="ctx-title">
            AI 预问诊助手
            <StatusTag
              v-if="appointment"
              :value="appointment.status"
            />
          </div>
          <div class="ctx-sub">
            <template v-if="appointment">
              {{ appointment.department_name }} · {{ appointment.doctor_name }} ·
              {{ appointment.sched_date }} {{ timeSlotLabel(appointment.time_slot) }}
            </template>
            <template v-else>
              预约号 #{{ appointmentId }}
            </template>
          </div>
        </div>
      </div>
      <el-button
        type="primary"
        :loading="submitting"
        :disabled="!canSubmit"
        @click="handleSubmit"
      >
        提交并生成总结
      </el-button>
    </div>

    <!-- 对话区 -->
    <div class="chat mc-card">
      <div
        ref="scroller"
        class="messages"
      >
        <div
          v-if="loadingHistory"
          class="history-loading"
        >
          <el-skeleton
            :rows="4"
            animated
          />
        </div>

        <div
          v-else-if="!messages.length"
          class="intro"
        >
          <div class="intro-icon">
            <AppIcon
              name="stethoscope"
              :size="26"
            />
          </div>
          <div class="intro-title">
            请描述你的不适
          </div>
          <p class="intro-desc">
            越具体越好：什么时候开始、什么部位、什么感觉、有没有加重。
            这些信息会整理成结构化病历摘要，供接诊医生提前了解病情。
          </p>
          <div class="quick">
            <button
              v-for="prompt in QUICK_PROMPTS"
              :key="prompt"
              type="button"
              class="quick-chip"
              @click="usePrompt(prompt)"
            >
              {{ prompt }}
            </button>
          </div>
        </div>

        <template v-else>
          <div
            v-for="(message, index) in messages"
            :key="index"
            class="row"
            :class="`row--${message.role}`"
          >
            <div
              v-if="message.role === 'assistant'"
              class="avatar avatar--ai"
            >
              <AppIcon
                name="pulse"
                :size="15"
              />
            </div>
            <div
              class="bubble"
              :class="{ 'bubble--user': message.role === 'user', 'bubble--failed': message.failed }"
            >
              <span class="bubble-text">{{ message.content }}</span>
              <span
                v-if="message.pending"
                class="mc-caret"
              />
            </div>
            <div
              v-if="message.role === 'user'"
              class="avatar avatar--me"
            >
              我
            </div>
          </div>
        </template>
      </div>

      <div
        v-if="errorText"
        class="err"
      >
        <AppIcon
          name="alert"
          :size="15"
        />
        <span>{{ errorText }}</span>
      </div>

      <!-- 输入区 -->
      <div class="composer">
        <el-input
          v-model="draft"
          type="textarea"
          :rows="2"
          resize="none"
          maxlength="500"
          placeholder="描述你的症状…（Enter 发送，Shift+Enter 换行）"
          :disabled="streaming"
          @keydown="handleKeydown"
        />
        <div class="composer-actions">
          <el-button
            v-if="streaming"
            type="warning"
            plain
            @click="abort()"
          >
            停止生成
          </el-button>
          <el-button
            type="primary"
            :disabled="!draft.trim() || streaming"
            :loading="streaming"
            @click="handleSend"
          >
            <AppIcon
              v-if="!streaming"
              name="send"
              :size="15"
            />
            <span class="btn-text">发送</span>
          </el-button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.chat-page {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

/* ---------------- 上下文条 ---------------- */

.ctx {
  display: flex;
  flex-direction: column;
  gap: 12px;
  padding: 12px 14px;
}

@media (min-width: 640px) {
  .ctx {
    flex-direction: row;
    align-items: center;
    justify-content: space-between;
  }
}

.ctx-left {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.ctx-avatar {
  width: 38px;
  height: 38px;
  flex-shrink: 0;
  border-radius: 11px;
  background: var(--mc-primary-soft);
  color: var(--mc-primary-dark);
  display: flex;
  align-items: center;
  justify-content: center;
}

.ctx-info {
  min-width: 0;
}

.ctx-title {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 14px;
  font-weight: 600;
}

.ctx-sub {
  font-size: 12px;
  color: var(--mc-text-muted);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* ---------------- 聊天容器 ---------------- */

.chat {
  display: flex;
  flex-direction: column;
  padding: 0;
  overflow: hidden;
  height: calc(100dvh - 260px);
  min-height: 380px;
}

@media (min-width: 768px) {
  .chat {
    height: min(68vh, 720px);
  }
}

.messages {
  flex: 1;
  overflow-y: auto;
  padding: 16px;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.history-loading {
  width: 100%;
}

/* ---------------- 引导 ---------------- */

.intro {
  margin: auto;
  text-align: center;
  max-width: 460px;
  padding: 20px 0;
}

.intro-icon {
  width: 54px;
  height: 54px;
  margin: 0 auto 12px;
  border-radius: 16px;
  background: var(--mc-primary-soft);
  color: var(--mc-primary-dark);
  display: flex;
  align-items: center;
  justify-content: center;
}

.intro-title {
  font-size: 16px;
  font-weight: 600;
  margin-bottom: 8px;
}

.intro-desc {
  font-size: 13px;
  color: var(--mc-text-soft);
  line-height: 1.75;
}

.quick {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  justify-content: center;
  margin-top: 18px;
}

.quick-chip {
  border: 1px solid var(--mc-border);
  background: var(--mc-surface-alt);
  border-radius: 999px;
  padding: 6px 14px;
  font-size: 13px;
  color: var(--mc-text-soft);
  cursor: pointer;
  font-family: inherit;
}

.quick-chip:hover {
  border-color: var(--mc-primary);
  color: var(--mc-primary-dark);
}

/* ---------------- 气泡 ---------------- */

.row {
  display: flex;
  align-items: flex-end;
  gap: 8px;
}

.row--user {
  justify-content: flex-end;
}

.avatar {
  width: 30px;
  height: 30px;
  flex-shrink: 0;
  border-radius: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
}

.avatar--ai {
  background: var(--mc-primary-soft);
  color: var(--mc-primary-dark);
}

.avatar--me {
  background: #e0e7ff;
  color: #4338ca;
  font-weight: 600;
}

.bubble {
  max-width: min(78%, 560px);
  padding: 10px 14px;
  border-radius: 14px;
  font-size: 14px;
  line-height: 1.7;
  background: var(--mc-surface-alt);
  border: 1px solid var(--mc-border);
  white-space: pre-wrap;
  word-break: break-word;
}

.bubble--user {
  background: var(--mc-primary);
  border-color: var(--mc-primary);
  color: #fff;
  border-bottom-right-radius: 4px;
}

.bubble--failed {
  background: #fef2f2;
  border-color: #fecaca;
  color: #b91c1c;
}

.bubble-text {
  white-space: pre-wrap;
}

/* ---------------- 错误 / 输入 ---------------- */

.err {
  display: flex;
  align-items: center;
  gap: 8px;
  margin: 0 16px 10px;
  padding: 8px 12px;
  border-radius: var(--mc-radius);
  background: #fef2f2;
  border: 1px solid #fecaca;
  color: #b91c1c;
  font-size: 13px;
}

.composer {
  border-top: 1px solid var(--mc-border);
  padding: 12px 14px calc(12px + var(--mc-safe-bottom));
  background: var(--mc-surface-alt);
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.composer-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}

.btn-text {
  margin-left: 4px;
}
</style>

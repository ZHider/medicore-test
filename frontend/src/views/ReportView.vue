<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'

import AppIcon from '@/components/AppIcon.vue'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import ReportPanel from '@/components/ReportPanel.vue'
import { listMine } from '@/api/appointment'
import { ErrorCode, toApiError } from '@/api/error'
import { getReport, submit } from '@/api/preconsult'
import { useAuthStore } from '@/stores/auth'
import type { AppointmentItem, PreconsultReport } from '@/types/api'
import { timeSlotLabel } from '@/utils/datetime'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const appointmentId = computed(() => Number(route.params.id))

/**
 * 本页三个角色共用（`meta.roles` 是 patient/doctor/admin），但**后端只有患者侧的子接口**：
 *
 * | 动作 | 接口 | 允许的角色 |
 * |---|---|---|
 * | 读报告 | `GET .../preconsult/report` | 患者本人 / 接诊医生 / 管理员 |
 * | 提交总结 | `POST .../preconsult/submit` | **仅患者** |
 * | 取预约详情 | `GET /appointments`（`listMine`） | **仅患者** |
 * | 发起对话 | `preconsult-chat` 路由 | **仅患者**（该路由也没注册给其他角色） |
 *
 * 所以"患者专属"的动作必须按角色收起。否则医生/管理员点下去是静默失败
 * （403 或"目标路由不存在"），页面看起来正常却什么也没发生。
 */
const isPatient = computed(() => auth.role === 'patient')

const report = ref<PreconsultReport | null>(null)
const appointment = ref<AppointmentItem | null>(null)
const loading = ref(true)
const submitting = ref(false)
const notFound = ref(false)
const fatalError = ref('')

/** 轮询状态 */
const pollCount = ref(0)
const timedOut = ref(false)
const POLL_INTERVAL = 1500
const POLL_LIMIT = 60 // 约 90 秒

let timer: number | null = null

const isTerminal = computed(
  () => report.value?.status === 'done' || report.value?.status === 'failed',
)

const isPolling = computed(() => !isTerminal.value && !timedOut.value && !fatalError.value)

function stopPolling(): void {
  if (timer !== null) {
    window.clearInterval(timer)
    timer = null
  }
}

function startPolling(): void {
  stopPolling()
  timer = window.setInterval(() => {
    void poll()
  }, POLL_INTERVAL)
}

async function poll(): Promise<void> {
  try {
    report.value = await getReport(appointmentId.value)
    pollCount.value += 1

    if (isTerminal.value || pollCount.value >= POLL_LIMIT) {
      stopPolling()
      if (!isTerminal.value) timedOut.value = true
    }
  } catch (e) {
    const err = toApiError(e)

    if (err.code === ErrorCode.NotFound) {
      // 报告还没生成：不是错误，提示用户先去完成对话
      notFound.value = true
      stopPolling()
    } else if (err.code === ErrorCode.NotOwner || err.code === ErrorCode.Forbidden) {
      // 资源归属校验失败：医生访问非本人接诊的预约
      fatalError.value =
        err.code === ErrorCode.NotOwner
          ? '这份报告属于其他医生的接诊记录，你无权查看'
          : '你没有查看该报告的权限'
      stopPolling()
    } else {
      fatalError.value = err.displayMessage
      stopPolling()
    }
  } finally {
    loading.value = false
  }
}

onMounted(async () => {
  // 预约详情只有患者拿得到（`GET /appointments` 是患者专属，其他角色 403）。
  // 医生/管理员进来时没有这条上下文，模板里 `v-if="appointment"` 会自动省掉那一行。
  if (isPatient.value) void loadAppointment()
  await poll()
  if (isPolling.value) startPolling()
})

onUnmounted(stopPolling)

async function loadAppointment(): Promise<void> {
  try {
    const page = await listMine({ size: 50 })
    appointment.value = page.items.find((item) => item.id === appointmentId.value) ?? null
  } catch {
    appointment.value = null
  }
}

async function retry(): Promise<void> {
  // 「重新提交总结」是**患者专属**动作（`POST .../preconsult/submit` 只对患者开放）。
  // 医生/管理员看的是同一份报告，他们的"重新获取"只能是重新拉取一次状态。
  if (!isPatient.value) {
    await poll()
    ElMessage.info('已刷新报告状态')
    return
  }

  submitting.value = true
  try {
    // submit 是幂等的：已提交过只会返回 already_submitted，不会重复入队
    await submit(appointmentId.value)
    ElMessage.info('已通知后端重新处理，继续轮询中…')
  } catch (e) {
    const err = toApiError(e)
    if (err.code === ErrorCode.Conflict) {
      ElMessage.warning('当前预约状态不允许提交总结')
    } else {
      ElMessage.error(err.displayMessage)
    }
  } finally {
    submitting.value = false
    notFound.value = false
    fatalError.value = ''
    timedOut.value = false
    pollCount.value = 0
    await poll()
    if (isPolling.value) startPolling()
  }
}
</script>

<template>
  <div>
    <PageHeader
      title="AI 预问诊报告"
      description="结构化总结由 AI 异步生成，页面会自动轮询到终态"
    >
      <template #actions>
        <el-button @click="router.back()">
          <AppIcon
            name="back"
            :size="15"
          />
          <span class="btn-text">返回</span>
        </el-button>
        <el-button
          type="primary"
          :loading="submitting"
          @click="retry"
        >
          <AppIcon
            name="sync"
            :size="15"
          />
          <span class="btn-text">重新获取</span>
        </el-button>
      </template>
    </PageHeader>

    <!-- 就诊上下文 -->
    <div
      v-if="appointment"
      class="ctx mc-card"
    >
      <div class="ctx-line">
        <span class="ctx-dept">{{ appointment.department_name }}</span>
        <span class="ctx-doctor">{{ appointment.doctor_name }}</span>
        <span class="mc-muted">
          {{ appointment.sched_date }} {{ timeSlotLabel(appointment.time_slot) }}
        </span>
      </div>
      <div class="ctx-line mc-muted">
        预约号 #{{ appointment.id }}
      </div>
    </div>

    <!-- 未生成 -->
    <EmptyState
      v-if="notFound"
      icon="document"
      title="还没有生成预问诊报告"
      :description="isPatient
        ? '请先完成 AI 预问诊对话并提交，系统随后会异步生成结构化总结'
        : '该患者尚未提交 AI 预问诊，暂时没有可查看的报告'"
    >
      <!-- 发起预问诊对话只有患者能做：这个路由也没注册给医生/管理员 -->
      <el-button
        v-if="isPatient"
        type="primary"
        @click="router.push({ name: 'preconsult-chat', params: { id: String(appointmentId) } })"
      >
        去完成预问诊
      </el-button>
    </EmptyState>

    <!-- 权限 / 致命错误 -->
    <EmptyState
      v-else-if="fatalError"
      icon="shield"
      :title="fatalError"
      description="报告受资源归属校验保护，只有患者本人、接诊医生与管理员可以查看"
    >
      <el-button @click="retry()">
        重试
      </el-button>
    </EmptyState>

    <!-- 正常 -->
    <template v-else>
      <el-alert
        v-if="timedOut"
        type="warning"
        show-icon
        :closable="false"
        title="生成时间超出预期"
        description="后台可能仍在处理，你可以点击「重新获取」继续查看，或稍后回来刷新。"
        class="mb"
      />

      <ReportPanel
        :report="report"
        :loading="loading"
      >
        <template #retry>
          <el-button
            type="primary"
            :loading="submitting"
            @click="retry()"
          >
            重新获取
          </el-button>
          <el-button
            v-if="isPatient"
            @click="router.push({ name: 'preconsult-chat', params: { id: String(appointmentId) } })"
          >
            返回对话
          </el-button>
        </template>
      </ReportPanel>

      <p
        v-if="isPolling"
        class="poll-hint"
      >
        <span class="dot" />
        正在自动刷新（已轮询 {{ pollCount }} 次，约每 1.5 秒一次）
      </p>
    </template>
  </div>
</template>

<style scoped>
.ctx {
  margin-bottom: 14px;
  padding: 12px 14px;
}

.ctx-line {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 8px;
  font-size: 13px;
}

.ctx-dept {
  font-weight: 600;
}

.ctx-doctor {
  color: var(--mc-text-soft);
}

.mb {
  margin-bottom: 14px;
}

.poll-hint {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 14px;
  font-size: 12px;
  color: var(--mc-text-muted);
}

.dot {
  width: 7px;
  height: 7px;
  border-radius: 50%;
  background: var(--mc-primary);
  animation: pulse 1.2s ease-in-out infinite;
}

@keyframes pulse {
  0%,
  100% {
    opacity: 0.35;
  }
  50% {
    opacity: 1;
  }
}

.btn-text {
  margin-left: 4px;
}
</style>

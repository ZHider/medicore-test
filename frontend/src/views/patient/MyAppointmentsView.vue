<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'

import AppIcon from '@/components/AppIcon.vue'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatusTag from '@/components/StatusTag.vue'
import { cancel, checkIn, listMine } from '@/api/appointment'
import { ErrorCode, toApiError } from '@/api/error'
import { useCursorList, useInfiniteScroll } from '@/composables/useCursorList'
import type { AppointmentItem } from '@/types/api'
import { formatCountdown, formatDateTime, millisUntil, timeSlotLabel } from '@/utils/datetime'

const router = useRouter()

const list = useCursorList<AppointmentItem>((query) => listMine(query), { size: 20 })

const sentinel = ref<HTMLElement | null>(null)
const canLoadMore = computed(() => !list.finished.value && !list.loading.value)

const { connect, disconnect } = useInfiniteScroll(
  sentinel,
  () => {
    void list.loadMore()
  },
  canLoadMore,
)

onMounted(() => {
  void nextTick(() => connect())
})
onUnmounted(disconnect)
watch(sentinel, () => connect())

// ---------------------------------------------------------------------------
// pending 预约的 15 分钟确认倒计时
//
// 单个 tick 驱动全部条目，而不是每条起一个 interval —— 列表有 20+ 条时后者会很浪费。
// ---------------------------------------------------------------------------

const nowTick = ref(Date.now())
let timer: number | null = null

const hasPending = computed(() => list.items.value.some((item) => item.status === 'pending'))

function startTick(): void {
  if (timer !== null) return
  timer = window.setInterval(() => {
    nowTick.value = Date.now()
  }, 1000)
}

function stopTick(): void {
  if (timer === null) return
  window.clearInterval(timer)
  timer = null
}

watch(hasPending, (value) => (value ? startTick() : stopTick()))
onUnmounted(stopTick)

function remainingOf(item: AppointmentItem): number {
  void nowTick.value
  return millisUntil(item.expire_at)
}

// ---------------------------------------------------------------------------
// 操作
// ---------------------------------------------------------------------------

const busyId = ref<number | null>(null)

async function handleCancel(item: AppointmentItem): Promise<void> {
  let reason: string
  try {
    const result = await ElMessageBox.prompt(
      `确定取消 ${item.sched_date} ${timeSlotLabel(item.time_slot)} 的门诊预约吗？`,
      '取消预约',
      {
        confirmButtonText: '确认取消',
        cancelButtonText: '再想想',
        inputPlaceholder: '取消原因（可留空）',
        inputValidator: () => true,
        // 后端 binding 限制 max=200
        inputErrorMessage: '取消原因最多 200 字',
        type: 'warning',
      },
    )
    reason = result.value ?? ''
  } catch {
    return // 用户放弃
  }

  busyId.value = item.id
  try {
    await cancel(item.id, reason || undefined)
    ElMessage.success('预约已取消，号源已释放')
    await list.refresh()
  } catch (e) {
    const err = toApiError(e)
    if (err.code === ErrorCode.Conflict) {
      ElMessage.warning('该预约当前状态无法取消，已为你刷新')
      await list.refresh()
    } else {
      ElMessage.error(err.displayMessage)
    }
  } finally {
    busyId.value = null
  }
}

async function handleCheckIn(item: AppointmentItem): Promise<void> {
  busyId.value = item.id
  try {
    const result = await checkIn(item.id)
    ElMessage.success(
      result.queue_no ? `签到成功，你的就诊序号是 ${result.queue_no}` : '签到成功',
    )
    await list.refresh()
  } catch (e) {
    const err = toApiError(e)
    if (err.code === ErrorCode.Conflict) {
      ElMessage.warning('当前状态无法签到，已为你刷新')
      await list.refresh()
    } else {
      ElMessage.error(err.displayMessage)
    }
  } finally {
    busyId.value = null
  }
}

function goChat(item: AppointmentItem): void {
  void router.push({ name: 'preconsult-chat', params: { id: String(item.id) } })
}

function goReport(item: AppointmentItem): void {
  void router.push({ name: 'preconsult-report', params: { id: String(item.id) } })
}

function isActive(item: AppointmentItem): boolean {
  return item.status === 'pending' || item.status === 'confirmed'
}
</script>

<template>
  <div>
    <PageHeader
      title="我的预约"
      description="查看预约状态、签到取号，或进入 AI 预问诊"
    >
      <template #actions>
        <el-button
          :loading="list.loading.value"
          @click="list.refresh()"
        >
          <AppIcon
            name="sync"
            :size="15"
          />
          <span class="btn-text">刷新</span>
        </el-button>
      </template>
    </PageHeader>

    <!-- 首屏骨架 -->
    <div
      v-if="!list.loaded.value && list.loading.value"
      class="list"
    >
      <el-skeleton
        v-for="i in 3"
        :key="i"
        :rows="4"
        animated
      />
    </div>

    <EmptyState
      v-else-if="!list.items.value.length && list.error.value"
      icon="alert"
      title="加载失败"
      :description="toApiError(list.error.value).displayMessage"
    >
      <el-button
        type="primary"
        @click="list.refresh()"
      >
        重试
      </el-button>
    </EmptyState>

    <EmptyState
      v-else-if="!list.items.value.length"
      icon="calendar"
      title="还没有预约"
      description="去「预约挂号」选一位医生开始吧"
    >
      <el-button
        type="primary"
        @click="router.push({ name: 'booking' })"
      >
        立即挂号
      </el-button>
    </EmptyState>

    <template v-else>
      <div class="list">
        <article
          v-for="item in list.items.value"
          :key="item.id"
          class="appt"
        >
          <div class="appt-main">
            <div class="appt-head">
              <span class="appt-dept">{{ item.department_name }}</span>
              <span class="appt-doctor">{{ item.doctor_name }}</span>
              <StatusTag :value="item.status" />
              <span
                v-if="item.queue_no"
                class="mc-tag mc-tag--blue"
              >
                序号 {{ item.queue_no }}
              </span>
            </div>

            <div class="appt-meta">
              <span class="meta-item">
                <AppIcon
                  name="calendar"
                  :size="14"
                />
                {{ item.sched_date }} {{ timeSlotLabel(item.time_slot) }}
              </span>
              <span class="meta-item">
                <AppIcon
                  name="clock"
                  :size="14"
                />
                下单 {{ formatDateTime(item.created_at) }}
              </span>
            </div>

            <!-- pending 的 15 分钟确认窗口 -->
            <div
              v-if="item.status === 'pending'"
              class="countdown"
            >
              <AppIcon
                name="clock"
                :size="14"
              />
              <template v-if="remainingOf(item) > 0">
                <span>剩余确认时间</span>
                <b>{{ formatCountdown(remainingOf(item)) }}</b>
                <span class="mc-muted">超时号源将自动释放</span>
              </template>
              <template v-else>
                <span>确认时间已到，号源即将释放，请刷新查看最新状态</span>
              </template>
            </div>
          </div>

          <div class="appt-actions">
            <el-button
              v-if="isActive(item) && !item.queue_no"
              type="primary"
              plain
              :loading="busyId === item.id"
              @click="handleCheckIn(item)"
            >
              签到取号
            </el-button>
            <el-button
              v-if="isActive(item)"
              :disabled="busyId === item.id"
              @click="goChat(item)"
            >
              <AppIcon
                name="chat"
                :size="15"
              />
              <span class="btn-text">AI 预问诊</span>
            </el-button>
            <el-button @click="goReport(item)">
              <AppIcon
                name="document"
                :size="15"
              />
              <span class="btn-text">报告</span>
            </el-button>
            <el-button
              v-if="isActive(item)"
              type="danger"
              plain
              :disabled="busyId === item.id"
              @click="handleCancel(item)"
            >
              取消
            </el-button>
          </div>
        </article>
      </div>

      <!-- 无限滚动哨兵：用 Intersection Observer，不是滚动事件 -->
      <div
        ref="sentinel"
        class="sentinel"
      >
        <span v-if="list.loading.value">加载中…</span>
        <span
          v-else-if="list.finished.value && list.items.value.length"
          class="mc-muted"
        >
          没有更多了
        </span>
      </div>
    </template>
  </div>
</template>

<style scoped>
.list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.appt {
  background: var(--mc-surface);
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius-lg);
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  box-shadow: var(--mc-shadow-sm);
}

@media (min-width: 900px) {
  .appt {
    flex-direction: row;
    align-items: center;
    justify-content: space-between;
    gap: 20px;
    padding: 16px 18px;
  }
}

.appt-main {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
  flex: 1;
}

.appt-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.appt-dept {
  font-size: 15px;
  font-weight: 600;
}

.appt-doctor {
  font-size: 14px;
  color: var(--mc-text-soft);
}

.appt-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 16px;
  font-size: 12px;
  color: var(--mc-text-muted);
}

.meta-item {
  display: inline-flex;
  align-items: center;
  gap: 5px;
}

.countdown {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 6px;
  font-size: 12px;
  color: #c2410c;
  background: #fff7ed;
  border: 1px solid #fed7aa;
  border-radius: var(--mc-radius);
  padding: 6px 10px;
  width: fit-content;
}

.countdown b {
  font-size: 14px;
  font-family: var(--mc-mono);
}

.appt-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.appt-actions :deep(.el-button + .el-button) {
  margin-left: 0;
}

.btn-text {
  margin-left: 4px;
}

.sentinel {
  padding: 20px 0;
  text-align: center;
  font-size: 13px;
  color: var(--mc-text-soft);
}
</style>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'

import AppIcon from '@/components/AppIcon.vue'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatusTag from '@/components/StatusTag.vue'
import { getQueue, listForDoctor } from '@/api/appointment'
import { ErrorCode, toApiError } from '@/api/error'
import { useCursorList, useInfiniteScroll } from '@/composables/useCursorList'
import { useIsMobile } from '@/composables/useMediaQuery'
import { useAuthStore } from '@/stores/auth'
import type { AppointmentStatus, DoctorAppointmentItem, QueueItem } from '@/types/api'
import { formatDateTime, timeSlotLabel, today } from '@/utils/datetime'

const router = useRouter()
const auth = useAuthStore()
const isMobile = useIsMobile()

// 筛选状态（date/status 直接传给后端；status 必须在白名单内，否则 400）
const filterDate = ref(today())
const filterStatus = ref<AppointmentStatus | ''>('')
const onlyUnprocessed = ref(false)

const list = useCursorList<DoctorAppointmentItem>(
  (query) => listForDoctor(query),
  {
    size: 20,
    params: () => ({
      date: filterDate.value,
      status: filterStatus.value,
    }),
    immediate: false,
  },
)

const sentinel = ref<HTMLElement | null>(null)
const canLoadMore = computed(() => !list.finished.value && !list.loading.value)
const { connect, disconnect } = useInfiniteScroll(
  sentinel,
  () => {
    void list.loadMore()
  },
  canLoadMore,
)

onMounted(async () => {
  await list.refresh()
  await nextTick()
  connect()
})
onUnmounted(disconnect)
watch(sentinel, () => connect())

// 筛选变化即重新拉取
watch([filterDate, filterStatus], () => {
  void list.refresh()
})

const STATUS_OPTIONS: Array<{ value: AppointmentStatus | ''; label: string }> = [
  { value: '', label: '全部状态' },
  { value: 'pending', label: '待确认' },
  { value: 'confirmed', label: '已确认' },
  { value: 'cancelled', label: '已取消' },
  { value: 'expired', label: '已过期' },
]

// ---------------------------------------------------------------------------
// 统计与筛选
// ---------------------------------------------------------------------------

const visibleItems = computed(() =>
  onlyUnprocessed.value
    ? list.items.value.filter((item) => !item.has_record)
    : list.items.value,
)

/** 待处理 = 仍有效但还没写病历。这是医生最关心的数字。 */
const unprocessedCount = computed(
  () => list.items.value.filter((item) => !item.has_record && isActive(item)).length,
)

const checkedInCount = computed(
  () => list.items.value.filter((item) => item.queue_no !== undefined).length,
)

function isActive(item: DoctorAppointmentItem): boolean {
  return item.status === 'pending' || item.status === 'confirmed'
}

// ---------------------------------------------------------------------------
// 候诊队列
// ---------------------------------------------------------------------------

const queueOpen = ref(false)
const queueLoading = ref(false)
const queueItems = ref<QueueItem[]>([])
const queueTitle = ref('')
/** 队列端点做了资源层归属校验，非本人排班会返回 40302 —— 这是正常业务分支 */
const queueError = ref('')

async function openQueue(item: DoctorAppointmentItem): Promise<void> {
  queueOpen.value = true
  queueLoading.value = true
  queueItems.value = []
  queueError.value = ''
  queueTitle.value = `${item.sched_date} ${timeSlotLabel(item.time_slot)} 候诊队列`

  try {
    const result = await getQueue(item.schedule_id)
    queueItems.value = result.items ?? []
  } catch (e) {
    const err = toApiError(e)
    if (err.code === ErrorCode.NotOwner) {
      queueError.value = '这是其他医生的排班，你无权查看其候诊队列'
    } else if (err.code === ErrorCode.NotFound) {
      queueError.value = '该排班不存在或已取消'
    } else {
      queueError.value = err.displayMessage
    }
  } finally {
    queueLoading.value = false
  }
}

function writeRecord(item: DoctorAppointmentItem): void {
  void router.push({ name: 'write-record', params: { id: String(item.id) } })
}

function viewReport(item: DoctorAppointmentItem): void {
  void router.push({ name: 'preconsult-report', params: { id: String(item.id) } })
}

function viewRecord(item: DoctorAppointmentItem): void {
  void router.push({ name: 'records' })
  ElMessage.info(`可在「病历记录」中查看 ${item.patient_name} 的病历`)
}
</script>

<template>
  <div>
    <PageHeader
      title="医生工作台"
      :description="auth.role === 'admin' ? '管理员视角可见全部预约' : '查看今日接诊、候诊队列并书写病历'"
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

    <!-- 概览 -->
    <div class="stats">
      <div class="stat">
        <span class="stat-label">今日预约</span>
        <span class="stat-value">{{ list.items.value.length }}</span>
      </div>
      <div class="stat stat--warn">
        <span class="stat-label">待处理（未写病历）</span>
        <span class="stat-value">{{ unprocessedCount }}</span>
      </div>
      <div class="stat">
        <span class="stat-label">已签到</span>
        <span class="stat-value">{{ checkedInCount }}</span>
      </div>
    </div>

    <!-- 筛选 -->
    <div class="filters mc-card">
      <div class="filter">
        <label>就诊日期</label>
        <el-date-picker
          v-model="filterDate"
          type="date"
          value-format="YYYY-MM-DD"
          placeholder="选择日期"
          :clearable="false"
        />
      </div>
      <div class="filter">
        <label>预约状态</label>
        <el-select
          v-model="filterStatus"
          placeholder="全部状态"
        >
          <el-option
            v-for="option in STATUS_OPTIONS"
            :key="option.value"
            :label="option.label"
            :value="option.value"
          />
        </el-select>
      </div>
      <div class="filter filter--switch">
        <label>只看未处理</label>
        <el-switch v-model="onlyUnprocessed" />
      </div>
      <p class="filter-note">
        <AppIcon
          name="alert"
          :size="13"
        />
        <span>
          「只看未处理」是在**已加载**的预约里筛选（后端未提供该筛选条件），
          列表会随滚动继续加载。
        </span>
      </p>
    </div>

    <!-- 列表 -->
    <div
      v-if="list.loading.value && !list.loaded.value"
      class="list"
    >
      <el-skeleton
        v-for="i in 3"
        :key="i"
        :rows="3"
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
      title="该日期没有预约"
      :description="`${filterDate} 暂无患者预约，可切换日期查看`"
    />

    <template v-else>
      <div class="list">
        <article
          v-for="item in visibleItems"
          :key="item.id"
          class="patient"
          :class="{ 'is-unprocessed': !item.has_record && isActive(item) }"
        >
          <div class="patient-main">
            <div class="patient-head">
              <span class="patient-avatar">{{ item.patient_name.slice(0, 1) }}</span>
              <div class="patient-info">
                <!-- 患者姓名刻意不脱敏：医生必须能识别面前的患者 -->
                <span class="patient-name">{{ item.patient_name }}</span>
                <span class="patient-sub mc-muted">
                  {{ item.sched_date }} {{ timeSlotLabel(item.time_slot) }}
                  · 患者 ID {{ item.patient_id }}
                </span>
              </div>
              <div class="patient-tags">
                <StatusTag :value="item.status" />
                <span
                  v-if="item.queue_no"
                  class="mc-tag mc-tag--blue"
                >号 {{ item.queue_no }}</span>
                <!-- has_record 是后端特意 JOIN 出来的，医生一眼看出还有谁没处理 -->
                <span
                  v-if="item.has_record"
                  class="mc-tag mc-tag--green"
                >已写病历</span>
                <span
                  v-else
                  class="mc-tag mc-tag--orange"
                >待写病历</span>
              </div>
            </div>

            <div class="patient-meta mc-muted">
              <span class="meta-item">
                <AppIcon
                  name="clock"
                  :size="13"
                />
                预约于 {{ formatDateTime(item.created_at) }}
              </span>
              <span class="meta-item">
                <AppIcon
                  name="list"
                  :size="13"
                />
                排班 #{{ item.schedule_id }}
              </span>
            </div>
          </div>

          <div class="patient-actions">
            <el-button
              plain
              @click="openQueue(item)"
            >
              <AppIcon
                name="list"
                :size="15"
              />
              <span class="btn-text">候诊队列</span>
            </el-button>
            <el-button
              plain
              @click="viewReport(item)"
            >
              <AppIcon
                name="chat"
                :size="15"
              />
              <span class="btn-text">AI 报告</span>
            </el-button>
            <el-button
              v-if="item.has_record"
              type="success"
              plain
              @click="viewRecord(item)"
            >
              查看病历
            </el-button>
            <!--
              管理员只读：`/appointments/:id/record` 只允许**该预约的接诊医生**写
              （后端 §3.8 的表里 病历写 对 admin 是 ❌），而且 `write-record`
              路由也没有注册给管理员 —— 若照旧显示这个按钮，管理员点下去
              是一次"目标路由不存在"的静默失败。
            -->
            <el-button
              v-else-if="auth.role !== 'admin'"
              type="primary"
              @click="writeRecord(item)"
            >
              书写病历
            </el-button>
            <span
              v-else
              class="readonly-hint"
            >待医生书写</span>
          </div>
        </article>
      </div>

      <div
        ref="sentinel"
        class="sentinel"
      >
        <span v-if="list.loading.value">加载中…</span>
        <span
          v-else-if="list.finished.value"
          class="mc-muted"
        >没有更多了</span>
      </div>
    </template>

    <!-- 候诊队列抽屉 -->
    <el-drawer
      v-model="queueOpen"
      :size="isMobile ? '100%' : '460px'"
      :title="queueTitle"
    >
      <div
        v-if="queueLoading"
        class="queue-loading"
      >
        <el-skeleton
          :rows="5"
          animated
        />
      </div>

      <EmptyState
        v-else-if="queueError"
        icon="shield"
        :title="queueError"
        description="候诊队列只对排班的接诊医生开放"
      />

      <EmptyState
        v-else-if="!queueItems.length"
        icon="list"
        title="暂无已签到患者"
        description="患者签到取号后才会出现在队列中"
      />

      <ol
        v-else
        class="queue"
      >
        <li
          v-for="entry in queueItems"
          :key="entry.appointment_id"
          class="queue-item"
        >
          <span class="queue-no">{{ entry.queue_no }}</span>
          <div class="queue-info">
            <span class="queue-name">{{ entry.patient_name }}</span>
            <span class="mc-muted">签到时间 {{ formatDateTime(entry.checked_in_at) }}</span>
          </div>
          <StatusTag :value="entry.status" />
        </li>
      </ol>
    </el-drawer>
  </div>
</template>

<style scoped>
/* ---------------- 概览卡片 ---------------- */

.stats {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
  margin-bottom: 14px;
}

.stat {
  background: var(--mc-surface);
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius-lg);
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
  gap: 2px;
  box-shadow: var(--mc-shadow-sm);
}

.stat--warn {
  border-color: #fed7aa;
  background: #fffbf5;
}

.stat-label {
  font-size: 12px;
  color: var(--mc-text-muted);
}

.stat-value {
  font-size: 22px;
  font-weight: 700;
  color: var(--mc-text);
}

.stat--warn .stat-value {
  color: #c2410c;
}

/* ---------------- 筛选 ---------------- */

.filters {
  display: flex;
  flex-direction: column;
  gap: 12px;
  margin-bottom: 16px;
}

@media (min-width: 768px) {
  .filters {
    flex-direction: row;
    align-items: flex-end;
    flex-wrap: wrap;
  }
}

.filter {
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.filter label {
  font-size: 12px;
  color: var(--mc-text-muted);
}

.filter--switch {
  flex-direction: row;
  align-items: center;
  gap: 8px;
  padding-bottom: 8px;
}

.filter-note {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  flex-basis: 100%;
  font-size: 12px;
  color: var(--mc-text-muted);
}

/* ---------------- 患者列表 ---------------- */

.list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.patient {
  background: var(--mc-surface);
  border: 1px solid var(--mc-border);
  border-left: 3px solid transparent;
  border-radius: var(--mc-radius-lg);
  padding: 14px 16px;
  display: flex;
  flex-direction: column;
  gap: 12px;
  box-shadow: var(--mc-shadow-sm);
}

/* 未处理的高亮左侧边，扫一眼就知道还有谁没看 */
.patient.is-unprocessed {
  border-left-color: var(--mc-warning);
}

@media (min-width: 1024px) {
  .patient {
    flex-direction: row;
    align-items: center;
    justify-content: space-between;
    gap: 20px;
  }
}

.patient-main {
  min-width: 0;
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.patient-head {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.patient-avatar {
  width: 36px;
  height: 36px;
  flex-shrink: 0;
  border-radius: 50%;
  background: var(--mc-primary-soft);
  color: var(--mc-primary-dark);
  font-weight: 600;
  display: flex;
  align-items: center;
  justify-content: center;
}

.patient-info {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.patient-name {
  font-size: 15px;
  font-weight: 600;
}

.patient-sub {
  font-size: 12px;
}

.patient-tags {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-left: auto;
}

.patient-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 16px;
  font-size: 12px;
}

.meta-item {
  display: inline-flex;
  align-items: center;
  gap: 5px;
}

.patient-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
}

.patient-actions :deep(.el-button + .el-button) {
  margin-left: 0;
}

/* 管理员的只读占位：与按钮同高，避免行内元素把卡片撑得高低不齐 */
.readonly-hint {
  display: inline-flex;
  align-items: center;
  padding: 0 12px;
  font-size: 13px;
  color: var(--mc-text-soft);
  border: 1px dashed var(--mc-border-strong);
  border-radius: var(--el-border-radius-base);
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

/* ---------------- 队列 ---------------- */

.queue {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.queue-item {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 12px;
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius);
}

.queue-no {
  width: 32px;
  height: 32px;
  flex-shrink: 0;
  border-radius: 50%;
  background: var(--mc-primary);
  color: #fff;
  font-weight: 700;
  display: flex;
  align-items: center;
  justify-content: center;
}

.queue-info {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  font-size: 12px;
}

.queue-name {
  font-size: 14px;
  font-weight: 600;
  color: var(--mc-text);
}

.queue-loading {
  padding: 8px;
}
</style>

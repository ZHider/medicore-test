<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'

import AppIcon from '@/components/AppIcon.vue'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { grab } from '@/api/appointment'
import { listDepartments, listDoctors, listSchedulesByDoctor } from '@/api/catalog'
import { ErrorCode, toApiError } from '@/api/error'
import { useBookingStore } from '@/stores/booking'
import type { DepartmentBrief, DoctorListItem, ScheduleListItem } from '@/types/api'
import {
  addDays,
  formatDateHuman,
  formatMonthDay,
  timeSlotLabel,
  today,
  weekdayOf,
} from '@/utils/datetime'
import { createIdempotencyKey } from '@/utils/id'

const router = useRouter()
const booking = useBookingStore()

/** 一次性把未来两周的号源都拉回来，避免用户每点一天发一次请求。 */
const DAYS_AHEAD = 13

const departments = ref<DepartmentBrief[]>([])
const doctors = ref<DoctorListItem[]>([])
const schedules = ref<ScheduleListItem[]>([])

const loadingDepartments = ref(false)
const loadingDoctors = ref(false)
const loadingSchedules = ref(false)
const grabbingId = ref<number | null>(null)

const selectedDate = ref(today())

const step = computed(() => {
  if (!booking.departmentId) return 1
  if (!booking.doctorId) return 2
  return 3
})

// ---------------------------------------------------------------------------
// 加载
// ---------------------------------------------------------------------------

async function loadDepartments(): Promise<void> {
  loadingDepartments.value = true
  try {
    const result = await listDepartments()
    departments.value = result.items ?? []
  } catch (e) {
    ElMessage.error(toApiError(e).displayMessage)
  } finally {
    loadingDepartments.value = false
  }
}

async function loadDoctors(departmentId: number): Promise<void> {
  loadingDoctors.value = true
  doctors.value = []
  try {
    const result = await listDoctors(departmentId)
    doctors.value = result.items ?? []
  } catch (e) {
    // 「科室不存在」(40003) 与「该科室暂无医生」是两回事，文案必须分开
    const err = toApiError(e)
    if (err.code === ErrorCode.NotFound) {
      ElMessage.warning('该科室不存在或已下线')
      booking.reset()
    } else {
      ElMessage.error(err.displayMessage)
    }
  } finally {
    loadingDoctors.value = false
  }
}

async function loadSchedules(doctorId: number): Promise<void> {
  loadingSchedules.value = true
  schedules.value = []
  try {
    // from/to 必填且 from <= to；后端按 id 游标分页，size 上限 100
    const result = await listSchedulesByDoctor(doctorId, {
      from: today(),
      to: addDays(today(), DAYS_AHEAD),
      size: 100,
    })
    schedules.value = result.items ?? []
  } catch (e) {
    ElMessage.error(toApiError(e).displayMessage)
  } finally {
    loadingSchedules.value = false
  }
}

onMounted(async () => {
  await loadDepartments()
  if (booking.departmentId) await loadDoctors(booking.departmentId)
  if (booking.doctorId) await loadSchedules(booking.doctorId)
})

// ---------------------------------------------------------------------------
// 派生数据
// ---------------------------------------------------------------------------

/** 只展示可抢号的：remaining > 0 且 status === 'open'。 */
const grabbable = computed(() =>
  schedules.value.filter((item) => item.status === 'open' && item.remaining > 0),
)

const groupedByDate = computed(() => {
  const map = new Map<string, ScheduleListItem[]>()
  for (const item of grabbable.value) {
    const list = map.get(item.sched_date) ?? []
    list.push(item)
    map.set(item.sched_date, list)
  }
  return new Map([...map.entries()].sort((a, b) => a[0].localeCompare(b[0])))
})

const availableDates = computed(() => [...groupedByDate.value.keys()])
const slotsOfSelectedDate = computed(() => groupedByDate.value.get(selectedDate.value) ?? [])

// 号源重新加载后，若当前选中日期已无号，自动落到最近有号的日期
watch(availableDates, (dates) => {
  if (dates.length && !dates.includes(selectedDate.value)) {
    selectedDate.value = dates[0]
  }
})

// ---------------------------------------------------------------------------
// 交互
// ---------------------------------------------------------------------------

function chooseDepartment(item: DepartmentBrief): void {
  booking.selectDepartment(item.id, item.name)
  void loadDoctors(item.id)
}

function chooseDoctor(item: DoctorListItem): void {
  booking.selectDoctor(item.id, item.name)
  void loadSchedules(item.id)
}

function backToDepartments(): void {
  booking.reset()
  doctors.value = []
  schedules.value = []
  selectedDate.value = today()
}

function backToDoctors(): void {
  booking.selectDoctor(0, '')
  schedules.value = []
}

/** 抢号。每次点击生成新的幂等键 —— 只有"同一次提交的重试"才复用同一个 key。 */
async function handleGrab(item: ScheduleListItem): Promise<void> {
  if (grabbingId.value !== null) return
  grabbingId.value = item.id

  try {
    const appointment = await grab({
      schedule_id: item.id,
      idempotency_key: createIdempotencyKey(),
    })
    ElMessage.success(
      appointment.queue_no
        ? `预约成功，就诊序号 ${appointment.queue_no}`
        : '预约成功，请在「我的预约」查看',
    )
    await router.push({ name: 'my-appointments' })
  } catch (e) {
    const err = toApiError(e)

    if (err.code === ErrorCode.NoStock) {
      // 号源已变，必须刷新，否则用户会对着过期数字反复点击
      ElMessage.warning('该时段号源不足，已为你刷新最新号源')
      if (booking.doctorId) await loadSchedules(booking.doctorId)
    } else if (err.code === ErrorCode.AlreadyDone) {
      // 40005 是幂等语义而非失败，绝不能弹红
      ElMessage.info('请求正在处理中或已创建，请稍后在「我的预约」查看')
    } else if (err.code === ErrorCode.RateLimited) {
      ElMessage.warning(`操作过于频繁，请 ${err.retryAfter ?? 60} 秒后重试`)
    } else {
      ElMessage.error(err.displayMessage)
    }
  } finally {
    grabbingId.value = null
  }
}
</script>

<template>
  <div>
    <PageHeader
      title="预约挂号"
      description="选择科室与医生，查看未来两周号源并在线抢号"
    />

    <!-- 步骤指示：窄屏纵向、宽屏横向 -->
    <ol class="steps">
      <li
        class="step"
        :class="{ 'is-active': step >= 1 }"
      >
        <span class="step-no">1</span>
        <span class="step-text">
          <b>选择科室</b>
          <em>{{ booking.departmentName || '未选择' }}</em>
        </span>
      </li>
      <li
        class="step"
        :class="{ 'is-active': step >= 2 }"
      >
        <span class="step-no">2</span>
        <span class="step-text">
          <b>选择医生</b>
          <em>{{ booking.doctorName || '未选择' }}</em>
        </span>
      </li>
      <li
        class="step"
        :class="{ 'is-active': step >= 3 }"
      >
        <span class="step-no">3</span>
        <span class="step-text">
          <b>选择号源</b>
          <em v-if="step >= 3">{{ slotsOfSelectedDate.length }} 个可预约</em>
          <em v-else>未选择</em>
        </span>
      </li>
    </ol>

    <!-- 1. 科室 -->
    <section class="mc-card">
      <div class="mc-card-title">
        <span>科室</span>
        <el-button
          v-if="step > 1"
          link
          type="primary"
          @click="backToDepartments"
        >
          重新选择
        </el-button>
      </div>

      <div
        v-if="loadingDepartments"
        class="dept-grid"
      >
        <el-skeleton
          v-for="i in 4"
          :key="i"
          :rows="2"
          animated
        />
      </div>
      <EmptyState
        v-else-if="!departments.length"
        title="暂无可预约科室"
        description="请联系医院服务台"
      />
      <div
        v-else
        class="dept-grid"
      >
        <button
          v-for="item in departments"
          :key="item.id"
          type="button"
          class="dept"
          :class="{ 'is-active': booking.departmentId === item.id }"
          @click="chooseDepartment(item)"
        >
          <span class="dept-name">{{ item.name }}</span>
          <span class="dept-code mc-mono">{{ item.code }}</span>
          <span
            v-if="item.description"
            class="dept-desc"
          >{{ item.description }}</span>
        </button>
      </div>
    </section>

    <!-- 2. 医生 -->
    <section
      v-if="booking.departmentId"
      class="mc-card"
    >
      <div class="mc-card-title">
        <span>{{ booking.departmentName }} · 医生</span>
        <el-button
          v-if="step > 2"
          link
          type="primary"
          @click="backToDoctors"
        >
          重新选择
        </el-button>
      </div>

      <div
        v-if="loadingDoctors"
        class="mc-grid"
      >
        <el-skeleton
          v-for="i in 3"
          :key="i"
          :rows="3"
          animated
        />
      </div>
      <EmptyState
        v-else-if="!doctors.length"
        icon="stethoscope"
        title="该科室暂无出诊医生"
        description="可以换一个科室看看"
      />
      <div
        v-else
        class="mc-grid"
      >
        <button
          v-for="doctor in doctors"
          :key="doctor.id"
          type="button"
          class="doctor"
          :class="{ 'is-active': booking.doctorId === doctor.id }"
          @click="chooseDoctor(doctor)"
        >
          <div class="doctor-head">
            <span class="doctor-avatar">{{ doctor.name.slice(0, 1) }}</span>
            <div class="doctor-info">
              <span class="doctor-name">{{ doctor.name }}</span>
              <span class="doctor-title">{{ doctor.title }}</span>
            </div>
            <span
              v-if="doctor.is_primary"
              class="mc-tag mc-tag--teal"
            >主诊</span>
          </div>
          <p
            v-if="doctor.specialty"
            class="doctor-specialty"
          >
            {{ doctor.specialty }}
          </p>
          <div class="doctor-foot">
            <span class="mc-muted">挂号费</span>
            <!-- consult_fee 是字符串（DECIMAL），只展示、不做算术 -->
            <span class="fee">¥{{ doctor.consult_fee }}</span>
          </div>
        </button>
      </div>
    </section>

    <!-- 3. 号源 -->
    <section
      v-if="booking.doctorId"
      class="mc-card"
    >
      <div class="mc-card-title">
        <span>号源</span>
        <span class="mc-card-sub">{{ booking.doctorName }}</span>
      </div>

      <div
        v-if="loadingSchedules"
        class="slots"
      >
        <el-skeleton
          v-for="i in 2"
          :key="i"
          :rows="3"
          animated
        />
      </div>

      <EmptyState
        v-else-if="!availableDates.length"
        icon="calendar"
        title="未来两周暂无可用号源"
        description="可以换一位医生，或稍后再来看看"
      />

      <template v-else>
        <div class="date-scroll mc-nowrap-scroll">
          <button
            v-for="date in availableDates"
            :key="date"
            type="button"
            class="date-chip"
            :class="{ 'is-active': selectedDate === date }"
            @click="selectedDate = date"
          >
            <span class="date-main">{{ formatDateHuman(date) }}</span>
            <span class="date-sub">{{ formatMonthDay(date) }}</span>
            <span class="date-week">{{ weekdayOf(date) }}</span>
          </button>
        </div>

        <div class="slots">
          <article
            v-for="slot in slotsOfSelectedDate"
            :key="slot.id"
            class="slot"
          >
            <div class="slot-main">
              <div class="slot-head">
                <span class="slot-name">{{ timeSlotLabel(slot.time_slot) }}</span>
                <span class="mc-tag mc-tag--green">可预约</span>
              </div>
              <div class="slot-stock">
                剩余
                <b :class="{ 'is-low': slot.remaining <= 5 }">{{ slot.remaining }}</b>
                / {{ slot.total }}
              </div>
              <el-progress
                :percentage="Math.round((slot.remaining / Math.max(slot.total, 1)) * 100)"
                :show-text="false"
                :stroke-width="6"
                :color="slot.remaining <= 5 ? '#ea580c' : '#0d9488'"
              />
            </div>
            <el-button
              type="primary"
              :loading="grabbingId === slot.id"
              :disabled="grabbingId !== null"
              @click="handleGrab(slot)"
            >
              <AppIcon
                v-if="grabbingId !== slot.id"
                name="bolt"
                :size="15"
              />
              <span class="grab-text">抢号</span>
            </el-button>
          </article>
        </div>

        <p class="tip">
          <AppIcon
            name="clock"
            :size="14"
          />
          <span>抢号成功后请在 15 分钟内完成确认，超时号源会自动释放。</span>
        </p>
      </template>
    </section>
  </div>
</template>

<style scoped>
/* ---------------- 步骤条 ---------------- */

.steps {
  list-style: none;
  display: grid;
  grid-template-columns: 1fr;
  gap: 8px;
  padding: 0;
  margin: 0 0 14px;
}

@media (min-width: 768px) {
  .steps {
    grid-template-columns: repeat(3, 1fr);
    gap: 12px;
    margin-bottom: 18px;
  }
}

.step {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  background: var(--mc-surface);
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius);
  opacity: 0.6;
}

.step.is-active {
  opacity: 1;
  border-color: var(--mc-primary);
  background: #f0fdfa;
}

.step-no {
  width: 24px;
  height: 24px;
  flex-shrink: 0;
  border-radius: 50%;
  background: var(--mc-border);
  color: var(--mc-text-soft);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 700;
}

.step.is-active .step-no {
  background: var(--mc-primary);
  color: #fff;
}

.step-text {
  display: flex;
  flex-direction: column;
  min-width: 0;
}

.step-text b {
  font-size: 13px;
}

.step-text em {
  font-style: normal;
  font-size: 12px;
  color: var(--mc-text-muted);
}

/* ---------------- 科室 ---------------- */

.dept-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
  gap: 10px;
}

.dept {
  display: flex;
  flex-direction: column;
  gap: 2px;
  text-align: left;
  padding: 12px 14px;
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius);
  background: var(--mc-surface);
  cursor: pointer;
  font-family: inherit;
  transition: all 0.16s ease;
}

.dept:hover {
  border-color: var(--mc-primary);
}

.dept.is-active {
  border-color: var(--mc-primary);
  background: #f0fdfa;
  box-shadow: 0 0 0 2px rgba(13, 148, 136, 0.12);
}

.dept-name {
  font-size: 15px;
  font-weight: 600;
  color: var(--mc-text);
}

.dept-code {
  color: var(--mc-text-muted);
}

.dept-desc {
  font-size: 12px;
  color: var(--mc-text-muted);
  margin-top: 4px;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

/* ---------------- 医生 ---------------- */

.doctor {
  display: flex;
  flex-direction: column;
  gap: 8px;
  text-align: left;
  padding: 14px;
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius);
  background: var(--mc-surface);
  cursor: pointer;
  font-family: inherit;
  transition: all 0.16s ease;
}

.doctor:hover {
  border-color: var(--mc-primary);
}

.doctor.is-active {
  border-color: var(--mc-primary);
  background: #f0fdfa;
  box-shadow: 0 0 0 2px rgba(13, 148, 136, 0.12);
}

.doctor-head {
  display: flex;
  align-items: center;
  gap: 10px;
}

.doctor-avatar {
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

.doctor-info {
  display: flex;
  flex-direction: column;
  min-width: 0;
  flex: 1;
}

.doctor-name {
  font-size: 15px;
  font-weight: 600;
}

.doctor-title {
  font-size: 12px;
  color: var(--mc-text-muted);
}

.doctor-specialty {
  font-size: 12px;
  color: var(--mc-text-soft);
  display: -webkit-box;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}

.doctor-foot {
  display: flex;
  align-items: baseline;
  justify-content: space-between;
  font-size: 12px;
  border-top: 1px dashed var(--mc-border);
  padding-top: 8px;
}

.fee {
  font-size: 15px;
  font-weight: 700;
  color: var(--mc-primary-dark);
}

/* ---------------- 日期 chips ---------------- */

.date-scroll {
  margin-bottom: 14px;
}

.date-chip {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 1px;
  min-width: 76px;
  padding: 8px 10px;
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius);
  background: var(--mc-surface);
  cursor: pointer;
  font-family: inherit;
}

.date-chip.is-active {
  border-color: var(--mc-primary);
  background: var(--mc-primary);
  color: #fff;
}

.date-main {
  font-size: 13px;
  font-weight: 600;
}

.date-sub,
.date-week {
  font-size: 11px;
  color: var(--mc-text-muted);
}

.date-chip.is-active .date-sub,
.date-chip.is-active .date-week {
  color: rgba(255, 255, 255, 0.85);
}

/* ---------------- 号源 ---------------- */

.slots {
  display: grid;
  grid-template-columns: 1fr;
  gap: 10px;
}

@media (min-width: 640px) {
  .slots {
    grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
  }
}

.slot {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius);
  background: var(--mc-surface-alt);
}

.slot-main {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}

.slot-head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.slot-name {
  font-size: 15px;
  font-weight: 600;
}

.slot-stock {
  font-size: 12px;
  color: var(--mc-text-soft);
}

.slot-stock b {
  font-size: 16px;
  color: var(--mc-primary-dark);
  margin: 0 2px;
}

.slot-stock b.is-low {
  color: var(--mc-warning);
}

.grab-text {
  margin-left: 4px;
}

.tip {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 14px;
  font-size: 12px;
  color: var(--mc-text-muted);
}
</style>

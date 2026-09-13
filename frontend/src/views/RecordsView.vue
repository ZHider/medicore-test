<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'

import AppIcon from '@/components/AppIcon.vue'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { getRecord, listRecords } from '@/api/record'
import { ErrorCode, toApiError } from '@/api/error'
import { useCursorList, useInfiniteScroll } from '@/composables/useCursorList'
import { useIsMobile } from '@/composables/useMediaQuery'
import { useAuthStore } from '@/stores/auth'
import type { MedicalRecordListItem, MedicalRecordView } from '@/types/api'
import { formatDateTime } from '@/utils/datetime'

const auth = useAuthStore()
const isMobile = useIsMobile()

const list = useCursorList<MedicalRecordListItem>((query) => listRecords(query), { size: 20 })

const sentinel = ref<HTMLElement | null>(null)
const canLoadMore = computed(() => !list.finished.value && !list.loading.value)
const { connect, disconnect } = useInfiniteScroll(
  sentinel,
  () => {
    void list.loadMore()
  },
  canLoadMore,
)

onMounted(() => void nextTick(() => connect()))
onUnmounted(disconnect)
watch(sentinel, () => connect())

// ---------------------------------------------------------------------------
// 详情
// ---------------------------------------------------------------------------

const drawerOpen = ref(false)
const detail = ref<MedicalRecordView | null>(null)
const detailLoading = ref(false)

const drawerSize = computed(() => (isMobile.value ? '100%' : '620px'))

/** 列表接口按角色返回不同内容，这里给出对应的说明文案。 */
const listHint = computed(() => {
  switch (auth.role) {
    case 'patient':
      return '这里是你的就诊病历记录'
    case 'doctor':
      return '这里是你书写过的病历'
    case 'admin':
      return '管理员可见全部病历'
    default:
      return ''
  }
})

async function openDetail(item: MedicalRecordListItem): Promise<void> {
  drawerOpen.value = true
  detail.value = null
  detailLoading.value = true

  try {
    detail.value = await getRecord(item.appointment_id)
  } catch (e) {
    const err = toApiError(e)
    if (err.code === ErrorCode.NotFound) {
      ElMessage.warning('该病历已不存在')
    } else if (err.code === ErrorCode.NotOwner) {
      ElMessage.error('无权查看该病历')
    } else {
      ElMessage.error(err.displayMessage)
    }
    drawerOpen.value = false
  } finally {
    detailLoading.value = false
  }
}
</script>

<template>
  <div>
    <PageHeader
      title="病历记录"
      :description="listHint"
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

    <div
      v-if="!list.loaded.value && list.loading.value"
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
      icon="document"
      title="暂无病历记录"
      description="完成一次就诊并由医生书写病历后，这里就会有内容"
    />

    <template v-else>
      <div class="list">
        <button
          v-for="item in list.items.value"
          :key="item.id"
          type="button"
          class="record"
          @click="openDetail(item)"
        >
          <div class="record-head">
            <span class="record-patient">{{ item.patient_name }}</span>
            <span class="mc-tag mc-tag--teal">{{ item.department_name }}</span>
            <span class="record-date mc-muted">{{ item.sched_date }}</span>
          </div>
          <div class="record-diag">
            <span class="diag-label">诊断</span>
            <span class="diag-text">{{ item.diagnosis }}</span>
          </div>
          <div class="record-foot mc-muted">
            <span>接诊医生：{{ item.doctor_name }}</span>
            <span>书写于 {{ formatDateTime(item.created_at) }}</span>
          </div>
        </button>
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

    <!-- 详情抽屉：手机上全屏，桌面上右侧 620px -->
    <el-drawer
      v-model="drawerOpen"
      :size="drawerSize"
      :title="detail ? `${detail.patient_name} 的病历` : '病历详情'"
    >
      <div
        v-if="detailLoading"
        class="detail-loading"
      >
        <el-skeleton
          :rows="6"
          animated
        />
      </div>

      <div
        v-else-if="detail"
        class="detail"
      >
        <div class="detail-meta">
          <div class="meta-item">
            <span class="meta-label">就诊科室</span>
            <span>{{ detail.department_name }}</span>
          </div>
          <div class="meta-item">
            <span class="meta-label">接诊医生</span>
            <span>{{ detail.doctor_name }}</span>
          </div>
          <div class="meta-item">
            <span class="meta-label">就诊日期</span>
            <span>{{ detail.sched_date }}</span>
          </div>
          <div class="meta-item">
            <span class="meta-label">书写时间</span>
            <span>{{ formatDateTime(detail.created_at) }}</span>
          </div>
        </div>

        <section class="block">
          <h3>主诉</h3>
          <p>{{ detail.chief_complaint || '—' }}</p>
        </section>

        <section class="block">
          <h3>现病史</h3>
          <p>{{ detail.present_illness || '—' }}</p>
        </section>

        <section class="block block--highlight">
          <h3>诊断</h3>
          <p class="strong">
            {{ detail.diagnosis }}
          </p>
        </section>

        <section class="block">
          <h3>医嘱</h3>
          <p>{{ detail.treatment_advice || '—' }}</p>
        </section>

        <section class="block">
          <h3>处方（{{ detail.prescriptions?.length ?? 0 }} 项）</h3>
          <div
            v-if="detail.prescriptions?.length"
            class="rx-list"
          >
            <div
              v-for="(rx, index) in detail.prescriptions"
              :key="index"
              class="rx"
            >
              <div class="rx-line">
                <span class="rx-name">{{ rx.drug_name }}</span>
                <span class="rx-qty">× {{ rx.quantity }}</span>
              </div>
              <div class="rx-line mc-muted">
                <span v-if="rx.spec">{{ rx.spec }}</span>
                <span v-if="rx.usage_desc">{{ rx.usage_desc }}</span>
                <span v-if="rx.days">{{ rx.days }} 天</span>
              </div>
            </div>
          </div>
          <p
            v-else
            class="mc-muted"
          >
            本次未开具处方
          </p>
        </section>
      </div>

      <EmptyState
        v-else
        title="没有数据"
      />
    </el-drawer>
  </div>
</template>

<style scoped>
.list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.record {
  display: flex;
  flex-direction: column;
  gap: 8px;
  text-align: left;
  width: 100%;
  padding: 14px 16px;
  background: var(--mc-surface);
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius-lg);
  box-shadow: var(--mc-shadow-sm);
  cursor: pointer;
  font-family: inherit;
  transition: all 0.16s ease;
}

.record:hover {
  border-color: var(--mc-primary);
  box-shadow: var(--mc-shadow);
}

.record-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.record-patient {
  font-size: 15px;
  font-weight: 600;
}

.record-date {
  font-size: 12px;
}

.record-diag {
  display: flex;
  gap: 8px;
  font-size: 14px;
  align-items: baseline;
}

.diag-label {
  font-size: 12px;
  color: var(--mc-text-muted);
  flex-shrink: 0;
}

.diag-text {
  color: var(--mc-text);
  overflow: hidden;
  text-overflow: ellipsis;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  line-clamp: 2;
  -webkit-box-orient: vertical;
}

.record-foot {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 16px;
  font-size: 12px;
}

.sentinel {
  padding: 20px 0;
  text-align: center;
  font-size: 13px;
  color: var(--mc-text-soft);
}

/* ---------------- 抽屉内容 ---------------- */

.detail-meta {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
  padding: 12px 14px;
  background: var(--mc-surface-alt);
  border-radius: var(--mc-radius);
  margin-bottom: 18px;
}

.meta-item {
  display: flex;
  flex-direction: column;
  font-size: 13px;
}

.meta-label {
  font-size: 11px;
  color: var(--mc-text-muted);
  margin-bottom: 2px;
}

.block {
  margin-bottom: 18px;
}

.block h3 {
  font-size: 13px;
  color: var(--mc-text-muted);
  font-weight: 600;
  margin-bottom: 6px;
}

.block p {
  font-size: 14px;
  line-height: 1.75;
  white-space: pre-wrap;
  word-break: break-word;
}

.block--highlight {
  background: #f0fdfa;
  border-left: 3px solid var(--mc-primary);
  border-radius: 0 var(--mc-radius) var(--mc-radius) 0;
  padding: 10px 14px;
}

.block--highlight h3 {
  color: var(--mc-primary-dark);
}

.strong {
  font-weight: 600;
  font-size: 15px;
}

.rx-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.rx {
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius);
  padding: 10px 12px;
}

.rx-line {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  font-size: 13px;
}

.rx-name {
  font-weight: 600;
}

.rx-qty {
  color: var(--mc-primary-dark);
  font-weight: 600;
}

.detail-loading {
  padding: 8px;
}

.btn-text {
  margin-left: 4px;
}
</style>

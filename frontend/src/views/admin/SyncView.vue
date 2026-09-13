<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { ElMessage, ElMessageBox } from 'element-plus'

import AppIcon from '@/components/AppIcon.vue'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatusTag from '@/components/StatusTag.vue'
import { listSyncLogs, triggerSync } from '@/api/admin'
import { toApiError } from '@/api/error'
import { useCursorList, useInfiniteScroll } from '@/composables/useCursorList'
import type { SyncLogView } from '@/types/api'
import { formatDateTime } from '@/utils/datetime'

/** 当前后端只接受这一个数据源，其它值返回 404。 */
const SOURCE = 'mockhis'

const sourceFilter = ref('')

const list = useCursorList<SyncLogView>((query) => listSyncLogs(query), {
  size: 20,
  params: () => (sourceFilter.value ? { source: sourceFilter.value } : {}),
  immediate: false,
})

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

// ---------------------------------------------------------------------------
// 手动触发同步
// ---------------------------------------------------------------------------

const syncing = ref(false)
const lastResult = ref<SyncLogView | null>(null)

async function handleSync(): Promise<void> {
  try {
    await ElMessageBox.confirm(
      '将从模拟 HIS 数据源拉取医生与排班数据，不会删除已有数据。是否继续？',
      '触发 HIS 同步',
      { confirmButtonText: '开始同步', cancelButtonText: '取消', type: 'info' },
    )
  } catch {
    return
  }

  syncing.value = true
  try {
    // 该端点是**同步返回**的（不是 202），前端不需要轮询
    const result = await triggerSync(SOURCE)
    lastResult.value = result

    if (result.status === 'success') {
      ElMessage.success(`同步完成：共 ${result.total} 条，成功 ${result.succeeded} 条`)
    } else if (result.status === 'partial') {
      ElMessage.warning(
        `部分成功：共 ${result.total} 条，成功 ${result.succeeded}，失败 ${result.failed}`,
      )
    } else {
      ElMessage.error(result.error_message || '同步失败，请查看同步记录')
    }

    await list.refresh()
  } catch (e) {
    const err = toApiError(e)
    if (err.code === 40003) {
      ElMessage.error(`未知的数据源，当前仅支持 ${SOURCE}`)
    } else {
      ElMessage.error(err.displayMessage)
    }
  } finally {
    syncing.value = false
  }
}

function applySourceFilter(): void {
  void list.refresh()
}
</script>

<template>
  <div>
    <PageHeader
      title="HIS 数据同步"
      description="从外部医院信息系统拉取医生与排班数据，同步过程与结果全程留痕"
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
          <span class="btn-text">刷新记录</span>
        </el-button>
      </template>
    </PageHeader>

    <!-- 触发区 -->
    <div class="mc-card trigger">
      <div class="trigger-main">
        <div class="trigger-icon">
          <AppIcon
            name="database"
            :size="20"
          />
        </div>
        <div class="trigger-info">
          <div class="trigger-title">
            数据源 · {{ SOURCE }}
          </div>
          <p class="mc-muted">
            模拟 HIS（mockhis）。点击后同步**同步执行**并直接返回结果，无需轮询。
            接入真实 HIS 后若耗时变长，后端会改为异步 + 轮询模式。
          </p>
        </div>
      </div>
      <el-button
        type="primary"
        size="large"
        :loading="syncing"
        @click="handleSync"
      >
        <AppIcon
          v-if="!syncing"
          name="bolt"
          :size="16"
        />
        <span class="btn-text">立即同步</span>
      </el-button>
    </div>

    <!-- 本次结果 -->
    <div
      v-if="lastResult"
      class="mc-card"
    >
      <div class="mc-card-title">
        <span>本次同步结果</span>
        <StatusTag :value="lastResult.status" />
      </div>
      <div class="result-grid">
        <div class="result-item">
          <span class="result-label">总计</span>
          <span class="result-value">{{ lastResult.total }}</span>
        </div>
        <div class="result-item">
          <span class="result-label">成功</span>
          <span class="result-value ok">{{ lastResult.succeeded }}</span>
        </div>
        <div class="result-item">
          <span class="result-label">失败</span>
          <span
            class="result-value"
            :class="{ bad: lastResult.failed > 0 }"
          >
            {{ lastResult.failed }}
          </span>
        </div>
        <div class="result-item">
          <span class="result-label">触发方式</span>
          <span class="result-value small">
            {{ lastResult.trigger_type === 'manual' ? '手动' : '定时' }}
          </span>
        </div>
        <div class="result-item">
          <span class="result-label">开始</span>
          <span class="result-value small">{{ formatDateTime(lastResult.started_at) }}</span>
        </div>
        <div class="result-item">
          <span class="result-label">结束</span>
          <span class="result-value small">{{ formatDateTime(lastResult.finished_at) }}</span>
        </div>
      </div>
      <el-alert
        v-if="lastResult.error_message"
        type="error"
        show-icon
        :closable="false"
        :title="lastResult.error_message"
        class="mt"
      />
    </div>

    <!-- 历史记录 -->
    <div class="mc-card">
      <div class="mc-card-title">
        <span>同步记录</span>
        <div class="source-filter">
          <el-input
            v-model="sourceFilter"
            placeholder="按数据源筛选，如 mockhis"
            clearable
            size="small"
            @change="applySourceFilter"
          />
        </div>
      </div>

      <div v-if="!list.loaded.value && list.loading.value">
        <el-skeleton
          :rows="5"
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
        icon="database"
        title="暂无同步记录"
        description="点击上方「立即同步」触发第一次同步"
      />

      <template v-else>
        <!-- 请求记录：窄屏卡片，宽屏表格 -->
        <div class="logs">
          <article
            v-for="log in list.items.value"
            :key="log.id"
            class="log"
          >
            <div class="log-head">
              <span class="log-source">{{ log.source }}</span>
              <span
                class="mc-tag"
                :class="log.trigger_type === 'manual' ? 'mc-tag--blue' : 'mc-tag--gray'"
              >
                {{ log.trigger_type === 'manual' ? '手动' : '定时' }}
              </span>
              <StatusTag :value="log.status" />
              <span class="log-time mc-muted">{{ formatDateTime(log.started_at) }}</span>
            </div>
            <div class="log-stats">
              <span>总计 <b>{{ log.total }}</b></span>
              <span>成功 <b class="ok">{{ log.succeeded }}</b></span>
              <span>失败 <b :class="{ bad: log.failed > 0 }">{{ log.failed }}</b></span>
              <span class="mc-muted">结束于 {{ formatDateTime(log.finished_at) }}</span>
            </div>
            <p
              v-if="log.error_message"
              class="log-error"
            >
              {{ log.error_message }}
            </p>
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
    </div>
  </div>
</template>

<style scoped>
/* ---------------- 触发区 ---------------- */

.trigger {
  display: flex;
  flex-direction: column;
  gap: 14px;
  margin-bottom: 14px;
}

@media (min-width: 768px) {
  .trigger {
    flex-direction: row;
    align-items: center;
    justify-content: space-between;
  }
}

.trigger-main {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  min-width: 0;
}

.trigger-icon {
  width: 42px;
  height: 42px;
  flex-shrink: 0;
  border-radius: 12px;
  background: var(--mc-primary-soft);
  color: var(--mc-primary-dark);
  display: flex;
  align-items: center;
  justify-content: center;
}

.trigger-title {
  font-size: 15px;
  font-weight: 600;
  margin-bottom: 2px;
}

.trigger-info p {
  font-size: 12px;
  line-height: 1.65;
}

.trigger :deep(.el-button) {
  flex-shrink: 0;
}

/* ---------------- 结果 ---------------- */

.result-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

@media (min-width: 768px) {
  .result-grid {
    grid-template-columns: repeat(6, minmax(0, 1fr));
  }
}

.result-item {
  display: flex;
  flex-direction: column;
  padding: 8px 10px;
  background: var(--mc-surface-alt);
  border-radius: var(--mc-radius);
  min-width: 0;
}

.result-label {
  font-size: 11px;
  color: var(--mc-text-muted);
}

.result-value {
  font-size: 18px;
  font-weight: 700;
}

.result-value.small {
  font-size: 12px;
  font-weight: 500;
  word-break: break-all;
}

.result-value.ok {
  color: #15803d;
}

.result-value.bad {
  color: var(--mc-danger);
}

.mt {
  margin-top: 14px;
}

/* ---------------- 记录 ---------------- */

.source-filter {
  width: 200px;
  max-width: 100%;
}

.logs {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.log {
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius);
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.log-head {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}

.log-source {
  font-weight: 600;
  font-size: 14px;
  font-family: var(--mc-mono);
}

.log-time {
  margin-left: auto;
  font-size: 12px;
}

.log-stats {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 16px;
  font-size: 12px;
  color: var(--mc-text-soft);
}

.log-stats b {
  font-size: 14px;
  color: var(--mc-text);
}

.log-stats b.ok {
  color: #15803d;
}

.log-stats b.bad {
  color: var(--mc-danger);
}

.log-error {
  font-size: 12px;
  color: #b91c1c;
  background: #fef2f2;
  border-radius: 6px;
  padding: 6px 10px;
  word-break: break-all;
}

.sentinel {
  padding: 18px 0 0;
  text-align: center;
  font-size: 13px;
  color: var(--mc-text-soft);
}

.btn-text {
  margin-left: 4px;
}
</style>

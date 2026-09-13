<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { ElMessage } from 'element-plus'

import AppIcon from '@/components/AppIcon.vue'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { listAuditLogs } from '@/api/admin'
import { toApiError } from '@/api/error'
import { useCursorList, useInfiniteScroll } from '@/composables/useCursorList'
import { useIsMobile } from '@/composables/useMediaQuery'
import type { AuditLogQuery, AuditLogView } from '@/types/api'
import { formatDateTime } from '@/utils/datetime'

const isMobile = useIsMobile()

// 筛选条件。注意 from/to 接受 YYYY-MM-DD 或 YYYY-MM-DD HH:MM:SS
const filters = ref<{
  resource: string
  action: string
  trace_id: string
  user_id: string
  from: string
  to: string
}>({
  resource: '',
  action: '',
  trace_id: '',
  user_id: '',
  from: '',
  to: '',
})

// 显式给出 P：AuditLogQuery 是 interface，无法从 fetcher 的交叉类型参数里被推断出来
const list = useCursorList<AuditLogView, AuditLogQuery>((query) => listAuditLogs(query), {
  size: 20,
  params: () => buildQuery(),
  immediate: false,
})

/**
 * 组装查询参数。
 *
 * `user_id` 必须自己做数字校验：后端对非法值**静默忽略**（不报错），
 * 于是"筛选了却没生效"极难察觉 —— 这里宁可把它当作未填写并给出提示。
 *
 * `trace_id` 相反：后端会校验格式并返回 40001，前端只做**宽松**的预处理
 * （去空白、转小写），不拦请求 —— 免得前后端的校验规则各写一套、日渐漂移。
 */
function buildQuery(): AuditLogQuery {
  const query: AuditLogQuery = {}
  if (filters.value.resource) query.resource = filters.value.resource
  if (filters.value.action) query.action = filters.value.action
  if (filters.value.from) query.from = filters.value.from
  if (filters.value.to) query.to = filters.value.to

  // 从界面复制粘贴常带空白，而列排序规则是逐字节比较 —— 归一化交给后端，
  // 但空白在 URL 里会被编码成 %20，所以这里顺手去掉。
  const traceId = filters.value.trace_id.trim()
  if (traceId) query.trace_id = traceId

  if (filters.value.user_id) {
    const parsed = Number(filters.value.user_id)
    if (Number.isInteger(parsed) && parsed > 0) query.user_id = parsed
  }
  return query
}

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

let searchTimer: number | null = null

function applyFilters(): void {
  if (filters.value.user_id) {
    const parsed = Number(filters.value.user_id)
    if (!Number.isInteger(parsed) || parsed <= 0) {
      ElMessage.warning('用户 ID 必须是正整数，已忽略该条件')
    }
  }
  void list.refresh()
}

/** 输入防抖，避免每敲一个字符就发一次请求 */
function scheduleApply(): void {
  if (searchTimer !== null) window.clearTimeout(searchTimer)
  searchTimer = window.setTimeout(applyFilters, 400)
}

onUnmounted(() => {
  if (searchTimer !== null) window.clearTimeout(searchTimer)
})

function resetFilters(): void {
  filters.value = { resource: '', action: '', trace_id: '', user_id: '', from: '', to: '' }
  void list.refresh()
}

/** 用某一行的追踪号直接筛选 —— 把"看到 → 定位到这一条"的闭环合上。 */
function filterByTrace(traceId: string): void {
  filters.value.trace_id = traceId
  void list.refresh()
}

async function copyTrace(traceId: string): Promise<void> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(traceId)
    } else {
      // 非安全上下文下 clipboard 不可用，退回临时 textarea
      const el = document.createElement('textarea')
      el.value = traceId
      document.body.appendChild(el)
      el.select()
      document.execCommand('copy')
      document.body.removeChild(el)
    }
    ElMessage.success('追踪号已复制，可在后端日志与审计中检索')
  } catch {
    ElMessage.warning(`复制失败，追踪号：${traceId}`)
  }
}

function statusClass(code: number): string {
  if (code >= 500) return 'code-bad'
  if (code >= 400) return 'code-warn'
  return 'code-ok'
}
</script>

<template>
  <div>
    <PageHeader
      title="审计日志"
      description="支持按资源、动作、用户与时间范围筛选；追踪号可用于定位单次请求"
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

    <!-- 筛选 -->
    <div class="mc-card filters">
      <div class="field">
        <label>资源</label>
        <el-input
          v-model="filters.resource"
          placeholder="如 appointments"
          clearable
          @change="scheduleApply"
        />
      </div>
      <div class="field">
        <label>动作</label>
        <el-input
          v-model="filters.action"
          placeholder="如 create / cancel"
          clearable
          @change="scheduleApply"
        />
      </div>
      <div class="field">
        <label>追踪号</label>
        <el-input
          v-model="filters.trace_id"
          placeholder="32 位十六进制"
          clearable
          @change="scheduleApply"
        />
      </div>
      <div class="field">
        <label>用户 ID</label>
        <el-input
          v-model="filters.user_id"
          placeholder="正整数"
          inputmode="numeric"
          clearable
          @change="scheduleApply"
        />
      </div>
      <div class="field">
        <label>起始时间</label>
        <el-date-picker
          v-model="filters.from"
          type="datetime"
          value-format="YYYY-MM-DD HH:mm:ss"
          placeholder="YYYY-MM-DD HH:MM:SS"
          @change="scheduleApply"
        />
      </div>
      <div class="field">
        <label>结束时间</label>
        <el-date-picker
          v-model="filters.to"
          type="datetime"
          value-format="YYYY-MM-DD HH:mm:ss"
          placeholder="YYYY-MM-DD HH:MM:SS"
          @change="scheduleApply"
        />
      </div>
      <div class="field field--actions">
        <el-button @click="resetFilters">
          重置
        </el-button>
        <el-button
          type="primary"
          @click="applyFilters"
        >
          查询
        </el-button>
      </div>
    </div>

    <!-- 加载态 -->
    <div
      v-if="!list.loaded.value && list.loading.value"
      class="mc-card"
    >
      <el-skeleton
        :rows="6"
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
      icon="shield"
      title="没有匹配的审计记录"
      description="放宽筛选条件或调整时间范围再试"
    />

    <template v-else>
      <!-- 移动端：卡片列表（7 列宽表在手机上无法阅读） -->
      <div
        v-if="isMobile"
        class="cards"
      >
        <article
          v-for="log in list.items.value"
          :key="log.id"
          class="log-card"
        >
          <div class="log-card-head">
            <span class="method">{{ log.method }}</span>
            <span :class="statusClass(log.status_code)">{{ log.status_code }}</span>
            <span class="mc-muted latency">{{ log.latency_ms }}ms</span>
          </div>
          <div class="log-path">
            {{ log.path }}
          </div>
          <div class="log-meta">
            <span>{{ formatDateTime(log.created_at) }}</span>
            <span v-if="log.user_id">用户 {{ log.user_id }} ({{ log.role ?? '—' }})</span>
            <span v-if="log.resource">{{ log.resource }}#{{ log.resource_id ?? '—' }}</span>
            <span v-if="log.action">动作 {{ log.action }}</span>
          </div>
          <div class="trace-actions">
            <button
              class="trace-btn"
              type="button"
              title="复制追踪号"
              @click="copyTrace(log.trace_id)"
            >
              <AppIcon
                name="search"
                :size="13"
              />
              <span class="mc-mono">{{ log.trace_id }}</span>
            </button>
            <button
              class="trace-filter-btn"
              type="button"
              title="以该追踪号筛选"
              @click="filterByTrace(log.trace_id)"
            >
              筛选
            </button>
          </div>
        </article>
      </div>

      <!-- 桌面端：表格 -->
      <div
        v-else
        class="mc-card table-card"
      >
        <div class="mc-scroll-x">
          <table class="table">
            <thead>
              <tr>
                <th>时间</th>
                <th>用户</th>
                <th>方法</th>
                <th>路径</th>
                <th>资源</th>
                <th>动作</th>
                <th>状态</th>
                <th>耗时</th>
                <th>追踪号</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="log in list.items.value"
                :key="log.id"
              >
                <td class="nowrap">
                  {{ formatDateTime(log.created_at) }}
                </td>
                <td class="nowrap">
                  {{ log.user_id ?? '—' }}
                  <span class="mc-muted">{{ log.role ?? '' }}</span>
                </td>
                <td class="nowrap">
                  {{ log.method }}
                </td>
                <td class="path">
                  {{ log.path }}
                </td>
                <td class="nowrap">
                  {{ log.resource ?? '—' }}
                </td>
                <td class="nowrap">
                  {{ log.action ?? '—' }}
                </td>
                <td class="nowrap">
                  <span :class="statusClass(log.status_code)">{{ log.status_code }}</span>
                </td>
                <td class="nowrap">
                  {{ log.latency_ms }}ms
                </td>
                <td>
                  <div class="trace-actions">
                    <button
                      class="trace-btn trace-btn--sm"
                      type="button"
                      title="复制追踪号"
                      @click="copyTrace(log.trace_id)"
                    >
                      <span class="mc-mono">{{ log.trace_id }}</span>
                    </button>
                    <button
                      class="trace-filter-btn"
                      type="button"
                      title="以该追踪号筛选"
                      @click="filterByTrace(log.trace_id)"
                    >
                      筛选
                    </button>
                  </div>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
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
</template>

<style scoped>
.filters {
  display: grid;
  grid-template-columns: 1fr;
  gap: 10px;
  margin-bottom: 14px;
}

@media (min-width: 640px) {
  .filters {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

@media (min-width: 1100px) {
  .filters {
    grid-template-columns: repeat(3, minmax(0, 1fr)) auto;
  }

  .field--actions {
    grid-column: span 1;
  }
}

.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.field label {
  font-size: 12px;
  color: var(--mc-text-muted);
}

.field :deep(.el-date-editor) {
  width: 100%;
}

.field--actions {
  flex-direction: row;
  align-items: flex-end;
  gap: 8px;
}

/* ---------------- 移动端卡片 ---------------- */

.cards {
  display: flex;
  flex-direction: column;
  gap: 10px;
}

.log-card {
  background: var(--mc-surface);
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius-lg);
  padding: 12px 14px;
  display: flex;
  flex-direction: column;
  gap: 6px;
  box-shadow: var(--mc-shadow-sm);
}

.log-card-head {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 13px;
}

.method {
  font-weight: 700;
  font-family: var(--mc-mono);
}

.latency {
  margin-left: auto;
  font-size: 12px;
}

.log-path {
  font-size: 13px;
  word-break: break-all;
  color: var(--mc-text);
}

.log-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 14px;
  font-size: 11px;
  color: var(--mc-text-muted);
}

/* ---------------- 表格 ---------------- */

.table-card {
  padding: 0;
  overflow: hidden;
}

.table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
  min-width: 1040px;
}

.table th,
.table td {
  text-align: left;
  padding: 9px 12px;
  border-bottom: 1px solid var(--mc-border);
}

.table th {
  background: var(--mc-surface-alt);
  color: var(--mc-text-muted);
  font-weight: 500;
  position: sticky;
  top: 0;
}

.table tbody tr:hover {
  background: #f8fafc;
}

.nowrap {
  white-space: nowrap;
}

.path {
  max-width: 280px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.code-ok {
  color: #15803d;
  font-weight: 600;
}

.code-warn {
  color: #c2410c;
  font-weight: 600;
}

.code-bad {
  color: #b91c1c;
  font-weight: 700;
}

.trace-btn {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  border: 1px dashed var(--mc-border-strong);
  background: transparent;
  color: var(--mc-text-soft);
  border-radius: 6px;
  padding: 3px 8px;
  cursor: pointer;
  font-family: inherit;
  max-width: 100%;
  overflow: hidden;
}

.trace-btn:hover {
  border-color: var(--mc-primary);
  color: var(--mc-primary-dark);
}

.trace-btn--sm {
  font-size: 11px;
}

/* 追踪号的两个动作：复制（拿去查日志）与筛选（就地看这一条链路） */
.trace-actions {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  max-width: 100%;
}

.trace-filter-btn {
  flex: none;
  border: none;
  background: transparent;
  color: var(--mc-primary-dark);
  cursor: pointer;
  font-family: inherit;
  font-size: 12px;
  padding: 2px 4px;
  border-radius: 4px;
}

.trace-filter-btn:hover {
  background: var(--mc-primary-soft);
  text-decoration: underline;
}

.sentinel {
  padding: 20px 0;
  text-align: center;
  font-size: 13px;
  color: var(--mc-text-soft);
}

.btn-text {
  margin-left: 4px;
}
</style>

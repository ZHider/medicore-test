<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
import { ElMessage } from 'element-plus'
import type { EChartsOption } from 'echarts'

import AppIcon from '@/components/AppIcon.vue'
import EChart from '@/components/EChart.vue'
import PageHeader from '@/components/PageHeader.vue'
import StatusTag from '@/components/StatusTag.vue'
import { listAuditLogs } from '@/api/admin'
import { listForDoctor } from '@/api/appointment'
import { listDepartments } from '@/api/catalog'
import { toApiError } from '@/api/error'
import { fetchMetrics, fetchReadyz } from '@/api/system'
import type {
  AuditLogView,
  CheckResult,
  DoctorAppointmentItem,
  MetricSample,
} from '@/types/api'
import { formatDateTime, timeSlotLabel } from '@/utils/datetime'
import { filterByName, groupByLabel, parsePrometheus, sumByName } from '@/utils/prometheus'

/** 一次拉取的最大样本数（后端 size 上限 100）。 */
const SAMPLE_SIZE = 100
const REFRESH_INTERVAL = 30000

const loading = ref(false)
const lastUpdated = ref('')

const readyz = ref<Record<string, CheckResult>>({})
const readyzCode = ref(0)
const metrics = ref<MetricSample[]>([])
const appointments = ref<DoctorAppointmentItem[]>([])
const auditLogs = ref<AuditLogView[]>([])
const departmentNames = ref<Map<number, string>>(new Map())

let timer: number | null = null

// ---------------------------------------------------------------------------
// 加载
// ---------------------------------------------------------------------------

async function loadAll(showError = true): Promise<void> {
  loading.value = true

  // 四个来源互不依赖，并发拉取；任一失败不影响其余面板
  const [health, metricText, apptPage, auditPage, depts] = await Promise.allSettled([
    fetchReadyz(),
    fetchMetrics(),
    listForDoctor({ size: SAMPLE_SIZE }),
    listAuditLogs({ size: 30 }),
    listDepartments(),
  ])

  if (health.status === 'fulfilled') {
    readyz.value = health.value.data
    readyzCode.value = health.value.httpStatus
  } else if (showError) {
    ElMessage.error('健康检查失败，后端可能未启动')
  }

  if (metricText.status === 'fulfilled') {
    metrics.value = parsePrometheus(metricText.value)
  }

  if (apptPage.status === 'fulfilled') {
    appointments.value = apptPage.value.items ?? []
  } else if (showError) {
    ElMessage.error(toApiError(apptPage.reason).displayMessage)
  }

  if (auditPage.status === 'fulfilled') {
    auditLogs.value = auditPage.value.items ?? []
  }

  if (depts.status === 'fulfilled') {
    departmentNames.value = new Map(
      (depts.value.items ?? []).map((item) => [item.id, item.name]),
    )
  }

  lastUpdated.value = formatDateTime(new Date().toISOString().slice(0, 19))
  loading.value = false
}

onMounted(async () => {
  await loadAll()
  timer = window.setInterval(() => void loadAll(false), REFRESH_INTERVAL)
})

onUnmounted(() => {
  if (timer !== null) window.clearInterval(timer)
})

// ---------------------------------------------------------------------------
// KPI
// ---------------------------------------------------------------------------

const dependencyList = computed(() =>
  Object.entries(readyz.value).map(([name, result]) => ({ name, ...result })),
)

const unhealthyCount = computed(
  () => dependencyList.value.filter((item) => item.status !== 'ok').length,
)

const totalRequests = computed(() => sumByName(metrics.value, 'http_requests_total'))
const totalErrors = computed(() =>
  filterByName(metrics.value, 'http_requests_total')
    .filter((sample) => {
      const status = Number(sample.labels.status ?? 0)
      return status >= 500
    })
    .reduce((acc, sample) => acc + sample.value, 0),
)

const errorRate = computed(() => {
  if (!totalRequests.value) return '0.00%'
  return `${((totalErrors.value / totalRequests.value) * 100).toFixed(2)}%`
})

const grabTotal = computed(() => sumByName(metrics.value, 'appointment_grab_total'))
const llmTotal = computed(() => sumByName(metrics.value, 'llm_calls_total'))
const summaryTotal = computed(() => sumByName(metrics.value, 'summary_tasks_total'))

const pendingRecords = computed(
  () => appointments.value.filter((item) => !item.has_record).length,
)

// ---------------------------------------------------------------------------
// 图表
// ---------------------------------------------------------------------------

const AXIS_STYLE = {
  axisLine: { lineStyle: { color: '#e2e8f0' } },
  axisLabel: { color: '#64748b', fontSize: 11 },
  splitLine: { lineStyle: { color: '#f1f5f9' } },
}

const BASE_GRID = { left: 8, right: 20, top: 30, bottom: 8, containLabel: true }

/** 预约趋势：按就诊日统计（数据来自 /doctor/appointments 的最近 100 条样本）。 */
const trendOption = computed<EChartsOption>(() => {
  const counter = new Map<string, number>()
  for (const item of appointments.value) {
    counter.set(item.sched_date, (counter.get(item.sched_date) ?? 0) + 1)
  }
  const sorted = [...counter.entries()].sort((a, b) => a[0].localeCompare(b[0]))

  return {
    tooltip: { trigger: 'axis' },
    grid: BASE_GRID,
    xAxis: { type: 'category', data: sorted.map(([date]) => date), ...AXIS_STYLE },
    yAxis: { type: 'value', minInterval: 1, ...AXIS_STYLE },
    series: [
      {
        name: '预约量',
        type: 'line',
        smooth: true,
        symbolSize: 7,
        data: sorted.map(([, count]) => count),
        itemStyle: { color: '#0d9488' },
        lineStyle: { width: 3 },
        areaStyle: {
          color: {
            type: 'linear',
            x: 0,
            y: 0,
            x2: 0,
            y2: 1,
            colorStops: [
              { offset: 0, color: 'rgba(13,148,136,0.35)' },
              { offset: 1, color: 'rgba(13,148,136,0.02)' },
            ],
          },
        },
      },
    ],
  }
})

/** 科室 TOP：把预约按科室聚合后降序取前 8。 */
const departmentOption = computed<EChartsOption>(() => {
  const counter = new Map<string, number>()
  for (const item of appointments.value) {
    const name = departmentNames.value.get(item.department_id) ?? `科室 ${item.department_id}`
    counter.set(name, (counter.get(name) ?? 0) + 1)
  }
  const sorted = [...counter.entries()].sort((a, b) => a[1] - b[1]).slice(-8)

  return {
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    grid: BASE_GRID,
    xAxis: { type: 'value', minInterval: 1, ...AXIS_STYLE },
    yAxis: { type: 'category', data: sorted.map(([name]) => name), ...AXIS_STYLE },
    series: [
      {
        name: '预约量',
        type: 'bar',
        data: sorted.map(([, count]) => count),
        barWidth: '55%',
        itemStyle: { color: '#2563eb', borderRadius: [0, 6, 6, 0] },
      },
    ],
  }
})

/** 接口调用 TOP：metrics 的 path 标签是路由模板，不会因 ID 爆炸。 */
const endpointOption = computed<EChartsOption>(() => {
  const grouped = groupByLabel(filterByName(metrics.value, 'http_requests_total'), 'path', 8)
  const sorted = [...grouped].reverse()

  return {
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' } },
    grid: { ...BASE_GRID, left: 8 },
    xAxis: { type: 'value', minInterval: 1, ...AXIS_STYLE },
    yAxis: {
      type: 'category',
      data: sorted.map((item) => item.key),
      ...AXIS_STYLE,
      axisLabel: { color: '#64748b', fontSize: 10, width: 150, overflow: 'truncate' },
    },
    series: [
      {
        name: '请求数',
        type: 'bar',
        data: sorted.map((item) => item.value),
        barWidth: '55%',
        itemStyle: { color: '#7c3aed', borderRadius: [0, 6, 6, 0] },
      },
    ],
  }
})

/** 抢号结果分布。 */
const grabOption = computed<EChartsOption>(() => {
  const LABELS: Record<string, string> = {
    success: '成功',
    duplicate: '幂等重放',
    in_flight: '处理中',
    no_stock: '号源不足',
    error: '失败',
  }
  const grouped = groupByLabel(filterByName(metrics.value, 'appointment_grab_total'), 'result', 10)
  const data = (grouped.length ? grouped : [{ key: '暂无数据', value: 0 }]).map((item) => ({
    name: LABELS[item.key] ?? item.key,
    value: item.value,
  }))

  return {
    tooltip: { trigger: 'item' },
    legend: { bottom: 0, textStyle: { fontSize: 11, color: '#64748b' } },
    color: ['#0d9488', '#2563eb', '#f59e0b', '#ea580c', '#dc2626', '#94a3b8'],
    series: [
      {
        name: '抢号结果',
        type: 'pie',
        radius: ['45%', '68%'],
        center: ['50%', '44%'],
        label: { show: false },
        data,
      },
    ],
  }
})

/** HTTP 状态码分布。 */
const statusOption = computed<EChartsOption>(() => {
  const grouped = groupByLabel(filterByName(metrics.value, 'http_requests_total'), 'status', 10)
  const data = (grouped.length ? grouped : [{ key: '暂无数据', value: 0 }]).map((item) => ({
    name: item.key,
    value: item.value,
  }))

  return {
    tooltip: { trigger: 'item' },
    legend: { bottom: 0, textStyle: { fontSize: 11, color: '#64748b' } },
    color: ['#0d9488', '#2563eb', '#f59e0b', '#dc2626', '#94a3b8'],
    series: [
      {
        name: '状态码',
        type: 'pie',
        radius: ['45%', '68%'],
        center: ['50%', '44%'],
        label: { show: false },
        data,
      },
    ],
  }
})

/** 关键计数器明细表。 */
const COUNTER_LABELS: Record<string, string> = {
  appointment_grab_total: '抢号',
  llm_calls_total: 'AI 调用',
  summary_tasks_total: '总结任务',
  stock_reconcile_total: '号源对账',
  audit_entries_total: '审计写入',
  ratelimit_errors_total: '限流拦截',
}

const counterRows = computed(() =>
  Object.entries(COUNTER_LABELS).map(([name, label]) => {
    const samples = filterByName(metrics.value, name)
    const total = samples.reduce((acc, sample) => acc + sample.value, 0)
    const detail = samples
      .filter((sample) => Object.keys(sample.labels).length > 0)
      .map((sample) => {
        const key = Object.values(sample.labels)[0] ?? ''
        return `${key}:${sample.value}`
      })
      .join('  ')
    return { name, label, total, detail }
  }),
)
</script>

<template>
  <div>
    <PageHeader
      title="运营看板"
      :description="`数据采集于 ${lastUpdated || '—'}，每 30 秒自动刷新`"
    >
      <template #actions>
        <el-button
          :loading="loading"
          @click="loadAll()"
        >
          <AppIcon
            name="sync"
            :size="15"
          />
          <span class="btn-text">刷新</span>
        </el-button>
      </template>
    </PageHeader>

    <!-- KPI -->
    <div class="kpis">
      <div class="kpi">
        <div class="kpi-label">
          依赖健康
        </div>
        <div
          class="kpi-value"
          :class="{ 'is-bad': unhealthyCount > 0 }"
        >
          {{ dependencyList.length - unhealthyCount }}/{{ dependencyList.length }}
        </div>
        <div class="kpi-sub">
          <StatusTag
            :value="unhealthyCount > 0 ? 'down' : 'ok'"
            :label="unhealthyCount > 0 ? `${unhealthyCount} 项异常` : '全部正常'"
            :tone="unhealthyCount > 0 ? 'red' : 'green'"
          />
        </div>
      </div>

      <div class="kpi">
        <div class="kpi-label">
          累计请求
        </div>
        <div class="kpi-value">
          {{ totalRequests }}
        </div>
        <div class="kpi-sub mc-muted">
          5xx {{ totalErrors }} · 错误率 {{ errorRate }}
        </div>
      </div>

      <div class="kpi">
        <div class="kpi-label">
          抢号总次数
        </div>
        <div class="kpi-value">
          {{ grabTotal }}
        </div>
        <div class="kpi-sub mc-muted">
          含幂等重放与号源不足
        </div>
      </div>

      <div class="kpi">
        <div class="kpi-label">
          AI 调用
        </div>
        <div class="kpi-value">
          {{ llmTotal }}
        </div>
        <div class="kpi-sub mc-muted">
          总结任务 {{ summaryTotal }}
        </div>
      </div>

      <div class="kpi">
        <div class="kpi-label">
          样本内待写病历
        </div>
        <div class="kpi-value">
          {{ pendingRecords }}
        </div>
        <div class="kpi-sub mc-muted">
          近 {{ appointments.length }} 条预约
        </div>
      </div>

      <div class="kpi">
        <div class="kpi-label">
          探针状态
        </div>
        <div
          class="kpi-value"
          :class="{ 'is-bad': readyzCode >= 500 }"
        >
          {{ readyzCode || '—' }}
        </div>
        <div class="kpi-sub mc-muted">
          HTTP 状态码
        </div>
      </div>
    </div>

    <!-- 依赖明细 -->
    <div class="mc-card">
      <div class="mc-card-title">
        <span>系统健康度</span>
        <span class="mc-card-sub">GET /readyz（data 是以依赖名为 key 的对象）</span>
      </div>
      <div
        v-if="dependencyList.length"
        class="deps"
      >
        <div
          v-for="dep in dependencyList"
          :key="dep.name"
          class="dep"
          :class="{ 'is-down': dep.status !== 'ok' }"
        >
          <div class="dep-head">
            <AppIcon
              :name="dep.name === 'redis' ? 'bolt' : 'database'"
              :size="16"
            />
            <span class="dep-name">{{ dep.name.toUpperCase() }}</span>
            <StatusTag :value="dep.status" />
          </div>
          <p
            v-if="dep.error"
            class="dep-error"
          >
            {{ dep.error }}
          </p>
        </div>
      </div>
      <p
        v-else
        class="mc-muted"
      >
        未取到依赖明细（后端可能未启动）
      </p>
    </div>

    <!-- 图表 -->
    <div class="charts">
      <div class="mc-card chart-card">
        <div class="mc-card-title">
          <span>预约趋势（按就诊日）</span>
          <span class="mc-card-sub">近 {{ appointments.length }} 条样本</span>
        </div>
        <EChart
          :option="trendOption"
          :loading="loading"
        />
      </div>

      <div class="mc-card chart-card">
        <div class="mc-card-title">
          <span>科室 TOP</span>
          <span class="mc-card-sub">近 {{ appointments.length }} 条样本</span>
        </div>
        <EChart
          :option="departmentOption"
          :loading="loading"
        />
      </div>

      <div class="mc-card chart-card">
        <div class="mc-card-title">
          <span>接口调用 TOP</span>
          <span class="mc-card-sub">/metrics · 按路由模板聚合</span>
        </div>
        <EChart
          :option="endpointOption"
          :loading="loading"
        />
      </div>

      <div class="mc-card chart-card">
        <div class="mc-card-title">
          <span>抢号结果分布</span>
          <span class="mc-card-sub">appointment_grab_total</span>
        </div>
        <EChart
          :option="grabOption"
          :loading="loading"
        />
      </div>

      <div class="mc-card chart-card">
        <div class="mc-card-title">
          <span>HTTP 状态码分布</span>
          <span class="mc-card-sub">http_requests_total</span>
        </div>
        <EChart
          :option="statusOption"
          :loading="loading"
        />
      </div>

      <div class="mc-card chart-card">
        <div class="mc-card-title">
          <span>关键计数器</span>
          <span class="mc-card-sub">最近一次抓取</span>
        </div>
        <div class="counters mc-scroll-x">
          <table>
            <thead>
              <tr>
                <th>指标</th>
                <th>累计</th>
                <th>明细</th>
              </tr>
            </thead>
            <tbody>
              <tr
                v-for="row in counterRows"
                :key="row.name"
              >
                <td class="counter-name">
                  {{ row.label }}
                </td>
                <td class="counter-total">
                  {{ row.total }}
                </td>
                <td class="mc-mono counter-detail">
                  {{ row.detail || '—' }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>

    <!-- 最近审计 -->
    <div class="mc-card">
      <div class="mc-card-title">
        <span>最近请求</span>
        <RouterLink :to="{ name: 'admin-audit-logs' }">
          查看全部审计日志
        </RouterLink>
      </div>
      <div class="mc-scroll-x">
        <table class="recent">
          <thead>
            <tr>
              <th>时间</th>
              <th>用户</th>
              <th>方法</th>
              <th>路径</th>
              <th>状态</th>
              <th>耗时</th>
              <th>TraceID</th>
            </tr>
          </thead>
          <tbody>
            <tr
              v-for="log in auditLogs"
              :key="log.id"
            >
              <td class="nowrap">
                {{ formatDateTime(log.created_at) }}
              </td>
              <td>{{ log.user_id ?? '—' }}<span class="mc-muted"> {{ log.role ?? '' }}</span></td>
              <td>{{ log.method }}</td>
              <td class="path">
                {{ log.path }}
              </td>
              <td>
                <span :class="log.status_code >= 400 ? 'code-bad' : 'code-ok'">
                  {{ log.status_code }}
                </span>
              </td>
              <td>{{ log.latency_ms }}ms</td>
              <td class="mc-mono trace">
                {{ log.trace_id }}
              </td>
            </tr>
            <tr v-if="!auditLogs.length">
              <td
                colspan="7"
                class="mc-muted center"
              >
                暂无审计记录
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- 今日排班快照（帮助管理员确认数据确实在流动） -->
    <div class="mc-card">
      <div class="mc-card-title">
        <span>预约样本（最近 {{ appointments.length }} 条）</span>
      </div>
      <div class="chips">
        <span
          v-for="item in appointments.slice(0, 24)"
          :key="item.id"
          class="mc-tag mc-tag--gray"
        >
          {{ item.sched_date }} {{ timeSlotLabel(item.time_slot) }}
          · {{ departmentNames.get(item.department_id) ?? item.department_id }}
          <template v-if="!item.has_record">· 待写</template>
        </span>
        <span
          v-if="!appointments.length"
          class="mc-muted"
        >暂无数据</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* ---------------- KPI ---------------- */

.kpis {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
  margin-bottom: 14px;
}

@media (min-width: 768px) {
  .kpis {
    grid-template-columns: repeat(3, minmax(0, 1fr));
  }
}

@media (min-width: 1200px) {
  .kpis {
    grid-template-columns: repeat(6, minmax(0, 1fr));
  }
}

.kpi {
  background: var(--mc-surface);
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius-lg);
  padding: 12px 14px;
  box-shadow: var(--mc-shadow-sm);
  min-width: 0;
}

.kpi-label {
  font-size: 12px;
  color: var(--mc-text-muted);
}

.kpi-value {
  font-size: 22px;
  font-weight: 700;
  line-height: 1.3;
}

.kpi-value.is-bad {
  color: var(--mc-danger);
}

.kpi-sub {
  font-size: 11px;
  margin-top: 2px;
}

/* ---------------- 依赖 ---------------- */

.deps {
  display: grid;
  grid-template-columns: 1fr;
  gap: 10px;
}

@media (min-width: 640px) {
  .deps {
    grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
  }
}

.dep {
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius);
  padding: 10px 12px;
  background: var(--mc-surface-alt);
}

.dep.is-down {
  border-color: #fecaca;
  background: #fef2f2;
}

.dep-head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.dep-name {
  font-size: 13px;
  font-weight: 600;
  flex: 1;
}

.dep-error {
  margin-top: 6px;
  font-size: 11px;
  color: #b91c1c;
  word-break: break-all;
}

/* ---------------- 图表 ---------------- */

.charts {
  display: grid;
  grid-template-columns: 1fr;
  gap: 14px;
  margin: 14px 0;
}

@media (min-width: 1024px) {
  .charts {
    grid-template-columns: repeat(2, minmax(0, 1fr));
  }
}

.chart-card {
  min-width: 0;
  overflow: hidden;
}

.counters table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
  min-width: 420px;
}

.counters th,
.counters td {
  text-align: left;
  padding: 7px 8px;
  border-bottom: 1px solid var(--mc-border);
}

.counters th {
  color: var(--mc-text-muted);
  font-weight: 500;
}

.counter-total {
  font-weight: 700;
  color: var(--mc-primary-dark);
}

.counter-detail {
  color: var(--mc-text-soft);
  word-break: break-all;
}

/* ---------------- 最近请求 ---------------- */

.recent {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
  min-width: 720px;
}

.recent th,
.recent td {
  text-align: left;
  padding: 7px 8px;
  border-bottom: 1px solid var(--mc-border);
  white-space: nowrap;
}

.recent th {
  color: var(--mc-text-muted);
  font-weight: 500;
}

.nowrap {
  white-space: nowrap;
}

.path {
  max-width: 260px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.trace {
  max-width: 130px;
  overflow: hidden;
  text-overflow: ellipsis;
}

.code-ok {
  color: #15803d;
  font-weight: 600;
}

.code-bad {
  color: #b91c1c;
  font-weight: 600;
}

.center {
  text-align: center;
}

.chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.btn-text {
  margin-left: 4px;
}
</style>

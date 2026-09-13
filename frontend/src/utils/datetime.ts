/**
 * 时间工具。
 *
 * 后端所有时间字段统一是 `YYYY-MM-DD HH:MM:SS`（本地时区，无时区后缀），
 * **不是** ISO 8601。绝不能直接 `new Date("2026-09-13 14:05:00")` ——
 * Safari 与部分环境会判成 invalid date（本项目踩过的坑）。
 * 这里统一走 `parseApiTime`，把空格换成 `T`，语义仍是本地时区。
 */

const pad2 = (n: number): string => String(n).padStart(2, '0')

const WEEKDAYS = ['周日', '周一', '周二', '周三', '周四', '周五', '周六']

/** 把后端的 "YYYY-MM-DD HH:MM:SS" 解析为本地时区 Date。 */
export function parseApiTime(value: string): Date {
  // 'YYYY-MM-DD' 也要能解析（sched_date 是纯日期）
  return new Date(value.includes(' ') ? value.replace(' ', 'T') : `${value}T00:00:00`)
}

export function isValidDate(d: Date): boolean {
  return !Number.isNaN(d.getTime())
}

/** 输出为 YYYY-MM-DD（本地时区）。 */
export function toDateString(d: Date = new Date()): string {
  return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`
}

/** 输出为 YYYY-MM-DD HH:MM:SS（本地时区，与后端格式一致）。 */
export function toDateTimeString(d: Date = new Date()): string {
  return `${toDateString(d)} ${pad2(d.getHours())}:${pad2(d.getMinutes())}:${pad2(d.getSeconds())}`
}

/** 今天（本地时区）。 */
export function today(): string {
  return toDateString(new Date())
}

/** 在纯日期字符串上加减天数。 */
export function addDays(dateStr: string, days: number): string {
  const d = parseApiTime(dateStr)
  d.setDate(d.getDate() + days)
  return toDateString(d)
}

/** 两个纯日期字符串之间相差的天数（b - a）。 */
export function diffDays(a: string, b: string): number {
  const ms = parseApiTime(b).getTime() - parseApiTime(a).getTime()
  return Math.round(ms / 86400000)
}

/** 展示用：`2026-09-13 14:05`。 */
export function formatDateTime(value?: string | null): string {
  if (!value) return '—'
  const d = parseApiTime(value)
  if (!isValidDate(d)) return value
  return `${toDateString(d)} ${pad2(d.getHours())}:${pad2(d.getMinutes())}`
}

/** 展示用：`2026-09-13`。 */
export function formatDate(value?: string | null): string {
  if (!value) return '—'
  const d = parseApiTime(value)
  if (!isValidDate(d)) return value
  return toDateString(d)
}

/** 展示用：`09-13 周日`。 */
export function formatDateShort(value?: string | null): string {
  if (!value) return '—'
  const d = parseApiTime(value)
  if (!isValidDate(d)) return value
  return `${pad2(d.getMonth() + 1)}-${pad2(d.getDate())} ${WEEKDAYS[d.getDay()]}`
}

/** 展示用：`09-13`。 */
export function formatMonthDay(value?: string | null): string {
  if (!value) return '—'
  const d = parseApiTime(value)
  if (!isValidDate(d)) return value
  return `${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}`
}

/** 展示用：`14:05`。 */
export function formatTime(value?: string | null): string {
  if (!value) return '—'
  const d = parseApiTime(value)
  if (!isValidDate(d)) return value
  return `${pad2(d.getHours())}:${pad2(d.getMinutes())}`
}

export function weekdayOf(value: string): string {
  const d = parseApiTime(value)
  return isValidDate(d) ? WEEKDAYS[d.getDay()] : ''
}

/** 是今天 / 明天 / 昨天时返回人性化文案，否则返回 `MM-DD 周X`。 */
export function formatDateHuman(value: string): string {
  const t = today()
  if (value === t) return '今天'
  if (value === addDays(t, 1)) return '明天'
  if (value === addDays(t, -1)) return '昨天'
  return formatDateShort(value)
}

export function timeSlotLabel(slot: string): string {
  if (slot === 'am') return '上午'
  if (slot === 'pm') return '下午'
  return slot
}

/** 距离某个后端时间还有多少毫秒（已过期返回 0）。 */
export function millisUntil(value?: string | null): number {
  if (!value) return 0
  const d = parseApiTime(value)
  if (!isValidDate(d)) return 0
  return Math.max(0, d.getTime() - Date.now())
}

/** 把毫秒格式化成 `mm:ss`（超过一小时给 `hh:mm:ss`）。 */
export function formatCountdown(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000))
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  return h > 0 ? `${h}:${pad2(m)}:${pad2(s)}` : `${pad2(m)}:${pad2(s)}`
}

/** 生成"今天起 N 天"的日期选项，用于排班日期选择。 */
export function nextDays(count: number, from: string = today()): string[] {
  return Array.from({ length: count }, (_, i) => addDays(from, i))
}

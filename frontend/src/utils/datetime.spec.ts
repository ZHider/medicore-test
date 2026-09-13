import { describe, expect, it } from 'vitest'

import {
  addDays,
  diffDays,
  formatCountdown,
  formatDateHuman,
  millisUntil,
  parseApiTime,
  timeSlotLabel,
  toDateString,
  today,
  weekdayOf,
} from './datetime'

/**
 * 时间工具。这里的每条规则都对应一个具体的坑：
 * 后端返回的是 `YYYY-MM-DD HH:MM:SS`（本地时区、无时区后缀），
 * 而 `new Date("2026-09-13 14:05:00")` 在 Safari 下会得到 Invalid Date。
 *
 * 断言一律基于**字段值**而不是绝对时间戳，这样测试结果与运行机器的时区无关
 * （CI 是 UTC，本机是 Asia/Shanghai）。
 */
describe('parseApiTime', () => {
  it('把「空格分隔」的格式按**本地时区**解析，而不是 UTC', () => {
    const d = parseApiTime('2026-09-13 14:05:00')
    expect(d.getFullYear()).toBe(2026)
    expect(d.getMonth()).toBe(8) // 0-based
    expect(d.getDate()).toBe(13)
    expect(d.getHours()).toBe(14)
    expect(d.getMinutes()).toBe(5)
    expect(d.getSeconds()).toBe(0)
  })

  it('纯日期（sched_date）也能解析为本地零点', () => {
    const d = parseApiTime('2026-09-13')
    expect(d.getFullYear()).toBe(2026)
    expect(d.getDate()).toBe(13)
    expect(d.getHours()).toBe(0)
  })

  it('不是 Invalid Date（Safari 兼容性的回归防线）', () => {
    expect(Number.isNaN(parseApiTime('2026-09-13 14:05:00').getTime())).toBe(false)
    expect(Number.isNaN(parseApiTime('2026-09-13').getTime())).toBe(false)
  })
})

describe('toDateString', () => {
  it('月/日补零', () => {
    expect(toDateString(new Date(2026, 0, 5))).toBe('2026-01-05')
    expect(toDateString(new Date(2026, 11, 31))).toBe('2026-12-31')
  })
})

describe('addDays / diffDays', () => {
  it('跨月与跨年正确', () => {
    expect(addDays('2026-01-31', 1)).toBe('2026-02-01')
    expect(addDays('2026-12-31', 1)).toBe('2027-01-01')
    expect(addDays('2026-03-01', -1)).toBe('2026-02-28')
  })

  it('闰年 2 月', () => {
    // 2028 是闰年
    expect(addDays('2028-02-28', 1)).toBe('2028-02-29')
    expect(addDays('2028-02-29', 1)).toBe('2028-03-01')
  })

  it('diffDays 与 addDays 互逆', () => {
    expect(diffDays('2026-09-13', '2026-09-20')).toBe(7)
    expect(diffDays('2026-09-20', '2026-09-13')).toBe(-7)
    expect(diffDays('2026-09-13', '2026-09-13')).toBe(0)
  })

  it('today() 与 addDays 自洽', () => {
    expect(diffDays(today(), addDays(today(), 3))).toBe(3)
  })
})

describe('millisUntil', () => {
  it('已过去的时间返回 0（不返回负数，否则倒计时会显示负数）', () => {
    expect(millisUntil('2000-01-01 00:00:00')).toBe(0)
  })

  it('缺失或非法输入返回 0', () => {
    expect(millisUntil(undefined)).toBe(0)
    expect(millisUntil(null)).toBe(0)
    expect(millisUntil('不是时间')).toBe(0)
  })

  it('未来时间返回正数', () => {
    const future = new Date(Date.now() + 60_000)
    const y = future.getFullYear()
    const m = String(future.getMonth() + 1).padStart(2, '0')
    const d = String(future.getDate()).padStart(2, '0')
    const hh = String(future.getHours()).padStart(2, '0')
    const mm = String(future.getMinutes()).padStart(2, '0')
    const ss = String(future.getSeconds()).padStart(2, '0')
    expect(millisUntil(`${y}-${m}-${d} ${hh}:${mm}:${ss}`)).toBeGreaterThan(0)
  })
})

describe('formatCountdown', () => {
  it('不足一小时显示 mm:ss 并补零', () => {
    expect(formatCountdown(0)).toBe('00:00')
    expect(formatCountdown(1_000)).toBe('00:01')
    expect(formatCountdown(65_000)).toBe('01:05')
    expect(formatCountdown(15 * 60 * 1000)).toBe('15:00')
  })

  it('超过一小时显示 h:mm:ss', () => {
    expect(formatCountdown(3_600_000)).toBe('1:00:00')
    expect(formatCountdown(3_661_000)).toBe('1:01:01')
  })

  it('负数归零（已过期时不应显示 -00:01）', () => {
    expect(formatCountdown(-5_000)).toBe('00:00')
  })
})

describe('展示用格式化', () => {
  it('formatDateHuman 把今天/明天/昨天说成人话', () => {
    expect(formatDateHuman(today())).toBe('今天')
    expect(formatDateHuman(addDays(today(), 1))).toBe('明天')
    expect(formatDateHuman(addDays(today(), -1))).toBe('昨天')
  })

  it('timeSlotLabel 映射 am/pm', () => {
    expect(timeSlotLabel('am')).toBe('上午')
    expect(timeSlotLabel('pm')).toBe('下午')
  })

  it('weekdayOf 返回中文星期', () => {
    // 2026-09-13 是周日
    expect(weekdayOf('2026-09-13')).toBe('周日')
    expect(weekdayOf('2026-09-14')).toBe('周一')
  })
})

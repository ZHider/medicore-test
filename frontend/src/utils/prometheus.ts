/**
 * 极简 Prometheus 文本格式解析器。
 *
 * `/metrics` 返回的是文本而非 JSON，而本项目对依赖面很敏感（不引入新依赖），
 * 一个几十行的解析函数就够了。
 *
 * 支持：
 *   metric_name 1
 *   metric_name{a="1",b="2"} 1
 * 跳过 `#` 注释与空行；容忍 `NaN` / `+Inf` / `-Inf`（解析为 0，调用方按需过滤）。
 */

import type { MetricSample } from '@/types/api'

/** 解析标签串 `a="1",b="2"`。值里可能含转义引号与逗号，按字符扫描而不是 split(',')。 */
function parseLabels(raw: string): Record<string, string> {
  const labels: Record<string, string> = {}
  let i = 0

  while (i < raw.length) {
    // 跳过前导分隔符与空白
    while (i < raw.length && (raw[i] === ',' || raw[i] === ' ')) i++
    const eq = raw.indexOf('=', i)
    if (eq === -1) break

    const key = raw.slice(i, eq).trim()
    let j = eq + 1
    while (j < raw.length && raw[j] === ' ') j++

    if (raw[j] !== '"') break
    j++ // 跳过开引号

    let value = ''
    while (j < raw.length) {
      const ch = raw[j]
      if (ch === '\\') {
        // 转义序列：\" \\ \n 等
        const next = raw[j + 1] ?? ''
        if (next === 'n') value += '\n'
        else value += next
        j += 2
        continue
      }
      if (ch === '"') {
        j++
        break
      }
      value += ch
      j++
    }

    if (key) labels[key] = value
    i = j
  }

  return labels
}

export function parsePrometheus(text: string): MetricSample[] {
  const samples: MetricSample[] = []

  for (const line of text.split('\n')) {
    const trimmed = line.trim()
    if (!trimmed || trimmed.startsWith('#')) continue

    const braceStart = trimmed.indexOf('{')
    let name: string
    let labels: Record<string, string>
    let rest: string

    if (braceStart !== -1) {
      // 标签值里可能出现 '}'，所以要找**最后一个** '}' 才能正确切出数值部分
      const braceEnd = trimmed.lastIndexOf('}')
      if (braceEnd < braceStart) continue
      name = trimmed.slice(0, braceStart).trim()
      labels = parseLabels(trimmed.slice(braceStart + 1, braceEnd))
      rest = trimmed.slice(braceEnd + 1)
    } else {
      const space = trimmed.indexOf(' ')
      if (space === -1) continue
      name = trimmed.slice(0, space)
      labels = {}
      rest = trimmed.slice(space + 1)
    }

    if (!name) continue

    // 数值后面可能还有时间戳，取第一个 token
    const valueToken = rest.trim().split(/\s+/)[0] ?? ''
    const value = Number(valueToken)

    samples.push({ name, labels, value: Number.isFinite(value) ? value : 0 })
  }

  return samples
}

/** 按指标名筛选。 */
export function filterByName(samples: MetricSample[], name: string): MetricSample[] {
  return samples.filter((s) => s.name === name)
}

/** 按指标名求和。 */
export function sumByName(samples: MetricSample[], name: string): number {
  return filterByName(samples, name).reduce((acc, s) => acc + s.value, 0)
}

/** 把样本聚合到某个标签维度上并降序排列，用于 TOP 榜。 */
export function groupByLabel(
  samples: MetricSample[],
  label: string,
  limit = 8,
): Array<{ key: string; value: number }> {
  const acc = new Map<string, number>()
  for (const s of samples) {
    const key = s.labels[label] ?? '未知'
    acc.set(key, (acc.get(key) ?? 0) + s.value)
  }
  return [...acc.entries()]
    .map(([key, value]) => ({ key, value }))
    .sort((a, b) => b.value - a.value)
    .slice(0, limit)
}

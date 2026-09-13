import { describe, expect, it } from 'vitest'

import { filterByName, groupByLabel, parsePrometheus, sumByName } from './prometheus'

/**
 * `/metrics` 是 Prometheus 文本格式。这个解析器是手写的（项目不引新依赖），
 * 而它要处理的边角情况不少：标签值里可能有 `}`、转义引号、以及 NaN/Inf。
 * 解析错了的表现是"看板数字不对"，在浏览器里完全看不出异常，所以值得锁住。
 */
describe('parsePrometheus', () => {
  it('解析不带标签的样本', () => {
    expect(parsePrometheus('go_goroutines 42')).toEqual([
      { name: 'go_goroutines', labels: {}, value: 42 },
    ])
  })

  it('解析带标签的样本', () => {
    expect(parsePrometheus('http_requests_total{method="GET",path="/api/v1/departments",status="200"} 7')).toEqual([
      {
        name: 'http_requests_total',
        labels: { method: 'GET', path: '/api/v1/departments', status: '200' },
        value: 7,
      },
    ])
  })

  it('跳过 HELP / TYPE 注释与空行', () => {
    const text = [
      '# HELP http_requests_total total requests',
      '# TYPE http_requests_total counter',
      '',
      'http_requests_total{status="200"} 3',
      '',
    ].join('\n')
    expect(parsePrometheus(text)).toEqual([
      { name: 'http_requests_total', labels: { status: '200' }, value: 3 },
    ])
  })

  it('取第一个数值 token，忽略后面的时间戳', () => {
    expect(parsePrometheus('up 1 1699999999000')[0]?.value).toBe(1)
  })

  it('标签值里含 `}` 时不截断（必须取最后一个 `}`）', () => {
    // 这正是实现里用 lastIndexOf 而不是 indexOf 的原因。
    // 用 indexOf 会把 name 与 rest 切错，得到 value=NaN。
    const samples = parsePrometheus('weird{path="/a}b"} 5')
    expect(samples).toEqual([{ name: 'weird', labels: { path: '/a}b' }, value: 5 }])
  })

  it('标签值里的转义引号与逗号不被误当分隔符', () => {
    const samples = parsePrometheus('m{msg="a\\"b,c",k="v"} 1')
    expect(samples[0]?.labels).toEqual({ msg: 'a"b,c', k: 'v' })
  })

  it('NaN 与 ±Inf 归零（否则会污染求和与图表）', () => {
    expect(parsePrometheus('m NaN')[0]?.value).toBe(0)
    expect(parsePrometheus('m +Inf')[0]?.value).toBe(0)
    expect(parsePrometheus('m -Inf')[0]?.value).toBe(0)
  })

  it('小数与科学计数法正常解析', () => {
    expect(parsePrometheus('m 0.0005154')[0]?.value).toBeCloseTo(0.0005154)
    expect(parsePrometheus('m 1e3')[0]?.value).toBe(1000)
  })

  it('没有空格的非法行被跳过而不是产生垃圾样本', () => {
    expect(parsePrometheus('this-line-has-no-value')).toEqual([])
  })

  it('空输入返回空数组', () => {
    expect(parsePrometheus('')).toEqual([])
  })
})

describe('聚合辅助', () => {
  const samples = parsePrometheus(
    [
      'http_requests_total{path="/a",status="200"} 3',
      'http_requests_total{path="/a",status="500"} 1',
      'http_requests_total{path="/b",status="200"} 10',
      'appointment_grab_total{result="success"} 2',
    ].join('\n'),
  )

  it('filterByName 只留指定指标', () => {
    expect(filterByName(samples, 'http_requests_total')).toHaveLength(3)
  })

  it('sumByName 求和', () => {
    expect(sumByName(samples, 'http_requests_total')).toBe(14)
    expect(sumByName(samples, '不存在的指标')).toBe(0)
  })

  it('groupByLabel 按维度聚合且降序、可截断', () => {
    // 只用 http_requests_total 这一族做断言：samples 里还有一条
    // appointment_grab_total（没有 path 标签），会被正确归入"未知"，
    // 那属于下一个用例的范围。
    const httpOnly = filterByName(samples, 'http_requests_total')
    expect(groupByLabel(httpOnly, 'path')).toEqual([
      { key: '/b', value: 10 },
      { key: '/a', value: 4 },
    ])
    expect(groupByLabel(httpOnly, 'path', 1)).toEqual([{ key: '/b', value: 10 }])
  })

  it('groupByLabel 对缺失标签归为「未知」而不是丢弃', () => {
    // 丢掉会让看板总数对不上，比显示"未知"更难排查。
    const total = groupByLabel(samples, 'result').reduce((acc, g) => acc + g.value, 0)
    expect(total).toBe(16)
  })
})

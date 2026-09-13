// @vitest-environment jsdom
import { mount } from '@vue/test-utils'
import ElementPlus from 'element-plus'
import { describe, expect, it } from 'vitest'

import ReportPanel from './ReportPanel.vue'
import type { PreconsultReport, StructuredSummary, Urgency } from '@/types/api'

/**
 * ReportPanel 的核心价值是**三种终态的分支正确**：
 *
 *   正常  done && !fallback → 渲染 structured
 *   降级  done &&  fallback → structured 是 null，必须改展示原始对话
 *   失败  failed            → 展示 error_message
 *
 * 其中「降级」分支在真实环境里**从未被走到过**（mock LLM 恒返回 fallback=false，
 * 见 frontend/AGENTS.md §7.1），所以这里用的是契约里写的形状，
 * 而不是实测样本 —— 这条测试是它唯一的防线。
 */
const STRUCTURED: StructuredSummary = {
  chief_complaint: '发热伴咳嗽 3 天',
  present_illness: '3 天前受凉后发热，最高 38.9℃',
  past_history: '无',
  medications: '布洛芬',
  allergies: '否认药物过敏史',
  red_flags: [],
  possible_departments: ['呼吸内科'],
  urgency: 'routine',
}

function makeReport(patch: Partial<PreconsultReport> = {}): PreconsultReport {
  return {
    id: 42,
    appointment_id: 501,
    status: 'done',
    fallback: false,
    llm_model: 'mock-llm-v1',
    prompt_version: 'preconsult_v1',
    created_at: '2026-09-13 14:20:00',
    finished_at: '2026-09-13 14:20:04',
    ...patch,
  }
}

function render(report: PreconsultReport | null) {
  return mount(ReportPanel, {
    props: { report },
    global: { plugins: [ElementPlus] },
  })
}

describe('ReportPanel —— 生成中', () => {
  it('pending / processing 时提示正在生成，不渲染任何结构化字段', () => {
    for (const status of ['pending', 'processing'] as const) {
      const wrapper = render(makeReport({ status }))
      expect(wrapper.text()).toContain('AI 正在生成结构化总结')
      expect(wrapper.text()).not.toContain('主诉')
      wrapper.unmount()
    }
  })
})

describe('ReportPanel —— 正常终态（done && !fallback）', () => {
  it('渲染五个文本字段与建议科室', () => {
    const wrapper = render(makeReport({ structured: STRUCTURED }))

    expect(wrapper.text()).toContain('AI 结构化预问诊总结')
    expect(wrapper.text()).toContain('发热伴咳嗽 3 天')
    expect(wrapper.text()).toContain('3 天前受凉后发热，最高 38.9℃')
    expect(wrapper.text()).toContain('布洛芬')
    expect(wrapper.text()).toContain('否认药物过敏史')
    expect(wrapper.text()).toContain('呼吸内科')
    wrapper.unmount()
  })

  it('red_flags 非空时必须渲染成高亮标签', () => {
    const wrapper = render(
      makeReport({ structured: { ...STRUCTURED, red_flags: ['胸痛', '意识模糊'] } }),
    )
    const flags = wrapper.findAll('.redflag').map((n) => n.text())
    expect(flags).toEqual(['胸痛', '意识模糊'])
    expect(wrapper.text()).toContain('危险信号')
    wrapper.unmount()
  })

  it('red_flags 为空时给「未识别到危险信号」，而不是留白', () => {
    // 留白会让医生不确定"是没查到"还是"页面坏了"。
    const wrapper = render(makeReport({ structured: STRUCTURED }))
    expect(wrapper.text()).toContain('未识别到危险信号')
    expect(wrapper.findAll('.redflag')).toHaveLength(0)
    wrapper.unmount()
  })

  it('urgency 三档都要有对应文案', () => {
    const cases: Array<[Urgency, string]> = [
      ['routine', '常规'],
      ['urgent', '紧急'],
      ['emergency', '危重'],
    ]
    for (const [urgency, label] of cases) {
      const wrapper = render(makeReport({ structured: { ...STRUCTURED, urgency } }))
      expect(wrapper.text()).toContain(`紧急度：${label}`)
      wrapper.unmount()
    }
  })

  it('字段为空串时显示占位符而不是空白', () => {
    const wrapper = render(
      makeReport({ structured: { ...STRUCTURED, past_history: '   ' } }),
    )
    // 「既往史」那一条的 dd 应为占位符
    const dds = wrapper.findAll('.field dd').map((n) => n.text())
    expect(dds).toContain('—')
    wrapper.unmount()
  })
})

describe('ReportPanel —— 降级终态（done && fallback）★ 真实环境从未走到过', () => {
  it('明确标注「AI 未能结构化总结」，并且**不去渲染 structured**', () => {
    const wrapper = render(
      makeReport({ fallback: true, structured: undefined, conversation: [] }),
    )
    expect(wrapper.text()).toContain('AI 未能结构化总结')
    // 关键：不能因为 structured 缺失就走"暂无报告数据"或白屏
    expect(wrapper.text()).not.toContain('暂无报告数据')
    expect(wrapper.text()).not.toContain('主诉')
    wrapper.unmount()
  })

  it('把原始对话按 患者 / AI 渲染出来', () => {
    const wrapper = render(
      makeReport({
        fallback: true,
        structured: undefined,
        conversation: [
          { role: 'user', content: '我发烧三天了' },
          { role: 'assistant', content: '请问最高体温多少？' },
          { role: 'user', content: '38.9 度' },
        ],
      }),
    )
    const rows = wrapper.findAll('.raw-row')
    expect(rows).toHaveLength(3)
    expect(wrapper.text()).toContain('我发烧三天了')
    expect(wrapper.text()).toContain('请问最高体温多少？')
    // 角色标签：user -> 患者，其余 -> AI
    expect(wrapper.findAll('.raw-role--user')).toHaveLength(2)
    // 卡片标题显示的是**消息总数**（不是患者发言数）
    expect(wrapper.text()).toContain('3 条')
    wrapper.unmount()
  })

  it('conversation 为 null 时不崩（后端 json.RawMessage 可能序列化成 null）', () => {
    const wrapper = render(
      makeReport({ fallback: true, structured: undefined, conversation: null }),
    )
    expect(wrapper.text()).toContain('AI 未能结构化总结')
    // 用结构断言而不是文本断言：提示语里本身也含"原始对话记录"四个字
    expect(wrapper.findAll('.raw-row')).toHaveLength(0)
    expect(wrapper.text()).toContain('没有可展示的对话记录')
    wrapper.unmount()
  })
})

describe('ReportPanel —— 失败终态', () => {
  it('展示后端给的 error_message 与重试插槽', () => {
    const wrapper = mount(ReportPanel, {
      props: {
        report: makeReport({ status: 'failed', error_message: 'AI 服务暂时不可用' }),
      },
      global: { plugins: [ElementPlus] },
      slots: { retry: '<button class="retry-btn">重新获取</button>' },
    })
    expect(wrapper.text()).toContain('AI 总结生成失败')
    expect(wrapper.text()).toContain('AI 服务暂时不可用')
    expect(wrapper.find('.retry-btn').exists()).toBe(true)
    wrapper.unmount()
  })

  it('缺少 error_message 时回退到通用文案', () => {
    const wrapper = render(makeReport({ status: 'failed' }))
    expect(wrapper.text()).toContain('上游服务暂时不可用，可稍后重试')
    wrapper.unmount()
  })
})

describe('ReportPanel —— 边界', () => {
  it('report 为 null + 正在加载 → 显示骨架屏，不谎称"AI 正在生成"', () => {
    const wrapper = mount(ReportPanel, {
      props: { report: null, loading: true },
      global: { plugins: [ElementPlus] },
    })
    expect(wrapper.text()).not.toContain('AI 正在生成')
    expect(wrapper.text()).not.toContain('暂无报告数据')
    wrapper.unmount()
  })

  it('report 为 null 且已加载完 → 显示空状态，而不是永远等一个不会来的结果', () => {
    // 这条是真实缺陷的回归防线：status 原先默认成 'pending'，
    // 于是"报告没拉到"会显示"AI 正在生成结构化总结"。若那次请求其实失败了，
    // 用户会一直等下去。
    const wrapper = mount(ReportPanel, {
      props: { report: null, loading: false },
      global: { plugins: [ElementPlus] },
    })
    expect(wrapper.text()).toContain('暂无报告数据')
    expect(wrapper.text()).not.toContain('AI 正在生成')
    wrapper.unmount()
  })

  it('done 但 structured 缺失（非 fallback）时也不能白屏', () => {
    // 契约上不该出现，但真出现时必须有兜底，否则页面全空且无任何提示。
    const wrapper = render(makeReport({ structured: undefined }))
    expect(wrapper.text()).toContain('暂无报告数据')
    wrapper.unmount()
  })
})

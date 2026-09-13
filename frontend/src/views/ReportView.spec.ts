// @vitest-environment jsdom
import { createPinia, setActivePinia } from 'pinia'
import { mount } from '@vue/test-utils'
import type { VueWrapper } from '@vue/test-utils'
// 必须显式装 Element Plus：应用里由 main.ts 全局安装，
// 不装的话 el-button 渲染成未知元素，`findAll('button')` 是空的（踩过）
import ElementPlus from 'element-plus'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiError, ErrorCode } from '@/api/error'
import { useAuthStore } from '@/stores/auth'
import type { PreconsultReport, Role, UserProfile } from '@/types/api'

/**
 * `ReportView` 是**三个角色共用**的页面（`meta.roles` 是 patient/doctor/admin），
 * 但后端只有患者侧的子接口：
 *
 *   - `POST .../preconsult/submit`（重新提交总结）—— 仅患者
 *   - `GET /appointments`（`listMine`，取预约上下文）—— 仅患者
 *   - `preconsult-chat` 路由（发起对话）—— 仅患者
 *
 * 这正是"页面看起来正常、点下去什么也没发生"的高发地带：医生/管理员点
 * 「重新获取」会拿到 403，点「返回对话」会因目标路由未注册而被静默中断。
 *
 * 所以这里逐个角色断言：**患者专属的调用不能被发出、患者专属的按钮不能被渲染**。
 * 光断言"页面能渲染"是不够的 —— 缺陷恰恰是"渲染得挺好但按钮是死的"。
 */

const mocks = vi.hoisted(() => ({
  getReport: vi.fn(),
  submit: vi.fn(),
  listMine: vi.fn(),
  push: vi.fn(),
  back: vi.fn(),
}))

vi.mock('@/api/preconsult', () => ({ getReport: mocks.getReport, submit: mocks.submit }))
vi.mock('@/api/appointment', () => ({ listMine: mocks.listMine }))
vi.mock('vue-router', () => ({
  useRoute: () => ({ params: { id: '5' }, query: {} }),
  useRouter: () => ({ push: mocks.push, back: mocks.back, replace: vi.fn() }),
}))

import ReportView from './ReportView.vue'

const REPORT_ID = 5

const failedReport: PreconsultReport = {
  id: 42,
  appointment_id: REPORT_ID,
  status: 'failed',
  fallback: false,
  conversation: [],
  llm_model: 'mock-llm-v1',
  prompt_version: 'preconsult_v1',
  error_message: '上游超时',
  created_at: '2026-09-13 14:20:00',
  finished_at: '2026-09-13 14:21:00',
}

function profileOf(role: Role): UserProfile {
  return { id: 7, role, name: '测试', phone: '138****8888' }
}

/** 挂载并把登录态设成某个角色。 */
async function mountAs(role: Role) {
  const pinia = createPinia()
  setActivePinia(pinia)
  // 直接写 profile，跳过 /auth/me：本文件只关心角色带来的分支
  useAuthStore().profile = profileOf(role)

  const wrapper = mount(ReportView, { global: { plugins: [pinia, ElementPlus] } })
  // 等 onMounted 里的 poll() 落地
  await vi.waitFor(() => expect(mocks.getReport).toHaveBeenCalled())
  await wrapper.vm.$nextTick()
  return wrapper
}

/** 按文案找按钮；找不到时把页面上的按钮都列出来，避免只报一个 "undefined"。 */
function buttonByText(wrapper: VueWrapper, label: string) {
  const buttons = wrapper.findAll('button')
  const btn = buttons.find((b) => b.text().includes(label))
  if (!btn) {
    throw new Error(
      `没找到按钮「${label}」。页面上的按钮：${JSON.stringify(buttons.map((b) => b.text()))}`,
    )
  }
  return btn
}

beforeEach(() => {
  mocks.getReport.mockReset().mockResolvedValue(failedReport)
  mocks.submit.mockReset().mockResolvedValue({ report_id: 42, status: 'pending', already_submitted: false })
  mocks.listMine.mockReset().mockResolvedValue({ items: [], has_more: false })
  mocks.push.mockReset()
  mocks.back.mockReset()
})

describe('患者视角：三个动作都可用', () => {
  it('会取预约上下文（listMine 是患者专属接口）', async () => {
    await mountAs('patient')
    expect(mocks.listMine).toHaveBeenCalled()
  })

  it('「重新获取」走 submit（重新提交总结）', async () => {
    const wrapper = await mountAs('patient')

    await buttonByText(wrapper, '重新获取').trigger('click')
    await vi.waitFor(() => expect(mocks.submit).toHaveBeenCalledWith(REPORT_ID))
  })

  it('渲染「返回对话」（跳患者专属的 preconsult-chat）', async () => {
    const wrapper = await mountAs('patient')
    expect(wrapper.text()).toContain('返回对话')
  })
})

describe.each(['doctor', 'admin'] as Role[])('%s 视角：患者专属动作必须收起', (role) => {
  // 这条是 REPORTVIEW 缺陷的核心：别的角色调这个接口只会拿 403
  it('不去取患者专属的预约上下文', async () => {
    await mountAs(role)
    expect(mocks.listMine).not.toHaveBeenCalled()
  })

  // 这条更重要：不是"少调一次"，而是"点了也不能发"
  it('「重新获取」不会提交总结，只刷新状态', async () => {
    const wrapper = await mountAs(role)
    mocks.getReport.mockClear()

    await buttonByText(wrapper, '重新获取').trigger('click')
    await vi.waitFor(() => expect(mocks.getReport).toHaveBeenCalled())

    expect(mocks.submit, '医生/管理员不该调用患者专属的 submit').not.toHaveBeenCalled()
  })

  it('不渲染「返回对话」（目标路由没注册给该角色）', async () => {
    const wrapper = await mountAs(role)
    expect(wrapper.text()).not.toContain('返回对话')
  })

  it('「去完成预问诊」也不渲染', async () => {
    // 让报告不存在，走到 notFound 分支。
    // 必须用真正的 ApiError：视图是按错误码（而不是 message）判断"没有报告"的。
    mocks.getReport.mockRejectedValue(
      new ApiError({ code: ErrorCode.NotFound, message: '资源不存在', traceId: 't' }),
    )
    const wrapper = await mountAs(role)

    expect(wrapper.text()).not.toContain('去完成预问诊')
    // 但要有给医生看的说明，而不是一片空白
    expect(wrapper.text()).toContain('尚未提交')
  })
})

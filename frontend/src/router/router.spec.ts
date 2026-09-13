// @vitest-environment jsdom
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { clearTokens, saveTokens } from '@/api/tokens'
import type { Role, UserProfile } from '@/types/api'

/**
 * 路由守卫与按角色动态注册的回归测试。
 *
 * 这个文件是被一个**真实缺陷**逼出来的：登录后能看到管理看板，
 * **一刷新就变成「页面不存在」**。原因不是视图坏了，而是守卫的判断顺序：
 *
 *   catchAll 路由带着 `public: true`，而未注册时任何深链接都会被 catchAll 抢走匹配 ——
 *   于是 `if (to.meta.public) return true` 在 `installRoleRoutes()` **之前**就返回了，
 *   角色路由永远装不上。原本用来救这个场景的那段代码（`justInstalled && not-found`）
 *   因此成了**永远走不到的死代码**。
 *
 * 同时暴露了第二类问题：路由的**注册分组**与它自己声明的 `meta.roles` 不一致 ——
 * 例如 `preconsult-report` 声明 patient/doctor/admin 可访问，却只挂在 patient 组里，
 * 于是医生工作台的「AI 报告」按钮点了没反应。
 *
 * 所以这里除了逐个分支的用例，还有一条**不变量**：
 * `meta.roles` 是承诺，凡是声明允许某角色的路由，那个角色就必须真的注册到它。
 * 这类"声明与实现不一致"的缺陷靠单个用例是堵不住的。
 */

const mocks = vi.hoisted(() => ({
  login: vi.fn(),
  me: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  // 视图全部换成哨兵组件：本文件只关心"路由匹配到了哪个视图"，不关心视图内部
  mk: (tag: string) => ({ default: { name: tag, template: `<div data-view="${tag}"></div>` } }),
}))

vi.mock('@/api/auth', () => ({
  login: mocks.login,
  me: mocks.me,
  register: mocks.register,
  logout: mocks.logout,
}))

vi.mock('@/layouts/AppLayout.vue', () => mocks.mk('AppLayout'))
vi.mock('@/views/LoginView.vue', () => mocks.mk('LoginView'))
vi.mock('@/views/RegisterView.vue', () => mocks.mk('RegisterView'))
vi.mock('@/views/NotFoundView.vue', () => mocks.mk('NotFoundView'))
vi.mock('@/views/RecordsView.vue', () => mocks.mk('RecordsView'))
vi.mock('@/views/ReportView.vue', () => mocks.mk('ReportView'))
vi.mock('@/views/patient/BookingView.vue', () => mocks.mk('BookingView'))
vi.mock('@/views/patient/MyAppointmentsView.vue', () => mocks.mk('MyAppointmentsView'))
vi.mock('@/views/patient/PreconsultChatView.vue', () => mocks.mk('PreconsultChatView'))
vi.mock('@/views/doctor/WorkbenchView.vue', () => mocks.mk('WorkbenchView'))
vi.mock('@/views/doctor/WriteRecordView.vue', () => mocks.mk('WriteRecordView'))
vi.mock('@/views/admin/DashboardView.vue', () => mocks.mk('DashboardView'))
vi.mock('@/views/admin/AuditLogsView.vue', () => mocks.mk('AuditLogsView'))
vi.mock('@/views/admin/SyncView.vue', () => mocks.mk('SyncView'))

import router, { homeRouteName, resetRoleRoutes, routesForRole } from './index'

const pair = {
  access_token: 'access-1',
  refresh_token: 'refresh-1',
  token_type: 'Bearer',
  expires_in: 7200,
}

const ROLES: Role[] = ['patient', 'doctor', 'admin']

const ROLE_NAMES: Record<Role, string> = {
  patient: '张三',
  doctor: '李医生',
  admin: '王管理',
}

function profileOf(role: Role): UserProfile {
  return { id: 7, role, name: ROLE_NAMES[role], phone: '138****8888' }
}

/**
 * 换一个全新的 pinia 并把自己设成指定角色。
 *
 * **每个用例都必须换新 pinia**：守卫只在首次导航时 `bootstrap()`，
 * 而 store 的 `bootstrapped` 一旦为 true 就不会再恢复登录态。
 * 复用上一个用例的 store，会让"已登录"的角色永远恢复不出来 ——
 * 本文件第一版就是这么做夹具的，结果一整片用例"失败"其实是我的夹具问题不是代码问题。
 */
function asRole(role: Role): void {
  setActivePinia(createPinia())
  saveTokens(pair)
  mocks.me.mockResolvedValue(profileOf(role))
}

/** 换一个全新的 pinia 并且未登录。 */
function asGuest(): void {
  setActivePinia(createPinia())
  clearTokens()
}

/** 导航到某个地址，返回最终落在的路由名。 */
async function go(path: string): Promise<string> {
  await router.push(path).catch(() => undefined)
  return String(router.currentRoute.value.name ?? '')
}

beforeEach(async () => {
  resetRoleRoutes()
  mocks.me.mockReset()
  mocks.login.mockReset()
  mocks.register.mockReset()
  mocks.logout.mockReset()

  // 统一起点：一个未登录访问不到的地址。它会被送到 `/login?redirect=...`，
  // 与各用例的目标地址都不同 —— 避免"导航到当前地址"被判为重复导航而跳过守卫。
  asGuest()
  await router.replace('/__boot__').catch(() => undefined)
})

/** 各角色首页对应的深链接（用于模拟"刷新时浏览器直接请求这个地址"）。 */
function homePath(role: Role): string {
  switch (role) {
    case 'patient':
      return '/booking'
    case 'doctor':
      return '/workbench'
    case 'admin':
      return '/admin/dashboard'
  }
}

describe('刷新 / 首次导航（回归：登录后刷新变成「页面不存在」）', () => {
  // 这是用户实际踩到的那条。刷新时目标路由尚未注册，本次匹配由 catchAll 完成。
  it.each(ROLES)('已登录的 %s 首次导航到自己的首页能匹配到真实视图', async (role) => {
    asRole(role)

    const landed = await go(homePath(role))

    expect(landed).toBe(homeRouteName(role))
    expect(landed).not.toBe('not-found')
  })

  it('已登录的 admin 刷新审计日志页不会掉进 404', async () => {
    asRole('admin')
    expect(await go('/admin/audit-logs')).toBe('admin-audit-logs')
  })

  it('已登录的 admin 刷新 HIS 同步页不会掉进 404', async () => {
    asRole('admin')
    expect(await go('/admin/sync')).toBe('admin-sync')
  })

  it('已登录的 patient 刷新「我的预约」不会掉进 404', async () => {
    asRole('patient')
    expect(await go('/appointments')).toBe('my-appointments')
  })

  it('已登录的 doctor 刷新写病历页不会掉进 404', async () => {
    asRole('doctor')
    expect(await go('/workbench/9/record')).toBe('write-record')
  })
})

describe('meta.roles 是承诺，必须真的注册（不变量）', () => {
  /**
   * 这条用例就是为"注册分组与 meta.roles 不一致"这一类缺陷准备的。
   *
   * 它不关心具体哪个路由，只关心一件事：**凡声明允许某角色访问的路由，
   * 那个角色登录后就必须真的拿得到**。新增路由时写错分组会在这里立刻变红。
   */
  it('每个角色都能拿到所有声明允许它的路由', () => {
    for (const role of ROLES) {
      const installed = new Set(
        routesForRole(role).map((r) => String(r.name ?? '')),
      )

      // 全部角色的并集 = 项目里所有按角色注册的路由
      const all = ROLES.flatMap((r) => routesForRole(r))
      for (const route of all) {
        const roles = route.meta?.roles
        if (!roles || !roles.includes(role)) continue

        expect(
          installed.has(String(route.name)),
          `路由 ${String(route.name)} 声明 ${role} 可访问，却没有为该角色注册`,
        ).toBe(true)
      }
    }
  })

  it('不得为角色注册它没有声明权限的路由', () => {
    for (const role of ROLES) {
      for (const route of routesForRole(role)) {
        const roles = route.meta?.roles
        expect(
          !roles || roles.includes(role),
          `路由 ${String(route.name)} 被注册给了 ${role}，但 meta.roles 里没有它`,
        ).toBe(true)
      }
    }
  })
})

describe('跨角色可达性（行为面）', () => {
  // 工作台里有「AI 报告」按钮 → preconsult-report；它声明 doctor 可访问。
  it('doctor 能打开 AI 预问诊报告', async () => {
    asRole('doctor')
    expect(await go('/appointments/5/report')).toBe('preconsult-report')
  })

  // 侧栏对 admin 显示「医生工作台」（meta.roles 也是 doctor+admin）
  it('admin 能打开医生工作台', async () => {
    asRole('admin')
    expect(await go('/workbench')).toBe('workbench')
  })

  it('admin 也能打开 AI 报告', async () => {
    asRole('admin')
    expect(await go('/appointments/5/report')).toBe('preconsult-report')
  })

  // 这三条的落点刻意是 **404 而不是首页**：
  // 对当前角色来说这些路由根本没注册，所以匹配阶段就是"不存在"，
  // 与"匹配上了但 meta.roles 不允许"是两条不同的路径（后者会回首页）。
  // 界面上的导航按角色过滤，正常情况下点不到这些地址，手改 URL 才是入口。
  it('patient 拿不到医生工作台', async () => {
    asRole('patient')
    expect(await go('/workbench')).toBe('not-found')
  })

  it('doctor 拿不到管理看板', async () => {
    asRole('doctor')
    expect(await go('/admin/dashboard')).toBe('not-found')
  })

  it('doctor 拿不到审计日志', async () => {
    asRole('doctor')
    expect(await go('/admin/audit-logs')).toBe('not-found')
  })
})

describe('守卫的其余分支', () => {
  it('未登录访问受保护路径 → 登录页，并带上 redirect', async () => {
    expect(await go('/admin/dashboard')).toBe('login')
    expect(router.currentRoute.value.query.redirect).toBe('/admin/dashboard')
  })

  it('未登录访问不存在路径 → 先去登录（登录后才会得到 404）', async () => {
    // 登录前无法区分"这个地址不存在"与"这个地址需要登录"（不知道用户角色），
    // 所以不能断言成 404 —— 那会给"收藏了受保护地址"的用户一个撒谎的结论。
    expect(await go('/nope/nothing')).toBe('login')
    expect(router.currentRoute.value.query.redirect).toBe('/nope/nothing')
  })

  it('已登录访问 /login → 该角色首页', async () => {
    for (const role of ROLES) {
      resetRoleRoutes()
      asRole(role)
      expect(await go('/login')).toBe(homeRouteName(role))
    }
  })

  it('已登录访问 / → 该角色首页', async () => {
    for (const role of ROLES) {
      resetRoleRoutes()
      asRole(role)
      expect(await go('/')).toBe(homeRouteName(role))
    }
  })

  it('已登录访问真正不存在的路径 → 404（不会被误当成"路由还没注册"）', async () => {
    asRole('admin')
    expect(await go('/definitely/not/a/page')).toBe('not-found')
  })

  it('登出（resetRoleRoutes）后角色路由确实不可达', async () => {
    asRole('admin')
    expect(await go('/admin/dashboard')).toBe('admin-dashboard')

    // 真实登出 = resetRoleRoutes() + auth.reset()（清 profile）+ 清令牌。
    // 只清令牌是不够的：store 里仍然留着 profile，`isLoggedIn` 还是 true。
    resetRoleRoutes()
    asGuest()

    expect(router.hasRoute('admin-dashboard')).toBe(false)
    expect(await go('/admin/dashboard')).toBe('login')
  })

  it('switch 角色时不会残留上一个角色的路由', async () => {
    asRole('admin')
    expect(await go('/admin/dashboard')).toBe('admin-dashboard')

    // 换成一个 patient：真实场景里这中间必然有一次 resetRoleRoutes（登出/令牌失效）
    resetRoleRoutes()
    asRole('patient')

    expect(await go('/booking')).toBe('booking')
    expect(router.hasRoute('admin-dashboard')).toBe(false)
  })
})

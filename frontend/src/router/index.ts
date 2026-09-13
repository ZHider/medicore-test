import { createRouter, createWebHistory } from 'vue-router'
import type { RouteRecordRaw } from 'vue-router'

import { setAuthFailureHandler } from '@/api/client'
import AppLayout from '@/layouts/AppLayout.vue'
import { useAuthStore } from '@/stores/auth'
import type { Role } from '@/types/api'

declare module 'vue-router' {
  interface RouteMeta {
    /** 免登录页面 */
    public?: boolean
    /** 允许访问的角色；缺省表示任何已登录角色都可以 */
    roles?: Role[]
    title?: string
  }
}

const HOME_BY_ROLE: Record<Role, string> = {
  patient: 'booking',
  doctor: 'workbench',
  admin: 'admin-dashboard',
}

function homeRouteName(role: Role): string {
  return HOME_BY_ROLE[role]
}

// ---------------------------------------------------------------------------
// 静态路由
// ---------------------------------------------------------------------------

const baseRoutes: RouteRecordRaw[] = [
  {
    path: '/login',
    name: 'login',
    component: () => import('@/views/LoginView.vue'),
    meta: { public: true, title: '登录' },
  },
  {
    path: '/register',
    name: 'register',
    component: () => import('@/views/RegisterView.vue'),
    meta: { public: true, title: '注册' },
  },
  {
    // 布局壳：子路由按角色动态注册
    path: '/',
    name: 'app',
    component: AppLayout,
    children: [],
  },
  {
    path: '/:pathMatch(.*)*',
    name: 'not-found',
    component: () => import('@/views/NotFoundView.vue'),
    meta: { public: true, title: '页面不存在' },
  },
]

// ---------------------------------------------------------------------------
// 按角色动态注册的路由（spec §8.2 要求）
// ---------------------------------------------------------------------------

const patientRoutes: RouteRecordRaw[] = [
  {
    path: 'booking',
    name: 'booking',
    component: () => import('@/views/patient/BookingView.vue'),
    meta: { roles: ['patient'], title: '预约挂号' },
  },
  {
    path: 'appointments',
    name: 'my-appointments',
    component: () => import('@/views/patient/MyAppointmentsView.vue'),
    meta: { roles: ['patient'], title: '我的预约' },
  },
  {
    path: 'appointments/:id/chat',
    name: 'preconsult-chat',
    component: () => import('@/views/patient/PreconsultChatView.vue'),
    meta: { roles: ['patient'], title: 'AI 预问诊' },
  },
  {
    path: 'appointments/:id/report',
    name: 'preconsult-report',
    component: () => import('@/views/ReportView.vue'),
    meta: { roles: ['patient', 'doctor', 'admin'], title: '预问诊报告' },
  },
]

const doctorRoutes: RouteRecordRaw[] = [
  {
    path: 'workbench',
    name: 'workbench',
    component: () => import('@/views/doctor/WorkbenchView.vue'),
    meta: { roles: ['doctor', 'admin'], title: '医生工作台' },
  },
  {
    path: 'workbench/:id/record',
    name: 'write-record',
    component: () => import('@/views/doctor/WriteRecordView.vue'),
    meta: { roles: ['doctor'], title: '书写病历' },
  },
]

const adminRoutes: RouteRecordRaw[] = [
  {
    path: 'admin/dashboard',
    name: 'admin-dashboard',
    component: () => import('@/views/admin/DashboardView.vue'),
    meta: { roles: ['admin'], title: '运营看板' },
  },
  {
    path: 'admin/audit-logs',
    name: 'admin-audit-logs',
    component: () => import('@/views/admin/AuditLogsView.vue'),
    meta: { roles: ['admin'], title: '审计日志' },
  },
  {
    path: 'admin/sync',
    name: 'admin-sync',
    component: () => import('@/views/admin/SyncView.vue'),
    meta: { roles: ['admin'], title: 'HIS 数据同步' },
  },
]

/** 任意已登录角色都可访问（后端按身份自动分流返回内容）。 */
const commonRoutes: RouteRecordRaw[] = [
  {
    path: 'records',
    name: 'records',
    component: () => import('@/views/RecordsView.vue'),
    meta: { roles: ['patient', 'doctor', 'admin'], title: '病历记录' },
  },
]

/**
 * 全部按角色注册的路由。
 *
 * 数组只负责"读起来分组清楚"，**不参与注册决策** —— 注册集合由下面的
 * `routesForRole` 从 `meta.roles` 推导。原因见那个函数的注释。
 */
const ALL_ROLE_ROUTES: RouteRecordRaw[] = [
  ...patientRoutes,
  ...doctorRoutes,
  ...adminRoutes,
  ...commonRoutes,
]

/**
 * 某角色应该注册到哪些路由。
 *
 * **判据是 `meta.roles`，不是上面数组的名字。** 这里刻意做成"推导"而不是手写映射
 * （`ROUTES_BY_ROLE[role]`），因为手写映射会与路由自己声明的 `meta.roles` 漂移，
 * 而漂移的后果是**"界面里点了没反应"** —— 一个既不报错、也不容易联想的症状：
 *
 *   - `preconsult-report` 声明 `patient/doctor/admin`，却只挂在 patient 组里
 *     → 医生工作台的「AI 报告」按钮点不动；
 *   - `workbench` 声明 `doctor/admin`，却只挂在 doctor 组里
 *     → 管理员侧栏里的「医生工作台」点不动。
 *
 * 两处都是同一个根因。改成从 `meta.roles` 推导之后，"声明"与"注册"不可能再不一致。
 */
export function routesForRole(role: Role): RouteRecordRaw[] {
  return ALL_ROLE_ROUTES.filter((route) => {
    const roles = route.meta?.roles
    // 未声明 roles 的路由视为"任何已登录角色都可用"
    return !roles || roles.includes(role)
  })
}

/** 已注册过角色路由的角色，避免重复 addRoute 产生重复渲染。 */
const installedRoles = new Set<Role>()
/** 已动态添加的路由名，用于登出时彻底移除。 */
const addedRouteNames = new Set<string>()

function collectNames(routes: RouteRecordRaw[]): void {
  for (const route of routes) {
    if (typeof route.name === 'string') addedRouteNames.add(route.name)
    if (route.children) collectNames(route.children)
  }
}

function installRoleRoutes(role: Role): boolean {
  if (installedRoles.has(role)) return false
  installedRoles.add(role)

  const routes = routesForRole(role)
  for (const route of routes) {
    router.addRoute('app', route)
  }
  collectNames(routes)
  return true
}

/** 登出 / 令牌失效时把角色路由全部摘掉，避免残留可访问的路由。 */
function resetRoleRoutes(): void {
  for (const name of addedRouteNames) {
    if (router.hasRoute(name)) router.removeRoute(name)
  }
  addedRouteNames.clear()
  installedRoles.clear()
}

// ---------------------------------------------------------------------------
// 路由实例
// ---------------------------------------------------------------------------

const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: baseRoutes,
  scrollBehavior(_to, _from, saved) {
    return saved ?? { top: 0 }
  },
})

router.beforeEach(async (to) => {
  const auth = useAuthStore()

  // 首次导航前恢复登录态（刷新页面后仍保持登录）
  if (!auth.bootstrapped) await auth.bootstrap()

  // ---------------------------------------------------------------------------
  // 1) 已登录就**先把角色路由装上**，要排在其他判断之前
  //
  // 两个坑都出在"装晚了"：
  //
  //   a. 角色路由是登录后才注册的，于是刷新页面时**任何深链接都会被 catchAll 抢走匹配**。
  //      而 catchAll 带着 `public: true` —— 若先判 public 就 return，角色路由永远装不上，
  //      刷新必然落到「页面不存在」。（用户实际报的就是这个：登录后能进管理看板，一刷新就 404。）
  //   b. 守卫后面有好几处"按 name 跳转"（首页、角色不匹配、已登录访问登录页）。
  //      **目标 name 必须已经注册**，否则 vue-router 以"无匹配"中断本次导航 ——
  //      表现同样是"点了没反应 / 停在原地"。（LoginView 里那段注释说的就是这个坑，
  //      这里曾经在 public 分支里原样又犯了一次。）
  // ---------------------------------------------------------------------------
  // ---------------------------------------------------------------------------
  // 1) 已登录就**先把角色路由装上**，要排在其他判断之前
  //
  // 两个坑都出在"装晚了"：
  //
  //   a. 角色路由是登录后才注册的，于是刷新页面时**任何深链接都会被 catchAll 抢走匹配**。
  //      而 catchAll 带着 `public: true` —— 若先判 public 就 return，角色路由永远装不上，
  //      刷新必然落到「页面不存在」。（用户实际报的就是这个：登录后能进管理看板，一刷新就 404。）
  //   b. 守卫后面有好几处"按 name 跳转"（首页、角色不匹配、已登录访问登录页）。
  //      **目标 name 必须已经注册**，否则 vue-router 以"无匹配"中断本次导航 ——
  //      表现同样是"点了没反应 / 停在原地"。（LoginView 里那段注释说的就是这个坑，
  //      这里曾经在 public 分支里原样又犯了一次。）
  // ---------------------------------------------------------------------------
  if (auth.isLoggedIn) {
    const role = auth.role as Role
    if (installRoleRoutes(role) && to.name === 'not-found') {
      // 本次匹配是按 catchAll 完成的，用真实路径重新解析一次
      return to.fullPath
    }
  }

  // ---------------------------------------------------------------------------
  // 2) 没匹配到任何真实路由（被 catchAll 接住），且上面没能救回来
  // ---------------------------------------------------------------------------
  if (to.name === 'not-found') {
    // 已登录：角色路由已装齐，这个地址确实不属于该角色（或确实不存在）
    if (auth.isLoggedIn) return true

    // 未登录：**无法区分**"这个地址不存在"与"这个地址需要登录"（我们不知道他的角色），
    // 所以先送去登录页。直接放行成 404 的话，用户收藏的受保护地址会显示
    //「页面不存在」—— 一个会撒谎的结论。
    return { name: 'login', query: { redirect: to.fullPath } }
  }

  // ---------------------------------------------------------------------------
  // 3) 公开页面
  // ---------------------------------------------------------------------------
  if (to.meta.public) {
    if (auth.isLoggedIn && (to.name === 'login' || to.name === 'register')) {
      return { name: homeRouteName(auth.role as Role) }
    }
    return true
  }

  // ---------------------------------------------------------------------------
  // 4) 需要登录
  // ---------------------------------------------------------------------------
  if (!auth.isLoggedIn) {
    return {
      name: 'login',
      query: to.fullPath && to.fullPath !== '/' ? { redirect: to.fullPath } : undefined,
    }
  }

  const role = auth.role as Role

  if (to.path === '/') {
    return { name: homeRouteName(role) }
  }

  if (to.meta.roles && !to.meta.roles.includes(role)) {
    return { name: homeRouteName(role) }
  }

  return true
})

router.afterEach((to) => {
  const title = to.meta.title
  document.title = title ? `${title} · MediCore` : 'MediCore 医疗数据中台'
})

// 令牌彻底失效时（刷新也救不回来）统一回登录页
setAuthFailureHandler(() => {
  const auth = useAuthStore()
  auth.reset()
  resetRoleRoutes()

  const current = router.currentRoute.value
  if (current.name === 'login') return

  void router.replace({
    name: 'login',
    query: current.fullPath && current.fullPath !== '/' ? { redirect: current.fullPath } : undefined,
  })
})

/**
 * 登录成功后必须先注册角色路由再跳转：
 * 此刻目标路由还不存在，直接 `router.replace({ name: 'booking' })` 会因"无匹配"而中断导航。
 */
export { homeRouteName, resetRoleRoutes, installRoleRoutes as ensureRoleRoutes }
export default router
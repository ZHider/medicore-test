// @vitest-environment jsdom
import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { clearTokens, getAccessToken, getRefreshToken, saveTokens } from '@/api/tokens'
import { useAuthStore } from './auth'
import type { UserProfile } from '@/types/api'

/**
 * stores/auth.ts 锁住的是**登录态的边界**，三条都很难在浏览器里手工验证：
 *
 *   1. **角色必须来自 `/auth/me`**，不能信 localStorage —— 改一下存储就能看到 admin 页面
 *      是个真实的越权入口（路由守卫的后半段）。
 *   2. **`/auth/me` 失败必须清干净令牌**。否则"用户以为登出了 / 其实还残留"，
 *      而且残留的令牌会一直触发刷新请求。
 *   3. **登出时服务端撤销失败也要清本地** —— `finally` 分支，走不到就永远发现不了。
 *
 * 用 jsdom：`api/tokens.ts` 直接读 `window.localStorage`（不注入），
 * 这里刻意**不 mock 它** —— 令牌的读写本身就是要验证的一部分。
 */

const mocks = vi.hoisted(() => ({
  login: vi.fn(),
  me: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
}))

// 整个 @/api/auth 模块被替换掉，所以**不需要** `import * as authApi` ——
// 那会是一个用不到的导入（eslint 的 no-unused-vars 会拦下来）。
// store 内部的 `authApi.me()` 拿到的是这里的 mocks.me。
vi.mock('@/api/auth', () => mocks)

const profile: UserProfile = { id: 12, role: 'patient', name: '张三', phone: '138****8888' }

const pair = {
  access_token: 'access-1',
  refresh_token: 'refresh-1',
  token_type: 'Bearer',
  expires_in: 7200,
}

/** 只断言被调用的边界，不需要真的解出 JWT。 */
function storedTokens(): { access: string; refresh: string } {
  return { access: getAccessToken(), refresh: getRefreshToken() }
}

beforeEach(() => {
  setActivePinia(createPinia())
  // 必须用 clearTokens() 而不是 localStorage.clear()：tokens.ts 有一份**模块级内存副本**
  // （localStorage 不可用时的退化路径），它跨用例存活，只清 localStorage 清不掉。
  clearTokens()
  mocks.login.mockReset().mockResolvedValue(pair)
  mocks.me.mockReset().mockResolvedValue(profile)
  mocks.register.mockReset().mockResolvedValue(profile)
  mocks.logout.mockReset().mockResolvedValue({ status: 'logged_out' })
})

describe('bootstrap', () => {
  it('无令牌时不去请求 /auth/me', async () => {
    const store = useAuthStore()
    await store.bootstrap()

    expect(mocks.me).not.toHaveBeenCalled()
    expect(store.profile).toBeNull()
    expect(store.isLoggedIn).toBe(false)
  })

  it('有令牌时用 /auth/me 恢复登录态', async () => {
    saveTokens(pair)
    const store = useAuthStore()
    await store.bootstrap()

    expect(mocks.me).toHaveBeenCalledTimes(1)
    expect(store.profile).toEqual(profile)
    expect(store.isLoggedIn).toBe(true)
    expect(store.role).toBe('patient')
  })

  it('只存了 refresh_token 也算有令牌（access 过期的那半天）', async () => {
    localStorage.setItem('medicore.refresh_token', 'refresh-only')
    const store = useAuthStore()
    await store.bootstrap()

    expect(mocks.me).toHaveBeenCalledTimes(1)
    expect(store.isLoggedIn).toBe(true)
  })

  it('登录态必须真的打 /auth/me，不能只信本地存储里的角色', async () => {
    // 就算本地已经被写进了角色，bootstrap 也必须回后端确认一次：
    // localStorage 可以被改，令牌也可能已过期或被撤销（§5.6）。
    localStorage.setItem('medicore.role', 'admin')
    saveTokens(pair)
    mocks.me.mockResolvedValue({ ...profile, role: 'patient' })

    const store = useAuthStore()
    await store.bootstrap()

    expect(mocks.me).toHaveBeenCalledTimes(1)
    expect(store.role).toBe('patient')
  })

  it('/auth/me 失败（过期或被撤销）要清掉令牌而不是留着', async () => {
    saveTokens(pair)
    mocks.me.mockRejectedValue(new Error('40101'))

    const store = useAuthStore()
    await store.bootstrap()

    expect(store.profile).toBeNull()
    expect(storedTokens()).toEqual({ access: '', refresh: '' })
  })

  it('幂等：重复调用只请求一次', async () => {
    saveTokens(pair)
    const store = useAuthStore()
    await store.bootstrap()
    await store.bootstrap()

    expect(mocks.me).toHaveBeenCalledTimes(1)
  })

  it('无令牌时也算启动完成，避免每次挂载都重新检查', async () => {
    const store = useAuthStore()
    await store.bootstrap()
    await store.bootstrap()

    expect(store.bootstrapped).toBe(true)
    expect(mocks.me).not.toHaveBeenCalled()
  })
})

describe('login', () => {
  it('登录成功后两个令牌都被持久化', async () => {
    const store = useAuthStore()
    await store.login({ phone: '13800008888', password: 'Passw0rd!' })

    expect(storedTokens()).toEqual({ access: 'access-1', refresh: 'refresh-1' })
  })

  it('profile 来自 /auth/me，而不是登录响应', async () => {
    // login 返回的是 TokenPair，里面**没有** role ——
    // 若实现改成从登录响应推断角色，这条会立刻变红。
    mocks.me.mockResolvedValue({ ...profile, role: 'doctor', name: '李医生' })

    const store = useAuthStore()
    const returned = await store.login({ phone: '13900000001', password: 'Doctor@123' })

    expect(mocks.me).toHaveBeenCalledTimes(1)
    expect(returned.role).toBe('doctor')
    expect(store.role).toBe('doctor')
  })

  it('登录失败时不留下半截状态', async () => {
    mocks.login.mockRejectedValue(new Error('40101'))

    const store = useAuthStore()
    await expect(store.login({ phone: '13800008888', password: 'wrong' })).rejects.toThrow()

    expect(storedTokens()).toEqual({ access: '', refresh: '' })
    expect(store.profile).toBeNull()
  })

  it('/auth/me 失败时不返回 profile（不能拿 null 当登录成功）', async () => {
    mocks.me.mockRejectedValue(new Error('50001'))

    const store = useAuthStore()
    await expect(store.login({ phone: '13800008888', password: 'Passw0rd!' })).rejects.toThrow()

    expect(store.isLoggedIn).toBe(false)
  })
})

describe('registerAndLogin', () => {
  it('先注册再用同一组凭据登录', async () => {
    const store = useAuthStore()
    await store.registerAndLogin({ phone: '13800008888', name: '张三', password: 'Passw0rd!' })

    expect(mocks.register).toHaveBeenCalledWith({
      phone: '13800008888',
      name: '张三',
      password: 'Passw0rd!',
    })
    expect(mocks.login).toHaveBeenCalledWith({ phone: '13800008888', password: 'Passw0rd!' })
    expect(store.isLoggedIn).toBe(true)
  })

  it('注册失败时不尝试登录', async () => {
    mocks.register.mockRejectedValue(new Error('40001'))

    const store = useAuthStore()
    await expect(
      store.registerAndLogin({ phone: '138', name: '张', password: 'x' }),
    ).rejects.toThrow()

    expect(mocks.login).not.toHaveBeenCalled()
    expect(storedTokens()).toEqual({ access: '', refresh: '' })
  })
})

describe('logout', () => {
  it('把 refresh_token 一起发给后端（只删本地不会撤销服务端那份）', async () => {
    const store = useAuthStore()
    await store.login({ phone: '13800008888', password: 'Passw0rd!' })
    await store.logout()

    expect(mocks.logout).toHaveBeenCalledWith('refresh-1')
  })

  it('服务端撤销失败也要清干净本地', async () => {
    const store = useAuthStore()
    await store.login({ phone: '13800008888', password: 'Passw0rd!' })
    mocks.logout.mockRejectedValue(new Error('网络异常'))

    await expect(store.logout()).resolves.toBeUndefined()

    expect(storedTokens()).toEqual({ access: '', refresh: '' })
    expect(store.profile).toBeNull()
    expect(store.isLoggedIn).toBe(false)
    expect(store.role).toBeNull()
  })

  it('没有 refresh_token 时不发请求，但仍然清干净', async () => {
    const store = useAuthStore()
    await store.logout()

    expect(mocks.logout).not.toHaveBeenCalled()
    expect(storedTokens()).toEqual({ access: '', refresh: '' })
  })
})

describe('reset', () => {
  it('清令牌与 profile（令牌失效时由拦截器回调触发）', async () => {
    const store = useAuthStore()
    await store.login({ phone: '13800008888', password: 'Passw0rd!' })

    store.reset()

    expect(storedTokens()).toEqual({ access: '', refresh: '' })
    expect(store.profile).toBeNull()
    expect(store.isLoggedIn).toBe(false)
  })

  it('reset 不请求后端（与 logout 的区别）', async () => {
    const store = useAuthStore()
    await store.login({ phone: '13800008888', password: 'Passw0rd!' })

    store.reset()

    expect(mocks.logout).not.toHaveBeenCalled()
  })
})

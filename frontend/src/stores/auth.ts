import { computed, ref } from 'vue'
import { defineStore } from 'pinia'

import * as authApi from '@/api/auth'
import { clearTokens, getRefreshToken, hasTokens, saveTokens } from '@/api/tokens'
import type { LoginRequest, RegisterRequest, Role, UserProfile } from '@/types/api'

export const useAuthStore = defineStore('auth', () => {
  const profile = ref<UserProfile | null>(null)
  const bootstrapped = ref(false)

  const isLoggedIn = computed(() => profile.value !== null)
  const role = computed<Role | null>(() => profile.value?.role ?? null)

  /**
   * 启动时恢复登录态。
   *
   * 这里调用 `/auth/me` 而不是直接读 localStorage 里的角色：
   * localStorage 可以被改，令牌也可能已过期或被撤销。
   * 若 access_token 刚过期（2 小时），axios 拦截器会先用 refresh_token 自动续期再重试，
   * 所以用户第二天回来仍是登录态。
   */
  async function bootstrap(): Promise<void> {
    if (bootstrapped.value) return
    bootstrapped.value = true
    if (!hasTokens()) return

    try {
      profile.value = await authApi.me()
    } catch {
      clearTokens()
      profile.value = null
    }
  }

  async function login(payload: LoginRequest): Promise<UserProfile> {
    const pair = await authApi.login(payload)
    saveTokens(pair)
    // 角色必须来自后端，不能由前端推断
    profile.value = await authApi.me()
    return profile.value
  }

  /** 注册（只会得到 patient 角色）后自动登录，省掉一次手动输入。 */
  async function registerAndLogin(payload: RegisterRequest): Promise<UserProfile> {
    await authApi.register(payload)
    return login({ phone: payload.phone, password: payload.password })
  }

  /** 登出：先撤销服务端那份 refresh_token，再清本地。 */
  async function logout(): Promise<void> {
    const refreshToken = getRefreshToken()
    try {
      if (refreshToken) await authApi.logout(refreshToken)
    } catch {
      // 服务端撤销失败也要保证本地被清干净，否则用户以为登出了其实没有
    } finally {
      reset()
    }
  }

  /** 仅清本地状态（令牌失效时由拦截器回调触发）。 */
  function reset(): void {
    clearTokens()
    profile.value = null
  }

  return { profile, role, isLoggedIn, bootstrapped, bootstrap, login, registerAndLogin, logout, reset }
})

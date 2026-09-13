import { http } from './client'
import type { LoginRequest, LogoutResult, RegisterRequest, TokenPair, UserProfile } from '@/types/api'

/** POST /auth/register —— 只开放患者角色，返回 UserProfile（不含令牌）。 */
export function register(payload: RegisterRequest): Promise<UserProfile> {
  return http.post<UserProfile>('/auth/register', payload)
}

/** POST /auth/login —— 限流 5 次/分钟（按 IP）。 */
export function login(payload: LoginRequest): Promise<TokenPair> {
  return http.post<TokenPair>('/auth/login', payload)
}

/** GET /auth/me —— 校验令牌有效性并取回角色（路由守卫必须用它，不能只信本地 role）。 */
export function me(): Promise<UserProfile> {
  return http.get<UserProfile>('/auth/me')
}

/** POST /auth/logout —— 必须把 refresh_token 一起发过去，只删本地不会撤销服务端那一份。 */
export function logout(refreshToken: string): Promise<LogoutResult> {
  return http.post<LogoutResult>('/auth/logout', { refresh_token: refreshToken })
}

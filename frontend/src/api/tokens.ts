/**
 * 双令牌的本地存储。
 *
 * - access_token：JWT，2 小时，随 `Authorization: Bearer` 发送
 * - refresh_token：不透明串，7 天，**一次性**（服务端 GETDEL 原子消费）
 *
 * 所有读写都包了 try/catch：Safari 无痕模式下访问 localStorage 会抛异常，
 * 不能让一个存储异常把整个应用打挂（此时退化为"内存态"，功能仍可用）。
 */

import type { TokenPair } from '@/types/api'

const ACCESS_KEY = 'medicore.access_token'
const REFRESH_KEY = 'medicore.refresh_token'

let memoryAccess = ''
let memoryRefresh = ''

function read(key: string): string {
  try {
    return window.localStorage.getItem(key) ?? ''
  } catch {
    return ''
  }
}

function write(key: string, value: string): void {
  try {
    if (value) window.localStorage.setItem(key, value)
    else window.localStorage.removeItem(key)
  } catch {
    /* 存储不可用时只保留内存副本 */
  }
}

export function getAccessToken(): string {
  return read(ACCESS_KEY) || memoryAccess
}

export function getRefreshToken(): string {
  return read(REFRESH_KEY) || memoryRefresh
}

/** 保存令牌对。**刷新后必须两个都覆盖** —— 旧的 refresh_token 已被服务端消费。 */
export function saveTokens(pair: TokenPair): void {
  memoryAccess = pair.access_token
  memoryRefresh = pair.refresh_token
  write(ACCESS_KEY, pair.access_token)
  write(REFRESH_KEY, pair.refresh_token)
}

export function clearTokens(): void {
  memoryAccess = ''
  memoryRefresh = ''
  write(ACCESS_KEY, '')
  write(REFRESH_KEY, '')
}

export function hasTokens(): boolean {
  return Boolean(getAccessToken() || getRefreshToken())
}

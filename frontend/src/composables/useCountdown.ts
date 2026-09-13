import { onUnmounted, ref } from 'vue'
import type { Ref } from 'vue'

import { millisUntil } from '@/utils/datetime'

/**
 * 秒级倒计时。
 *
 * 用于 pending 预约的 15 分钟确认窗口、以及登录被限流后的 Retry-After 等待。
 * 传入 getter 而不是值，这样目标时间变化时会自动跟上。
 */
export function useCountdown(
  getTarget: () => string | null | undefined,
  onExpire?: () => void,
): { remaining: Ref<number>; start: () => void; stop: () => void } {
  const remaining = ref(0)
  let timer: number | null = null

  function tick(): void {
    const ms = millisUntil(getTarget())
    remaining.value = ms
    if (ms <= 0) {
      stop()
      onExpire?.()
    }
  }

  function stop(): void {
    if (timer !== null) {
      window.clearInterval(timer)
      timer = null
    }
  }

  function start(): void {
    stop()
    tick()
    if (remaining.value > 0) {
      timer = window.setInterval(tick, 1000)
    }
  }

  onUnmounted(stop)

  return { remaining, start, stop }
}

/** 单纯的秒数倒计时（例如限流的 Retry-After），到点触发回调。 */
export function useSecondCountdown(onDone?: () => void): {
  seconds: Ref<number>
  start: (from: number) => void
  stop: () => void
} {
  const seconds = ref(0)
  let timer: number | null = null

  function stop(): void {
    if (timer !== null) {
      window.clearInterval(timer)
      timer = null
    }
  }

  function start(from: number): void {
    stop()
    seconds.value = Math.max(0, Math.ceil(from))
    if (seconds.value <= 0) return

    timer = window.setInterval(() => {
      seconds.value -= 1
      if (seconds.value <= 0) {
        stop()
        onDone?.()
      }
    }, 1000)
  }

  onUnmounted(stop)

  return { seconds, start, stop }
}

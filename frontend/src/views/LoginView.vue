<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'

import AppIcon from '@/components/AppIcon.vue'
import { ErrorCode, toApiError } from '@/api/error'
import { useSecondCountdown } from '@/composables/useCountdown'
import { ensureRoleRoutes, homeRouteName } from '@/router'
import { useAuthStore } from '@/stores/auth'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()

const formRef = ref<FormInstance>()
const form = reactive({ phone: '', password: '' })
const submitting = ref(false)
const errorText = ref('')

/** 被限流后的等待倒计时（秒），读自 Retry-After 响应头而不是写死 */
const { seconds: waitSeconds, start: startWait } = useSecondCountdown(() => {
  lockMessage.value = ''
})
const lockMessage = ref('')

const locked = computed(() => waitSeconds.value > 0)

const rules: FormRules = {
  phone: [
    { required: true, message: '请输入手机号', trigger: 'blur' },
    { pattern: /^\d{11}$/, message: '手机号必须是 11 位数字', trigger: 'blur' },
  ],
  password: [
    { required: true, message: '请输入密码', trigger: 'blur' },
    { min: 8, max: 64, message: '密码长度为 8–64 位', trigger: 'blur' },
  ],
}

async function submit(): Promise<void> {
  if (locked.value || submitting.value) return

  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  submitting.value = true
  errorText.value = ''

  try {
    const profile = await auth.login({ phone: form.phone, password: form.password })
    // 先把角色路由注册进去，否则下面按 name 跳转会找不到目标
    ensureRoleRoutes(profile.role)

    ElMessage.success(`欢迎回来，${profile.name}`)

    const redirect = route.query.redirect
    if (typeof redirect === 'string' && redirect) {
      await router.replace(redirect)
    } else {
      await router.replace({ name: homeRouteName(profile.role) })
    }
  } catch (e) {
    const err = toApiError(e)

    if (err.code === ErrorCode.RateLimited) {
      // 限流时绝不能提示"密码错误"——用户会去改密码，越改越锁
      const wait = err.retryAfter ?? 60
      startWait(wait)
      lockMessage.value = `登录尝试过于频繁，请 ${wait} 秒后重试`
      return
    }

    // 后端已统一为「手机号或密码错误」，前端原样透传即可（防用户枚举）
    errorText.value = err.displayMessage
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="auth">
    <!-- 桌面端的品牌侧栏：窄屏隐藏，避免挤压表单 -->
    <section class="auth-aside">
      <div class="aside-inner">
        <div class="aside-brand">
          <div class="aside-mark">
            <AppIcon
              name="pulse"
              :size="22"
            />
          </div>
          <span>MediCore</span>
        </div>
        <h2 class="aside-title">
          医疗数据中台<br>与 AI 预问诊平台
        </h2>
        <p class="aside-desc">
          患者在线挂号与 AI 分诊，医生工作台与病历处方，管理端运营看板与全链路审计追踪。
        </p>
        <ul class="aside-points">
          <li>
            <AppIcon
              name="check"
              :size="16"
            /><span>Redis 原子扣减，号源不超卖</span>
          </li>
          <li>
            <AppIcon
              name="check"
              :size="16"
            /><span>AI 预问诊流式对话与结构化总结</span>
          </li>
          <li>
            <AppIcon
              name="check"
              :size="16"
            /><span>TraceID 贯穿，一次请求全程可查</span>
          </li>
        </ul>
      </div>
    </section>

    <section class="auth-main">
      <div class="auth-card">
        <div class="auth-head">
          <h1>登录</h1>
          <p class="mc-muted">
            请使用已注册的手机号登录
          </p>
        </div>

        <el-alert
          v-if="lockMessage"
          type="warning"
          show-icon
          :closable="false"
          :title="lockMessage"
          class="auth-alert"
        />
        <el-alert
          v-else-if="errorText"
          type="error"
          show-icon
          :closable="false"
          title="登录失败"
          :description="errorText"
          class="auth-alert"
        />

        <el-form
          ref="formRef"
          :model="form"
          :rules="rules"
          label-position="top"
          size="large"
          @submit.prevent="submit"
        >
          <el-form-item
            label="手机号"
            prop="phone"
          >
            <el-input
              v-model="form.phone"
              placeholder="11 位手机号"
              maxlength="11"
              inputmode="numeric"
              autocomplete="username"
            />
          </el-form-item>

          <el-form-item
            label="密码"
            prop="password"
          >
            <el-input
              v-model="form.password"
              type="password"
              placeholder="8–64 位"
              show-password
              autocomplete="current-password"
              @keyup.enter="submit"
            />
          </el-form-item>

          <el-button
            type="primary"
            class="auth-submit"
            size="large"
            :loading="submitting"
            :disabled="locked"
            @click="submit"
          >
            {{ locked ? `请等待 ${waitSeconds} 秒` : '登录' }}
          </el-button>
        </el-form>

        <div class="auth-foot">
          <span class="mc-muted">还没有账号？</span>
          <RouterLink :to="{ name: 'register' }">
            立即注册
          </RouterLink>
        </div>

        <div class="auth-tip">
          <div class="tip-title">
            演示账号
          </div>
          <div class="tip-row">
            <b>医生</b> 13900000001 / Doctor@123
          </div>
          <div class="tip-row">
            <b>管理员</b> 13800000000 / Admin@12345
          </div>
          <div class="tip-row">
            <b>患者</b> 未预置，请先注册
          </div>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
.auth {
  min-height: 100vh;
  min-height: 100dvh;
  display: flex;
  background: var(--mc-bg);
}

/* ---- 左侧品牌区：仅宽屏显示 ---- */

.auth-aside {
  display: none;
}

@media (min-width: 900px) {
  .auth-aside {
    display: flex;
    flex: 1 1 46%;
    background: linear-gradient(150deg, #0f766e 0%, #0d9488 45%, #14b8a6 100%);
    color: #fff;
    padding: 48px;
    align-items: center;
  }
}

.aside-inner {
  max-width: 420px;
}

.aside-brand {
  display: flex;
  align-items: center;
  gap: 10px;
  font-weight: 700;
  font-size: 17px;
  letter-spacing: 0.3px;
}

.aside-mark {
  width: 38px;
  height: 38px;
  border-radius: 11px;
  background: rgba(255, 255, 255, 0.18);
  display: flex;
  align-items: center;
  justify-content: center;
}

.aside-title {
  margin: 32px 0 16px;
  font-size: 30px;
  line-height: 1.35;
  color: #fff;
  font-weight: 600;
}

.aside-desc {
  color: rgba(255, 255, 255, 0.82);
  font-size: 14px;
  line-height: 1.8;
}

.aside-points {
  list-style: none;
  padding: 0;
  margin: 32px 0 0;
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.aside-points li {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 14px;
  color: rgba(255, 255, 255, 0.92);
}

/* ---- 右侧表单 ---- */

.auth-main {
  flex: 1 1 auto;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 28px 18px calc(28px + var(--mc-safe-bottom));
}

@media (min-width: 900px) {
  .auth-main {
    flex: 1 1 54%;
    padding: 40px;
  }
}

.auth-card {
  width: 100%;
  max-width: 400px;
  background: var(--mc-surface);
  border: 1px solid var(--mc-border);
  border-radius: 18px;
  box-shadow: var(--mc-shadow);
  padding: 26px 22px;
}

@media (min-width: 768px) {
  .auth-card {
    padding: 34px 30px;
  }
}

.auth-head h1 {
  font-size: 24px;
  margin-bottom: 4px;
}

.auth-head {
  margin-bottom: 20px;
}

.auth-alert {
  margin-bottom: 16px;
}

.auth-submit {
  width: 100%;
  margin-top: 4px;
}

.auth-foot {
  margin-top: 18px;
  font-size: 13px;
  text-align: center;
  display: flex;
  gap: 6px;
  justify-content: center;
}

.auth-tip {
  margin-top: 22px;
  padding: 12px 14px;
  border-radius: var(--mc-radius);
  background: var(--mc-surface-alt);
  border: 1px dashed var(--mc-border-strong);
  font-size: 12px;
  color: var(--mc-text-soft);
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.tip-title {
  font-weight: 600;
  color: var(--mc-text);
  margin-bottom: 2px;
}

.tip-row b {
  display: inline-block;
  min-width: 46px;
  color: var(--mc-primary-dark);
}
</style>

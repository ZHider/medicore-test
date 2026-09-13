<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'

import AppIcon from '@/components/AppIcon.vue'
import { ErrorCode, toApiError } from '@/api/error'
import { useSecondCountdown } from '@/composables/useCountdown'
import { ensureRoleRoutes, homeRouteName } from '@/router'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const auth = useAuthStore()

const formRef = ref<FormInstance>()
const form = reactive({ phone: '', name: '', password: '', confirm: '', id_card: '' })
const submitting = ref(false)
const errorText = ref('')

const { seconds: waitSeconds, start: startWait } = useSecondCountdown(() => {
  lockMessage.value = ''
})
const lockMessage = ref('')

const rules: FormRules = {
  phone: [
    { required: true, message: '请输入手机号', trigger: 'blur' },
    // 与后端 binding:"required,len=11,numeric" 完全对齐，不必等 40001
    { pattern: /^\d{11}$/, message: '手机号必须是 11 位数字', trigger: 'blur' },
  ],
  name: [
    { required: true, message: '请输入姓名', trigger: 'blur' },
    { min: 2, max: 32, message: '姓名为 2–32 个字符', trigger: 'blur' },
  ],
  password: [
    { required: true, message: '请输入密码', trigger: 'blur' },
    { min: 8, max: 64, message: '密码长度 8–64 位', trigger: 'blur' },
  ],
  confirm: [
    { required: true, message: '请再次输入密码', trigger: 'blur' },
    {
      validator: (_rule, value: string, callback: (error?: Error) => void) => {
        if (value !== form.password) callback(new Error('两次输入的密码不一致'))
        else callback()
      },
      trigger: 'blur',
    },
  ],
  id_card: [
    {
      validator: (_rule, value: string, callback: (error?: Error) => void) => {
        // 可选；填了就必须是 18 位（与后端 omitempty,len=18 一致）
        if (value && value.length !== 18) callback(new Error('身份证号必须为 18 位'))
        else callback()
      },
      trigger: 'blur',
    },
  ],
}

async function submit(): Promise<void> {
  if (submitting.value || waitSeconds.value > 0) return

  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  submitting.value = true
  errorText.value = ''

  try {
    const profile = await auth.registerAndLogin({
      phone: form.phone,
      name: form.name,
      password: form.password,
      // 空串会被后端当作"未提供"，这里直接省略更干净
      ...(form.id_card ? { id_card: form.id_card } : {}),
    })

    ensureRoleRoutes(profile.role)
    ElMessage.success('注册成功，已自动登录')
    await router.replace({ name: homeRouteName(profile.role) })
  } catch (e) {
    const err = toApiError(e)

    if (err.code === ErrorCode.RateLimited) {
      const wait = err.retryAfter ?? 60
      startWait(wait)
      lockMessage.value = `操作过于频繁，请 ${wait} 秒后重试`
      return
    }

    // 40004（该手机号已注册）等业务错误原样展示，不做二次包装
    errorText.value = err.displayMessage
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div class="auth">
    <section class="auth-aside">
      <div class="aside-inner">
        <div class="aside-brand">
          <div class="aside-mark">
            <AppIcon
              name="heart"
              :size="22"
            />
          </div>
          <span>MediCore</span>
        </div>
        <h2 class="aside-title">
          三步开启<br>你的在线就诊
        </h2>
        <ol class="aside-steps">
          <li><span class="step-no">1</span><span>注册患者账号（仅需手机号）</span></li>
          <li><span class="step-no">2</span><span>选择科室、医生与就诊时段</span></li>
          <li><span class="step-no">3</span><span>AI 预问诊，医生提前了解病情</span></li>
        </ol>
      </div>
    </section>

    <section class="auth-main">
      <div class="auth-card">
        <div class="auth-head">
          <h1>注册</h1>
          <p class="mc-muted">
            注册后默认获得患者身份
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
          title="注册失败"
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
            />
          </el-form-item>

          <el-form-item
            label="姓名"
            prop="name"
          >
            <el-input
              v-model="form.name"
              placeholder="真实姓名"
              maxlength="32"
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
            />
          </el-form-item>

          <el-form-item
            label="确认密码"
            prop="confirm"
          >
            <el-input
              v-model="form.confirm"
              type="password"
              placeholder="再次输入密码"
              show-password
            />
          </el-form-item>

          <el-form-item
            label="身份证号（选填）"
            prop="id_card"
          >
            <el-input
              v-model="form.id_card"
              placeholder="18 位，可留空"
              maxlength="18"
            />
          </el-form-item>

          <el-button
            type="primary"
            class="auth-submit"
            size="large"
            :loading="submitting"
            :disabled="waitSeconds > 0"
            @click="submit"
          >
            注册并登录
          </el-button>
        </el-form>

        <div class="auth-foot">
          <span class="mc-muted">已有账号？</span>
          <RouterLink :to="{ name: 'login' }">
            返回登录
          </RouterLink>
        </div>
      </div>
    </section>
  </div>
</template>

<style scoped>
/* 与 LoginView 共用同一套视觉，仅步骤区不同 */
.auth {
  min-height: 100vh;
  min-height: 100dvh;
  display: flex;
  background: var(--mc-bg);
}

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
  margin: 32px 0 24px;
  font-size: 30px;
  line-height: 1.35;
  color: #fff;
  font-weight: 600;
}

.aside-steps {
  list-style: none;
  padding: 0;
  margin: 0;
  display: flex;
  flex-direction: column;
  gap: 14px;
}

.aside-steps li {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 14px;
  color: rgba(255, 255, 255, 0.92);
}

.step-no {
  width: 24px;
  height: 24px;
  flex-shrink: 0;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.2);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 600;
}

.auth-main {
  flex: 1 1 auto;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px 18px calc(24px + var(--mc-safe-bottom));
}

@media (min-width: 900px) {
  .auth-main {
    flex: 1 1 54%;
    padding: 40px;
  }
}

.auth-card {
  width: 100%;
  max-width: 420px;
  background: var(--mc-surface);
  border: 1px solid var(--mc-border);
  border-radius: 18px;
  box-shadow: var(--mc-shadow);
  padding: 24px 20px;
}

@media (min-width: 768px) {
  .auth-card {
    padding: 30px;
  }
}

.auth-head {
  margin-bottom: 18px;
}

.auth-head h1 {
  font-size: 24px;
  margin-bottom: 4px;
}

.auth-alert {
  margin-bottom: 16px;
}

.auth-submit {
  width: 100%;
}

.auth-foot {
  margin-top: 16px;
  font-size: 13px;
  display: flex;
  gap: 6px;
  justify-content: center;
}
</style>

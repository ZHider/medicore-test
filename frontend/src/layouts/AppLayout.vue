<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, ElMessageBox } from 'element-plus'

import AppIcon from '@/components/AppIcon.vue'
import AppNav from '@/components/AppNav.vue'
import { useIsCompact } from '@/composables/useMediaQuery'
import { useAuthStore } from '@/stores/auth'
import type { Role } from '@/types/api'
import type { NavItem } from '@/types/ui'

const route = useRoute()
const router = useRouter()
const auth = useAuthStore()
const isCompact = useIsCompact()

const drawerOpen = ref(false)

// 导航项按角色过滤 —— 与 router 里动态注册的路由表保持一致
const NAV_ITEMS: NavItem[] = [
  { label: '预约挂号', name: 'booking', icon: 'calendar', roles: ['patient'] },
  { label: '我的预约', name: 'my-appointments', icon: 'list', roles: ['patient'] },
  { label: '医生工作台', name: 'workbench', icon: 'stethoscope', roles: ['doctor', 'admin'] },
  { label: '病历记录', name: 'records', icon: 'document', roles: ['patient', 'doctor', 'admin'] },
  { label: '运营看板', name: 'admin-dashboard', icon: 'chart', roles: ['admin'] },
  { label: '审计日志', name: 'admin-audit-logs', icon: 'shield', roles: ['admin'] },
  { label: 'HIS 数据同步', name: 'admin-sync', icon: 'sync', roles: ['admin'] },
]

const ROLE_LABEL: Record<Role, string> = {
  patient: '患者',
  doctor: '医生',
  admin: '管理员',
}

const navItems = computed(() => {
  const role = auth.role
  if (!role) return []
  return NAV_ITEMS.filter((item) => !item.roles || item.roles.includes(role))
})

const pageTitle = computed(() => route.meta.title ?? 'MediCore')
const roleLabel = computed(() => (auth.role ? ROLE_LABEL[auth.role] : ''))

// 切页时自动收起抽屉，否则手机上点完菜单还挡着内容
watch(() => route.fullPath, () => {
  drawerOpen.value = false
})

async function handleLogout(): Promise<void> {
  try {
    await ElMessageBox.confirm('确定要退出登录吗？', '退出登录', {
      confirmButtonText: '退出',
      cancelButtonText: '取消',
      type: 'warning',
    })
  } catch {
    return // 用户取消
  }

  await auth.logout()
  ElMessage.success('已退出登录')
  await router.replace({ name: 'login' })
}
</script>

<template>
  <div class="mc-shell">
    <!-- 桌面侧栏：固定，不随内容滚动 -->
    <aside
      v-if="!isCompact"
      class="side"
    >
      <div class="brand">
        <div class="brand-mark">
          <AppIcon
            name="pulse"
            :size="20"
          />
        </div>
        <div class="brand-text">
          <div class="brand-name">
            MediCore
          </div>
          <div class="brand-sub">
            医疗数据中台
          </div>
        </div>
      </div>

      <AppNav :items="navItems" />

      <div class="side-foot">
        <div class="user">
          <div class="avatar">
            {{ auth.profile?.name?.slice(0, 1) ?? '?' }}
          </div>
          <div class="user-meta">
            <div class="user-name mc-ellipsis">
              {{ auth.profile?.name }}
            </div>
            <div class="user-role">
              {{ roleLabel }} · {{ auth.profile?.phone }}
            </div>
          </div>
        </div>
        <button
          class="logout"
          type="button"
          @click="handleLogout"
        >
          <AppIcon
            name="logout"
            :size="16"
          />
          <span>退出</span>
        </button>
      </div>
    </aside>

    <div class="mc-main">
      <header class="top">
        <button
          v-if="isCompact"
          class="burger"
          type="button"
          aria-label="打开菜单"
          @click="drawerOpen = true"
        >
          <AppIcon
            name="menu"
            :size="20"
          />
        </button>

        <div class="top-title">
          {{ pageTitle }}
        </div>

        <div class="mc-spacer" />

        <div
          v-if="isCompact"
          class="top-user"
        >
          <div class="avatar avatar--sm">
            {{ auth.profile?.name?.slice(0, 1) ?? '?' }}
          </div>
        </div>
        <button
          v-else
          class="logout logout--top"
          type="button"
          title="退出登录"
          @click="handleLogout"
        >
          <AppIcon
            name="logout"
            :size="16"
          />
        </button>
      </header>

      <main class="mc-content">
        <RouterView v-slot="{ Component }">
          <Transition
            name="mc-fade"
            mode="out-in"
          >
            <component :is="Component" />
          </Transition>
        </RouterView>
      </main>
    </div>

    <!-- 移动端抽屉导航 -->
    <el-drawer
      v-model="drawerOpen"
      direction="ltr"
      size="264px"
      :with-header="false"
      class="mc-drawer-nav"
    >
      <div class="drawer-body">
        <div class="brand brand--drawer">
          <div class="brand-mark">
            <AppIcon
              name="pulse"
              :size="20"
            />
          </div>
          <div class="brand-text">
            <div class="brand-name">
              MediCore
            </div>
            <div class="brand-sub">
              医疗数据中台
            </div>
          </div>
        </div>

        <AppNav
          :items="navItems"
          @navigate="drawerOpen = false"
        />

        <div class="drawer-foot">
          <div class="user">
            <div class="avatar">
              {{ auth.profile?.name?.slice(0, 1) ?? '?' }}
            </div>
            <div class="user-meta">
              <div class="user-name mc-ellipsis">
                {{ auth.profile?.name }}
              </div>
              <div class="user-role">
                {{ roleLabel }}
              </div>
            </div>
          </div>
          <el-button
            class="drawer-logout"
            plain
            @click="handleLogout"
          >
            <AppIcon
              name="logout"
              :size="16"
            />
            <span style="margin-left: 6px">退出登录</span>
          </el-button>
        </div>
      </div>
    </el-drawer>
  </div>
</template>

<style scoped>
/* ---------------- 侧栏（仅 ≥1024px） ---------------- */

.side {
  width: var(--mc-sidebar-w);
  flex-shrink: 0;
  background: var(--mc-surface);
  border-right: 1px solid var(--mc-border);
  display: flex;
  flex-direction: column;
  position: sticky;
  top: 0;
  height: 100vh;
  height: 100dvh;
}

.brand {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 18px 16px 14px;
}

.brand--drawer {
  border-bottom: 1px solid var(--mc-border);
}

.brand-mark {
  width: 34px;
  height: 34px;
  border-radius: 10px;
  background: linear-gradient(135deg, var(--mc-primary), #14b8a6);
  color: #fff;
  display: flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
}

.brand-name {
  font-weight: 700;
  font-size: 15px;
  letter-spacing: 0.2px;
}

.brand-sub {
  font-size: 11px;
  color: var(--mc-text-muted);
}

.side-foot {
  margin-top: auto;
  border-top: 1px solid var(--mc-border);
  padding: 12px;
}

.user {
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
}

.avatar {
  width: 34px;
  height: 34px;
  flex-shrink: 0;
  border-radius: 50%;
  background: var(--mc-primary-soft);
  color: var(--mc-primary-dark);
  font-weight: 600;
  display: flex;
  align-items: center;
  justify-content: center;
}

.avatar--sm {
  width: 30px;
  height: 30px;
  font-size: 13px;
}

.user-meta {
  min-width: 0;
}

.user-name {
  font-size: 13px;
  font-weight: 600;
}

.user-role {
  font-size: 11px;
  color: var(--mc-text-muted);
}

.logout {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  border: none;
  background: transparent;
  color: var(--mc-text-soft);
  font-size: 13px;
  padding: 6px 8px;
  border-radius: 8px;
  cursor: pointer;
  font-family: inherit;
}

.logout:hover {
  background: var(--mc-surface-alt);
  color: var(--mc-danger);
}

.logout--top {
  padding: 8px;
}

.side-foot .logout {
  margin-top: 10px;
  width: 100%;
  justify-content: center;
  border: 1px solid var(--mc-border);
  padding: 8px;
}

/* ---------------- 顶栏 ---------------- */

.top {
  height: var(--mc-topbar-h);
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 14px;
  background: rgba(255, 255, 255, 0.92);
  backdrop-filter: blur(8px);
  border-bottom: 1px solid var(--mc-border);
  position: sticky;
  top: 0;
  z-index: 20;
}

@media (min-width: 768px) {
  .top {
    padding: 0 20px;
  }
}

@media (min-width: 1024px) {
  .top {
    padding: 0 28px;
  }
}

.burger {
  width: 34px;
  height: 34px;
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--mc-border);
  background: var(--mc-surface);
  border-radius: 9px;
  color: var(--mc-text-soft);
  cursor: pointer;
}

.top-title {
  font-size: 15px;
  font-weight: 600;
}

.top-user {
  display: flex;
  align-items: center;
}

/* ---------------- 抽屉 ---------------- */

.drawer-body {
  display: flex;
  flex-direction: column;
  height: 100%;
}

.drawer-foot {
  margin-top: auto;
  padding: 14px;
  border-top: 1px solid var(--mc-border);
}

.drawer-logout {
  width: 100%;
  margin-top: 12px;
}
</style>

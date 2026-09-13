<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'

import EmptyState from '@/components/EmptyState.vue'
import { homeRouteName } from '@/router'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const auth = useAuthStore()

const homeName = computed(() => (auth.role ? homeRouteName(auth.role) : 'login'))
</script>

<template>
  <div class="wrap">
    <div class="mc-card">
      <EmptyState
        icon="search"
        title="页面不存在"
        description="你访问的地址没有对应的页面。"
      >
        <el-button
          type="primary"
          @click="router.replace({ name: homeName })"
        >
          返回首页
        </el-button>
      </EmptyState>
    </div>
  </div>
</template>

<style scoped>
.wrap {
  min-height: 100vh;
  min-height: 100dvh;
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 20px;
}

.mc-card {
  width: 100%;
  max-width: 420px;
}
</style>

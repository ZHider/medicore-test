<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

import AppIcon from './AppIcon.vue'
import type { NavItem } from '@/types/ui'

defineProps<{ items: NavItem[] }>()
const emit = defineEmits<{ navigate: [] }>()

const route = useRoute()
const activeName = computed(() => String(route.name ?? ''))

/** 高亮当前项：子页面（如预问诊对话/报告、写病历）也要点亮其父入口。 */
function isActive(item: NavItem): boolean {
  if (activeName.value === item.name) return true
  if (item.name === 'my-appointments' && activeName.value.startsWith('preconsult')) return true
  if (item.name === 'workbench' && activeName.value === 'write-record') return true
  return false
}
</script>

<template>
  <nav class="nav">
    <RouterLink
      v-for="item in items"
      :key="item.name"
      class="nav-item"
      :class="{ 'is-active': isActive(item) }"
      :to="{ name: item.name }"
      @click="emit('navigate')"
    >
      <AppIcon
        :name="item.icon"
        :size="18"
      />
      <span class="nav-label">{{ item.label }}</span>
    </RouterLink>
  </nav>
</template>

<style scoped>
.nav {
  display: flex;
  flex-direction: column;
  gap: 2px;
  padding: 8px;
}

.nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 12px;
  border-radius: 10px;
  color: var(--mc-text-soft);
  font-size: 14px;
  font-weight: 500;
  transition:
    background 0.16s ease,
    color 0.16s ease;
}

.nav-item:hover {
  background: var(--mc-surface-alt);
  color: var(--mc-text);
}

.nav-item.is-active {
  background: var(--mc-primary-soft);
  color: var(--mc-primary-dark);
  font-weight: 600;
}

.nav-label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>

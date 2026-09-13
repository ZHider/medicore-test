<script setup lang="ts">
import AppIcon from './AppIcon.vue'

withDefaults(
  defineProps<{
    icon?: string
    title: string
    description?: string
  }>(),
  { icon: 'search' },
)
</script>

<template>
  <div class="mc-empty">
    <div class="mc-empty-icon">
      <AppIcon
        :name="icon"
        :size="28"
      />
    </div>
    <div class="empty-title">
      {{ title }}
    </div>
    <div
      v-if="description"
      class="empty-desc"
    >
      {{ description }}
    </div>
    <div
      v-if="$slots.default"
      class="empty-action"
    >
      <slot />
    </div>
  </div>
</template>

<style scoped>
.empty-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--mc-text-soft);
}

.empty-desc {
  font-size: 12px;
  margin-top: 4px;
}

.empty-action {
  margin-top: 14px;
}
</style>

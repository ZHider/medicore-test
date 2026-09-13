<script setup lang="ts">
/** 页面标题区：窄屏竖排，宽屏左右分栏（样式在 style.css 的 .mc-page-head）。 */
defineProps<{
  title: string
  description?: string
}>()
</script>

<template>
  <div class="mc-page-head">
    <div>
      <h1 class="mc-page-title">
        {{ title }}
      </h1>
      <p
        v-if="description"
        class="mc-page-desc"
      >
        {{ description }}
      </p>
    </div>
    <div
      v-if="$slots.actions"
      class="head-actions"
    >
      <slot name="actions" />
    </div>
  </div>
</template>

<style scoped>
.head-actions {
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
  align-items: center;
}
</style>

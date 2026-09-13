import { ref } from 'vue'
import { defineStore } from 'pinia'

import { today } from '@/utils/datetime'

/**
 * 挂号流程的草稿状态。
 *
 * 科室 → 医生 → 排班 → 抢号是四步，中途用户可能返回上一步或误触浏览器后退。
 * 把选择放进 store，返回时不必重新一路点回来。
 */
export const useBookingStore = defineStore('booking', () => {
  const departmentId = ref<number | null>(null)
  const departmentName = ref('')
  const doctorId = ref<number | null>(null)
  const doctorName = ref('')
  const date = ref(today())

  function selectDepartment(id: number, name: string): void {
    if (departmentId.value !== id) {
      // 换了科室，之前选的医生不再适用
      doctorId.value = null
      doctorName.value = ''
    }
    departmentId.value = id
    departmentName.value = name
  }

  function selectDoctor(id: number, name: string): void {
    doctorId.value = id
    doctorName.value = name
  }

  function setDate(value: string): void {
    date.value = value
  }

  function reset(): void {
    departmentId.value = null
    departmentName.value = ''
    doctorId.value = null
    doctorName.value = ''
    date.value = today()
  }

  return {
    departmentId,
    departmentName,
    doctorId,
    doctorName,
    date,
    selectDepartment,
    selectDoctor,
    setDate,
    reset,
  }
})

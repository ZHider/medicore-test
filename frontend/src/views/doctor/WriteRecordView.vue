<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import type { FormInstance, FormRules } from 'element-plus'

import AppIcon from '@/components/AppIcon.vue'
import EmptyState from '@/components/EmptyState.vue'
import PageHeader from '@/components/PageHeader.vue'
import { ErrorCode, toApiError } from '@/api/error'
import { getRecord, writeRecord } from '@/api/record'
import type { PrescriptionItemInput, WriteRecordRequest } from '@/types/api'
import { formatDateTime } from '@/utils/datetime'

const route = useRoute()
const router = useRouter()

const appointmentId = computed(() => Number(route.params.id))

const loading = ref(true)
const submitting = ref(false)

/** 已有病历则改为只读展示：一次就诊只有一份病历，重复写会返回 40004 */
const existing = ref<Awaited<ReturnType<typeof getRecord>> | null>(null)

const formRef = ref<FormInstance>()
const form = reactive<WriteRecordRequest>({
  chief_complaint: '',
  present_illness: '',
  diagnosis: '',
  treatment_advice: '',
  prescriptions: [],
})

const rules: FormRules = {
  diagnosis: [
    { required: true, message: '请填写诊断', trigger: 'blur' },
    { min: 2, max: 512, message: '诊断为 2–512 个字符', trigger: 'blur' },
  ],
  chief_complaint: [{ max: 512, message: '主诉最多 512 个字符', trigger: 'blur' }],
  present_illness: [{ max: 2000, message: '现病史最多 2000 个字符', trigger: 'blur' }],
  treatment_advice: [{ max: 2000, message: '医嘱最多 2000 个字符', trigger: 'blur' }],
}

onMounted(load)

async function load(): Promise<void> {
  loading.value = true
  try {
    existing.value = await getRecord(appointmentId.value)
  } catch (e) {
    const err = toApiError(e)
    // 40003 = 还没写过病历，这正是可以书写的状态
    if (err.code !== ErrorCode.NotFound) {
      ElMessage.error(err.displayMessage)
    }
    existing.value = null
  } finally {
    loading.value = false
  }
}

// ---------------------------------------------------------------------------
// 处方明细
// ---------------------------------------------------------------------------

function addPrescription(): void {
  if ((form.prescriptions?.length ?? 0) >= 20) {
    ElMessage.warning('一次最多开具 20 项处方')
    return
  }
  form.prescriptions = [
    ...(form.prescriptions ?? []),
    { drug_name: '', spec: '', quantity: 1, usage_desc: '', days: undefined },
  ]
}

function removePrescription(index: number): void {
  form.prescriptions = (form.prescriptions ?? []).filter((_, i) => i !== index)
}

/** 处方明细的校验放在提交前手写一遍：它比顶层字段多两条跨界规则。 */
function validatePrescriptions(): string {
  const items = form.prescriptions ?? []
  if (items.length > 20) return '处方最多 20 项'

  for (let i = 0; i < items.length; i += 1) {
    const item = items[i]
    const label = `第 ${i + 1} 项处方`
    if (!item.drug_name?.trim()) return `${label}：药品名称必填`
    if (item.drug_name.length > 128) return `${label}：药品名称最多 128 字`
    if (item.spec && item.spec.length > 64) return `${label}：规格最多 64 字`
    if (!Number.isInteger(item.quantity) || item.quantity < 1 || item.quantity > 999) {
      return `${label}：数量需为 1–999 的整数`
    }
    if (item.usage_desc && item.usage_desc.length > 255) return `${label}：用法最多 255 字`
    if (item.days !== undefined && item.days !== null) {
      if (!Number.isInteger(item.days) || item.days < 1 || item.days > 365) {
        return `${label}：用药天数需为 1–365`
      }
    }
  }
  return ''
}

async function submit(): Promise<void> {
  const valid = await formRef.value?.validate().catch(() => false)
  if (!valid) return

  const rxError = validatePrescriptions()
  if (rxError) {
    ElMessage.warning(rxError)
    return
  }

  submitting.value = true
  try {
    // 空串会被后端 binding 视为有效值，这里统一转成 undefined 让 omitempty 生效
    await writeRecord(appointmentId.value, {
      chief_complaint: form.chief_complaint?.trim() || undefined,
      present_illness: form.present_illness?.trim() || undefined,
      diagnosis: form.diagnosis.trim(),
      treatment_advice: form.treatment_advice?.trim() || undefined,
      prescriptions: (form.prescriptions ?? []).map<PrescriptionItemInput>((item) => ({
        drug_name: item.drug_name.trim(),
        spec: item.spec?.trim() || undefined,
        quantity: item.quantity,
        usage_desc: item.usage_desc?.trim() || undefined,
        days: item.days ?? undefined,
      })),
    })

    ElMessage.success('病历已保存')
    await router.replace({ name: 'workbench' })
  } catch (e) {
    const err = toApiError(e)

    if (err.code === ErrorCode.Conflict) {
      // 40004：并发下已被写入 —— 转成只读，避免医生以为白写了
      ElMessage.warning('该就诊已有病历，已切换为只读视图')
      await load()
    } else if (err.code === ErrorCode.NotOwner) {
      ElMessage.error('你不是该预约的接诊医生，无法书写病历')
    } else if (err.code === ErrorCode.Forbidden) {
      ElMessage.error('当前角色无权书写病历')
    } else {
      ElMessage.error(err.displayMessage)
    }
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <div>
    <PageHeader
      :title="existing ? '病历详情（只读）' : '书写病历'"
      :description="existing ? '一次就诊只有一份病历，已提交的内容不可再次编辑' : '诊断必填，处方可留空'"
    >
      <template #actions>
        <el-button @click="router.back()">
          <AppIcon
            name="back"
            :size="15"
          />
          <span class="btn-text">返回</span>
        </el-button>
        <el-button @click="router.push({ name: 'preconsult-report', params: { id: String(appointmentId) } })">
          <AppIcon
            name="chat"
            :size="15"
          />
          <span class="btn-text">查看 AI 报告</span>
        </el-button>
      </template>
    </PageHeader>

    <div
      v-if="loading"
      class="mc-card"
    >
      <el-skeleton
        :rows="8"
        animated
      />
    </div>

    <!-- 只读模式 -->
    <template v-else-if="existing">
      <div class="mc-card">
        <div class="ro-head">
          <div>
            <div class="ro-patient">
              {{ existing.patient_name }}
            </div>
            <div class="mc-muted">
              {{ existing.department_name }} · {{ existing.doctor_name }} ·
              {{ existing.sched_date }} · 书写于 {{ formatDateTime(existing.created_at) }}
            </div>
          </div>
          <span class="mc-tag mc-tag--green">已归档</span>
        </div>

        <section class="block">
          <h3>主诉</h3>
          <p>{{ existing.chief_complaint || '—' }}</p>
        </section>
        <section class="block">
          <h3>现病史</h3>
          <p>{{ existing.present_illness || '—' }}</p>
        </section>
        <section class="block block--highlight">
          <h3>诊断</h3>
          <p class="strong">
            {{ existing.diagnosis }}
          </p>
        </section>
        <section class="block">
          <h3>医嘱</h3>
          <p>{{ existing.treatment_advice || '—' }}</p>
        </section>
        <section class="block">
          <h3>处方（{{ existing.prescriptions?.length ?? 0 }} 项）</h3>
          <div
            v-if="existing.prescriptions?.length"
            class="rx-list"
          >
            <div
              v-for="(rx, index) in existing.prescriptions"
              :key="index"
              class="rx"
            >
              <div class="rx-line">
                <span class="rx-name">{{ rx.drug_name }}</span>
                <span class="rx-qty">× {{ rx.quantity }}</span>
              </div>
              <div class="rx-line mc-muted">
                <span v-if="rx.spec">{{ rx.spec }}</span>
                <span v-if="rx.usage_desc">{{ rx.usage_desc }}</span>
                <span v-if="rx.days">{{ rx.days }} 天</span>
              </div>
            </div>
          </div>
          <p
            v-else
            class="mc-muted"
          >
            本次未开具处方
          </p>
        </section>
      </div>
    </template>

    <!-- 书写模式 -->
    <el-form
      v-else
      ref="formRef"
      :model="form"
      :rules="rules"
      label-position="top"
      class="mc-card"
    >
      <el-form-item
        label="主诉"
        prop="chief_complaint"
      >
        <el-input
          v-model="form.chief_complaint"
          type="textarea"
          :rows="2"
          maxlength="512"
          show-word-limit
          placeholder="患者自述的主要症状与持续时间"
        />
      </el-form-item>

      <el-form-item
        label="现病史"
        prop="present_illness"
      >
        <el-input
          v-model="form.present_illness"
          type="textarea"
          :rows="4"
          maxlength="2000"
          show-word-limit
          placeholder="起病经过、伴随症状、诊疗经过等"
        />
      </el-form-item>

      <el-form-item
        label="诊断"
        prop="diagnosis"
      >
        <el-input
          v-model="form.diagnosis"
          type="textarea"
          :rows="2"
          maxlength="512"
          show-word-limit
          placeholder="必填，2–512 字"
        />
      </el-form-item>

      <el-form-item
        label="医嘱"
        prop="treatment_advice"
      >
        <el-input
          v-model="form.treatment_advice"
          type="textarea"
          :rows="3"
          maxlength="2000"
          show-word-limit
          placeholder="生活建议、复诊安排等"
        />
      </el-form-item>

      <div class="rx-head">
        <span class="rx-title">处方明细（{{ form.prescriptions?.length ?? 0 }}/20）</span>
        <el-button
          plain
          @click="addPrescription"
        >
          <AppIcon
            name="plus"
            :size="15"
          />
          <span class="btn-text">添加药品</span>
        </el-button>
      </div>

      <EmptyState
        v-if="!form.prescriptions?.length"
        icon="document"
        title="尚未添加处方"
        description="本次就诊不需要开药时可留空"
      />

      <div
        v-else
        class="rx-edit-list"
      >
        <div
          v-for="(item, index) in form.prescriptions"
          :key="index"
          class="rx-edit"
        >
          <div class="rx-edit-head">
            <span class="rx-index">#{{ index + 1 }}</span>
            <el-button
              link
              type="danger"
              @click="removePrescription(index)"
            >
              移除
            </el-button>
          </div>

          <div class="rx-grid">
            <div class="field field--wide">
              <label>药品名称 *</label>
              <el-input
                v-model="item.drug_name"
                maxlength="128"
                placeholder="如：阿莫西林胶囊"
              />
            </div>
            <div class="field">
              <label>规格</label>
              <el-input
                v-model="item.spec"
                maxlength="64"
                placeholder="如：0.25g×24"
              />
            </div>
            <div class="field">
              <label>数量 *</label>
              <el-input-number
                v-model="item.quantity"
                :min="1"
                :max="999"
                controls-position="right"
              />
            </div>
            <div class="field field--wide">
              <label>用法用量</label>
              <el-input
                v-model="item.usage_desc"
                maxlength="255"
                placeholder="如：口服，一次 2 粒，一日 3 次"
              />
            </div>
            <div class="field">
              <label>用药天数</label>
              <el-input-number
                v-model="item.days"
                :min="1"
                :max="365"
                controls-position="right"
                placeholder="可留空"
              />
            </div>
          </div>
        </div>
      </div>

      <div class="submit-bar">
        <el-button @click="router.back()">
          取消
        </el-button>
        <el-button
          type="primary"
          :loading="submitting"
          @click="submit"
        >
          保存病历
        </el-button>
      </div>
    </el-form>
  </div>
</template>

<style scoped>
/* ---------------- 只读 ---------------- */

.ro-head {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding-bottom: 14px;
  margin-bottom: 16px;
  border-bottom: 1px dashed var(--mc-border);
}

@media (min-width: 640px) {
  .ro-head {
    flex-direction: row;
    align-items: center;
    justify-content: space-between;
  }
}

.ro-patient {
  font-size: 17px;
  font-weight: 600;
  margin-bottom: 2px;
}

.block {
  margin-bottom: 18px;
}

.block h3 {
  font-size: 13px;
  color: var(--mc-text-muted);
  font-weight: 600;
  margin-bottom: 6px;
}

.block p {
  font-size: 14px;
  line-height: 1.75;
  white-space: pre-wrap;
  word-break: break-word;
}

.block--highlight {
  background: #f0fdfa;
  border-left: 3px solid var(--mc-primary);
  border-radius: 0 var(--mc-radius) var(--mc-radius) 0;
  padding: 10px 14px;
}

.strong {
  font-weight: 600;
  font-size: 15px;
}

.rx-list {
  display: flex;
  flex-direction: column;
  gap: 8px;
}

.rx {
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius);
  padding: 10px 12px;
}

.rx-line {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 12px;
  font-size: 13px;
}

.rx-name {
  font-weight: 600;
}

.rx-qty {
  color: var(--mc-primary-dark);
  font-weight: 600;
}

/* ---------------- 编辑 ---------------- */

.rx-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin: 6px 0 12px;
  padding-top: 14px;
  border-top: 1px dashed var(--mc-border);
}

.rx-title {
  font-size: 14px;
  font-weight: 600;
}

.rx-edit-list {
  display: flex;
  flex-direction: column;
  gap: 12px;
}

.rx-edit {
  border: 1px solid var(--mc-border);
  border-radius: var(--mc-radius);
  padding: 12px;
  background: var(--mc-surface-alt);
}

.rx-edit-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
}

.rx-index {
  font-size: 12px;
  font-weight: 600;
  color: var(--mc-text-muted);
}

.rx-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}

@media (min-width: 768px) {
  .rx-grid {
    grid-template-columns: repeat(4, minmax(0, 1fr));
  }
}

.field {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.field--wide {
  grid-column: span 2;
}

.field label {
  font-size: 12px;
  color: var(--mc-text-muted);
}

.field :deep(.el-input-number) {
  width: 100%;
}

.submit-bar {
  display: flex;
  justify-content: flex-end;
  gap: 10px;
  margin-top: 22px;
  padding-top: 16px;
  border-top: 1px solid var(--mc-border);
}

.btn-text {
  margin-left: 4px;
}
</style>

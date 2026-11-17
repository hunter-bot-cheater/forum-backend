<template>
  <el-dialog
    :model-value="modelValue"
    title="举报"
    width="420px"
    :close-on-click-modal="false"
    @update:model-value="emit('update:modelValue', $event)"
    @closed="reset"
  >
    <!-- 说明放在选项上方：先让人知道「举报会被人看到、乱报也有后果」，
         再让他选理由。反过来写的话，很多人选完就直接点了。 -->
    <p class="rp__hint">举报会提交给管理员人工核实，恶意举报同样会被处理。</p>

    <el-radio-group v-model="reason" class="rp__reasons">
      <el-radio v-for="item in REPORT_REASONS" :key="item.value" :value="item.value">
        {{ item.label }}
      </el-radio>
    </el-radio-group>

    <el-input
      v-model="detail"
      type="textarea"
      :autosize="{ minRows: 3 }"
      maxlength="200"
      show-word-limit
      resize="none"
      placeholder="补充说明（选填）"
    />

    <template #footer>
      <el-button @click="emit('update:modelValue', false)">取消</el-button>
      <el-button type="primary" :disabled="!reason" :loading="submitting" @click="submit">
        提交举报
      </el-button>
    </template>
  </el-dialog>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { ElMessage } from 'element-plus'
import { REPORT_REASONS, submitReport, type ReportReason, type ReportTarget } from '@/api/report'

// 一个弹窗同时服务文章与评论：两处的表单内容完全一样，
// 分成两个组件只会让分类列表维护两遍。
// 打开时由父组件传 targetType / targetId，弹窗自己不关心举报的是什么东西。
const props = defineProps<{
  modelValue: boolean
  targetType: ReportTarget
  targetId: number
}>()

const emit = defineEmits<{
  'update:modelValue': [boolean]
}>()

// 空串表示还没选。按钮靠它禁用，不用额外一个布尔值。
const reason = ref<ReportReason | ''>('')
const detail = ref('')
const submitting = ref(false)

// reset 在弹窗关闭动画结束时清空表单。
// 不清的话，下次举报别的内容会看到上一次选的理由——
// 手快的人会直接点提交，把上一条的理由带过去。
function reset() {
  reason.value = ''
  detail.value = ''
}

// submit 提交举报。成功后关弹窗。
//
// 失败（网络错误、409 重复举报、被封禁）不在这里额外提示：
// 拦截器已经按后端返回的 message 弹过一次，再弹一次会叠两条。
async function submit() {
  if (!reason.value) {
    return
  }
  submitting.value = true
  try {
    await submitReport(props.targetType, props.targetId, reason.value, detail.value)
    ElMessage.success('举报已提交，我们会尽快核实')
    emit('update:modelValue', false)
  } catch {
    // 失败提示已经由请求拦截器按后端 message 弹过（409 重复举报、403 被封禁、
    // 网络错误），这里只需要把拒绝吞掉再让 finally 复位按钮。
    // 少了这个 catch，async 事件处理器会把异常抛成未捕获的 Promise 拒绝。
  } finally {
    // 放在 finally 而不是只写在成功分支：失败后按钮必须恢复可点，
    // 否则用户改完理由想再试一次，按钮还是转圈状态。
    submitting.value = false
  }
}
</script>

<style scoped>
.rp__hint {
  margin: 0 0 12px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--fs-text-3);
}

/* 五个选项竖着排：横排到第四个就会换行，形成一个歪掉的 2×2 加一个孤零零的项。 */
.rp__reasons {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  gap: 2px;
  margin-bottom: 12px;
}
</style>

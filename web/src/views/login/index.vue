<template>
  <AuthCard title="欢迎回来" subtitle="登录后可以参与讨论、关注作者、收发私信。">
    <el-form
      ref="formRef"
      :model="form"
      :rules="rules"
      size="large"
      @keyup.enter="onSubmit"
    >
      <el-form-item prop="username">
        <el-input v-model="form.username" placeholder="用户名" :prefix-icon="User" />
      </el-form-item>
      <el-form-item prop="password">
        <el-input
          v-model="form.password"
          type="password"
          show-password
          placeholder="密码"
          :prefix-icon="Lock"
        />
      </el-form-item>
      <el-button type="primary" :loading="loading" @click="onSubmit">登录</el-button>
    </el-form>

    <template #foot>
      还没有账号？<router-link to="/register">立即注册</router-link>
    </template>
  </AuthCard>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { Lock, User } from '@element-plus/icons-vue'
import AuthCard from '@/components/AuthCard.vue'
import { useUserStore } from '@/store/modules/user'
import { safeRedirect } from '@/utils/redirect'

const route = useRoute()
const router = useRouter()
const userStore = useUserStore()
const formRef = ref<FormInstance>()
const loading = ref(false)

const form = reactive({ username: '', password: '' })
const rules: FormRules = {
  username: [{ required: true, message: '请输入用户名', trigger: 'blur' }],
  password: [{ required: true, message: '请输入密码', trigger: 'blur' }],
}

async function onSubmit() {
  if (!formRef.value) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return
  loading.value = true
  try {
    await userStore.login({ ...form })
    ElMessage.success('登录成功')
    // 回到守卫记下的来处。没有或不合规时 safeRedirect 会给回文章列表。
    router.replace(safeRedirect(route.query.redirect))
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="auth-container">
    <el-card class="auth-card">
      <h2 style="text-align: center; margin-top: 0">注册</h2>
      <el-form
        ref="formRef"
        :model="form"
        :rules="rules"
        label-width="80px"
        @keyup.enter="onSubmit"
      >
        <el-form-item label="用户名" prop="username">
          <el-input v-model="form.username" placeholder="3-32 位字母数字下划线" />
        </el-form-item>
        <el-form-item label="密码" prop="password">
          <el-input
            v-model="form.password"
            type="password"
            show-password
            placeholder="8-72 位"
          />
        </el-form-item>
        <el-form-item>
          <el-button type="primary" :loading="loading" @click="onSubmit">注册并登录</el-button>
          <el-button @click="goLogin">去登录</el-button>
        </el-form-item>
      </el-form>
    </el-card>
  </div>
</template>

<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage, type FormInstance, type FormRules } from 'element-plus'
import { useUserStore } from '@/store/modules/user'

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
    // 注册接口不自动登录，store.register 内部会再调一次登录拿 token。
    await userStore.register({ ...form })
    ElMessage.success('注册成功')
    router.replace('/articles')
  } finally {
    loading.value = false
  }
}

function goLogin() {
  router.push('/login')
}
</script>

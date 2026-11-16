<template>
  <div class="auth">
    <div class="auth__panel">
      <div class="auth__brand">
        <span class="auth__logo">论</span>
        <span class="auth__name">论坛</span>
      </div>

      <h1 class="auth__title">{{ title }}</h1>
      <p class="auth__sub">{{ subtitle }}</p>

      <div class="auth__body">
        <slot />
      </div>

      <div class="auth__foot">
        <slot name="foot" />
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
// 登录与注册共用的外框：品牌标记、标题、副标题、页脚。
// 两页的骨架本来一模一样，各写一份的结果是改了一边忘了另一边，
// 最后只有其中一个页面有新品牌标记。
defineProps<{
  title: string
  subtitle: string
}>()
</script>

<style scoped>
/* 背景用一块很淡的主色光晕，从页面顶部往下化开：
   纯灰底会让登录页看起来像后台管理系统，加一层同色系的渐变就有了品牌感，
   又不会抢走表单的注意力。 */
.auth {
  display: grid;
  place-items: center;
  min-height: 100vh;
  padding: 24px;
  background:
    radial-gradient(900px 420px at 50% -10%, #e6f0ff 0%, rgba(230, 240, 255, 0) 70%),
    var(--fs-bg);
}

.auth__panel {
  width: 100%;
  max-width: 400px;
  padding: 34px 32px 26px;
  background: var(--fs-surface);
  border-radius: 16px;
  box-shadow: var(--fs-shadow);
}

.auth__brand {
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 22px;
}

.auth__logo {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  border-radius: 9px;
  background: var(--fs-primary);
  color: #fff;
  font-size: 16px;
  font-weight: 700;
}

.auth__name {
  font-size: 16px;
  font-weight: 700;
  letter-spacing: 1px;
  color: var(--fs-text-1);
}

.auth__title {
  font-size: 22px;
  font-weight: 700;
  color: var(--fs-text-1);
}

.auth__sub {
  margin-top: 6px;
  font-size: 13px;
  line-height: 1.6;
  color: var(--fs-text-3);
}

.auth__body {
  margin-top: 22px;
}

/* 表单不写 label，全靠 placeholder：登录表单只有两个字段，
   左侧留一列「用户名/密码」的标签反而把输入框挤窄。 */
.auth__body :deep(.el-form-item) {
  margin-bottom: 16px;
}

/* 主按钮占满整行：这一页只有一个动作，宽度撑满比小按钮更好点。 */
.auth__body :deep(.el-button) {
  width: 100%;
}

.auth__foot {
  margin-top: 18px;
  font-size: 13px;
  text-align: center;
  color: var(--fs-text-3);
}

.auth__foot :deep(a) {
  color: var(--fs-primary);
  font-weight: 500;
}

.auth__foot :deep(a:hover) {
  color: var(--fs-primary-hover);
}
</style>

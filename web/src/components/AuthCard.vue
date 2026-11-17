<template>
  <div class="auth">
    <div class="auth__panel">
      <!-- 左栏只在大屏出现：它是「这个站是什么」的介绍，不是表单的一部分。
           窄屏直接隐藏，把宽度全留给输入框——手机上没人愿意先划过一个广告位。 -->
      <aside class="auth__aside">
        <div class="auth__brand">
          <span class="auth__logo">论</span>
          <span class="auth__name">论坛</span>
        </div>

        <div class="auth__pitch">
          <h2 class="auth__slogan">把问题聊透，<br />把经验留下。</h2>
          <ul class="auth__points">
            <li v-for="point in points" :key="point" class="auth__point">
              {{ point }}
            </li>
          </ul>
        </div>

        <p class="auth__note">一个安静的技术讨论区，内容公开可读。</p>
      </aside>

      <main class="auth__main">
        <!-- 窄屏下左栏隐藏，品牌标记在这里补一份，否则整页看不到站名。 -->
        <div class="auth__brand auth__brand--compact">
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
      </main>
    </div>
  </div>
</template>

<script setup lang="ts">
// 登录与注册共用的外框：左栏品牌介绍、右栏标题与表单。
// 两页的骨架本来一模一样，各写一份的结果是改了一边忘了另一边，
// 最后只有其中一个页面有新品牌标记。
//
// points 是左栏的三条卖点。给默认值而不是必填：
// 两个页面说的是同一件事——这个站有什么可看的——
// 各传一份只会让两边文案慢慢分叉。
withDefaults(
  defineProps<{
    title: string
    subtitle: string
    points?: string[]
  }>(),
  {
    points: () => [
      '公开讨论，帖子所有人可见',
      '关注作者、私信交流',
      '点赞与热榜，好内容浮上来',
    ],
  },
)
</script>

<style scoped>
/* 背景用一块很淡的主色光晕，从页面顶部往下化开：
   纯灰底会让登录页看起来像后台管理系统，加一层同色系的渐变就有了品牌感，
   又不会抢走表单的注意力。
   右下角再补一小块更淡的，是为了让整块背景不是「上蓝下灰」的简单分层。 */
.auth {
  /* 校验失败用的红比 Element 默认的 #f56c6c 深一档：
     12px 的浅红细字压在浅灰底上，白底上几乎糊在一起，
     用户看不出「哪里填错了」。只在这一页覆盖，不影响站内其它表单。 */
  --el-color-danger: #e0453f;

  display: grid;
  place-items: center;
  min-height: 100vh;
  /* 与外壳同一个理由：iOS Safari 的 100vh 把底部工具栏也算进去，
     登录卡会被顶出屏幕一点，出现一条没有意义的滚动。 */
  min-height: 100dvh;
  padding: 32px 24px;
  background:
    radial-gradient(820px 380px at 50% -8%, #dfeaff 0%, rgba(223, 234, 255, 0) 72%),
    radial-gradient(520px 320px at 96% 106%, #edf3ff 0%, rgba(237, 243, 255, 0) 70%),
    var(--fs-bg);
}

/* 一张卡片左右两栏，而不是两张卡片并排：
   两栏之间不该有缝，表单和介绍本来就是同一个入口的两面。 */
.auth__panel {
  display: grid;
  grid-template-columns: 320px 1fr;
  width: 100%;
  max-width: 820px;
  /* 给一个下限高度：不加时卡片高度完全由内容决定，
     两个输入框只撑出 388px，宽屏上像一条扁扁的横条。 */
  min-height: 462px;
  overflow: hidden;
  background: var(--fs-surface);
  border-radius: 20px;
  /* 两层阴影：贴边那层描出卡片边缘，远的那层给悬浮感。
     只用一层时，要么边缘发虚，要么像贴纸浮在页面外面。 */
  box-shadow:
    0 1px 2px rgba(29, 33, 41, 0.04),
    0 18px 50px rgba(29, 33, 41, 0.09);
  animation: auth-in 0.28s ease-out both;
}

/* 进场从下方 8px 浮上来。幅度小、时间短：登录页是高频入口，
   动效一长就变成「每次登录都要等」。 */
@keyframes auth-in {
  from {
    opacity: 0;
    transform: translateY(8px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

@media (prefers-reduced-motion: reduce) {
  .auth__panel {
    animation: none;
  }
}

/* 左栏是整张卡唯一的深色块：颜色统一取主色渐变，
   不给登录页另配一套插画或照片——那需要维护素材，也和站内风格不搭。 */
.auth__aside {
  position: relative;
  display: flex;
  flex-direction: column;
  padding: 40px 32px 32px;
  overflow: hidden;
  background: linear-gradient(150deg, #4a95ff 0%, var(--fs-primary) 62%, #1668db 100%);
  color: #fff;
}

/* 两个半透明圆做底纹。放在 aside 里并 overflow:hidden，
   不会漏到右侧白卡上。 */
.auth__aside::before,
.auth__aside::after {
  position: absolute;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.12);
  content: '';
}

.auth__aside::before {
  top: -70px;
  right: -60px;
  width: 220px;
  height: 220px;
}

.auth__aside::after {
  bottom: -90px;
  left: -70px;
  width: 260px;
  height: 260px;
  background: rgba(255, 255, 255, 0.08);
}

.auth__brand {
  position: relative;
  display: flex;
  align-items: center;
  gap: 10px;
}

/* 左栏里的标志用半透明白底 + 白字：
   在蓝色块上再铺一块纯白，会把视线从右边的表单上抢走。 */
.auth__logo {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 34px;
  border-radius: 10px;
  background: rgba(255, 255, 255, 0.18);
  color: #fff;
  font-size: 17px;
  font-weight: 700;
}

.auth__name {
  font-size: 17px;
  font-weight: 700;
  letter-spacing: 1px;
}

.auth__pitch {
  position: relative;
  margin-top: auto;
  margin-bottom: auto;
  padding: 28px 0;
}

.auth__slogan {
  font-size: 24px;
  font-weight: 700;
  line-height: 1.5;
  letter-spacing: 0.5px;
}

.auth__points {
  margin-top: 22px;
  padding: 0;
  list-style: none;
}

/* 每条卖点前用一个小圆点：这三行是并列关系，
   用圆点比用数字或箭头更像「清单」而不是「步骤」。 */
.auth__point {
  position: relative;
  padding-left: 16px;
  font-size: 13.5px;
  line-height: 1.9;
  color: rgba(255, 255, 255, 0.88);
}

.auth__point::before {
  position: absolute;
  top: 10px;
  left: 0;
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: rgba(255, 255, 255, 0.6);
  content: '';
}

.auth__note {
  position: relative;
  font-size: 12px;
  line-height: 1.6;
  color: rgba(255, 255, 255, 0.62);
}

.auth__main {
  display: flex;
  flex-direction: column;
  /* 内容整体垂直居中：两个字段的表单在 462px 高的卡片里贴顶排会显得上重下轻，
     底部只留一大块空白。 */
  justify-content: center;
  padding: 44px 44px 34px;
}

/* 右栏的品牌行只在窄屏出现，宽屏由左栏负责。 */
.auth__brand--compact {
  display: none;
  margin-bottom: 22px;
}

.auth__brand--compact .auth__logo {
  background: linear-gradient(135deg, #3d8dff 0%, var(--fs-primary) 100%);
  box-shadow: 0 4px 10px rgba(28, 125, 255, 0.24);
}

.auth__brand--compact .auth__name {
  color: var(--fs-text-1);
}

.auth__title {
  font-size: 26px;
  font-weight: 700;
  letter-spacing: 0.5px;
  color: var(--fs-text-1);
}

.auth__sub {
  margin-top: 8px;
  font-size: 13.5px;
  line-height: 1.7;
  color: var(--fs-text-3);
}

.auth__body {
  margin-top: 26px;
}

/* 输入框与按钮都取 Element 的 large 尺寸，这里统一抬到 44px：
   40px 在只有两个字段的页面里略显局促，44px 也是移动端推荐的最小触摸高度。 */
.auth__body :deep(.el-form) {
  --el-component-size-large: 44px;
}

/* 表单不写 label，全靠 placeholder：登录表单只有两个字段，
   左侧留一列「用户名/密码」的标签反而把输入框挤窄。 */
.auth__body :deep(.el-form-item) {
  margin-bottom: 18px;
}

.auth__body :deep(.el-input__wrapper) {
  padding: 1px 14px;
  border-radius: 10px;
}

/* 错误文案默认是 absolute + top:100%，浮在输入框外面：
   和下一个字段的 margin 叠在一起，红字正好压在下个输入框的上边框上。
   改成静态流式后它会把下面的内容推开，多高就占多高，不会再重叠。
   代价是出错时卡片会变高、下面的元素下移十几像素，比重叠可读得多。 */
.auth__body :deep(.el-form-item__error) {
  position: static;
  padding-top: 8px;
  padding-left: 2px;
  font-size: 12.5px;
  font-weight: 500;
  line-height: 1.4;
}

/* 主按钮占满整行：这一页只有一个动作，宽度撑满比小按钮更好点。 */
.auth__body :deep(.el-button) {
  width: 100%;
  border-radius: 10px;
  font-size: 15px;
  font-weight: 600;
  letter-spacing: 1px;
  box-shadow: 0 6px 16px rgba(28, 125, 255, 0.22);
  transition: background-color 0.15s, box-shadow 0.15s, transform 0.15s;
}

/* hover 抬起 1px：这一页只有一个按钮，给它一点「按下去」的物理感，
   比单纯换色更容易让人确认点到了。 */
.auth__body :deep(.el-button:hover) {
  box-shadow: 0 8px 20px rgba(28, 125, 255, 0.28);
  transform: translateY(-1px);
}

.auth__foot {
  margin-top: 26px;
  padding-top: 20px;
  border-top: 1px solid var(--fs-line-soft);
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

/* 窄屏（含平板竖屏）：收成一栏，左栏整个隐藏。
   820px 的卡片在 768 宽下会把介绍挤成两字一行，不如直接不要。 */
@media (max-width: 860px) {
  .auth__panel {
    grid-template-columns: 1fr;
    max-width: 420px;
    /* 窄屏只有一栏，高度交给内容决定；留着 462px 会把空态拉出一块空白。 */
    min-height: 0;
  }

  .auth__aside {
    display: none;
  }

  .auth__brand--compact {
    display: flex;
  }

  .auth__main {
    padding: 32px 28px 26px;
  }
}

@media (max-width: 480px) {
  .auth {
    padding: 24px 20px;
  }

  .auth__panel {
    border-radius: 16px;
  }

  .auth__main {
    padding: 26px 22px 22px;
  }

  .auth__title {
    font-size: 23px;
  }
}
</style>

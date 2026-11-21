<template>
  <div class="pm">
    <!-- 顶部只留一条窄栏。对局页不套站点 Layout：60px 顶栏加内容区留白，
         在竖屏手机上会吃掉球台将近六分之一的高度。 -->
    <header class="pm__bar">
      <button class="pm__back" type="button" aria-label="返回" @click="leave">
        <el-icon :size="18"><ArrowLeft /></el-icon>
      </button>
      <span class="pm__title">对局 #{{ id }}</span>
      <span class="pm__state">等待接入</span>
    </header>

    <main class="pm__stage">
      <PoolTable :balls="balls" />
    </main>
  </div>
</template>

<script setup lang="ts">
// 对局页。M1 阶段只有静态球台：球位来自八球开球摆球，不连服务端。
// 后续接入顺序是 M2 加瞄准输入、M5 换成快照里的真实球位。
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { ArrowLeft } from '@element-plus/icons-vue'
import PoolTable from '@/components/pool/PoolTable.vue'
import { RACK_EIGHTBALL } from '@/game/table'
import type { BallState } from '@/game/types'
import { goBack } from '@/utils/nav'

// 对局 id 由路由 props 注入，模板直接用 `id`，所以不必接一个变量。
// 后续接入服务端时这个名字会用来拉快照与建 WS。
defineProps<{
  id: string
}>()

const router = useRouter()

// 开球摆球转成渲染用的球状态。z 为 0 表示全部贴在台面上。
const balls = computed<BallState[]>(() => {
  const list: BallState[] = []
  for (const ball of RACK_EIGHTBALL) {
    list.push({ id: ball.id, x: ball.x, y: ball.y, z: 0, on_table: true })
  }
  return list
})

// 对局页可能从大厅、房间页或直接输入地址进来，返回优先回大厅。
// 收进 goBack 是为了不在历史里越压越多，见 utils/nav.ts。
function leave() {
  goBack(router, '/articles')
}
</script>

<style scoped>
/* 100dvh 而不是 100vh：移动端浏览器地址栏收起时 vh 不会变，
   用 vh 会让底部一截被工具栏压住，球台贴着屏幕下沿就点不到。 */
.pm {
  display: flex;
  flex-direction: column;
  height: 100dvh;
  background: #0e1116;
}

.pm__bar {
  display: flex;
  flex: none;
  align-items: center;
  gap: 10px;
  height: 44px;
  padding: 0 10px;
  color: #e8eaed;
}

.pm__back {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 30px;
  height: 30px;
  padding: 0;
  color: inherit;
  background: rgba(255, 255, 255, 0.08);
  border: none;
  border-radius: 8px;
  cursor: pointer;
}

.pm__back:active {
  background: rgba(255, 255, 255, 0.16);
}

.pm__title {
  font-size: 13px;
  font-weight: 600;
}

.pm__state {
  margin-left: auto;
  font-size: 12px;
  color: #8a919c;
}

/* 剩余空间全给球台。min-height: 0 是 flex 子项能收缩的前提，
   少了它 canvas 会按内容高度撑开，把窄栏顶出屏幕。 */
.pm__stage {
  flex: 1;
  min-height: 0;
  padding: 0 8px 8px;
}
</style>

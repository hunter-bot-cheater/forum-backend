<template>
  <div class="sc" :class="{ 'is-active': active, 'is-me': isMe, 'is-empty': !player.nickname }">
    <el-avatar
      :size="34"
      :src="player.avatar || undefined"
      :style="{ backgroundColor: avatarColor(player.nickname) }"
    >
      {{ initial }}
    </el-avatar>

    <div class="sc__body">
      <div class="sc__line">
        <span class="sc__name">{{ player.nickname || '等待中' }}</span>
        <span v-if="isMe" class="sc__me">你</span>
        <!-- 剩余颗数是这一栏最重要的数字：判断「再进两颗就到我打黑八」
             靠的就是它，比一排小球点更快读。 -->
        <span v-if="assigned" class="sc__count">{{ remaining.length }}</span>
      </div>

      <!-- 球点用真实球色，花色球画成白底加一圈色环：
           光看数字要在 1~15 里找，带颜色一眼就能分出全色与花色。 -->
      <div v-if="!assigned" class="sc__group">{{ groupLabel(group) }}</div>
      <div v-else class="sc__dots">
        <span
          v-for="id in remaining"
          :key="id"
          class="sc__dot"
          :class="{ 'is-striped': isStriped(id) }"
          :style="dotStyle(id)"
        >{{ id }}</span>
        <!-- 自己组清完了但还没打完：显示一颗黑八，提示「该打黑八了」。
             这时候 remaining 是空数组，不给提示会让人以为界面出错了。 -->
        <span v-if="remaining.length === 0" class="sc__finish">打黑八</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
// 双方条上的一张座位卡：头像、昵称、剩余球。
//
// 只做展示，不读 store 也不发请求：对局页拿一份快照，两边各传一次。
// 这样「谁该打」只有对局页一个判断源，卡片不会自己算出第二套结论。
import { computed } from 'vue'
import { ballLookOf } from '@/game/balls'
import { groupLabel, hasGroup } from '@/game/score'
import type { BallGroup, PlayerBrief } from '@/game/types'
import { avatarColor } from '@/utils/avatar'
import { initials } from '@/utils/format'

const props = defineProps<{
  player: PlayerBrief
  // group 是这一侧的组别，空串表示还没定组（后端用 Go 零值当哨兵，不是 'none'）。
  group: BallGroup
  // remaining 是自己组里还在台面上的球号。
  remaining: string[]
  // active 表示当前轮到他打。
  active: boolean
  isMe: boolean
}>()

// assigned 表示这一侧已经定组。
// 「清完自己组」与「还没定组」的 remaining 都是空数组，
// 靠组别区分：前者该提示打黑八，后者什么都不该显示。
const assigned = computed(() => hasGroup(props.group))

const initial = computed(() => {
  if (!props.player.nickname) {
    return ''
  }
  return initials(props.player.nickname)
})

// isStriped 判断花色球，用来决定球点画实心还是描边。
function isStriped(id: string): boolean {
  return ballLookOf(id).striped
}

// dotStyle 给球点上色。
//
// 花色球用白底 + 色字 + 色环，全色球用色底 + 白字：
// 这是真实台球的样子，也是玩家扫一眼就能分组的依据。
function dotStyle(id: string): Record<string, string> {
  const look = ballLookOf(id)
  if (look.striped) {
    return { color: look.color, borderColor: look.color, background: '#fff' }
  }
  return { color: '#fff', background: look.color, borderColor: look.color }
}
</script>

<style scoped>
.sc {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
  padding: 6px 8px;
  border-radius: 10px;
  transition: background-color 0.2s;
}

/* 轮到他时给一层底色而不是边框：球台就在下面，
   画边框会让整条状态栏看起来像被框住了一块。 */
.sc.is-active {
  background: rgba(255, 255, 255, 0.09);
}

.sc.is-empty {
  opacity: 0.55;
}

.sc__body {
  display: flex;
  flex-direction: column;
  gap: 3px;
  min-width: 0;
}

.sc__line {
  display: flex;
  align-items: baseline;
  gap: 4px;
  min-width: 0;
}

.sc__name {
  max-width: 96px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 12px;
  color: #e8eaed;
}

.sc__me {
  font-size: 10px;
  color: #8a919c;
}

.sc__count {
  font-size: 12px;
  font-weight: 700;
  color: #f0c02f;
}

.sc__group {
  font-size: 10px;
  color: #6f7681;
}

.sc__dots {
  display: flex;
  align-items: center;
  gap: 2px;
}

/* 球点压到 13px：一排 7 颗要放进半屏宽的一条状态栏里，
   再大就会把昵称挤成省略号。 */
.sc__dot {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 13px;
  height: 13px;
  font-size: 8px;
  font-weight: 700;
  line-height: 1;
  border: 1px solid transparent;
  border-radius: 50%;
}

.sc__finish {
  font-size: 10px;
  font-weight: 600;
  color: #f0c02f;
}
</style>

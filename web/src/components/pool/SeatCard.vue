<template>
  <div class="sc" :class="{ 'is-active': active, 'is-me': isMe, 'is-empty': !player.nickname }">
    <!-- 轮到他时给头像套一圈金环。整张卡只是底色亮一档，眼睛不会往那儿落；
         一圈亮边能让「现在是谁在打」在余光里就定下来。 -->
    <span class="sc__avatar" :class="{ 'is-active': active }">
      <el-avatar
        :size="38"
        :src="player.avatar || undefined"
        :style="{ backgroundColor: avatarColor(player.nickname) }"
      >
        {{ initial }}
      </el-avatar>
    </span>

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

      <!-- 「继续击打」是进球之后的连击状态：回合没换人，但玩家容易以为是
           对手在打（球台上杆子还在、读秒还归自己）。放在卡片里一直挂着，
           直到这一杆出杆才消失，比一条三秒就滑走的横幅可靠。 -->
      <div v-if="continuing" class="sc__cont">继续击打</div>
    </div>
  </div>
</template>

<script setup lang="ts">
// 一张座位卡：头像、昵称、剩余球。对局页把它放在顶栏左上与右上两个角，
// 观战页同样两侧各一张。
//
// 只做展示，不读 store 也不发请求：页面拿一份快照，两边各传一次。
// 这样「谁该打」只有页面一个判断源，卡片不会自己算出第二套结论。
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
  // continuing 表示这一手是「上一杆进球之后接着打」，不是换人。
  // 观战页不传：观战者看到的是轮次，谁连打由画面自己说明。
  continuing?: boolean
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
  padding: 6px 10px 6px 8px;
  border-radius: 12px;
  transition: background-color 0.2s;
}

/* 轮到他时给一层底色而不是边框：球台就在下面，
   画边框会让整条状态栏看起来像被框住了一块。 */
.sc.is-active {
  background: rgba(255, 255, 255, 0.1);
}

.sc.is-empty {
  opacity: 0.55;
}

/* 头像外面那圈金环。
   用「底色 + 内边距」而不是 border：border 会让元素尺寸变化，
   轮到谁时整张卡会跟着抖一下。padding 是固定的，换的只是底色与光晕。 */
.sc__avatar {
  display: inline-flex;
  flex: none;
  padding: 2px;
  border-radius: 50%;
  transition:
    background 0.25s,
    box-shadow 0.25s;
}

.sc__avatar.is-active {
  background: linear-gradient(135deg, #ffe08a, #f0a92f);
  box-shadow:
    0 0 0 1px rgba(255, 214, 120, 0.45),
    0 0 14px rgba(240, 169, 47, 0.5);
}

.sc__body {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
}

.sc__line {
  display: flex;
  align-items: baseline;
  gap: 4px;
  min-width: 0;
}

.sc__name {
  max-width: 110px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13px;
  color: #e8eaed;
}

.sc__me {
  font-size: 11px;
  color: #8a919c;
}

.sc__count {
  font-size: 13px;
  font-weight: 700;
  color: #f0c02f;
}

.sc__group {
  font-size: 11px;
  color: #79808b;
}

/* 球点允许折行：一组最多 7 颗，窄屏上一行放不下。
   挤成一行会溢出卡片、压到时钟上，折成两行反而更好认。 */
.sc__dots {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 3px;
}

.sc__dot {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  font-size: 9px;
  font-weight: 700;
  line-height: 1;
  border: 1px solid transparent;
  border-radius: 50%;
}

/* 手机上每张卡只有约 130px 宽：球点小一档，才装得下一整组。 */
@media (max-width: 520px) {
  .sc__dot {
    width: 14px;
    height: 14px;
    font-size: 8px;
  }
}

.sc__finish {
  font-size: 11px;
  font-weight: 600;
  color: #f0c02f;
}

/* 连击标记：黄底深字的小胶囊。用底色而不是只有一行字，
   是因为它在余光里要能被看见——玩家正盯着球台，不会去读卡片。 */
.sc__cont {
  align-self: flex-start;
  padding: 1px 7px;
  font-size: 11px;
  font-weight: 600;
  color: #2a2100;
  background: #f0c02f;
  border-radius: 999px;
}
</style>

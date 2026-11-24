<template>
  <!-- 外层这一格始终占着 46×46：收起态是小球，展开态是面板的锚点。
       它不能跟着一起消失，否则面板没有参照物，会掉到球台中间去。 -->
  <div class="sp">
    <!-- 收起态：一颗小球，红点就是击球点，左边挂当前塞的角度。
         故意做成「球」而不是图标：这是唯一一处需要玩家读「击球点在哪」的控件，
         形状本身就在解释怎么用——点它在球面上选位置。 -->
    <button
      v-if="!open"
      class="sp__toggle"
      type="button"
      :disabled="disabled && !readonly"
      :aria-label="readonly ? '对家的击球点' : '打开加塞，当前 ' + angleText"
      @click="toggle"
    >
      <span class="sp__badge">{{ angleText }}</span>
      <span class="sp__mini" :class="{ 'is-peer': readonly }">
        <span class="sp__mini-dot" :style="dotStyle"></span>
      </span>
    </button>

    <!-- 展开态：整块面板从这颗球往上长。尺寸与配色对着参考图做，
         球面上一横一竖两条虚线是「中杆」的基准，红点离开哪条线就是加了哪一项。 -->
    <div v-else ref="panelEl" class="sp__panel">
      <div class="sp__head">
        <button class="sp__reset" type="button" @click="reset">重置</button>
        <span class="sp__value">{{ valueText }}</span>
      </div>

      <div
        ref="stage"
        class="sp__stage"
        :class="{ 'is-peer': readonly }"
        @pointerdown="onPointerDown"
        @pointermove="onPointerMove"
        @pointerup="onPointerUp"
        @pointercancel="onPointerUp"
      >
        <span class="sp__ball"></span>
        <span class="sp__gloss"></span>
        <span class="sp__cross sp__cross--h"></span>
        <span class="sp__cross sp__cross--v"></span>
        <span class="sp__ring"></span>
        <span class="sp__dot" :style="dotStyle"></span>
      </div>

      <!-- 收起箭头挂在面板右缘，所以要放进面板里当绝对定位的子元素：
           挂到外层容器上的话，那个容器在展开态是 0 高度，箭头会跑到面板下面去。 -->
      <button class="sp__fold" type="button" aria-label="收起加塞面板" @click="toggle">
        <svg viewBox="0 0 16 16" width="16" height="16" aria-hidden="true">
          <path d="M6 3.5 L10.5 8 L6 12.5" fill="none" stroke="currentColor" stroke-width="1.8" />
        </svg>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
// 加塞面板：在球面上点一个位置，决定这一杆打的是球的哪一点。
//
// 两个方向的量分开报给页面：a 是左右（-1 左塞 … +1 右塞），b 是高低
// （-1 低杆 … +1 高杆）。页面把它们原样塞进出杆参数，物理见 predict.ts。
//
// 读数沿用参考图的写法：(x, y) 是百分点，一位小数——「(‑0.5,‑28.6)」
// 比「低杆 28.6%」更容易和球面上那个点的位置对上。
//
// 拖动半径内才有效：超出球面就沿半径方向夹到边上，不会因为手指滑出去而丢掉
// 已经调好的比例。中心留 8% 的死区，想打中杆时不用在像素级对准圆心。
//
// 点面板外面任何地方都算「关掉面板」，而且那一下会被吃掉（见 onDocPointerDown）：
// 玩家点外面是为了关面板，不是为了顺手重新瞄一杆。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'

// 中心死区（占半径的比例）。手指按在中心附近时吸附回 (0, 0)。
const DEAD_ZONE = 0.08

// 拖动时的最大偏移（占半径的比例）。留一点边：红点贴着球缘时看起来像掉出去了。
const MAX_OFFSET = 0.92

const props = defineProps<{
  // a 左右塞，b 高低杆，都在 -1 到 1 之间。
  a: number
  b: number
  // 不是自己的回合、或者正在摆球时不可调。
  disabled?: boolean
  // 只读镜像：显示的是对家的击球点，能看不能点，也不压暗。
  //
  // 与 disabled 分开是为了外观：自己回合之外这颗球被压暗是对的，
  // 但对家正在调塞时压暗一颗正在动的红点，看起来像对方掉线了。
  readonly?: boolean
}>()

const emit = defineEmits<{
  'update:a': [value: number]
  'update:b': [value: number]
  // open-change 把面板开合告诉页面。展开期间页面要把瞄准停住：
  // 玩家为了关面板会往台面上点，手从面板移到台面的路上会一路发悬停，
  // 不拦的话「关面板」就等于「重新瞄一次」，刚瞄好的杆就这么被带走了。
  'open-change': [open: boolean]
}>()

const open = ref(false)
const stage = ref<HTMLDivElement | null>(null)
const panelEl = ref<HTMLDivElement | null>(null)
let dragging = false

// valueText 是右上角那串坐标，按参考图的格式：括号、逗号、一位小数。
const valueText = computed(() => {
  const x = (props.a * 100).toFixed(1)
  const y = (props.b * 100).toFixed(1)
  return '(' + x + ',' + y + ')'
})

// angleText 是收起态那颗球上方的标签。
//
// 取的是「击球点相对圆心的方位」：正上方 0°，右边 90°，正下方 180°。
// 中杆（点正好在圆心）没有方位，固定报 0°——和参考图一致。
const angleText = computed(() => {
  if (props.a === 0 && props.b === 0) {
    return '0°'
  }
  const deg = (Math.atan2(props.a, props.b) * 180) / Math.PI
  return Math.round(deg) + '°'
})

// dotStyle 把 (a, b) 换成红点在球面上的位置。
//
// 球面的 y 轴朝上（正数是高杆），屏幕的 y 轴朝下，所以竖直方向要取负号。
// 位置用百分比，球的大小变了红点也跟着走，不用在 JS 里量像素。
const dotStyle = computed(() => {
  const x = 50 + props.a * 50 * MAX_OFFSET
  const y = 50 - props.b * 50 * MAX_OFFSET
  return { left: x + '%', top: y + '%' }
})

// setOpen 统一改开合状态并通知页面，别处不要直接改 open——
// 漏一次通知，页面那边就会以为面板还开着，瞄准一直不动，看起来像卡住了。
function setOpen(next: boolean) {
  if (open.value === next) {
    return
  }
  open.value = next
  emit('open-change', next)
}

// toggle 切换展开／收起。收起时不动已经设好的塞：打完一杆还会想接着用。
function toggle() {
  if ((props.disabled && !open.value) || (props.readonly && !open.value)) {
    return
  }
  setOpen(!open.value)
}

// onDocPointerDown 处理「点面板外面」：关掉面板，并且把这一下吞掉。
//
// 监听挂在 window 的捕获阶段，事件还没到达球台就被拦住，所以这一次按下
// 既不会让球台锁定／解锁方向，也不会把母球落到别处、更不会出杆。
// 这是「关面板不许动杆」唯一可靠的落点：如果只关面板、让事件继续往下走，
// 球台会把这次点按当成一次 tap，方向当场被切换，玩家看到的是杆自己跳了一下。
//
// 面板内部（含右缘那个收起箭头）直接放行，否则拖加塞红点也会被自己关掉。
function onDocPointerDown(e: PointerEvent) {
  if (!open.value) {
    return
  }
  const panel = panelEl.value
  if (panel && e.target instanceof Node && panel.contains(e.target)) {
    return
  }
  e.preventDefault()
  e.stopPropagation()
  setOpen(false)
}

// 回合结束、开始摆球时面板就失效了，不等玩家去点：留在台上会挡住球台，
// 而且那时调塞根本没有意义（还没有可以出杆的母球）。
watch(
  () => props.disabled,
  (blocked) => {
    if (blocked) {
      setOpen(false)
    }
  },
)

onMounted(() => {
  window.addEventListener('pointerdown', onDocPointerDown, true)
})

onBeforeUnmount(() => {
  window.removeEventListener('pointerdown', onDocPointerDown, true)
})

// reset 把击球点放回圆心。
function reset() {
  emit('update:a', 0)
  emit('update:b', 0)
}

// onPointerDown 从按下那一刻就取点：点哪打哪，不用先按再拖。
//
// 捕获指针是为了手指移出面板之后仍然跟手——球面只有两百来像素，
// 拖到边缘时手指多半已经出了球，不捕获就会断在中间。
function onPointerDown(e: PointerEvent) {
  if (props.disabled || props.readonly) {
    return
  }
  dragging = true
  ;(e.currentTarget as HTMLElement).setPointerCapture(e.pointerId)
  applyPoint(e)
}

function onPointerMove(e: PointerEvent) {
  if (!dragging) {
    return
  }
  applyPoint(e)
}

function onPointerUp(e: PointerEvent) {
  if (!dragging) {
    return
  }
  dragging = false
  const target = e.currentTarget as HTMLElement
  if (target.hasPointerCapture(e.pointerId)) {
    target.releasePointerCapture(e.pointerId)
  }
}

// applyPoint 把一次指针位置换成 (a, b)。
//
// 以球心为原点、半径为单位长度算比例，超出圆就沿同一方向夹到边上：
// 这样手指滑出球面时塞量停在最大值，不会跳成 0 或反向。
function applyPoint(e: PointerEvent) {
  const el = stage.value
  if (!el) {
    return
  }
  const rect = el.getBoundingClientRect()
  const radius = Math.min(rect.width, rect.height) / 2
  if (radius <= 0) {
    return
  }
  let dx = (e.clientX - (rect.left + rect.width / 2)) / radius
  // 屏幕向下是低杆，取负号换成「向上为正」。
  let dy = -(e.clientY - (rect.top + rect.height / 2)) / radius

  const len = Math.sqrt(dx * dx + dy * dy)
  if (len < DEAD_ZONE) {
    dx = 0
    dy = 0
  } else if (len > MAX_OFFSET) {
    dx = (dx / len) * MAX_OFFSET
    dy = (dy / len) * MAX_OFFSET
  }
  emit('update:a', round2(dx))
  emit('update:b', round2(dy))
}

// round2 保留两位小数。百分点的读数只到一位，这里多留一位是为了
// 让「拖一点点」也能被记住，不至于原地跳 1%。
//
// 顺手把 -0 归一成 0：竖直方向取负号之后，「正好在水平中线上」会算出 -0，
// 它和 0 在数学上一样，但写进协议、回读日志、写断言时都只会让人愣一下。
function round2(value: number): number {
  const rounded = Math.round(value * 100) / 100
  return rounded === 0 ? 0 : rounded
}
</script>

<style scoped>
/* 收起态：挂在力度条正下方（父级是 .pm__power，见 match.vue），
   和滑轨同一条竖轴，下沿往下空 14px——紧贴着会看成滑轨的一部分。 */
.sp {
  position: absolute;
  top: calc(100% + 14px);
  /* 球 46 宽、滑轨 44 宽，往右多伸 1px 就是一根轴。 */
  right: -1px;
  width: 46px;
  height: 46px;
  z-index: 6;
}

.sp__toggle {
  position: relative;
  display: block;
  width: 46px;
  height: 46px;
  padding: 0;
  background: none;
  border: none;
  cursor: pointer;
  touch-action: none;
}

.sp__toggle:disabled {
  opacity: 0.4;
  cursor: default;
}

/* 收起态的小球：直径 40px，位置与展开态球面同一个画法，缩小后仍然认得出。 */
.sp__mini {
  position: absolute;
  right: 3px;
  bottom: 3px;
  width: 38px;
  height: 38px;
  border-radius: 50%;
  background: radial-gradient(circle at 36% 30%, #ffffff 0%, #e8ebee 46%, #b9c1c8 78%, #97a0a8 100%);
  box-shadow:
    0 2px 6px rgba(0, 0, 0, 0.45),
    inset 0 -3px 6px rgba(0, 0, 0, 0.18);
}

.sp__mini-dot {
  position: absolute;
  width: 9px;
  height: 9px;
  margin: -4.5px 0 0 -4.5px;
  background: #d33a2c;
  border-radius: 50%;
  box-shadow: 0 0 4px rgba(211, 58, 44, 0.75);
}

/* 角度标签挂在球的下方。
   原来挂在球的左边（right: calc(100% + 7px)），桌面看着没问题，但在手机上
   那块位置已经是球台的右边缘，标签会直接压进台面里。球的上方是力度条、
   下方是这块控件自己的留白，只有下面不会跟别的东西抢地方。 */
.sp__badge {
  position: absolute;
  top: calc(100% - 5px);
  left: 50%;
  padding: 1px 7px;
  font-size: 12px;
  font-variant-numeric: tabular-nums;
  line-height: 16px;
  color: rgba(255, 255, 255, 0.9);
  white-space: nowrap;
  background: rgba(12, 18, 24, 0.82);
  border-radius: 8px;
  transform: translateX(-50%);
}

/* 展开面板：从球往上长，整体往左让 15px——右缘挂着的收起箭头是 -14px 出去的，
   贴着屏幕右边摆的话它会被裁掉一半。台面本来就在屏幕里侧 3px，让出这一段正好。 */
.sp__panel {
  position: absolute;
  right: 15px;
  bottom: calc(100% + 10px);
  width: min(232px, 62vw);
  padding: 10px 12px 14px;
  /* 半透明深底 + 一圈冷色描边，和台呢的绿分得开又不刺眼。 */
  background: rgba(13, 30, 34, 0.86);
  border: 1.5px solid rgba(132, 214, 232, 0.55);
  border-radius: 18px;
  box-shadow:
    0 10px 28px rgba(0, 0, 0, 0.5),
    inset 0 0 40px rgba(80, 180, 200, 0.08);
  backdrop-filter: blur(6px);
}

.sp__head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  margin-bottom: 4px;
}

.sp__reset {
  padding: 3px 10px;
  font-size: 13px;
  color: rgba(220, 238, 244, 0.9);
  background: rgba(120, 190, 210, 0.16);
  border: 1px solid rgba(132, 214, 232, 0.35);
  border-radius: 8px;
  cursor: pointer;
}

.sp__reset:active {
  background: rgba(120, 190, 210, 0.3);
}

.sp__value {
  font-size: 14px;
  font-variant-numeric: tabular-nums;
  color: rgba(226, 240, 246, 0.92);
}

/* 球面容器：正方形，球本身铺满，所有细节都按百分比定位。 */
.sp__stage {
  position: relative;
  /* 球不顶到面板边上：留一圈底色，面板的圆角边框才看得出来是「框住一颗球」，
     顶满会变成「一个圆盖住一块矩形」。 */
  width: 91%;
  margin: 0 auto 4px;
  aspect-ratio: 1 / 1;
  touch-action: none;
  cursor: crosshair;
}

.sp__ball {
  position: absolute;
  inset: 0;
  border-radius: 50%;
  background: radial-gradient(
    circle at 38% 30%,
    #ffffff 0%,
    #f4f6f8 24%,
    #e3e8ec 50%,
    #c6ced5 74%,
    #a3adb6 94%,
    #8e99a2 100%
  );
  box-shadow:
    0 10px 22px rgba(0, 0, 0, 0.5),
    inset 0 -10px 22px rgba(48, 68, 80, 0.26);
}

/* 顶部那道弧形高光：参考图的球面上有一圈很亮的反光，少了它球是平的。 */
.sp__gloss {
  position: absolute;
  left: 20%;
  top: 7%;
  width: 58%;
  height: 42%;
  background: radial-gradient(
    ellipse at 50% 50%,
    rgba(255, 255, 255, 1) 0%,
    rgba(255, 255, 255, 0.55) 40%,
    rgba(255, 255, 255, 0) 74%
  );
  border-radius: 50%;
  pointer-events: none;
}

/* 一横一竖两条虚线：中杆的基准。分两段跨过圆心，红点压在上面也看得见。 */
.sp__cross {
  position: absolute;
  background-image: repeating-linear-gradient(
    to right,
    rgba(58, 96, 104, 0.5) 0 5px,
    rgba(58, 96, 104, 0) 5px 10px
  );
  pointer-events: none;
}

.sp__cross--h {
  top: 50%;
  left: 8%;
  width: 84%;
  height: 1px;
}

.sp__cross--v {
  left: 50%;
  top: 8%;
  width: 1px;
  height: 84%;
  background-image: repeating-linear-gradient(
    to bottom,
    rgba(58, 96, 104, 0.5) 0 5px,
    rgba(58, 96, 104, 0) 5px 10px
  );
}

/* 中心细圈：提示「点在这里就是中杆」，也让死区有个看得见的边界。 */
.sp__ring {
  position: absolute;
  left: 50%;
  top: 50%;
  width: 18%;
  height: 18%;
  border: 1px dashed rgba(58, 96, 104, 0.28);
  border-radius: 50%;
  transform: translate(-50%, -50%);
  pointer-events: none;
}

/* 远端每 60ms 一帧，补一点过渡才不会一格一格跳。自己拖的时候不加：
   过渡会让红点追不上手指。 */
.sp__mini.is-peer .sp__mini-dot,
.sp__stage.is-peer .sp__dot {
  transition:
    left 90ms linear,
    top 90ms linear;
}

/* 红点：击球点。带一圈发光，在浅色球面上才抓得住眼睛。 */
.sp__dot {
  position: absolute;
  width: 13px;
  height: 13px;
  margin: -6.5px 0 0 -6.5px;
  background: radial-gradient(circle at 38% 34%, #ef5a45 0%, #d33a2c 62%, #a92a20 100%);
  border-radius: 50%;
  box-shadow:
    0 0 8px rgba(224, 76, 58, 0.6),
    0 1px 2px rgba(0, 0, 0, 0.35);
  pointer-events: none;
}

/* 收起按钮贴在面板右缘，和参考图一样是一个挂着的小方块。 */
.sp__fold {
  position: absolute;
  top: 50%;
  right: -14px;
  /* 竖直居中：参考图里这个小箭头就挂在面板右缘的正中，
     贴底放会让人以为它是「回到底部」而不是「收起」。 */
  transform: translateY(-50%);
  display: flex;
  align-items: center;
  justify-content: center;
  width: 26px;
  height: 40px;
  color: rgba(228, 244, 248, 0.9);
  background: rgba(96, 186, 206, 0.85);
  border: none;
  border-radius: 0 10px 10px 0;
  cursor: pointer;
}
</style>

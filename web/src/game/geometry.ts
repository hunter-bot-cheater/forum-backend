// 台面世界坐标（米）与屏幕坐标（CSS 像素）之间的换算。
//
// 所有绘制与命中判定都必须先过这一层。各画各的缩放是这类项目最常见的错位来源：
// 台面按一种比例、球按另一种、点击又按第三种，窗口一缩放就会出现
// 「球看着在袋口、点下去没反应」。统一从这里取 scale，别的地方不许自己乘。
import { CUSHION_WIDTH, RAIL_WIDTH, TABLE_LENGTH, TABLE_WIDTH } from './table'

// 从台面内沿到最外层木框边缘的距离。
//
// 缩放必须按这个算，不能只按库边算：木框画在库边之外，少算这一圈，
// 木框的四个圆角就会顶出画布，被裁成直角。
const MARGIN = CUSHION_WIDTH + RAIL_WIDTH

// 台面加库边加木框的外沿尺寸。
export const TABLE_OUTER_WIDTH = TABLE_WIDTH + MARGIN * 2
export const TABLE_OUTER_LENGTH = TABLE_LENGTH + MARGIN * 2

// Viewport 是可绘制区域的 CSS 像素尺寸，由外层容器给出，已扣掉 HUD 占位。
export interface Viewport {
  width: number
  height: number
}

// ViewTransform 是「等比缩放 + 居中平移」。
// offsetX / offsetY 是外层矩形左上角在画布里的位置，不是台面内沿的位置。
export interface ViewTransform {
  scale: number
  offsetX: number
  offsetY: number
}

// Point 只是一个点在某个坐标系下的位置，不区分世界还是屏幕，调用方自己清楚。
export interface Point {
  x: number
  y: number
}

// fitTable 把整个台面（含库边）等比放进 viewport，四周至少留 pad 像素。
//
// 取宽高两个方向里更紧的那个做 scale：竖屏手机宽度不够、横屏桌面高度不够，
// 各自会在不同方向上受限。偏移取剩余空间的一半，保证左右或上下留白对称。
// 容器退化到 0 宽（display:none 的瞬间会出现）时按 1 像素兜底，
// 否则 scale 会算成 0 或 Infinity，后面所有坐标都变成 NaN。
export function fitTable(view: Viewport, pad: number): ViewTransform {
  const availWidth = Math.max(view.width - pad * 2, 1)
  const availHeight = Math.max(view.height - pad * 2, 1)

  const scale = Math.min(availWidth / TABLE_OUTER_WIDTH, availHeight / TABLE_OUTER_LENGTH)
  const contentWidth = TABLE_OUTER_WIDTH * scale
  const contentHeight = TABLE_OUTER_LENGTH * scale

  return {
    scale,
    offsetX: (view.width - contentWidth) / 2,
    offsetY: (view.height - contentHeight) / 2,
  }
}

// worldToScreen 把台面上的点（米）换成画布上的点（CSS 像素）。
//
// 两处偏移都不是随便加的：
//  1. 加 MARGIN 因为世界原点在台面内沿，而 offset 记的是最外层木框的左上角。
//  2. y 要翻转。pooltool 的 y 轴朝远端（y=0 是开球线那一端，y=1981mm 是摆球那一端），
//     屏幕的 y 轴朝下。直接沿用会让母球（y≈0.46）画在屏幕上方、球堆画在下方，
//     和玩家的视角正好相反——站在球台这一头看，母球永远在近处、在下面。
export function worldToScreen(t: ViewTransform, x: number, y: number): Point {
  return {
    x: t.offsetX + (x + MARGIN) * t.scale,
    y: t.offsetY + (TABLE_LENGTH - y + MARGIN) * t.scale,
  }
}

// screenToWorld 是 worldToScreen 的逆运算，用于把手势位置换成台面坐标。
// 手持球落位、点击瞄准都走它，和绘制共用同一个 scale 与同一次翻转，
// 否则会出现「看着点在中袋、实际落在中袋另一侧」这种镜像错误。
export function screenToWorld(t: ViewTransform, x: number, y: number): Point {
  return {
    x: (x - t.offsetX) / t.scale - MARGIN,
    y: TABLE_LENGTH - ((y - t.offsetY) / t.scale - MARGIN),
  }
}

// metersToPx 把一段长度换成像素。线宽、球半径、袋口半径都走它，
// 保证窗口放大时所有元素同比例变化，而不是只有台面被拉大。
export function metersToPx(t: ViewTransform, meters: number): number {
  return meters * t.scale
}

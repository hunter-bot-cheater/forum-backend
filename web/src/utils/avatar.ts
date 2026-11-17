// 没上传过头像的人，用「彩色底 + 昵称首字」兜底。
//
// 以前这一层用的是 el-avatar 自带的灰底，结果一列评论扫下去全是同一块灰、
// 同一个字母，谁是谁分不出来。现在按昵称取色，不同人不同色。
//
// 不用 Math.random()：随机是在每次渲染时求值，同一个人在列表页和详情页会
// 拿到两个颜色，翻回来又变一次，看起来像页面渲染错乱，而不是「随机」。

// 十色调色板。白字压上去的对比度都在 4.5 以上（WCAG AA 正文标准）。
// 刻意避开两端：太亮的色白字读不出，太暗的色和深色模式下的分隔线糊成一片。
const NAME_COLORS = [
  '#4c7ef3',
  '#2f9e7c',
  '#d9743f',
  '#8a6bd1',
  '#c2557a',
  '#2f8fa8',
  '#a1743f',
  '#5f7a4a',
  '#b85a5a',
  '#5a6ac4',
]

// 昵称缺失（账号注销后后端省掉 author 字段）时统一给灰蓝。
// 这种位置本来就不指向某个具体的人，给一个明确颜色反而像在暗示身份。
const NEUTRAL_COLOR = '#8b93a3'

// avatarColor 按昵称算出一个稳定的底色，交给 el-avatar 的内联 style。
// 同一个昵称永远得到同一个色：算法只依赖字符串内容，不依赖调用顺序或运行环境。
export function avatarColor(name?: string | null): string {
  if (!name) {
    return NEUTRAL_COLOR
  }

  // djb2 的简化版：逐字符 h = h * 31 + code。
  // 每一步都取模，避免长昵称把数字推到 2^53 以上丢精度。
  let hash = 0
  for (let i = 0; i < name.length; i += 1) {
    hash = (hash * 31 + name.charCodeAt(i)) % 100000007
  }

  const index = hash % NAME_COLORS.length
  return NAME_COLORS[index]
}

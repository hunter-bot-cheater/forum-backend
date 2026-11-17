// keepInPlace 记下某个元素此刻在视口中的纵向位置，返回一个「把它放回原处」的函数。
//
// 翻页时列表会整体换成另一页：条目数可能从 10 条变成 3 条，高度跟着变，
// 分页器就会被内容推着上下跑。用户刚点完「下一页」，一眨眼按钮跑掉了，
// 得再滚一次才找得到——这就是「翻页时页面在跳」的直接来源。
// 用这个函数把分页器钉在原来的位置，用户就能连点。
//
// 用法：翻页前 const restore = keepInPlace(pager.value)，
// 数据回来并渲染完（await nextTick）后 restore()。
export function keepInPlace(el: Element | null | undefined): () => void {
  if (!el) {
    return () => {}
  }
  const before = el.getBoundingClientRect().top
  return () => {
    // 元素可能已经不在了（列表塌成空态时整个分页器被 v-if 摘掉），此时无事可做。
    if (!el.isConnected) {
      return
    }
    const delta = el.getBoundingClientRect().top - before
    // delta 为正说明元素往下跑了，要把页面往下滚同样的距离，它才回到原位。
    if (delta !== 0) {
      window.scrollBy(0, delta)
    }
  }
}

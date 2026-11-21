import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import {
  acceptInvite,
  createInvite,
  declineInvite,
  listPendingInvites,
  type AcceptInviteResp,
  type InviteResp,
  type PendingInvite,
} from '@/api/pool'
import { onLogout } from '@/utils/auth'
import { onFrame, onReconnect } from '@/utils/ws'

// 台球的全局状态：待接受的邀请、我发出的那条邀请、以及「对方接受了」的跳转信号。
//
// 为什么这些必须提到 store 而不是留在页面里：
//   - 邀请到达时用户可能正停在文章详情页，弹层要能从任意页面弹出来；
//   - 「对方接受了」这条推送到达时，发起方可能在大厅、也可能已经切到别的页面，
//     只有全局状态能把它带回房间。
// 对局本身的状态不放这里：它一个连接只服务一个页面，进 store 反而要处理
// 「切换对局时清哪一份」的问题。

// PoolPushInvite 是后端推 pool_invite 时的 data 形状，与 PendingInvite 一致。
// 单独起名是为了让「这条来自推送、没有经过 HTTP 校验」这件事在类型上看得见。

export const usePoolStore = defineStore('pool', () => {
  // pending 是收到的、还没处理的邀请。同一个发起人同时只会有一条，
  // 所以这个列表通常最多一两项，不需要分页。
  const pending = ref<PendingInvite[]>([])
  // ignored 是本地「先放一放」的邀请 id。
  //
  // 忽略不通知服务端：邀请 60 秒后自然过期，为此多一个接口没必要。
  // 但要记住这些 id，否则下一次 refresh 会把它们又捞回来、弹层再弹一次。
  const ignored = ref<string[]>([])
  // outgoing 是我发出、还在等对方回应的那条邀请。
  // 同一时间只允许一条：后端在同一个发起人已有 pending 时会返回 409。
  const outgoing = ref<InviteResp | null>(null)
  // acceptedRoom 是「对方接受了」的跳转信号，由弹层组件消费后清空。
  //
  // store 自己不跳路由：让 store import router 会在单测里拉起整棵路由树，
  // 而且「谁负责导航」会变得说不清。这里只放信号，跳转交给挂载在布局里的组件。
  const acceptedRoom = ref<AcceptInviteResp | null>(null)
  // notice 是一次性提示文案（对方拒绝、邀请过期等），读走即清。
  const notice = ref('')

  // wsBound 保证帧处理只注册一次。store 是单例，但 initWS 会被多个入口调用。
  let wsBound = false

  // incoming 是当前应该弹出来的那条邀请。
  // 取第一条未被忽略的：同时收到两条时先处理早的那条，处理完自动轮到下一条。
  const incoming = computed<PendingInvite | null>(() => {
    for (const item of pending.value) {
      if (!ignored.value.includes(item.invite_id)) {
        return item
      }
    }
    return null
  })

  // refresh 拉一次待接受邀请，与本地列表对齐。
  //
  // 它覆盖两类 WebSocket 补不回来的情况：刷新页面后推送期间漏掉的邀请，
  // 以及弹层被关闭后想重新找回入口。失败不提示：
  // 邀请是「有就弹、没有就算了」的东西，为一次静默失败弹红条只会干扰阅读。
  async function refresh() {
    try {
      pending.value = await listPendingInvites()
    } catch {
      // 保持上一次的列表，等下一轮刷新。
    }
  }

  // invite 邀请一个关注的人，成功后记住这条邀请以便显示倒计时。
  // 失败原样抛出：调用方（大厅页）要按 409 与 400 给不同的提示。
  async function invite(toUserId: number): Promise<InviteResp> {
    const resp = await createInvite(toUserId)
    outgoing.value = resp
    return resp
  }

  // cancelOutgoing 收起「等待对方接受」的卡片。
  //
  // 只清本地：服务端那条邀请留在内存里，对方仍可能接受，
  // 届时 acceptedRoom 会把我们带回房间——这正是想要的行为，
  // 因为对方已经进房了，房间是真实存在的。
  function cancelOutgoing() {
    outgoing.value = null
  }

  // accept 接受邀请，成功后清掉这条并返回房间信息供调用方跳转。
  async function accept(inviteId: string): Promise<AcceptInviteResp> {
    const resp = await acceptInvite(inviteId)
    removePending(inviteId)
    return resp
  }

  // decline 拒绝邀请。
  //
  // 失败也把本地这条去掉，并且不往外抛：拒绝这件事在本地已经生效了，
  // 服务端失败最多是房间多挂 90 秒。抛出去只会让调用方多写一层 try/catch，
  // 忘写就变成一条 unhandled rejection。
  async function decline(inviteId: string) {
    try {
      await declineInvite(inviteId)
    } catch {
      // 拦截器已经提示过，这里只保证本地状态收敛。
    } finally {
      removePending(inviteId)
    }
  }

  // dismiss 本地忽略一条邀请，不调接口。
  function dismiss(inviteId: string) {
    if (!ignored.value.includes(inviteId)) {
      ignored.value = ignored.value.concat(inviteId)
    }
  }

  // removePending 从列表里摘掉一条。忽略标记也一并清掉：
  // 已经处理完的 id 不需要再记住，留着会让数组无限增长。
  function removePending(inviteId: string) {
    pending.value = pending.value.filter((item) => item.invite_id !== inviteId)
    ignored.value = ignored.value.filter((id) => id !== inviteId)
  }

  // onInvitePush 处理新邀请到达。
  //
  // 去重而不是直接 push：推送与 refresh 可能同时到达同一条邀请
  // （比如刚刷新完列表、推送才到），不去重会让弹层里出现两条一样的请求。
  function onInvitePush(data: PendingInvite) {
    for (const item of pending.value) {
      if (item.invite_id === data.invite_id) {
        return
      }
    }
    pending.value = [data].concat(pending.value)
  }

  // takeNotice 取走一次性提示，取完清空，避免同一句话弹两次。
  function takeNotice(): string {
    const text = notice.value
    notice.value = ''
    return text
  }

  // initWS 注册实时帧处理，幂等。
  function initWS() {
    if (wsBound) {
      return
    }
    wsBound = true

    onFrame('pool_invite', (data: PendingInvite) => {
      onInvitePush(data)
    })

    // 对方接受：清掉自己那条等待中的邀请，把房间信息交给组件去跳转。
    // 不清的话大厅会一直显示「等待对方接受」，而对方已经在房间里等着了。
    onFrame('pool_invite_accepted', (data: AcceptInviteResp) => {
      outgoing.value = null
      acceptedRoom.value = data
    })

    // 对方拒绝：服务端已经把这个房间解散了，本地只需要收起卡片并提示。
    onFrame('pool_invite_declined', () => {
      outgoing.value = null
      notice.value = '对方拒绝了这次邀请'
    })

    // 断线重连后补拉一次：断线期间的邀请推送是收不到的，
    // 不补拉的话那条邀请就只能等用户刷新页面才会出现。
    onReconnect(() => {
      void refresh()
    })
  }

  // reset 在登出时清空，否则下一个人登录会看到上一个人的邀请。
  function reset() {
    pending.value = []
    ignored.value = []
    outgoing.value = null
    acceptedRoom.value = null
    notice.value = ''
  }

  onLogout(reset)

  return {
    pending,
    incoming,
    outgoing,
    acceptedRoom,
    notice,
    refresh,
    invite,
    cancelOutgoing,
    accept,
    decline,
    dismiss,
    onInvitePush,
    takeNotice,
    initWS,
    reset,
  }
})

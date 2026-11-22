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

// TrackedInvite 是带本地截止时刻的邀请。
//
// 协议里的 expires_in 是「服务端发这一帧时还剩多少秒」，它只在到手那一刻准：
// 第二条排队邀请往往比第一条晚十几秒才轮到，此时再拿它的 expires_in 起表，
// 界面会显示「还剩 55 秒」而服务端那边已经过期。
// 所以在入库时一次性换成本地绝对时刻，之后所有倒数都按它算。
export type TrackedInvite<T> = T & { expires_at: number }

// track 把一条邀请的剩余秒数换成本地截止时刻。
function track<T extends { expires_in: number }>(item: T): TrackedInvite<T> {
  return { ...item, expires_at: Date.now() + item.expires_in * 1000 }
}

export const usePoolStore = defineStore('pool', () => {
  // pending 是收到的、还没处理的邀请。同一个发起人同时只会有一条，
  // 所以这个列表通常最多一两项，不需要分页。
  const pending = ref<TrackedInvite<PendingInvite>[]>([])
  // ignored 是本地「先放一放」的邀请 id。
  //
  // 忽略不通知服务端：邀请 60 秒后自然过期，为此多一个接口没必要。
  // 但要记住这些 id，否则下一次 refresh 会把它们又捞回来、弹层再弹一次。
  const ignored = ref<string[]>([])
  // outgoing 是我发出、还在等对方回应的那条邀请。
  // 同一时间只允许一条：后端在同一个发起人已有 pending 时会返回 409。
  const outgoing = ref<TrackedInvite<InviteResp> | null>(null)
  // outgoingTo 是这条邀请发给谁。
  //
  // 接口响应里没有被邀请者的 id，但大厅要按它把那一行显示成「已发送」。
  // 存在这里而不是大厅组件里：从对局页「再约一局」回大厅时组件是重新挂载的，
  // 本地 ref 会归零，那一行就会重新变成可点的「邀请」，点了必然 409。
  const outgoingTo = ref(0)
  // outgoingToName 是按下邀请那一刻对方的昵称。
  //
  // 不能每次渲染再去在线名单里查：从对局页点「再约一局」回大厅时，
  // 房间与在线名单都还在路上，等待卡会先渲染成「已邀请 对方」，
  // 等一下才变成名字。邀请是我们自己发出去的，那一刻就知道发给谁。
  const outgoingToName = ref('')
  // acceptedRoom 是「对方接受了」的跳转信号，由弹层组件消费后清空。
  //
  // store 自己不跳路由：让 store import router 会在单测里拉起整棵路由树，
  // 而且「谁负责导航」会变得说不清。这里只放信号，跳转交给挂载在布局里的组件。
  const acceptedRoom = ref<AcceptInviteResp | null>(null)
  // notice 是一次性提示文案（对方拒绝、邀请过期等），读走即清。
  const notice = ref('')

  // roomTick 在「我这一侧的房间变了」时自增，大厅据此立刻重拉一次。
  //
  // 有了它，大厅在只有自己一个人的时候可以慢速轮询（十秒一次）：
  // 对面进来人这件事由推送告诉我们，不必靠两秒一次的快轮询去撞。
  const roomTick = ref(0)

  // wsBound 保证帧处理只注册一次。store 是单例，但 initWS 会被多个入口调用。
  let wsBound = false

  // incoming 是当前应该弹出来的那条邀请。
  // 取第一条未被忽略的：同时收到两条时先处理早的那条，处理完自动轮到下一条。
  const incoming = computed<TrackedInvite<PendingInvite> | null>(() => {
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
      const list = await listPendingInvites()
      pending.value = list.map(track)
    } catch {
      // 保持上一次的列表，等下一轮刷新。
    }
  }

  // invite 邀请一个关注的人，成功后记住这条邀请以便显示倒计时。
  // 失败原样抛出：调用方（大厅页）要按 409 与 400 给不同的提示。
  //
  // nickname 只用来显示「已邀请 xxx」，允许为空：调用方拿不到昵称时
  // 等待卡会显示成「已邀请 对方」，不影响邀请本身。
  async function invite(toUserId: number, nickname = ''): Promise<TrackedInvite<InviteResp>> {
    const resp = track(await createInvite(toUserId))
    outgoing.value = resp
    outgoingTo.value = toUserId
    outgoingToName.value = nickname
    return resp
  }

  // clearOutgoing 收起「等待对方接受」的那条本地记录。
  //
  // 三样东西必须一起清：只清其中一样，大厅就会出现「按钮说已发送、
  // 等待卡却不见了」这种半截状态。取消、对方接受、对方拒绝、登出都走这里。
  function clearOutgoing() {
    outgoing.value = null
    outgoingTo.value = 0
    outgoingToName.value = ''
  }

  // cancelOutgoing 收起「等待对方接受」的卡片。
  //
  // 只清本地：服务端那条邀请留在内存里，对方仍可能接受，
  // 届时 acceptedRoom 会把我们带回房间——这正是想要的行为，
  // 因为对方已经进房了，房间是真实存在的。
  function cancelOutgoing() {
    clearOutgoing()
  }

  // accept 接受邀请，成功后清掉这条并返回房间信息供调用方跳转。
  async function accept(inviteId: string): Promise<AcceptInviteResp> {
    const resp = await acceptInvite(inviteId)
    removePending(inviteId)
    // 服务端在接受时会把我发出去、还没被回应的邀请一并作废
    // （我原来那间房已经没了，那条邀请点开也进不去）。
    // 本地这张「已邀请 xxx，等对方接受」的卡片要跟着收掉，
    // 否则大厅一直挂着一条永远不会被回应的邀请，还会占着快轮询。
    clearOutgoing()
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
    pending.value = [track(data)].concat(pending.value)
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
    //
    // 还要顺手让大厅重拉一次房间：从对局页「再来一局」发出的邀请，
    // 被接受时发起方已经站在大厅里了，router.replace('/pool') 是同一个路由、
    // 不会重新挂载组件，不重拉的话对面那个人永远不出现——这就是
    // 「对方已经进房，我这屏还是只有自己」的真因。
    onFrame('pool_invite_accepted', (data: AcceptInviteResp) => {
      clearOutgoing()
      acceptedRoom.value = data
      roomTick.value += 1
    })

    // 有人进了我的房间：大厅立刻重拉一次快照，对面那个空位马上变成人。
    onFrame('pool_room_joined', () => {
      roomTick.value += 1
    })

    // 对方拒绝：房间还留着（邀请人可能把房号又给了别人），
    // 本地只需要收起卡片并提示。
    onFrame('pool_invite_declined', () => {
      clearOutgoing()
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
    clearOutgoing()
    acceptedRoom.value = null
    notice.value = ''
    roomTick.value = 0
  }

  onLogout(reset)

  return {
    pending,
    incoming,
    outgoing,
    outgoingTo,
    outgoingToName,
    acceptedRoom,
    notice,
    roomTick,
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

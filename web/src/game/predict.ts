// 本地算一杆：出杆那一刻就把整杆算完，既是画面，也是这一杆的权威结果。
//
// 早先这里只算「前几百毫秒的预测」，真正的结果由服务端 pooltool 算，
// 两套模型落点必然不同，只能靠补间把球滑过去——玩家看到的就是
// 「击球瞬间卡一下、球堆整体挪位」。现在服务端直接采用这份结果，
// 画面与服务端状态出自同一次计算，没有第二个结果，也就没有拼接。
//
// 模型是刻意简化的：球只有平面运动，不跳、不弧、不计算侧旋，库边全部是线段
// （table.ts 的 CUSHION_SEGMENTS，与渲染同一份）。
import type { Shot, ShotFrame } from './animator'
import type { ShotEvent } from './protocol'
import { BALL_RADIUS, CUSHION_SEGMENTS, POCKETS } from './table'
import type { BallState, ShotParams } from './types'

// 重力与摩擦系数。u_s 是滑动摩擦、u_r 是纯滚动摩擦（台呢阻尼）。
//
// u_s 与 g 取 pooltool 的默认值（objects/ball/params.py 的 BallParams）：0.2、9.81。
// 三个碰撞参数（e_b、e_c、f_c）原本也是 pooltool 默认，后来按用户要求把
// 碰撞损耗减半，改成了下面那组——详见常量定义处的推导。
//
// u_r 当前 0.006。视频标定值是 0.012（下面那段推导），用户先要求「阻尼再小一倍」，
// 于是按字面减半——0.012 是「球缓缓停下」的参考量，减半之后球要多滚一倍的路。
//
// 视频 592×1280 / 29.97fps，球直径 25px（横穿球心一行量出边缘 −12.5/+12.5px），
// 开球后母球在 (264.9, 641.8) 停住，从 11.45s 到 13.65s 的轨迹做二次拟合，
// 得恒定减速 51px/s² = 2.04 D/s² = 0.117 m/s²，除以 g 就是 0.012。
// 拟合预测的停球时刻 13.69s 与实际 13.68s 只差 10 毫秒，说明视频里那一档是准的。
//
// 注意它与 pooltool 的 0.01 几乎相同——前面几轮为了「走得慢」把 u_r 压到 0.009、
// 又拉到 0.05/0.1，其实 0.05 比参考快四倍，玩家看到的就是「滑两下就刹住」。
// 想调手感优先动 SPEED_MAX（力度条给多少初速），不要用 u_r 去凑时长；
// u_r 每减半，满力球的滚动段时长就翻倍，很容易顶到 MAX_DURATION。
//
// 另外 u_r 必须小于 u_s：滑动阶段用的是 u_s，纯滚动之后换成 u_r，两者反过来的话
// 「滚动比滑动还费劲」，物理上讲不通，手感上会变成球一落稳就急刹。
const GRAVITY = 9.81
const SLIDE_FRICTION = 0.2
const ROLL_FRICTION = 0.006

// 滑动阶段球心减速度，以及转动让它提前进入纯滚动的比例。
// 球是实心球，转动惯量 2/5 mR²：滑动摩擦让球心减速 μg 的同时，
// 角速度以 5/2 倍 μg 增长，所以中心击球时球速降到 5/7 v0 就不再滑了。
const SLIDE_DECEL = SLIDE_FRICTION * GRAVITY
const ROLL_DECEL = ROLL_FRICTION * GRAVITY
const SPIN_GAIN = 2.5

// 球与球、球与库边的恢复系数，以及库边的切向摩擦。
//
// 初值取 pooltool 的默认值（e_b 0.95、e_c 0.85、f_c 0.2），之后按用户要求
// 连续两轮「把碰撞的能量损失减半」重标过。这里要按能量算不能按速度算：
// 碰撞损失的能量是动能的 (1 − e²)，要让它减半就解 1 − e'² = (1 − e²)/2，
// 也就是 e' = √((1 + e²)/2)。直接拿 e 乘 0.5 是错的——e 从 0.95 掉到 0.475
// 那是损失了八成功耗，球撞完几乎不动。
//
// 反过来也不能「提一倍」：恢复系数上限就是 1（完全弹性），0.975 翻番到 1.95
// 在物理上不存在。所以「往上提一倍」只能理解成把剩余损耗再减半一次，
// 还是同一个公式迭代：0.95 → 0.975 → 0.9876，0.85 → 0.928 → 0.9647。
// 越接近 1 每次能提的绝对量越小，这是这个模型的固有性质，不是没改到位：
// 现在球球正碰只丢 2.47% 的动能，再去抠它就只剩小数点后第三位了。
//
// 切向摩擦同样按动能口径减半两次：切向速度乘 (1 − f)，切向动能乘 (1 − f)²，
// 损失 36% → 19% → 9.5%，对应 f 从 0.2 收到 0.1 再收到 0.0487。
const BALL_RESTITUTION = 0.9876
const CUSHION_RESTITUTION = 0.9647
const CUSHION_FRICTION = 0.0487

// 碰撞分离时球带着多少滚动（0 到 1，按当时的速度算）。
//
// 刚性碰撞模型里接触是零时长、切向完全打滑，所以撞完 spin 归零，
// 接下来球得靠滑动摩擦重新把自转带起来：速度要掉到 5/7（动能少一半）
// 才回到纯滚动。刚体碰撞理论确实是这个结论，但手感上就是「撞完就泄气」，
// 被撞的球蹿出去一下立刻软掉。
//
// 真实台球的接触有几百微秒，台呢在这段时间里已经在带动球，分离时并不是
// 完全不打转。这里给一半的滚动：速度损失从 28.6% 收到 14.3%，
// 恰好等于把这一段动能损耗也减半，和上一轮恢复系数的口径一致。
const COLLISION_SPIN_CARRY = 0.5

// spinAfterImpact 算碰撞后这颗球该带多少滚动速度。
//
// spin 是标量，语义是「沿当前运动方向已经建立起来的滚动线速度」，
// 所以直接按合速度乘比例，不需要跟原来的方向扯上关系。
function spinAfterImpact(vx: number, vy: number): number {
  return Math.sqrt(vx * vx + vy * vy) * COLLISION_SPIN_CARRY
}

// 力度到母球出射速度的映射。上限按参考视频量的：开球那一杆母球在
// 842→511px 的四帧里匀速走 63px/帧，29.97fps 下是 1900px/s，
// 除以球直径 25px 得 76 D/s，换成我们 57.15mm 的球就是 4.34 m/s，取 4.4。
//
// 下限保留 0.05（pooltool 的 min_stroke_speed）：那是「轻碰一下」的量，
// 视频里没有对应镜头，没有实测依据就不动它。力度条线性插值，
// 中间档位自然落在两者之间。
const SPEED_MIN = 0.05
const SPEED_MAX = 4.4

// 速度低于这个值就当停住。pooltool 会让球以毫米级速度继续爬，
// 这里没有这个必要，但阈值也绝不能大：早先取 3 cm/s，在手机上相当于每秒
// 走十来个像素，球明明还在慢慢挪却被一步清零，看起来就是「说到就到」。
//
// 现在收到 1 mm/s。判定是按积分步（1/240 秒）做的，滚动减速 0.059 m/s²
// 一步只掉 0.25 mm/s，所以真正归零的速度是「阈值 + 一步的量」，约 1.25 mm/s。
// 手机上台面约 361 像素一米，换算下来每秒 0.45 个像素，四十个球径才走一个，
// 归零时既看不出位移，也留不下刹车点。
// 想更极致可以再往下收，但 1 mm/s 已经是「位移比像素还小」的量级，
// 再小只会让球在最后半秒多积几百帧。
const STOP_SPEED = 0.001

// 关键帧间隔。原来跟着 pool-sim 的 sample_hz=30，播放时靠 animator 线性
// 插值补到屏幕刷新率；但插值是直线，球撞库换向的那一下仍然能看出折角，
// 玩家反馈「看着有点卡」。先提到 60Hz，玩家还是觉得球在滚的时候一顿一顿。
//
// 原因是这代手机的屏幕大多是 120Hz：60Hz 的关键帧落上去，每个采样点要
// 顶两帧，滚动就变成「走一下、停一下」。现在直接对齐 120Hz，一帧一个采样点。
//
// 代价有三处，改这个数时都要一起看：
//   1. 上行消息翻倍（一杆约 52KB → 104KB，gameMaxMessage 512KB 仍有五倍余量）；
//   2. 服务端的 maxShotFrames 要跟着翻（internal/pool/match.go，现在是 4800）；
//   3. 播放时每帧都要重采样球面贴图，120Hz 下渲染开销翻倍（实测仍在 1ms 量级）。
const FRAME_DT = 1 / 120

// 每帧积分多少步。必须取整：用小数步长累加，浮点误差会让某一帧晚记一步，
// 4.4 m/s 的球在几毫秒里就走一厘米，与权威轨迹对比时是一串周期性的错位。
//
// 帧率翻倍后这里跟着减半，STEP_DT 仍是 1/240 秒：步长比原来的 1/300 略长，
// 但 4.4 m/s 一步也只走 18 毫米，仍然远小于球半径（34.3 毫米），
// 球与球不会在两步之间互相穿过，而步数少了五分之一，预测反而更快。
const STEPS_PER_FRAME = 2

// 积分步长。
const STEP_DT = FRAME_DT / STEPS_PER_FRAME

// 预测时长上限。它同时兜住「死循环」和「球滚不完」两件事：
// 只要还有球在动就会继续积，没有上限的话界面上会变成一个永远播不完的动画；
// 上限太小则会在球还在走的时候硬切，画面上是「球突然定住」。
//
// 原来给的是 20 秒，阻尼从 0.012 减半到 0.006 之后，满力单球要滚 17.98 秒，
// 离上限只剩 2 秒——斜着打、撞库次数多的杆子很容易触顶。提到 24 秒留出余量。
// 提这个数要同步看服务端的 maxShotFrames：24 秒 × 120Hz = 2880 帧，
// 那边是 4800，仍然够。
const MAX_DURATION = 24

// 库边的碰撞几何直接取 table.ts 那份：直线库边 + 十二条 45° 颊板 + 袋兜底。
//
// 以前这里抄的是 pooltool 的 18 条线段，袋口处是「库边方头切断」，
// 画面上却画成一个梯形缺口——两者对不上，玩家会看到「球从斜边上穿过去」。
// 现在物理与渲染共用同一份数据：看到的斜边就是能撞到的斜边。
// 顺手也修掉了「洞的最外沿和库边脸不对齐」：鼻尖就是从洞与库边脸的交点算出来的。


// SimBall 是预测里的一颗球。spin 是「滚动线速度」，与速度同向。
// 速度大于 spin 说明接触点在打滑，摩擦要用滑动的那一档。
interface SimBall {
  id: string
  x: number
  y: number
  vx: number
  vy: number
  spin: number
  onTable: boolean
}

// SimTrace 收集一杆里要报给服务端的事件与当前时刻。
//
// 服务端的规则判定只看事件（首碰哪颗、有没有碰库、落袋顺序），
// 不再自己算物理，所以这里产出的形状必须与 pool-sim 的 events 一致：
// 类型字符串相同，a/b 的含义也相同（见 internal/pool/sim/client.go）。
interface SimTrace {
  events: ShotEvent[]
  // t 是当前积分到第几秒，事件按它打时间戳。
  t: number
}

// PredictedShot 是本地算完的一杆：轨迹给播放器，事件与终局给服务端。
export interface PredictedShot extends Shot {
  // events 按发生顺序排列，形状与 pool-sim 的一致。
  events: ShotEvent[]
  // final 是终局球位，含已经落袋的球（on_table=false）。
  final: Array<{ id: string; x: number; y: number; z: number; on_table: boolean }>
}

// predictShot 算一杆，返回轨迹、事件与终局球位。
//
// 跳球（杆头明显上扬、球会离台）与母球不在台上时返回 null：
// 二维模型表达不了腾空，硬算出来的结果会与玩家的预期差得很远。
export function predictShot(
  balls: ReadonlyArray<BallState>,
  params: ShotParams,
): PredictedShot | null {
  if (params.theta > 0.12) {
    return null
  }
  const sim: SimBall[] = []
  for (const ball of balls) {
    if (!ball.on_table) {
      continue
    }
    sim.push({ id: ball.id, x: ball.x, y: ball.y, vx: 0, vy: 0, spin: 0, onTable: true })
  }
  const cue = sim.find((ball) => ball.id === 'cue')
  if (!cue) {
    return null
  }

  // 出杆：把这杆的速度直接给母球。中心击球时它先滑动再滚动，
  // 高低杆改变的是旋转，对二维轨迹影响很小，这里不建模。
  const speed = SPEED_MIN + params.power * (SPEED_MAX - SPEED_MIN)
  cue.vx = Math.cos(params.phi) * speed
  cue.vy = Math.sin(params.phi) * speed
  cue.spin = 0

  const trace: SimTrace = { events: [{ t: 0, type: 'stick_ball', a: 'cue', b: 'cue' }], t: 0 }
  const frames: ShotFrame[] = [snapshot(sim, 0)]
  let steps = 0
  let elapsed = 0

  while (elapsed < MAX_DURATION) {
    step(sim, STEP_DT, trace)
    steps += 1
    // 时间用「步数 × 步长」现算，不做累加：几千次浮点加法之后，
    // 累加值会与真实时间差出几十微秒，帧就落在采样点两侧。
    elapsed = steps * STEP_DT
    trace.t = elapsed
    // 帧只在正好走满一帧的步数时生成。每步都建数组会让一杆产生几千个
    // 马上要丢掉的短命对象，纯属给 GC 添工作量。
    if (steps % STEPS_PER_FRAME === 0) {
      frames.push(snapshot(sim, elapsed))
    }
    if (allStopped(sim)) {
      break
    }
  }

  // 收尾帧：球全停的时刻不一定正好落在采样点上，不补这一帧，
  // 播放结束时会停在上一次采样、离真实终点最多 33 毫秒的位置。
  const last = frames[frames.length - 1]
  if (last.t < elapsed - 1e-6) {
    frames.push(snapshot(sim, elapsed))
  }
  return {
    frames,
    duration: frames[frames.length - 1].t,
    events: trace.events,
    final: finalBalls(sim),
  }
}

// finalBalls 把模拟结果压成终局球位。落袋的球也要带上（on_table=false），
// 服务端按它更新台面，漏掉一颗被撞进袋的球，下一杆它就会重新出现。
function finalBalls(sim: SimBall[]): PredictedShot['final'] {
  const list: PredictedShot['final'] = []
  for (const ball of sim) {
    list.push({
      id: ball.id,
      x: round4(ball.x),
      y: round4(ball.y),
      z: 0,
      on_table: ball.onTable,
    })
  }
  return list
}

// emit 往事件流里追加一条。时间戳在调用点取当前步的时刻，
// 这样「首碰之后才碰库」这类时序判断能直接用事件的先后顺序。
function emit(trace: SimTrace, type: string, a: string, b: string): void {
  trace.events.push({ t: round4(trace.t), type, a, b })
}

// step 推进一小段时间：先走位，再解析碰撞，最后算摩擦。
//
// 顺序有讲究——先移动再检测，球在接触的那一步会被推开并交换速度；
// 反过来（先摩擦后碰撞）会让刚被撞出去的球白扣一帧摩擦，慢得能看出来。
function step(sim: SimBall[], dt: number, trace: SimTrace): void {
  for (const ball of sim) {
    if (!ball.onTable) {
      continue
    }
    ball.x += ball.vx * dt
    ball.y += ball.vy * dt
  }

  resolveBallCollisions(sim, trace)

  for (const ball of sim) {
    if (!ball.onTable) {
      continue
    }
    pocketCheck(ball, trace)
    if (!ball.onTable) {
      continue
    }
    cushionCheck(ball, trace)
    applyFriction(ball, dt)
  }
}

// resolveBallCollisions 处理所有两两接触。
//
// 第二轮只在第一轮真的碰到了才做：第二轮的意义是分开「A 推 B、B 又推 C」
// 留下的重叠，没有任何接触时它就是白跑一遍全量比较。
// 开球那一瞬间六七个球同时接触，两轮能把球堆摆平；平时一颗球在台面中间
// 滚，第二轮根本不会启动。
function resolveBallCollisions(sim: SimBall[], trace: SimTrace): void {
  const touched = separateRound(sim, trace)
  if (touched) {
    separateRound(sim, trace)
  }
}

// separateRound 跑一轮两两检测，返回这一轮是否发生过接触。
function separateRound(sim: SimBall[], trace: SimTrace): boolean {
  let touched = false
  for (let i = 0; i < sim.length; i += 1) {
    const a = sim[i]
    if (!a.onTable) {
      continue
    }
    const aStill = a.vx === 0 && a.vy === 0
    for (let j = i + 1; j < sim.length; j += 1) {
      const b = sim[j]
      if (!b.onTable) {
        continue
      }
      // 两颗都已经停住的球不会再靠近，也不会重叠（上一步已经分开过）。
      // 开球后半程球陆续停下，这一句能省掉七成以上的比较。
      if (aStill && b.vx === 0 && b.vy === 0) {
        continue
      }
      if (separate(a, b)) {
        touched = true
        emit(trace, 'ball_ball', a.id, b.id)
      }
    }
  }
  return touched
}

// separate 把一对相撞的球推开并交换法向速度，返回是否真的接触过。
function separate(a: SimBall, b: SimBall): boolean {
  const dx = b.x - a.x
  const dy = b.y - a.y
  const distanceSq = dx * dx + dy * dy
  const contact = BALL_RADIUS * 2
  // 先用平方比，绝大多数（没碰上的）球对在这里就返回，省一次开方。
  if (distanceSq >= contact * contact || distanceSq < 1e-18) {
    return false
  }
  const distance = Math.sqrt(distanceSq)
  const nx = dx / distance
  const ny = dy / distance
  const overlap = contact - distance
  a.x -= nx * overlap * 0.5
  a.y -= ny * overlap * 0.5
  b.x += nx * overlap * 0.5
  b.y += ny * overlap * 0.5

  // 只看接近速度：已经分开的两颗球不再交换一次速度，
  // 否则贴在一起的球会自己抖起来。
  const relative = (b.vx - a.vx) * nx + (b.vy - a.vy) * ny
  if (relative > 0) {
    return true
  }
  const impulse = (-(1 + BALL_RESTITUTION) * relative) / 2
  a.vx -= impulse * nx
  a.vy -= impulse * ny
  b.vx += impulse * nx
  b.vy += impulse * ny
  // 撞击后接触点重新打滑，但分离时已带着一部分滚动（见 COLLISION_SPIN_CARRY）。
  a.spin = spinAfterImpact(a.vx, a.vy)
  b.spin = spinAfterImpact(b.vx, b.vy)
  return true
}

// pocketCheck 判断球是否已经进袋，并记一条落袋事件。
//
// 球心进入袋口圆就算落袋，与 pooltool 的判定一致：袋口圆压在台面外的木框上，
// 球心走到那儿时球已经有一半悬空，没有「再滚两厘米」的余地。
function pocketCheck(ball: SimBall, trace: SimTrace): void {
  for (const pocket of POCKETS) {
    const dx = ball.x - pocket.x
    const dy = ball.y - pocket.y
    if (dx * dx + dy * dy < pocket.radius * pocket.radius) {
      ball.onTable = false
      ball.vx = 0
      ball.vy = 0
      emit(trace, 'ball_pocket', ball.id, pocket.id)
      return
    }
  }
}

// 每条库边一个外扩了一个球半径的包围盒。
//
// 库边检测是预测里最热的循环：一杆几千步，每步每颗球都要比 18 条线段。
// 先用包围盒筛一遍，球在台面中间滚动时全部落空，省下的开方与除法足够
// 把开球那一下从四十毫秒压到十毫秒以内——预测本来是为了消除卡顿，
// 自己算四十毫秒就成了新的卡顿。
interface CushionBox {
  minX: number
  maxX: number
  minY: number
  maxY: number
}

const CUSHION_BOXES: CushionBox[] = CUSHION_SEGMENTS.map(([x1, y1, x2, y2]) => ({
  minX: Math.min(x1, x2) - BALL_RADIUS,
  maxX: Math.max(x1, x2) + BALL_RADIUS,
  minY: Math.min(y1, y2) - BALL_RADIUS,
  maxY: Math.max(y1, y2) + BALL_RADIUS,
}))

// cushionCheck 处理撞库：逐条线段算「球心到它的距离」。
//
// 判据是「球心到库边的距离小于一个球半径」，也就是说球心和库边之间
// 还差一个半径时就该接触——球是圆的，撞库时球心并没有贴到库边上。
//
// 直线段、45° 颊板、袋兜底走同一套判定：它们在物理上没有区别，
// 差别只在渲染怎么画。所以这里不要再按「直线 / 袋口」分两段代码。
function cushionCheck(ball: SimBall, trace: SimTrace): void {
  let hit = false

  for (let i = 0; i < CUSHION_SEGMENTS.length; i += 1) {
    const box = CUSHION_BOXES[i]
    if (ball.x < box.minX || ball.x > box.maxX || ball.y < box.minY || ball.y > box.maxY) {
      continue
    }
    const [x1, y1, x2, y2] = CUSHION_SEGMENTS[i]
    const segX = x2 - x1
    const segY = y2 - y1
    const lengthSq = segX * segX + segY * segY
    let t = 0
    if (lengthSq > 0) {
      t = ((ball.x - x1) * segX + (ball.y - y1) * segY) / lengthSq
    }
    if (t < 0) {
      t = 0
    }
    if (t > 1) {
      t = 1
    }
    // 最近点直接摊在局部变量里，不建对象：这个循环一杆要跑几十万次，
    // 每次返回一个 {x, y} 会给 GC 堆上几十万个马上要死的短命对象。
    const closestX = x1 + segX * t
    const closestY = y1 + segY * t
    const dx = ball.x - closestX
    const dy = ball.y - closestY
    const distanceSq = dx * dx + dy * dy
    if (distanceSq >= BALL_RADIUS * BALL_RADIUS || distanceSq < 1e-18) {
      continue
    }
    const distance = Math.sqrt(distanceSq)
    const nx = dx / distance
    const ny = dy / distance
    ball.x = closestX + nx * BALL_RADIUS
    ball.y = closestY + ny * BALL_RADIUS
    reflect(ball, nx, ny)
    hit = true
  }

  // 撞库后滚动方向变了，原来的滚动速度不再沿着新方向。这里与球球碰撞
  // 同样按「分离时已带一部分滚动」处理，否则撞库回来的球要多滑掉一截速度，
  // 翻袋和走位会比撞球明显更短，同一次碰撞在两种情形下手感不一致。
  if (hit) {
    ball.spin = spinAfterImpact(ball.vx, ball.vy)
    // 一条 event 就够：规则只关心「哪颗球碰过库」与「碰库发生在首碰之后」，
    // 同一颗球连撞两条库边不需要各记一条。
    emit(trace, 'cushion', ball.id, '')
  }
}

// reflect 让速度沿法向反弹，并扣掉一点切向速度。
//
// 切向那一项不是可选的：没有它，球撞库会原样弹出，手感像撞到橡皮墙，
// 而不像撞在包着呢子的橡胶上。
function reflect(ball: SimBall, nx: number, ny: number): void {
  const normal = ball.vx * nx + ball.vy * ny
  if (normal >= 0) {
    return
  }
  ball.vx -= (1 + CUSHION_RESTITUTION) * normal * nx
  ball.vy -= (1 + CUSHION_RESTITUTION) * normal * ny

  const tx = -ny
  const ty = nx
  const tangent = ball.vx * tx + ball.vy * ty
  ball.vx -= tangent * CUSHION_FRICTION * tx
  ball.vy -= tangent * CUSHION_FRICTION * ty
}

// applyFriction 按「滑动还是滚动」扣速度。
//
// 两档差二十倍，选错档的后果很直观：一直按滑动扣，球推两米就停；
// 一直按滚动扣，球会绕着台子滚十几圈。真实情况是滑动一段后转滚动：
// 出杆瞬间完全不打转，要滑到 5/7 初速才滚起来；碰撞后已经带着一半滚动
// （见 COLLISION_SPIN_CARRY），滑到 6/7 左右就接上了。
function applyFriction(ball: SimBall, dt: number): void {
  const speed = Math.sqrt(ball.vx * ball.vx + ball.vy * ball.vy)
  if (speed <= STOP_SPEED) {
    ball.vx = 0
    ball.vy = 0
    ball.spin = 0
    return
  }

  let next = speed
  if (ball.spin < speed) {
    // 滑动：球心减速的同时，摩擦把球转起来，直到转动跟上平移。
    const drop = SLIDE_DECEL * dt
    next = speed - drop
    ball.spin += drop * SPIN_GAIN
    if (ball.spin > next) {
      ball.spin = next
    }
  } else {
    next = speed - ROLL_DECEL * dt
    ball.spin = next
  }

  if (next <= STOP_SPEED) {
    ball.vx = 0
    ball.vy = 0
    ball.spin = 0
    return
  }
  const scale = next / speed
  ball.vx *= scale
  ball.vy *= scale
}

// allStopped 表示台面上已经没有球在动了。落袋的球不算。
function allStopped(sim: SimBall[]): boolean {
  for (const ball of sim) {
    if (!ball.onTable) {
      continue
    }
    if (ball.vx !== 0 || ball.vy !== 0) {
      return false
    }
  }
  return true
}

// snapshot 取当前所有还在台面上的球，压成一帧。
//
// 坐标保留四位小数，与服务端的帧一模一样：播放时两种帧会混在一起插值，
// 精度不一致会让球在切换的那一刻抖一下。
function snapshot(sim: SimBall[], t: number): ShotFrame {
  const balls: ShotFrame['balls'] = []
  for (const ball of sim) {
    if (!ball.onTable) {
      continue
    }
    balls.push([ball.id, round4(ball.x), round4(ball.y), 0])
  }
  return { t: round4(t), balls }
}

// round4 保留四位小数，与 pool-sim 输出一致（单位米，0.1 毫米精度）。
function round4(value: number): number {
  return Math.round(value * 10000) / 10000
}

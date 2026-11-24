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
// u_r 当前 0.0025，另外还叠了一份跟速度成正比的阻尼（见 ROLL_VISCOUS）。
//
// 视频标定值是 0.012（下面那段推导），用户先要求「阻尼再小一倍」，于是按字面
// 减半到 0.006；后来「中段太快、末段又 abrupt」的问题改由速度项去解决，
// 常数项让到 0.005。用户仍然觉得「末尾有急停感」，再减半到 0.0025。
//
// 为什么急停感出在常数项：屏幕上台长 2.24 米只映射到三百多像素，1 像素约 6 毫米。
// 常数项 0.049 m/s² 时，球从 0.3 m/s 滚到停只用 4.3 秒、走 58 厘米——
// 折算到屏幕上不到 9 个像素，整条尾巴被压成"几乎不动然后停住"。
// 减半之后同一段变成 6.9 秒、86 厘米（13 像素），看得见"越滚越慢地化掉"。
//
// 代价是满力空杆的总时长从 16.4 秒涨到 20.7 秒，MAX_DURATION 24 秒只剩 3 秒余量，
// 而真实对局里球撞库、撞球都会按比例掉速，到不了这个上限。再想更绵就往
// ROLL_VISCOUS 上找，不要继续压这一项。
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
const ROLL_FRICTION = 0.0025

// 滑动阶段球心减速度，以及转动让它提前进入纯滚动的比例。
// 球是实心球，转动惯量 2/5 mR²：滑动摩擦让球心减速 μg 的同时，
// 角速度以 5/2 倍 μg 增长，所以中心击球时球速降到 5/7 v0 就不再滑了。
const SLIDE_DECEL = SLIDE_FRICTION * GRAVITY
const ROLL_DECEL = ROLL_FRICTION * GRAVITY
const SPIN_GAIN = 2.5

// ROLL_VISCOUS 是滚动阻尼里跟速度成正比的那一份，单位 1/s。
//
// 只用常数阻尼时，球在中段几乎是匀速——满力那杆从 2.4 m/s 到 1.6 m/s 要走一秒多，
// 画面上是「一冲出去就一直那么快」，然后低速段又只剩 0.05 m/s² 的刹车，
// 看着像「忽然停住」。加一份与速度成正比的阻尼之后，减速度随速度一起下降：
// 高速段掉得快（3 m/s 时合计 0.5 m/s²，是常数项的十倍），低速段几乎只剩常数项，
// 整条速度曲线是平滑的指数衰减，没有「匀速巡航」也没有「突然刹车」。
const ROLL_VISCOUS = 0.15

// 低速段的额外阻尼：模拟台呢的绒被球压出一个浅坑，越慢越陷得住。
//
// 上面两个系数合起来是纯指数收尾：球从 0.1 m/s 到静止要三秒多，而这三秒只走
// 一两厘米——屏幕上不到 2 个像素，等于「看着停了、其实还在爬」，玩家反馈的
// 「突然停下」就是这段看不见的尾巴。加一段只在低速起作用的阻尼之后，收尾
// 有了几厘米可见位移，问题从「看不见」变成了「不均匀」：
//
// 第一版让额外阻尼随速度线性增大（v→0 时最大 0.45 m/s²），结果减速度全堆在
// 最后半秒——前 1 秒每 0.1 秒只掉 3 px/s（几乎匀速），最后 0.6 秒从 57 px/s
// 直落 6 px/s。玩家看到的正是「匀速滚，然后啪一下刹住」。这是**形状**问题，
// 光加位移量治不好：把系数调大只是把那一跤摔得更狠。
//
// 现在改成「平滑升到恒定减速度」：进 TAIL_SPEED 时额外阻尼是 0，在 TAIL_RAMP
// 这段速度区间里用 smoothstep 升满，之后一直保持 TAIL_DECEL。于是低速段是
// 恒定减速度——速度线性趋零，每 0.1 秒的位移均匀地少一点点，收尾两秒多，
// 全程都在减速，没有「先匀速后急刹」的分界。
//
// 实测（单球自由滚，手机竖屏约 294 px/m。VISIBLE 指人眼在 60fps 下还能看出
// 「在动」的速度，约 1 像素/帧 = 60 px/s）：
//
//   方案                    收尾时长   看得见在动   最后 1 秒位移   末段总位移
//   旧（阻尼随速度增大）    1.61s      约 1.0s      49px            108px
//   恒减速 0.45/0.26        2.06s      1.37s        44px            165px
//   恒减速 0.50/0.22（现在）2.49s      1.70s        38px            215px
//
// 旧方案「最后 1 秒位移」看着不小，但那 49 像素里有 40 像素集中在头 0.4 秒，
// 后 0.6 秒从 57 px/s 直落 6 px/s——玩家看到的正是「匀速滚完，啪一下刹住」。
// 换成恒减速之后，看得见在动的时长从 1.0 秒拉到 1.7 秒，越往后每帧走得越少，
// 最后 0.4 秒仍在以 20~40 px/s 挪。
//
// 末段位移 215 像素听着多，但它摊在 2.5 秒里，不是一下子多滑出去半张台。
// 代价是低速段的滚距被吃掉一点（两成力度 1.82m → 1.68m），量级远小于
// 力度条本身的刻度。
const TAIL_SPEED = 0.4
const TAIL_DECEL = 0.16
const TAIL_RAMP = 0.2

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

// 加塞（杆法）三个系数。协议里的 a 是左右旋、b 是高低杆，都是 -1 到 1。
//
// 这是二维模型，只能表达「绕竖直轴的侧旋」与「沿行进方向的高低杆」两种效果，
// 不画弧线（竖直轴的旋转本来也不产生弧线，弧线要靠抬杆的横轴旋转）。
//
// a 的物理意义是「库边把它往切线方向带多少」：出杆时按球速给一份切向速度，
// 撞库时把这份速度的一部分加成到反弹后的切向分量上，于是顺塞放大反弹角、
// 反塞收窄反弹角，同一副塞在相对的两条库上方向相反——真实球台就是这样。
//
// b 在母球第一次撞球时结算：正高低杆让母球跟着往前送，负高低杆把它拉回来。
// 只给母球，且只用一次，用完清零——被撞的球不该继承母球的高低杆。
//
// 三个数都不碰目标球的出球方向。目标球永远沿球心连线走，引导线才画得准；
// 真实台球还有「甩球」（侧旋把目标球推偏一点点），这里不建模。
const SIDE_SPIN_GAIN = 0.5
const CUSHION_SPIN_GRIP = 0.5
const CUSHION_SPIN_DECAY = 0.55
const FOLLOW_GAIN = 0.45

// 塞在「撞到球」那一刻干的活：甩球、外传、自己留一点。
//
// 之前塞只在撞库时兑现（反弹角变大变小），撞球时完全不起作用，
// 玩家把塞拉满正碰一杆，看到的是和空杆一模一样——那不是加塞，那是装饰。
//
// 真实台球里母球带塞撞上目标球时，接触点有侧向相对滑动，摩擦力把目标球
// 从球心连线推开几度，这就是甩球（throw）。Dr. Dave 的实测数据：
//   - 最大约每英尺 1 英寸，合 5°；
//   - 直球、慢速、中杆（stun）、约一半塞时最大，塞再大反而变小
//     （接触点从「抓得住」变成「打滑」）；
//   - 高杆低杆各把甩球压掉大约四分之三；
//   - 球速越快甩得越少，2 m/s 以上基本只剩几十分之一度。
// 方向：右塞把目标球往连线左侧甩，左塞反之（资料里写的是
// "right english throws it left"，指的都是相对连线的那一侧）。
//
// 另外两件事也一起做：
//   - 目标球被搓出一点反向侧旋（两颗球像齿轮那样咬合），它撞库时能看出来；
//   - 母球自己留下大部分侧旋——撞完它还要接着跑、接着撞库。
//
// 不做「偏杆」（squirt/deflection）：真实里加塞会让母球出射方向偏离球杆
// 指向 2~3°，但那样一来引导线画的就不是球实际走的方向，玩家会立刻
// 反馈「线在骗人」。这是有意省略，不是漏了。
const THROW_MAX_DEG = 5
const SHOT_SPIN_TRANSFER = 0.25
const SHOT_SPIN_KEEP = 0.75

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
// 视频里没有对应镜头，没有实测依据就不动它。
const SPEED_MIN = 0.05
const SPEED_MAX = 4.4

// 力度与球速是**线性**的：拉一半，出速就是一半。
//
// 这里曾经改成平方（想让低端更细腻，把力气让给轻碰与常规杆），实机被否掉了。
// 原因在于「玩家心里的力度」其实是距离，不是初速：
//
//   力度：   10%    20%    30%    40%    50%    70%   100%
//   平方： 0.06m  0.33m  0.94m  1.91m  3.18m  6.09m  12.0m
//   线性： 1.10m  2.43m  3.82m  5.06m  6.23m  8.38m  12.0m
//
// 平方之后 50% 只有 1.14 m/s、滚 3.2 米，而一杆正常的球要拉到 60% 以上才够，
// 条子下半截等于不存在。
//
// 线性之所以正好对上直觉，是因为滚动段几乎全是与速度成正比的阻尼
// （ROLL_VISCOUS，见上文），速度是指数衰减的，总路程 d ≈ v0 / k，
// 也就是距离几乎正比于初速。初速正比于力度，距离就正比于力度：
// 上表 50% 滚 6.23 米，恰好是满力 12.0 米的一半多一点。
// 若以后要动这条曲线，先按「距离」量一遍，不要按 m/s 判断。
//
// 上限仍是 4.4：开球不能因为改曲线变软。
//
// 上表的测法：单颗母球放在长边中线上自由滚（phi 指向长边），
// 把 120Hz 关键帧之间的位移累加成总路程，含库边反弹的损耗。
//
// speedOfPower 把 0 到 1 的力度换成母球出射速度。
//
// 引导线那边也要用它：甩球角按球速算，不按力度算，两处必须同一个映射，
// 否则「线把目标球画歪 2°」和「实际歪 1.5°」又会变成两张皮。
// power 由力度条给出（0~1），服务端也按这个区间校验。
export function speedOfPower(power: number): number {
  return SPEED_MIN + (SPEED_MAX - SPEED_MIN) * power
}

// spinThrowDeg 算一颗带塞的球撞上去时把目标球甩偏多少度（带符号，右塞为负）。
//
// a 是击球点左右比例（-1 到 1），speed 是撞前球速（m/s），b 是高低杆。
// 三条曲线都按上面那组实测数据标定，见 THROW_MAX_DEG 的注释。
export function spinThrowDeg(a: number, speed: number, b: number): number {
  const mag = Math.abs(a)
  if (mag < 1e-6 || speed <= 0) {
    return 0
  }
  // 半颗塞最甩：0 → 0，0.5 → 1，满塞 → 0.6。
  let spinFactor = 1
  if (mag <= 0.5) {
    spinFactor = mag / 0.5
  } else {
    spinFactor = 1 - 0.4 * ((mag - 0.5) / 0.5)
  }
  // 慢球甩得多：0.5 m/s 时 0.9，2 m/s 时 0.39，4 m/s 时 0.14。
  const speedFactor = 1 / (1 + (speed / 1.6) ** 2)
  // 高杆低杆压掉四分之三。
  const stunFactor = 1 - 0.75 * Math.abs(b)
  const deg = THROW_MAX_DEG * spinFactor * speedFactor * stunFactor
  return a > 0 ? -deg : deg
}

// 停球阈值。给到 0.1 mm/s——实际上等于「不设阈值」：
//
// 判定按积分步（1/240 秒）做，滚动的常数减速度是 0.049 m/s²，一步掉 0.2 mm/s。
// 所以球速只要落在 0.1 mm/s 下面，下一步一定会变成负数，于是就地归零，
// 真正停下的速度上限仍是 0.2 mm/s（约每 40 秒走一个像素），肉眼就是「滚到没」。
// 阈值本身只用来保证积分一定收敛，不再是观感上的旋钮：
// 早先把它设成 3 cm/s（手机上一秒走十来个像素），球还在慢慢挪却被一步清零，
// 看起来就是「说到就到」。
const STOP_SPEED = 0.0001

// 侧旋的自然衰减率（每秒），见 applyFriction。
const SIDE_DECAY = 0.8

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
// 后来滚动阻尼加了速度项，满力单球降到 12 秒左右，24 秒是两倍余量，不用再动。
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
//
// side 是侧旋（绕竖直轴），记成「库边能把它换成多少切向速度」，单位 m/s；
// bias 是高低杆残留（-1 到 1），只在母球上非零，第一次撞球时用掉。
// 这两个字段加起来才是玩家说的「加塞」，物理口径见上面那组常量的注释。
//
// px / py 是「本步开始前」的位置，只在一步之内有效：球球碰撞要用它解出
// 这一步里真正碰上的那一刻（见 contactTime），碰完就重新记。
interface SimBall {
  id: string
  x: number
  y: number
  px: number
  py: number
  vx: number
  vy: number
  spin: number
  side: number
  bias: number
  // english 是击球点左右比例（-1 到 1），只在母球上非零。
  // 与 side 分开是因为两者量纲不同：side 是 m/s 的侧向速度，会随塞衰减；
  // english 是「打在球面哪个位置」，撞球时算甩球角要用它。
  english: number
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
    sim.push({
      id: ball.id,
      x: ball.x,
      y: ball.y,
      px: ball.x,
      py: ball.y,
      vx: 0,
      vy: 0,
      spin: 0,
      side: 0,
      bias: 0,
      english: 0,
      onTable: true,
    })
  }
  const cue = sim.find((ball) => ball.id === 'cue')
  if (!cue) {
    return null
  }

  // 出杆：把这杆的速度直接给母球。中心击球时它先滑动再滚动，
  // 高低杆改变的是旋转，对二维轨迹影响很小，这里不建模。
  //
  // 现在建模了：b 不给初始速度（那一份要等撞球才体现），只挂在母球身上；
  // a 直接按球速折算成侧旋。两者都在 ±1 里，服务端也按这个范围校验。
  const speed = speedOfPower(params.power)
  cue.vx = Math.cos(params.phi) * speed
  cue.vy = Math.sin(params.phi) * speed
  cue.spin = 0
  cue.side = params.a * speed * SIDE_SPIN_GAIN
  cue.bias = params.b
  cue.english = params.a

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
    // 记下这一步的起点，碰撞要按「起点 + 速度」解出真实接触时刻。
    ball.px = ball.x
    ball.py = ball.y
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
//
// 第二轮不解精确接触时刻（exact 为 false）：那时速度已经换过一次，
// 再用「起点 + 新速度」去回推接触时刻，得到的是不存在的时间点。
function resolveBallCollisions(sim: SimBall[], trace: SimTrace): void {
  const touched = separateRound(sim, trace, true)
  if (touched) {
    separateRound(sim, trace, false)
  }
}

// separateRound 跑一轮两两检测，返回这一轮是否发生过接触。
function separateRound(sim: SimBall[], trace: SimTrace, exact: boolean): boolean {
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
      if (separate(a, b, exact)) {
        touched = true
        emit(trace, 'ball_ball', a.id, b.id)
      }
    }
  }
  return touched
}

// separate 把一对相撞的球推开并交换法向速度，返回是否真的接触过。
//
// exact 为真时先解出这一步内真正相切的时刻，把两颗球退回那一刻再算冲量。
// 不退回去的话，法向用的是「整步走完、已经互相陷进去」的球心连线，
// 母球满速一步走 18.3 毫米，球心连线被这半个球半径的穿透量带偏，
// 打薄球能偏出五到九度——玩家看到的就是「引导线指这里，球飞那边」。
function separate(a: SimBall, b: SimBall, exact: boolean): boolean {
  const contact = BALL_RADIUS * 2
  const dxMove = b.x - a.x
  const dyMove = b.y - a.y
  // 先用平方比，绝大多数（没碰上的）球对在这里就返回，省一次开方。
  if (dxMove * dxMove + dyMove * dyMove >= contact * contact) {
    return false
  }

  // 本步内「还要走多久才真的碰上」。退回这段时间后，球心连线就是接触法向。
  // 退回期间球还没交换速度，所以在冲量之后再按新速度把这段时间补走，
  // 位置不会凭空少掉一截。
  let rest = 0
  if (exact) {
    const toi = contactTime(a, b)
    if (toi > 0) {
      rest = STEP_DT - toi
      a.x -= a.vx * rest
      a.y -= a.vy * rest
      b.x -= b.vx * rest
      b.y -= b.vy * rest
    }
  }

  const dx = b.x - a.x
  const dy = b.y - a.y
  const distanceSq = dx * dx + dy * dy
  if (distanceSq < 1e-18) {
    // 两颗球的圆心重合，没有可用的法向。这一步当作碰上但不动，
    // 下一步它们按各自速度分开，再判一次。
    return true
  }
  const distance = Math.sqrt(distanceSq)
  const nx = dx / distance
  const ny = dy / distance

  // 走到接触时刻之后一般不再重叠；仍然重叠（起点就贴在一起、或者浮点误差）
  // 才各退一半把它摆平。
  const overlap = contact - distance
  if (overlap > 0) {
    a.x -= nx * overlap * 0.5
    a.y -= ny * overlap * 0.5
    b.x += nx * overlap * 0.5
    b.y += ny * overlap * 0.5
  }

  // 只看接近速度：已经分开的两颗球不再交换一次速度，
  // 否则贴在一起的球会自己抖起来。
  const relative = (b.vx - a.vx) * nx + (b.vy - a.vy) * ny
  if (relative < 0) {
    // 母球的高杆／低杆要在换速度之前记下来：换完之后方向已经变成切线，
    // 再想「沿来球方向往回拉」就没有依据了。
    const aIn = { vx: a.vx, vy: a.vy }
    const bIn = { vx: b.vx, vy: b.vy }

    // 高低杆在这两步里都要用，而 applyFollow 会把 bias 清零，
    // 所以先把撞前的值取出来。
    const aBias = a.bias
    const bBias = b.bias

    const impulse = (-(1 + BALL_RESTITUTION) * relative) / 2
    a.vx -= impulse * nx
    a.vy -= impulse * ny
    b.vx += impulse * nx
    b.vy += impulse * ny
    // 撞击后接触点重新打滑，但分离时已带着一部分滚动（见 COLLISION_SPIN_CARRY）。
    a.spin = spinAfterImpact(a.vx, a.vy)
    b.spin = spinAfterImpact(b.vx, b.vy)

    // 塞的效果要在换完速度之后、结算高低杆之前做：
    // 甩球是「把目标球已经拿到的速度方向拧一下」，高低杆是「沿来球方向补一脚」，
    // 反过来做的话母球那一脚会被当成塞的一部分。
    applyEnglish(a, b, aIn.vx, aIn.vy, aBias)
    applyEnglish(b, a, bIn.vx, bIn.vy, bBias)

    applyFollow(a, aIn.vx, aIn.vy)
    applyFollow(b, bIn.vx, bIn.vy)
  }

  // 补走「接触时刻 → 步末」的那段时间。
  if (rest > 0) {
    a.x += a.vx * rest
    a.y += a.vy * rest
    b.x += b.vx * rest
    b.y += b.vy * rest
  }
  return true
}

// applyEnglish 结算「母球上的塞在撞到球那一刻做了什么」。
//
// 三件事，量都按 THROW_MAX_DEG 那组实测数据标定：
//   1. 甩球：把目标球的速度方向拧偏几度，右塞往连线左侧甩；
//   2. 外传：目标球被搓出一点反向侧旋（齿轮咬合），它撞库时看得出来；
//   3. 母球留旋：塞不会凭空消失，撞完它还带着四分之三，接着去撞库。
//
// 只在 shooter 带着塞时生效；english 用一次就清零——一颗球只有一次「第一碰」。
// 动量在这里不守恒（只改目标球方向、没给母球反作用），这一点是有意忽略的：
// 甩球的反作用在真实台球里也只有母球速度的百分之几，画不出来。
function applyEnglish(
  shooter: SimBall,
  target: SimBall,
  inVx: number,
  inVy: number,
  bias: number,
): void {
  if (shooter.english === 0) {
    return
  }
  const before = Math.sqrt(inVx * inVx + inVy * inVy)
  const deg = spinThrowDeg(shooter.english, before, bias)
  if (deg !== 0) {
    const rad = (deg * Math.PI) / 180
    const cos = Math.cos(rad)
    const sin = Math.sin(rad)
    const vx = target.vx * cos - target.vy * sin
    const vy = target.vx * sin + target.vy * cos
    target.vx = vx
    target.vy = vy
  }
  target.side += -SHOT_SPIN_TRANSFER * shooter.side
  shooter.side *= SHOT_SPIN_KEEP
  shooter.english = 0
}

// applyFollow 结算母球的高低杆：撞完给一份沿「来球方向」的残留速度。
//
// bias 只能是母球上的非零值（其他球初始化时就是 0），所以这里不用另外判身份。
// 正数（高杆）顺着来球方向送出去，负数（低杆）沿反方向拉回来；
// 力度按撞前球速乘 FOLLOW_GAIN，不按出杆时的初速——母球滚到目标球时
// 已经掉了一截速度，用初速算会让远距离的低杆夸张得像倒车。
//
// 用完之后立刻清零：一颗球只有一次「第一碰」，撞第二颗时已经没有杆法了。
function applyFollow(ball: SimBall, inVx: number, inVy: number): void {
  if (ball.bias === 0) {
    return
  }
  const before = Math.sqrt(inVx * inVx + inVy * inVy)
  const bias = ball.bias
  ball.bias = 0
  if (before <= 0) {
    return
  }
  const kick = bias * FOLLOW_GAIN * before
  ball.vx += (inVx / before) * kick
  ball.vy += (inVy / before) * kick
  // 低杆是「反着转」，进滑动档慢慢被台呢吃掉；高杆顺着滚，直接接上滚动。
  const after = Math.sqrt(ball.vx * ball.vx + ball.vy * ball.vy)
  ball.spin = bias > 0 ? after * COLLISION_SPIN_CARRY : 0
}

// contactTime 解出本步内两球刚好相切（球心距 = 2R）的时刻，单位秒。
//
// 球心相对位置按 q(t) = q0 + w·t 走，令 |q(t)| = 2R 就是一个一元二次方程，
// 取较小的那个根。返回 0 表示「这一刻没有可退的余量」：
// 起点已经贴着、还在远离、或者这一步内根本擦不到——三种情况都按现行位置碰撞。
//
// 公式里 q 取「a 减 b」，所以 q·w > 0 是互相远离。
function contactTime(a: SimBall, b: SimBall): number {
  const contact = BALL_RADIUS * 2
  const qx = a.px - b.px
  const qy = a.py - b.py
  const wx = a.vx - b.vx
  const wy = a.vy - b.vy
  const far = qx * qx + qy * qy - contact * contact
  if (far <= 0) {
    return 0
  }
  const ww = wx * wx + wy * wy
  if (ww < 1e-18) {
    return 0
  }
  const qw = qx * wx + qy * wy
  if (qw >= 0) {
    return 0
  }
  const disc = qw * qw - ww * far
  if (disc < 0) {
    return 0
  }
  const t = (-qw - Math.sqrt(disc)) / ww
  if (t <= 0 || t >= STEP_DT) {
    return 0
  }
  return t
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

// reflect 让速度沿法向反弹，扣掉一点切向速度，并把侧旋的一部分换成切向速度。
//
// 切向那一项不是可选的：没有它，球撞库会原样弹出，手感像撞到橡皮墙，
// 而不像撞在包着呢子的橡胶上。
//
// 侧旋（加塞）在库边才真正体现价值：库边摩擦往切向推一把，顺塞把反弹角放大、
// 反塞把它收窄。写成「先把速度拆成法向／切向、再加塞、再扣摩擦、最后合成」，
// 比原来那种边减边算的写法好读，也省得符号搞混。
function reflect(ball: SimBall, nx: number, ny: number): void {
  const normal = ball.vx * nx + ball.vy * ny
  if (normal >= 0) {
    return
  }
  // 切线方向固定取 (-ny, nx)，与哪一面库无关：同一副塞在相对的两条库上
  // 方向相反，正是真实球台的表现。
  const tx = -ny
  const ty = nx
  let tangent = ball.vx * tx + ball.vy * ty
  tangent += ball.side * CUSHION_SPIN_GRIP
  tangent *= 1 - CUSHION_FRICTION
  ball.side *= CUSHION_SPIN_DECAY

  const bounce = -normal * CUSHION_RESTITUTION
  ball.vx = nx * bounce + tx * tangent
  ball.vy = ny * bounce + ty * tangent
}

// applyFriction 按「滑动还是滚动」扣速度。
//
// 两档差二十倍，选错档的后果很直观：一直按滑动扣，球推两米就停；
// 一直按滚动扣，球会绕着台子滚十几圈。真实情况是滑动一段后转滚动：
// 出杆瞬间完全不打转，要滑到 5/7 初速才滚起来；碰撞后已经带着一半滚动
// （见 COLLISION_SPIN_CARRY），滑到 6/7 左右就接上了。
//
// 滚动段的减速度由三份组成：台呢的常数项（ROLL_DECEL）、与速度成正比的粘性项
// （ROLL_VISCOUS），以及只在低速段出现的末段项（tailDecel）。前两份负责中高速
// 的指数式衰减，第三份把最后这一截从「看不见的爬行」改成「匀速地一点点停」。
function applyFriction(ball: SimBall, dt: number): void {
  const speed = Math.sqrt(ball.vx * ball.vx + ball.vy * ball.vy)
  if (speed <= STOP_SPEED) {
    ball.vx = 0
    ball.vy = 0
    ball.spin = 0
    ball.side = 0
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
    // 滚动：常数项 + 粘性项 + 低速段的末段项（见 tailDecel）。
    // 末段项让低速段变成恒定减速度，速度因此线性趋零，位移每 0.1 秒均匀地少一点。
    const tail = tailDecel(speed)
    const decel = ROLL_DECEL + ROLL_VISCOUS * speed + tail
    next = speed - decel * dt
    ball.spin = next
  }

  // 侧旋不参与滚动，按同一份阻力慢慢泄掉；完全不管的话，一颗撞了三次库
  // 还在爬的球会一直带着初速那份塞，反弹角越滚越夸张。
  ball.side *= 1 - SIDE_DECAY * dt

  if (next <= STOP_SPEED) {
    ball.vx = 0
    ball.vy = 0
    ball.spin = 0
    ball.side = 0
    return
  }
  const scale = next / speed
  ball.vx *= scale
  ball.vy *= scale
}

// tailDecel 返回低速段的额外减速度（单位 m/s²）。
//
// 速度高于 TAIL_SPEED 时返回 0，中高速完全不受影响。进末段之后，在 TAIL_RAMP
// 这段速度区间里用 smoothstep 从 0 升到 TAIL_DECEL，再往下一直保持 TAIL_DECEL。
// 用 smoothstep 而不是线性插值，是为了两端的一阶导都为零：减速度曲线自身没有
// 折点，速度曲线上就看不出「从哪一刻开始刹车」的接缝。
//
// 结果是低速段恒定减速度、速度线性趋零。这一点是「一点点停」的关键：
// 如果像第一版那样让额外阻尼随速度增大（v 越小阻尼越大），减速度会全堆在
// 最后半秒，前段接近匀速，观感就是「匀速滚完啪一下刹住」。
function tailDecel(speed: number): number {
  if (speed >= TAIL_SPEED) {
    return 0
  }
  let k = (TAIL_SPEED - speed) / TAIL_RAMP
  if (k > 1) {
    k = 1
  }
  return TAIL_DECEL * k * k * (3 - 2 * k)
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

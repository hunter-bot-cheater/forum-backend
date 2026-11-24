"""把一次出杆请求交给 pooltool 算完，压成前端能直接播的形状。

这一层保持无状态：不缓存对局、不记住上一杆，每次请求都从给定的球位重算。
好处是容器随时可以重启，坏处是每杆都要重建 System（几毫秒，可接受）。

单位约定：
- 位置与半径都是米；
- 角度都是弧度；
- 输出的 z 是球底离台面的腾空高度（球心高度减一个半径），与前端一致。
"""

import math
import time

import pooltool as pt
import attrs
from pooltool.evolution import continuize, simulate

from .schemas import BallIn, BallOut, CueIn, EventOut, FrameOut, SimulateRequest, SimulateResponse

# 球半径，与前端 web/src/game/table.ts 的 BALL_RADIUS 保持同一个数：
# 标准球（28.575mm）放大两成，为了让手机屏幕上的球看得清。
# 它同时影响两处：z 的换算（球底 → 球心）与下面 RACK 的摆球间距。
BALL_RADIUS = 0.03429

# 落袋状态码，取自 pooltool 的 BallState.s（0 静止 / 1 旋转 / 2 滑动 / 3 滚动 / 4 落袋）。
POCKETED = 4

# 力度到母球速度的映射：0 对应轻轻一推，1 对应满力开球。
# 上下限讲的是**母球离开杆头后的速度**，不是球杆速度，两者差一个质量比（见 cue_velocity）。
# 上限 7 m/s 是业余强手开球的量级（职业约 8 m/s）；再往上 pooltool 的接触模型
# 容易算出不现实的跳球，而且开球会变成「必进球 + 必摔袋」。
MIN_BALL_SPEED = 1.0
MAX_BALL_SPEED = 7.0

# 台面模型只解析一次。每次请求都新建 Table 会重复读一遍袋口与库边定义，
# 而它在本服务里永远不变。
_TABLE = pt.Table.default()

# 八球开局的 16 颗球位置，与前端 web/src/game/table.ts 的 RACK_EIGHTBALL 是同一份数据。
# 本服务不靠它算对局（开局由 Go 那边按对局状态下发球位），只用于预热与测试：
# 一份真实的摆球比手工编的球位更能代表线上会遇到的情况。
RACK_EIGHTBALL = [
    BallIn(id="cue", x=0.594371, y=0.455658),
    BallIn(id="4", x=0.4953, y=1.4859),
    BallIn(id="12", x=0.46101, y=1.545292),
    BallIn(id="5", x=0.52959, y=1.545292),
    BallIn(id="7", x=0.42672, y=1.604684),
    BallIn(id="8", x=0.4953, y=1.604684),
    BallIn(id="9", x=0.56388, y=1.604684),
    BallIn(id="15", x=0.39243, y=1.664076),
    BallIn(id="1", x=0.46101, y=1.664076),
    BallIn(id="13", x=0.52959, y=1.664076),
    BallIn(id="3", x=0.59817, y=1.664076),
    BallIn(id="6", x=0.35814, y=1.723468),
    BallIn(id="14", x=0.42672, y=1.723468),
    BallIn(id="11", x=0.4953, y=1.723468),
    BallIn(id="2", x=0.56388, y=1.723468),
    BallIn(id="10", x=0.63246, y=1.723468),
]


def _apex_aim() -> float:
    """开球时瞄准顶点球的方位角，单位弧度。

    顶球是三角形最靠开球端的那颗。母球在 x=0.5944，顶球在 x=0.4953，
    两者差了将近一个球径，所以「朝 +y 直推」会切到球堆侧面：母球带着大半速度
    飞出去、球堆只散开一半。必须按两颗球的实际位置算角度。
    """
    cue = RACK_EIGHTBALL[0]
    apex = RACK_EIGHTBALL[1]
    for ball in RACK_EIGHTBALL[2:]:
        if ball.y < apex.y:
            apex = ball
    return math.atan2(apex.y - cue.y, apex.x - cue.x)


# 标准开球的方向，约 1.6667 弧度（95.5 度）。预热与测试都用它。
RACK_AIM = _apex_aim()


def power_to_speed(power: float) -> float:
    """把 0 到 1 的力度换成母球的出射速度，单位 m/s。

    注意：线上跑的是**客户端算好的那一杆**（见 internal/pool/match.go 的
    handleShoot），这条换算只在客户端没有随请求带结果时用作兜底，实际不会被
    走到。两边必须同一条曲线：前端 web/src/game/predict.ts 的 speedOfPower 是
    线性的（力度一半＝球速一半，距离也就大约一半，实测表在那边），这里照抄。
    """
    return MIN_BALL_SPEED + power * (MAX_BALL_SPEED - MIN_BALL_SPEED)


def cue_velocity(system: pt.System, ball_speed: float) -> float:
    """把「母球要跑多快」换算成 pooltool 的 V0。

    pooltool 的 V0 是**球杆**的速度，不是母球的速度。中心击球时两者差一个
    完全弹性碰撞的质量比：v_ball = 2M / (M + m) * V0，M 是杆的质量、m 是球的质量，
    pooltool 的默认参数下这个系数约为 1.54。

    直接把目标球速当 V0 传进去，所有球都会快一半多：满力开球从 7 m/s 变成 10.8 m/s，
    表现是开球必进球甚至母球摔袋。这里用 System 上的真实质量反解，不写死 1.54。
    """
    ball = next(iter(system.balls.values()))
    ball_mass = ball.params.m
    cue_mass = system.cue.specs.M
    return ball_speed * (1.0 + ball_mass / cue_mass) / 2.0


def apply_cue(system: pt.System, cue: CueIn) -> None:
    """把击球参数写进 System。

    **pooltool 的 phi 与 theta 吃的是度数**（内部 `phi *= np.pi / 180`），
    而本服务与前端、Go 之间统一用弧度。换算只在这一处做。

    漏了这一步不会报错：把 1.5708 弧度当成 1.57 度，球永远朝 +x 走，
    表现为「无论怎么瞄都是往右打」。这个坑已经踩过一次。
    """
    system.cue.set_state(
        V0=cue_velocity(system, power_to_speed(cue.power)),
        phi=math.degrees(cue.phi),
        theta=math.degrees(cue.theta),
        a=cue.a,
        b=cue.b,
    )


def build_system(req: SimulateRequest) -> pt.System:
    """按请求里的球位搭一台 System。

    球号原样保留（'cue' 与 '1'...'15'），因为事件里回传的就是这些 id，
    Go 侧要按 id 找球，换成数字下标会让两边对不上。
    """
    balls = {}
    for item in req.balls:
        ball = pt.Ball.create(item.id, xy=(item.x, item.y))
        # z 是球底高度，pooltool 要的是球心高度，加回一个半径。
        ball.state.rvw[0][2] = item.z + BALL_RADIUS
        balls[item.id] = ball

    system = pt.System(cue=pt.Cue.default(), table=_TABLE, balls=balls)
    # 球集合决定球的质量与半径参数。用默认的 pooltool_pocket 与前端画的球一致。
    system.set_ballset(pt.objects.ball.sets.BallSet("pooltool_pocket"))
    # set_ballset 会把每颗球的参数重置成球集默认值（半径 28.575mm），
    # 与线上放大两成的球对不上：半径不对，接触距离、摩擦都跟着偏。
    # BallParams 是 frozen 的 attrs 类，只能整体换一个新的。
    for ball in system.balls.values():
        ball.params = attrs.evolve(ball.params, R=BALL_RADIUS)
    return system


# pooltool 的事件类型到本服务四种事件名的映射。
#
# 库边事件在 0.6.0 里叫 ball_linear_cushion，不是文档里那种笼统的 cushion。
# 按 "cushion" 过滤会把**所有撞库事件静默丢掉**，而八球判定「击球后必须有球碰库
# 或有球进袋」全靠它：丢掉的后果是每一杆都被判成犯规，且没有任何报错。
# 这张表由 tests/test_contract.py 里的事件名用例钉住，升级 pooltool 时会立刻变红。
EVENT_KIND = {
    "stick_ball": "stick_ball",
    "ball_ball": "ball_ball",
    "ball_pocket": "ball_pocket",
    "ball_linear_cushion": "cushion",
    "ball_circular_cushion": "cushion",
}


def collect_events(system: pt.System) -> list[EventOut]:
    """把 pooltool 的事件压成四条字段。

    pooltool 一杆要产生上千个事件，其中绝大多数是 sliding_rolling、
    rolling_stationary 这类状态切换，规则判定完全用不到；只留出杆、球撞球、
    撞库、落袋四类，一杆开球从 1134 条降到 500 条，响应体能小一半。

    ball_pocket 的第二个 id 是袋口名（lb/lc/lt/rb/rc/rt），规则不关心进的是哪个袋，
    但留着它能让人复盘时看出「这球是从哪个口漏进去的」。
    """
    events: list[EventOut] = []
    for event in system.events:
        kind = EVENT_KIND.get(str(event.event_type.value))
        if kind is None:
            continue
        ids = [str(i) for i in event.ids]
        events.append(
            EventOut(
                t=round(float(event.time), 4),
                type=kind,
                a=ids[0] if ids else None,
                b=ids[1] if len(ids) > 1 else None,
            )
        )
    return events


def pocketed_at(system: pt.System) -> dict[str, float]:
    """每颗球落袋的时刻。没落袋的球不在返回的字典里。"""
    result: dict[str, float] = {}
    for ball_id, ball in system.balls.items():
        for state in ball.history:
            if state.s == POCKETED:
                result[ball_id] = float(state.t)
                break
    return result


def build_frames(system: pt.System, duration: float, sample_hz: int) -> list[FrameOut]:
    """按固定频率采样，生成关键帧。

    用 continuize 而不是逐点 interpolate_ball_states：后者按「从最近的事件状态
    重新积分」来取每个时刻，16 颗球 × 160 帧要跑上百毫秒；continuize 一次把
    整条轨迹铺成固定 dt 的历史，同一杆从 100ms 降到 20ms 出头。

    落袋的球从落袋那一刻起不再出现在帧里，前端据此把它从画面上去掉。
    不这样处理的话，进袋的球会停在袋口继续被画出来，看起来像被卡住了。
    """
    if duration <= 0:
        return [FrameOut(t=0.0, balls=_frame_balls(system, 0.0))]

    continuize(system, dt=1.0 / sample_hz, inplace=True)
    dropped = pocketed_at(system)

    # 所有球共享同一组时间点（continuize 保证），取第一颗球的轨迹做时间轴。
    timelines: dict[str, list] = {}
    timeline: list[float] = []
    for ball_id, ball in system.balls.items():
        history = ball.history_cts
        if len(history) == 0:
            continue
        if not timeline:
            timeline = [float(state.t) for state in history]
        timelines[ball_id] = history

    frames: list[FrameOut] = []
    for index, t in enumerate(timeline):
        balls: list[list] = []
        for ball_id, history in timelines.items():
            drop_time = dropped.get(ball_id)
            if drop_time is not None and t >= drop_time:
                continue
            # 落袋球的历史会短一截，索引越界就跳过，不要拿最后一点位置顶替。
            if index >= len(history):
                continue
            position = history[index].rvw[0]
            balls.append(
                [
                    ball_id,
                    round(float(position[0]), 4),
                    round(float(position[1]), 4),
                    round(float(position[2]) - BALL_RADIUS, 4),
                ]
            )
        frames.append(FrameOut(t=round(t, 4), balls=balls))
    return frames


def _frame_balls(system: pt.System, t: float) -> list[list]:
    """没动过的一杆：直接按当前球位出一帧。"""
    balls: list[list] = []
    for ball_id, ball in system.balls.items():
        position = ball.state.rvw[0]
        balls.append(
            [
                ball_id,
                round(float(position[0]), 4),
                round(float(position[1]), 4),
                round(float(position[2]) - BALL_RADIUS, 4),
            ]
        )
    return balls


# 判断「这一帧之后球还在动」的球速阈值，单位 m/s。
#
# 这里必须按**速度**判，不能按「每帧走多少毫米」判：每帧位移里藏着帧率，
# 30Hz 下 2mm/帧 就是 66mm/s，球明明还在肉眼可见地滚，却被判成静止。
# 曾被砍掉的那一段有两厘米，前端为了让画面接得上，又在动画结束后补了一段
# 补间把球「搬」过去——玩家看到的就是「球慢慢滚着，忽然加速滑到一个位置停下」。
#
# 10mm/s 在手机屏上约 0.1 像素/帧，看着就是停了；此时离真正静止还剩不到
# 1mm，接上去不会有可见的跳变。
STOP_SPEED = 0.01


def trim_still_tail(frames: list[FrameOut]) -> list[FrameOut]:
    """砍掉末尾那段所有球都停下的帧。

    pooltool 的球不会在某一刻「啪」地停住：速度降到很低之后它还会继续爬，
    一杆轻推能爬五秒多。不截断的话，前端要按 duration 播满，
    玩家会盯着一个几乎不动的画面等四五秒。

    判据是帧间的**速度**（位移 / 这一帧的时长），不是位移本身：帧率一变，
    同一个位移阈值代表的球速就跟着变一倍，那会让截断点随采样率漂移。

    保留「最后一次有位移的帧」作为收尾：落袋的那一刻也要算位移
    （球从帧里消失），否则有球的最后一次动作会被当成静止而砍掉。
    """
    if len(frames) <= 1:
        return frames

    last_moved = 0
    for index in range(1, len(frames)):
        span = frames[index].t - frames[index - 1].t
        if _frame_moved(frames[index - 1], frames[index], span):
            last_moved = index
    return frames[: last_moved + 1]


def _frame_moved(prev: FrameOut, cur: FrameOut, span: float) -> bool:
    """两帧之间是否有任何一颗球动得比 STOP_SPEED 快。

    span 是两帧的时长（秒）。位移超过「阈值速度 × 时长」就算动过，
    这样 30Hz 与 120Hz 的采样得到的是同一个物理判据。

    球在某帧消失（落袋）或出现都算动过：这是画面上看得见的变化。
    """
    eps = STOP_SPEED * span
    before = {}
    for item in prev.balls:
        before[item[0]] = (item[1], item[2])
    for item in cur.balls:
        start = before.pop(item[0], None)
        if start is None:
            return True
        if abs(item[1] - start[0]) > eps or abs(item[2] - start[1]) > eps:
            return True
    # 还有球留在 before 里说明它这一帧不见了，即落袋。
    return len(before) > 0


def final_positions(system: pt.System) -> list[BallOut]:
    """终局球位。落袋的球 on_table 为 False，坐标仍给出来，便于回放时把球放回袋口。"""
    dropped = pocketed_at(system)
    result: list[BallOut] = []
    for ball_id, ball in system.balls.items():
        position = ball.state.rvw[0]
        result.append(
            BallOut(
                id=ball_id,
                x=round(float(position[0]), 4),
                y=round(float(position[1]), 4),
                z=round(float(position[2]) - BALL_RADIUS, 4),
                on_table=ball_id not in dropped,
            )
        )
    return result


def simulate_shot(req: SimulateRequest) -> SimulateResponse:
    """算一杆，返回轨迹、事件与终局球位。"""
    started = time.perf_counter()

    system = build_system(req)
    apply_cue(system, req.cue)
    simulate(system, inplace=True)

    if system.events:
        duration = max(float(event.time) for event in system.events)
    else:
        duration = 0.0

    # 帧先按完整时长采样再砍尾巴，duration 跟着砍后的帧走：
    # 前端拿 duration 当播放总时长，两者不一致会多播一段静止画面。
    frames = trim_still_tail(build_frames(system, duration, req.sample_hz))
    if frames:
        duration = float(frames[-1].t)

    return SimulateResponse(
        duration=round(duration, 4),
        events=collect_events(system),
        final=final_positions(system),
        frames=frames,
        sim_ms=int((time.perf_counter() - started) * 1000),
    )


def warmup() -> None:
    """启动时跑一杆，把 numba 的 JIT 编译提前做掉。

    不做这一步，第一个真实请求要多等七秒多——那是编译，不是算物理。
    用满台 16 球而不是两颗球，是因为不同球的代码路径不同，少一颗就少编译一段；
    而且开球是全套代码里最重的一条路径（几百次球球接触），预热它等于预热了一切。
    """
    req = SimulateRequest(balls=list(RACK_EIGHTBALL), cue=CueIn(phi=RACK_AIM, power=1.0))
    simulate_shot(req)

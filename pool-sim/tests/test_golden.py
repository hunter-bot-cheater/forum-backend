"""金样例：把一杆的具体物理结果钉死。

这一层的意义是「物理版本闸门」：pooltool 升级、摩擦参数被改、球集合换了，
这里立刻变红。确认新结果是对的之后，按下面两步重录：

  1. 把 GOLDEN_BREAK 清空成 {}，跑 pytest，失败信息里会打印实际值；
  2. 照着打印结果填回来，并在提交信息里写清为什么变。

数值统一按 4 位小数比，容差 2mm：同一份代码在同一台机器上是逐位可复现的，
容差只是为了不受不同平台数学库末位差异的影响。
"""

from app.simulate import RACK_AIM, RACK_EIGHTBALL
from support import ball, count_events, event_types, final_of, moved_balls, shot

# 容差 2mm。1e-6 那种精度跨平台守不住，2mm 又足够抓住任何真实改动。
TOLERANCE = 2e-3

# 满力开球（瞄准顶点球）的终局球位。开局状态是 RACK_EIGHTBALL，
# 所以这张表同时也是「标准开球长什么样」的记录。
GOLDEN_BREAK = {
    "cue": (0.5692, 0.9843),
    "1": (0.4576, 1.6546),
    "2": (0.5460, 1.8414),
    "3": (0.9075, 1.8325),
    "4": (0.9592, 1.1658),
    "5": (0.5852, 1.5024),
    "6": (0.2624, 1.7082),
    "7": (0.3993, 1.7580),
    "8": (0.7787, 1.6400),
    "9": (0.6166, 1.5514),
    "10": (1.0201, -0.0295),
    "11": (0.5462, 1.9425),
    "12": (0.4640, 1.5476),
    "13": (0.5837, 1.7245),
    "14": (0.3059, 1.9159),
    "15": (0.2252, 1.9492),
}

# 开球一次落袋的球（母球没进，这是合规开球）。
GOLDEN_BREAK_POCKETED = {"10"}

# 开球的事件构成。ball_ball 数量很扎眼（近五百次），但那是真实的：
# 十五颗球挤成三角形，杆一落下去就要解几百次接触。
GOLDEN_BREAK_EVENTS = {"stick_ball": 1, "ball_ball": 491, "cushion": 13, "ball_pocket": 1}

# 开球的总时长与帧数（30Hz 采样）。时长是砍掉静止尾巴之后的。
GOLDEN_BREAK_DURATION = 4.3667
GOLDEN_BREAK_FRAMES = 132


def rack_balls():
    """按开局摆球造一份球位。"""
    balls = []
    for item in RACK_EIGHTBALL:
        balls.append(ball(item.id, item.x, item.y))
    return balls


def test_break_final_positions_are_stable():
    """满力开球后每颗球停在哪里，逐颗对照。"""
    response = shot(rack_balls(), phi=RACK_AIM, power=1.0)

    actual = {}
    for item in response.final:
        actual[item.id] = (item.x, item.y)
    assert set(actual) == set(GOLDEN_BREAK), "球号集合变了"

    for ball_id, expected in GOLDEN_BREAK.items():
        got = actual[ball_id]
        assert abs(got[0] - expected[0]) < TOLERANCE, "%s 的 x 偏了：%.4f vs %.4f" % (ball_id, got[0], expected[0])
        assert abs(got[1] - expected[1]) < TOLERANCE, "%s 的 y 偏了：%.4f vs %.4f" % (ball_id, got[1], expected[1])


def test_break_pockets_the_same_ball():
    """同一杆落的是同一颗球。"""
    response = shot(rack_balls(), phi=RACK_AIM, power=1.0)

    pocketed = set()
    for item in response.final:
        if not item.on_table:
            pocketed.add(item.id)
    assert pocketed == GOLDEN_BREAK_POCKETED


def test_break_event_mix_is_stable():
    """事件构成不变：出杆一次、撞库十几次、落袋一次。"""
    response = shot(rack_balls(), phi=RACK_AIM, power=1.0)

    actual = {}
    for kind in ("stick_ball", "ball_ball", "cushion", "ball_pocket"):
        actual[kind] = count_events(response, kind)
    assert actual == GOLDEN_BREAK_EVENTS


def test_break_duration_and_frame_count():
    """时长与帧数：截断静止尾巴之后，开球不应超过 5 秒、也不该少于 100 帧。

    上限 5.5 秒是给物理参数微调留的余量；下限挡住「帧被误砍」这类回归：
    真砍多了，前端播放起来就是球在瞬移。
    """
    response = shot(rack_balls(), phi=RACK_AIM, power=1.0)

    assert response.duration == GOLDEN_BREAK_DURATION
    assert len(response.frames) == GOLDEN_BREAK_FRAMES


def test_break_spreads_the_rack():
    """开球必须真的把球堆打开，而且母球留在台面上。

    这是「开球能不能玩」的底线：球堆只散开一半、母球飞出台面，
    都说明球速映射或初速方向错了。
    """
    balls = rack_balls()
    response = shot(balls, phi=RACK_AIM, power=1.0)

    moved = moved_balls(response, balls, 0.05)
    assert len(moved) >= 12, "开球后只有 %d 颗球离开原位超过 5cm" % len(moved)
    assert final_of(response, "cue").on_table, "母球摔袋了"


def test_two_identical_shots_give_identical_results():
    """同一个请求跑两次，结果逐位相同。

    物理里没有随机源。若这条挂了，说明有人往链路里塞了随机数或时间相关的量，
    那样就没法做回放与断线重连后的状态校正了。
    """
    first = shot(rack_balls(), phi=RACK_AIM, power=1.0)
    second = shot(rack_balls(), phi=RACK_AIM, power=1.0)

    for one, other in zip(first.final, second.final):
        assert one.model_dump() == other.model_dump()
    assert len(first.frames) == len(second.frames)


def test_single_ball_rolls_to_a_known_spot():
    """单球中力撞右库，来回几趟停在 x=0.5221。

    这条同时锁住了库边弹性与台呢摩擦：任何一个参数动了，停的位置就变。
    """
    response = shot([ball("cue", 0.30, 0.60)], phi=0.0, power=0.5)

    cue = final_of(response, "cue")
    assert abs(cue.x - 0.5221) < TOLERANCE
    assert abs(cue.y - 0.60) < TOLERANCE
    assert count_events(response, "cushion") == 4


def test_light_push_stops_near_the_end_cushion():
    """轻推 0.2 力，球走到右库又慢慢滚回来，停在 0.9487。"""
    response = shot([ball("cue", 0.30, 0.60)], phi=0.0, power=0.2)

    cue = final_of(response, "cue")
    assert abs(cue.x - 0.9487) < TOLERANCE


def test_straight_shot_event_sequence():
    """直线撞库的事件顺序：先出杆，再连续撞库，没有别的东西混进来。"""
    response = shot([ball("cue", 0.30, 0.60)], phi=0.0, power=0.5)

    kinds = event_types(response)
    assert kinds[0] == "stick_ball"
    assert kinds[1:] == ["cushion"] * 4

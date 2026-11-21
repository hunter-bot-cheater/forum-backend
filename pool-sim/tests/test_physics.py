"""物理语义测试：锁的是「参数的单位与含义」，不是某一杆的具体数值。

这三条约定错了都不会报错，只会表现成「球往奇怪的方向走」：
phi 与 theta 是弧度、z 是球底高度、power 是**母球出射速度**而不是球杆速度。
具体数值的稳定性由 test_golden 负责。
"""

import math

import pytest
from pooltool.evolution import simulate as pool_simulate

from app.schemas import CueIn, SimulateRequest
from app.simulate import apply_cue, build_system, power_to_speed
from support import ball, final_of, shot

# 台面中心，离六个袋口都远，短距离移动不会撞库。
CENTER = (0.4953, 0.9906)


def departure_speed(power: float) -> float:
    """出杆瞬间母球的实际速度，单位 m/s。

    直接读 pooltool 的状态历史：history[0] 是击球前，history[1] 是击球后。
    走内部接口是有意的——这条约定在响应体里看不出来，只能这么验。
    """
    req = SimulateRequest(balls=[ball("cue", 0.5, 0.6)], cue=CueIn(phi=0.0, power=power))
    system = build_system(req)
    apply_cue(system, req.cue)
    pool_simulate(system, inplace=True)
    velocity = system.balls["cue"].history[1].rvw[1]
    return math.hypot(float(velocity[0]), float(velocity[1]))


@pytest.mark.parametrize("power", [0.0, 0.5, 1.0])
def test_power_means_ball_speed(power):
    """力度换算出来的必须是母球速度，误差 1%。

    pooltool 的 V0 是球杆速度，母球出射速度是它的 2M/(M+m) 倍（默认参数下 1.54）。
    把目标球速直接当 V0 传，所有球快一半多：满力开球从 7 m/s 变成 10.8 m/s，
    表现是开球必进球、母球经常摔袋。
    """
    assert departure_speed(power) == pytest.approx(power_to_speed(power), rel=0.01)


def test_center_hit_does_not_leave_the_cloth():
    """中心击球、杆尾不抬，球全程贴在台面上。

    z 的语义是球底离台面的高度。若把球心高度当 z 传进来，
    每颗球都会被当成在跳，画面上表现为球浮在半空、影子离得很远。
    """
    response = shot([ball("cue", CENTER[0], CENTER[1])], phi=0.0, power=0.4)
    for frame in response.frames:
        for item in frame.balls:
            assert abs(item[3]) < 1e-6


@pytest.mark.parametrize(
    "phi,axis,sign",
    [
        (0.0, "x", 1),
        (math.pi / 2, "y", 1),
        (math.pi, "x", -1),
        (3 * math.pi / 2, "y", -1),
    ],
)
def test_phi_quadrants(phi, axis, sign):
    """四个正方向各推一杆，第一帧的位移要朝对应的方向。

    phi 是弧度：0 指 +x（朝右库），π/2 指 +y（朝远端）。按度数传进来时，
    1.5708 会被当成 1.57 度，四杆全部朝 +x 走——「怎么瞄都往右打」。
    """
    response = shot([ball("cue", CENTER[0], CENTER[1])], phi=phi, power=0.3)
    start = response.frames[0].balls[0]
    moved = response.frames[1].balls[0]

    dx = moved[1] - start[1]
    dy = moved[2] - start[2]
    assert (dx if axis == "x" else dy) * sign > 0.01
    # 另一轴几乎不动：一帧之内偏出去超过 1mm 就说明方向错了。
    assert abs(dy if axis == "x" else dx) < 0.001


def test_object_ball_takes_the_energy():
    """正碰之后目标球往前走，母球停在碰撞点之前。"""
    balls = [ball("cue", 0.30, 0.60), ball("1", 0.70, 0.60)]
    response = shot(balls, phi=0.0, power=0.4)

    target = final_of(response, "1")
    cue = final_of(response, "cue")
    assert target.x > 0.75, "目标球没被推动"
    assert cue.x < 0.70, "母球应该停在目标球原位之后"


def test_side_spin_bends_the_cue_path():
    """加侧旋后母球撞完球会横向跑开，不加则是直的。

    侧旋参数 a 是前端暂时不暴露的能力，但链路必须通：
    Go 会把它透传，将来做「加塞」只要加一个 UI 就行。
    """
    balls = [ball("cue", 0.30, 0.60), ball("1", 0.70, 0.60)]
    straight = final_of(shot(balls, phi=0.0, power=0.4), "cue")
    spun = final_of(shot(balls, phi=0.0, power=0.4, a=0.3), "cue")

    assert abs(straight.y - 0.60) < 0.005
    assert abs(spun.y - 0.60) > 0.10


def test_elevated_cue_loses_speed():
    """杆尾抬高时同样的力度跑得近：一部分能量变成了向下的分量。"""
    flat = final_of(shot([ball("cue", 0.30, 0.60)], phi=0.0, power=0.5), "cue")
    tilted = final_of(shot([ball("cue", 0.30, 0.60)], phi=0.0, power=0.5, theta=0.6), "cue")

    assert tilted.x < flat.x - 0.1


def test_ball_never_leaves_the_table():
    """任何一杆结束后，还在台面上的球必须落在台面范围内。

    越界说明物理算漏了库边，画面上会看到球跑到木框外面去。
    """
    response = shot([ball("cue", 0.30, 0.60), ball("1", 0.70, 0.60)], phi=0.0, power=1.0)
    for item in response.final:
        if not item.on_table:
            continue
        assert -0.03 < item.x < 1.02
        assert -0.03 < item.y < 2.02

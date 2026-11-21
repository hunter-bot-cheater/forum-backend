"""测试辅助函数：拼请求、按球号取终局球位、数事件。

放在单独模块而不是 conftest 里，是因为用例要 import 它；
conftest 里定义的东西只能靠 fixture 传给用例，跨文件 import conftest 是反模式。
"""

from app.schemas import BallIn, CueIn, SimulateRequest
from app.simulate import simulate_shot


def ball(ball_id: str, x: float, y: float, z: float = 0.0) -> BallIn:
    """造一颗球。默认静止在台面上。"""
    return BallIn(id=ball_id, x=x, y=y, z=z)


def shot(balls, phi=0.0, power=0.5, theta=0.0, a=0.0, b=0.0, sample_hz=30):
    """直接调用仿真函数算一杆，不走 HTTP。

    协议形状由 test_contract 走 HTTP 验，物理语义用这个函数验：
    少一层序列化，用例失败时栈也短一截。
    """
    req = SimulateRequest(
        balls=balls,
        cue=CueIn(phi=phi, power=power, theta=theta, a=a, b=b),
        sample_hz=sample_hz,
    )
    return simulate_shot(req)


def final_of(response, ball_id: str):
    """按球号取终局球位。取不到就直接失败，别让 KeyError 混在用例里。"""
    for item in response.final:
        if item.id == ball_id:
            return item
    raise AssertionError("终局里没有球 %s，实际有：%s" % (ball_id, [b.id for b in response.final]))


def event_types(response) -> list[str]:
    """事件类型序列，按时间顺序。"""
    kinds = []
    for item in response.events:
        kinds.append(item.type)
    return kinds


def count_events(response, kind: str) -> int:
    """某一类事件出现了多少次。"""
    total = 0
    for item in response.events:
        if item.type == kind:
            total += 1
    return total


def moved_balls(response, balls, threshold: float) -> list[str]:
    """终局时离开原位超过 threshold 米的球号。"""
    start = {}
    for item in balls:
        start[item.id] = (item.x, item.y)
    moved = []
    for item in response.final:
        origin = start[item.id]
        if abs(item.x - origin[0]) > threshold or abs(item.y - origin[1]) > threshold:
            moved.append(item.id)
    return moved

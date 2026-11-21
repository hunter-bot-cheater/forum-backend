"""仿真服务的请求与响应模型。

字段名与前端 web/src/game/types.ts、后端 internal/pool 的 DTO 逐字对齐，
任何一端改名都要同时改另外两处，否则会出现「字段读不到但不报错」。
"""

from pydantic import BaseModel, Field


class BallIn(BaseModel):
    """一颗球的起始位置，单位米。

    z 是球底离台面的腾空高度，静止球为 0。它不是球心高度：
    球心静止时本来就在一个半径高处，直接传球心高度会让每颗球都被当成跳球。
    """

    id: str
    x: float
    y: float
    z: float = 0.0


class CueIn(BaseModel):
    """击球参数，六个浮点数。

    phi 是方位角，单位弧度，沿用 pooltool 的约定（0 指向 +x，π/2 指向 +y）。
    不接受度数：前端、Go、Python 三处都按弧度算，中间夹一个换算就多一处出错的机会。
    """

    phi: float
    power: float = Field(ge=0, le=1)
    theta: float = 0.0
    a: float = Field(default=0.0, ge=-1, le=1)
    b: float = Field(default=0.0, ge=-1, le=1)


class SimulateRequest(BaseModel):
    """一次出杆的完整输入。

    balls 至少一颗：pooltool 的 System 要求母球必须在球集合里，
    真正空台会直接抛 ValueError。与其让它 500，不如在协议层就回 422——
    对局里永远有母球，空列表只可能是调用方写错了。
    """

    table: str = "seven_foot"
    balls: list[BallIn] = Field(min_length=1)
    cue: CueIn
    sample_hz: int = Field(default=30, ge=10, le=120)


class BallOut(BaseModel):
    """球在某一时刻的位置。z 与 BallIn 同义。"""

    id: str
    x: float
    y: float
    z: float
    on_table: bool


class EventOut(BaseModel):
    """一次碰撞事件，供规则判定使用。

    type 取自 pooltool 的 EventType：stick_ball 是出杆、ball_ball 是球撞球、
    cushion 是撞库、ball_pocket 是落袋。a / b 是涉及的球号，只有一个球时 b 为空。
    """

    t: float
    type: str
    a: str | None = None
    b: str | None = None


class FrameOut(BaseModel):
    """一帧关键帧。balls 是 [球号, x, y, z] 的数组。"""

    t: float
    balls: list[list]


class SimulateResponse(BaseModel):
    duration: float
    events: list[EventOut]
    final: list[BallOut]
    frames: list[FrameOut]
    sim_ms: int

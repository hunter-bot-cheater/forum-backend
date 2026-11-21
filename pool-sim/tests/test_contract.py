"""契约测试：钉住 /simulate 的对外形状。

这一层不验物理算得对不对（那是 test_golden 的事），只验三样东西没变：
字段名、字段类型、事件名的取值。它们是 Go 与前端解析的全部依据，
改一个而没同步改另外两端，表现是「数据收到了但读不到」，一路没有报错。
"""

from app.simulate import RACK_AIM, RACK_EIGHTBALL


def payload(balls, phi=0.0, power=0.5, sample_hz=30):
    """拼一次 /simulate 的请求体。"""
    body = {
        "balls": balls,
        "cue": {"phi": phi, "power": power},
        "sample_hz": sample_hz,
    }
    return body


def test_healthz(client):
    """健康检查只回固定值，不碰 pooltool，容器探活才敢设 10 秒一次。"""
    response = client.get("/healthz")
    assert response.status_code == 200
    assert response.json() == {"status": "ok"}


def test_simulate_returns_the_five_fields(client):
    """五个顶层字段一个都不能少，类型也要对。"""
    response = client.post("/simulate", json=payload([{"id": "cue", "x": 0.3, "y": 0.6}]))
    assert response.status_code == 200
    body = response.json()

    assert set(body.keys()) == {"duration", "events", "final", "frames", "sim_ms"}
    assert isinstance(body["duration"], float)
    assert isinstance(body["sim_ms"], int)
    assert body["sim_ms"] >= 0


def test_final_echoes_every_ball_id(client):
    """终局必须把请求里的每颗球都报回来，一颗不能少。

    Go 侧按 id 覆盖对局里的球位，少一颗的后果是那颗球永远停在上一杆的位置。
    """
    balls = [
        {"id": "cue", "x": 0.3, "y": 0.6},
        {"id": "7", "x": 0.7, "y": 0.6},
        {"id": "15", "x": 0.5, "y": 1.4},
    ]
    body = client.post("/simulate", json=payload(balls, phi=0.0, power=0.3)).json()

    assert set(item["id"] for item in body["final"]) == {"cue", "7", "15"}
    for item in body["final"]:
        assert set(item.keys()) == {"id", "x", "y", "z", "on_table"}
        assert isinstance(item["on_table"], bool)


def test_frames_are_ordered_and_only_contain_known_balls(client):
    """帧的时间必须单调不减，帧里的球号必须来自请求。

    球号对不上是最难查的一类问题：前端按 id 找贴图，找不到就静默跳过，
    画面上表现为「有些球不见了」。
    """
    balls = [
        {"id": "cue", "x": 0.3, "y": 0.6},
        {"id": "7", "x": 0.7, "y": 0.6},
    ]
    body = client.post("/simulate", json=payload(balls, phi=0.0, power=0.3)).json()

    assert len(body["frames"]) > 1
    previous = -1.0
    for frame in body["frames"]:
        assert frame["t"] >= previous
        previous = frame["t"]
        ids = []
        for item in frame["balls"]:
            assert len(item) == 4
            ids.append(item[0])
        assert len(ids) == len(set(ids)), "同一帧里出现重复球号：%s" % ids
        assert set(ids) <= {"cue", "7"}


def test_duration_covers_every_frame(client):
    """播完所有帧需要的时间不能超过 duration，否则前端会在半路被掐断。"""
    body = client.post("/simulate", json=payload([{"id": "cue", "x": 0.3, "y": 0.6}])).json()
    assert body["duration"] >= body["frames"][-1]["t"] - 1e-6


def test_pocketed_ball_is_reported_and_disappears_from_frames(client):
    """落袋的球要带 on_table=false，并从落袋那帧起不再出现在帧里。

    母球摆在 y=0.9906（右中袋的正对面），轻轻一推就直线进袋，
    这条用例同时锁住了「袋口坐标」与「落袋后从画面去掉」两件事。
    """
    body = client.post(
        "/simulate",
        json=payload([{"id": "cue", "x": 0.3, "y": 0.9906}], phi=0.0, power=0.2),
    ).json()

    pockets = [item for item in body["events"] if item["type"] == "ball_pocket"]
    assert pockets, "母球直冲袋口却没有 ball_pocket 事件"
    assert pockets[0]["a"] == "cue"
    assert pockets[0]["b"] == "rc", "右中袋的口名是 rc"

    assert body["final"][0]["on_table"] is False
    assert body["frames"][-1]["balls"] == [], "落袋的球不应留在最后一帧"


def test_cushion_event_survives_the_filter(client):
    """撞库事件必须出现在响应里。

    pooltool 0.6.0 的库边事件叫 ball_linear_cushion，不是 cushion；
    早先按 "cushion" 过滤会把它们全部丢掉，而八球判定「击球后球必须碰库或有球进袋」
    完全依赖它——丢掉之后每一杆都被判成犯规，且没有任何报错。
    """
    body = client.post(
        "/simulate",
        json=payload([{"id": "cue", "x": 0.3, "y": 0.6}], phi=0.0, power=0.2),
    ).json()

    kinds = [item["type"] for item in body["events"]]
    assert "cushion" in kinds
    for kind in kinds:
        assert kind in ("stick_ball", "ball_ball", "cushion", "ball_pocket")


def test_break_reports_all_four_kinds(client):
    """开球是最全的一条路径：出杆、球撞球、撞库、落袋一个都不会少。"""
    balls = []
    for item in RACK_EIGHTBALL:
        balls.append({"id": item.id, "x": item.x, "y": item.y})
    body = client.post("/simulate", json=payload(balls, phi=RACK_AIM, power=1.0)).json()

    kinds = set()
    for item in body["events"]:
        kinds.add(item["type"])
    assert kinds == {"stick_ball", "ball_ball", "cushion", "ball_pocket"}


def test_empty_ball_list_is_rejected(client):
    """空台要 422，不能 500。

    pooltool 的 System 要求母球在球集合里，真空台会抛 ValueError。
    对局里永远有母球，空列表只可能是调用方拼错了请求，
    在协议层拒掉比让它变成 500 更容易定位。
    """
    response = client.post("/simulate", json=payload([]))
    assert response.status_code == 422


def test_power_out_of_range_is_rejected(client):
    """力度越界要 422，不能算出一个飞出地球的球速。"""
    body = payload([{"id": "cue", "x": 0.3, "y": 0.6}], power=1.5)
    assert client.post("/simulate", json=body).status_code == 422


def test_missing_phi_is_rejected(client):
    """phi 是必填：缺了它 pooltool 会按 90 度打，看着「能跑」其实方向全错。"""
    body = {"balls": [{"id": "cue", "x": 0.3, "y": 0.6}], "cue": {"power": 0.5}}
    assert client.post("/simulate", json=body).status_code == 422


def test_sample_hz_out_of_range_is_rejected(client):
    """采样率有上下限：太低会看到球瞬移，太高响应体撑爆带宽。"""
    body = payload([{"id": "cue", "x": 0.3, "y": 0.6}], sample_hz=5)
    assert client.post("/simulate", json=body).status_code == 422

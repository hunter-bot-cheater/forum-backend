# pool-sim

台球物理服务。把 [pooltool](https://github.com/ekiefl/pooltool) 包成一个无状态的 HTTP 服务：
**只有它算物理**，对局状态、中式八球规则、连接管理全在 Go 那边。

## 为什么单独一个容器

pooltool 依赖 numpy / numba / scipy / panda3d，装进 Go 镜像会让镜像从 20MB 涨到 600MB 以上，
而且 numba 的 JIT 与 Go 的运行时抢同一份内存。拆开之后：

- 这个服务**无状态**：每次请求都从给定的球位重算，容器随时可以重启，
  崩了只会让正在出杆的那一杆失败，不会让对局丢失；
- Go 侧只管把球位传进来、把轨迹广播出去，不需要理解任何物理。

## 接口

### `GET /healthz`

```json
{"status": "ok"}
```

不碰 pooltool，只用于容器探活。

### `POST /simulate`

请求（球位单位米，角度单位**弧度**）：

```json
{
  "balls": [{"id": "cue", "x": 0.594371, "y": 0.455658, "z": 0}],
  "cue": {"phi": 1.6667, "power": 1.0, "theta": 0, "a": 0, "b": 0},
  "sample_hz": 30
}
```

| 字段 | 含义 |
| --- | --- |
| `balls[].id` | 球号，`cue` 是母球，其余是 `"1"`…`"15"`，原样回传 |
| `balls[].z` | 球**底**离台面的高度，静止球为 0。不是球心高度 |
| `cue.phi` | 方位角（弧度）。0 指向 +x（右库），π/2 指向 +y（远端） |
| `cue.power` | 0…1。0 是轻推，1 是满力开球，对应母球出射速度 1…7 m/s |
| `cue.theta` | 杆尾抬起的角度（弧度），0 是水平 |
| `cue.a` / `cue.b` | 击球点偏移，-1…1。a 是左右塞，b 是高低杆 |
| `sample_hz` | 关键帧采样率，10…120，默认 30 |

响应：

```json
{
  "duration": 4.3667,
  "events": [{"t": 0.0, "type": "stick_ball", "a": "cue_stick", "b": "cue"}],
  "final": [{"id": "cue", "x": 0.5692, "y": 0.9843, "z": 0, "on_table": true}],
  "frames": [{"t": 0.0, "balls": [["cue", 0.5944, 0.4557, 0.0]]}],
  "sim_ms": 688
}
```

- `events` 只保留四类：`stick_ball`（出杆）、`ball_ball`（球撞球）、`cushion`（撞库）、
  `ball_pocket`（落袋，第二个 id 是袋口名 `lb/lc/lt/rb/rc/rt`）。pooltool 一杆会产生上千条
  `sliding_rolling` 这类状态切换事件，全部丢弃。
- `frames` 是给前端直接播的：`[球号, x, y, z]`。落袋的球从落袋那帧起不再出现。
- `duration` 已经砍掉末尾静止段（见下文「静止尾巴」），前端拿它当播放总时长。
- `sim_ms` 是服务端耗时，用于观察性能，不参与业务。

## 三条容易踩的约定

**pooltool 的角度是度数。** `system.cue.set_state(phi=...)` 内部会 `phi *= π/180`。
把弧度直接传进去不会报错，只会让球永远朝 +x 走——「怎么瞄都往右打」。
换算只在 `apply_cue` 一处做。

**`V0` 是球杆速度，不是球速。** 中心击球时两者差一个完全弹性碰撞的质量比：
`v_ball = 2M/(M+m) · V0`，默认参数下约 1.54 倍。把目标球速当 `V0` 传，满力开球会从
7 m/s 变成 10.8 m/s，表现是开球必进球、母球经常摔袋。`cue_velocity()` 负责反解。

**库边事件叫 `ball_linear_cushion`。** 按 `"cushion"` 过滤会把撞库事件全部丢掉，
而八球判定「击球后必须有球碰库或有球进袋」全靠它：丢掉的后果是每一杆都被判成犯规，
且没有任何报错。`EVENT_KIND` 这张表由 `tests/test_contract.py` 钉住。

## 静止尾巴

pooltool 不把慢球直接停住，而是让它们以毫米级速度继续爬：一杆轻推能"动"五秒多。
`trim_still_tail()` 砍掉末尾所有球位移都小于 2mm 的帧，`duration` 跟着砍后的帧走。
不砍的话前端会播四五秒的静止画面。

## 本地跑

项目里没有给 pool-sim 单独建 venv，本机用的是 `D:\go学习\3d8balls\.venv`（已经装好 pooltool 0.6.0）。

```bash
cd pool-sim

# 起服务（改代码会自动重载）
POOL_SIM_SKIP_WARMUP=1 ../../3d8balls/.venv/Scripts/python.exe -m uvicorn app.main:app --port 8000

# 打一杆
curl -s localhost:8000/simulate -H 'content-type: application/json' -d '{
  "balls":[{"id":"cue","x":0.594371,"y":0.455658}],
  "cue":{"phi":1.6667,"power":1.0}
}'
```

不设 `POOL_SIM_SKIP_WARMUP` 时启动会先跑一杆满台开球做 JIT 预热，约五秒。

## 测试

```bash
cd pool-sim
../../3d8balls/.venv/Scripts/python.exe -m pytest        # 33 条，约 7 秒
```

容器里跑（Linux 上与本地数值若有差异，`test_golden` 会先报出来）：

```bash
docker compose build pool-sim-test
docker compose run --rm pool-sim-test
```

- `test_contract.py` 走 HTTP，锁字段名、字段类型、事件名取值；
- `test_physics.py` 锁参数的单位与含义（弧度、z 是球底、power 是球速）；
- `test_golden.py` 锁一杆的具体结果。物理版本变了会红，确认新结果正确后按
  `test_golden.py` 顶部注释重录。

## 实测性能

Windows / Python 3.13 / 单进程，30Hz 采样：

| 场景 | 耗时 | 帧数 | 响应体 |
| --- | --- | --- | --- |
| 满力开球（16 球、491 次球球接触） | 约 700–1500ms | 130 | 约 60KB |
| 两球对撞 | 3–10ms | 50–110 | 约 20KB |
| 单球撞库 | 3–5ms | 150 | 约 10KB |
| 首杆冷启动（JIT 未预热） | 7.5s | — | — |

开球是唯一的重活，一局只发生一次；Go 侧的超时按 10 秒设，别按平均值设。

## 镜像

`Dockerfile` 两个阶段：`runtime`（默认，只有 app 与运行依赖）与 `test`（加 pytest/httpx 与 tests）。
以 uid 10001 运行，与 api 容器一致。`NUMBA_CACHE_DIR=/tmp/numba` 指到容器内可写目录，
否则非 root 用户写缓存会失败。

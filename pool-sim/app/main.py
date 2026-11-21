"""pool-sim：把 pooltool 的物理包装成一个无状态的 HTTP 服务。

对外只有两个接口：/healthz 与 /simulate。所有对局状态都在 Go 那边，
这里每次请求都从给定的球位重算，所以容器崩了重启不影响任何进行中的对局。
"""

import logging
import os
from contextlib import asynccontextmanager

from fastapi import FastAPI, HTTPException

from .schemas import SimulateRequest, SimulateResponse
from .simulate import simulate_shot, warmup

logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
log = logging.getLogger("pool-sim")


@asynccontextmanager
async def lifespan(_app: FastAPI):
    """启动时预热一杆。

    numba 的 JIT 编译要七秒多，放在第一个真实请求里做，玩家会以为服务挂了。
    预热失败不阻断启动：物理本身可用，只是第一杆会慢，这比整个容器起不来要好。
    """
    started = []
    # 契约测试会连容器一起起，预热五秒会让每次跑测试都白等；
    # 生产环境不设这个变量，走完整预热。
    if os.environ.get("POOL_SIM_SKIP_WARMUP") == "1":
        log.info("跳过预热（POOL_SIM_SKIP_WARMUP=1）")
        started.append("warmup skipped")
    else:
        try:
            warmup()
            started.append("warmup ok")
            log.info("预热完成")
        except Exception:
            log.exception("预热失败，第一杆会明显变慢")
            started.append("warmup failed")
    yield
    log.info("shutdown: %s", ",".join(started))


app = FastAPI(title="pool-sim", version="0.1.0", lifespan=lifespan)


@app.get("/healthz")
def healthz() -> dict[str, str]:
    """容器健康检查。只回一个固定值，不碰 pooltool。"""
    return {"status": "ok"}


@app.post("/simulate", response_model=SimulateResponse)
def simulate(req: SimulateRequest) -> SimulateResponse:
    """算一杆。

    用同步 def 而不是 async def：物理计算是纯 CPU 活，FastAPI 会把同步端点
    放进线程池，不会卡住事件循环；写成 async 反而要自己再包一层线程。
    """
    try:
        return simulate_shot(req)
    except ValueError as err:
        # 球位重叠、球号不认识这类输入问题：回 422 表示「这一杆算不了」，
        # Go 侧会把它当成玩家可重试的失败。
        # 把请求一起打进日志：pooltool 的报错往往是「argmin of an empty sequence」
        # 这种不指向任何字段的话，只有看到球位才能判断到底是哪一颗球的问题。
        log.warning("simulate rejected: %s | req=%s", err, req.model_dump_json())
        raise HTTPException(status_code=422, detail=str(err)) from err
    except Exception as err:  # noqa: BLE001 - 兜底也要带原因
        # 不兜的话 Starlette 只回一行 "Internal Server Error"，
        # Go 日志里除了状态码什么都没有，排查时等于没有信息。
        log.exception("simulate failed | req=%s", req.model_dump_json())
        raise HTTPException(
            status_code=500, detail=f"{type(err).__name__}: {err}"
        ) from err

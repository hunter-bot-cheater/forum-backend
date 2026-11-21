"""pool-sim：把 pooltool 的物理包装成一个无状态的 HTTP 服务。

对外只有两个接口：/healthz 与 /simulate。所有对局状态都在 Go 那边，
这里每次请求都从给定的球位重算，所以容器崩了重启不影响任何进行中的对局。
"""

import logging
import os
from contextlib import asynccontextmanager

from fastapi import FastAPI

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
    return simulate_shot(req)

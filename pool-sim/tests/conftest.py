"""测试用的公共装置：只在进入 TestClient 时生效的准备动作。

跑法（本机没有把 pooltool 装进项目里，用的是 3d8balls 那份 venv）：

    cd pool-sim
    ..\\..\\3d8balls\\.venv\\Scripts\\python.exe -m pytest

容器里则直接 `docker compose run --rm pool-sim pytest`。
"""

import os

# 预热要五秒多（numba 编译），测试里等它没有意义。
# 这个变量必须在 import app 之前设好：lifespan 是在 TestClient 进入时才跑的，
# 而 app.main 在导入时就会把 FastAPI 实例建出来。
os.environ["POOL_SIM_SKIP_WARMUP"] = "1"

import pytest
from fastapi.testclient import TestClient

from app.main import app


@pytest.fixture(scope="session")
def client():
    """整个会话共用一个 TestClient。

    用 with 而不是直接构造：只有进入上下文才会触发 lifespan，
    否则拿到的是一个没跑过启动钩子的实例。
    """
    with TestClient(app) as test_client:
        yield test_client

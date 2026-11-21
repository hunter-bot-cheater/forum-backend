# pool-sim 的运行镜像。
#
# 依赖都有 manylinux wheel（numpy / numba / scipy / panda3d / pooltool），
# 不需要 gcc，因此不装 build-essential，镜像能小一百多兆。
#
# 分两个阶段：runtime 是默认产出（只有 app 与运行依赖），
# test 额外装 pytest 与 tests，给 `docker compose run --rm pool-sim-test` 用。
# 测试依赖不进 runtime，既省十几兆，也少一个能被利用的入口。

FROM python:3.13-slim AS base

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    NUMBA_CACHE_DIR=/tmp/numba

WORKDIR /app

# 先单独拷依赖清单：改代码时这一层能命中缓存，不用重装几百兆的包。
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt


FROM base AS runtime

COPY app ./app

# 以非 root 运行。uid 与 api 容器保持一致（10001），
# 出问题时宿主机上按 uid 找文件不会多出一堆陌生属主。
RUN useradd --create-home --uid 10001 pool \
    && mkdir -p /tmp/numba \
    && chown -R 10001:10001 /tmp/numba /app
USER 10001

EXPOSE 8000

# 单 worker：numba 的多进程会让每个进程各编译一份，内存直接翻倍。
# 一杆十几毫秒到一杆开球一秒多，单进程足够撑住几十局同时出杆。
CMD ["uvicorn", "app.main:app", "--host", "0.0.0.0", "--port", "8000", "--workers", "1"]


FROM base AS test

COPY requirements-dev.txt ./
RUN pip install --no-cache-dir -r requirements-dev.txt

COPY app ./app
COPY tests ./tests
COPY pytest.ini ./

# 测试阶段不切非 root：pytest 要写 .pytest_cache，uid 10001 对 /app 没有写权限。
CMD ["python", "-m", "pytest", "-q"]

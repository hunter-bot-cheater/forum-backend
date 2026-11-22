# pool-sim 的运行镜像。
#
# 依赖都有 manylinux wheel（numpy / numba / scipy / panda3d / pooltool），
# 不需要 gcc，因此不装 build-essential，镜像能小一百多兆。
#
# 分两个阶段：runtime 是默认产出（只有 app 与运行依赖），
# test 额外装 pytest 与 tests，给 `docker compose run --rm pool-sim-test` 用。
# 测试依赖不进 runtime，既省十几兆，也少一个能被利用的入口。
# 顺序上 runtime 必须放最后：不指定 target 时 Docker 构建最后一段。

# 3.12 而不是 3.13：本机 venv 就是 3.12，金样例是在这个解释器上跑出来的。
# pooltool 的几个依赖（numba / scipy / panda3d）在 3.13 上要么没有 wheel、
# 要么版本对不上，而物理结果对版本敏感，没必要在这上面赌。
FROM python:3.12-slim AS base

ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    PIP_NO_CACHE_DIR=1 \
    NUMBA_CACHE_DIR=/tmp/numba

WORKDIR /app

# 先单独拷依赖清单：改代码时这一层能命中缓存，不用重装几百兆的包。
COPY requirements.txt ./
RUN pip install --no-cache-dir -r requirements.txt
# pooltool 本体单独装，且不解析依赖：它的元数据把 Linux 上的 panda3d 钉在
# `1.11.0.dev3702`，那个版本从未发布到 PyPI，交给 pip 去解析必然失败。
# 它真正需要的包已经在上一行逐条装好了（清单见 requirements.txt 顶部注释）。
RUN pip install --no-cache-dir --no-deps pooltool-billiards==0.6.0


# 测试阶段。要放在 runtime 前面：`docker compose build pool-sim` 没有指定 target，
# Docker 默认构建**最后一个**阶段。之前 runtime 写在最后一段之后，跑起来的容器
# 命令是 `python -m pytest -q`，起来就退出，api 出杆全是「球桌算不动」。
FROM base AS test

COPY requirements-dev.txt ./
RUN pip install --no-cache-dir -r requirements-dev.txt
# 测试同样要 import pooltool，理由与 runtime 阶段那条一致。
RUN pip install --no-cache-dir --no-deps pooltool-billiards==0.6.0

COPY app ./app
COPY tests ./tests
COPY pytest.ini ./

# 测试阶段不切非 root：pytest 要写 .pytest_cache，uid 10001 对 /app 没有写权限。
CMD ["python", "-m", "pytest", "-q"]


# 运行阶段，也是默认构建目标，必须留在最后一段。
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


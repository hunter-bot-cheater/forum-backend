#!/usr/bin/env bash
# 本机交叉编译 Linux 产物，供服务器「只打镜像」用（见 docs/10 第 13.5 节）。
#
# 解决的是这个问题：2 核 2G 的机器上，拉 golang 镜像 + go build + npm ci + vite build
# 要好几分钟，内存不够时还会被 OOM 杀掉。本机把编译做完，服务器那一步只剩 COPY，
# 十几秒结束。
#
# 产物落在 .build/ 下，正好是两个镜像的构建上下文：
#   .build/api  forum-server、forum-migrate、config/config.yaml、Dockerfile
#   .build/web  dist/、default.conf、Dockerfile
# 把 Dockerfile 也拷进去，是为了让「上下文」与「Dockerfile」在同一个目录里，
# compose 那边不用处理「dockerfile 在上下文之外」这种边界情况。
#
# 用法：
#   scripts/pack-linux.sh              # 前后端都编译
#   scripts/pack-linux.sh --api-only   # 只改了后端
#   scripts/pack-linux.sh --web-only   # 只改了前端
#   scripts/pack-linux.sh --tar        # 额外打一个 tar.gz，方便 scp
#   scripts/pack-linux.sh --allow-dirty # 工作区有未提交改动时也打包（仅测试用）
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(dirname "$SCRIPT_DIR")"
OUT_DIR="$REPO_DIR/.build"

do_api=true
do_web=true
make_tar=false
allow_dirty=false

for arg in "$@"; do
  case "$arg" in
    --api-only)    do_web=false ;;
    --web-only)    do_api=false ;;
    --tar)         make_tar=true ;;
    --allow-dirty) allow_dirty=true ;;
    -h|--help)
      # 从「用法：」那行打到 set -euo 之前，行号会随文件头注释变化，所以按内容取
      awk '/^# 用法：/{f=1} f && /^set -euo/{exit} f{sub(/^# ?/,""); print}' "${BASH_SOURCE[0]}"
      exit 0
      ;;
    *)
      echo "未知参数：$arg（可用 --api-only / --web-only / --tar / --allow-dirty）" >&2
      exit 1
      ;;
  esac
done

cd "$REPO_DIR"

# 标签由「日期 + commit 短哈希」拼成，例如 1118-0228-7d2ac1f。
# 带哈希是为了让 docker images 里每一行都能对回 git 上的那次提交：
# 只有时间戳的话，隔几天就分不清 1116-2045 到底是哪一版。
GIT_SHA="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
IMAGE_TAG="$(date +%m%d-%H%M)-${GIT_SHA}"
DIRTY_COUNT="$(git status --porcelain | wc -l | tr -d ' ')"

# 工作区有未提交改动时拒绝打包：产物内容和 tag 上那个 commit 对不上，
# 线上出问题时会被这个哈希带偏，回退也找不到对应源码。确实要打测试包就加 --allow-dirty。
if [ "$DIRTY_COUNT" != "0" ] && [ "$allow_dirty" != true ]; then
  echo "工作区有 ${DIRTY_COUNT} 个未提交改动，先提交再打包（测试包可加 --allow-dirty）：" >&2
  git status --porcelain >&2
  exit 1
fi

# 记下这次产物对应的代码版本。服务器上 `cat .build/BUILD_INFO` 就知道跑的是哪一版，
# 不用再猜「我到底传上去没有」。
mkdir -p "$OUT_DIR"
{
  echo "tag:    $IMAGE_TAG"
  echo "commit: $GIT_SHA"
  echo "dirty:  ${DIRTY_COUNT} 个未提交改动"
  echo "built:  $(date '+%Y-%m-%d %H:%M:%S')"
  echo "target: linux/amd64"
} > "$OUT_DIR/BUILD_INFO"

# 单独放一个只含标签的文件，服务器上 $(cat .build/IMAGE_TAG) 就能直接写进 .env，
# 不用从 BUILD_INFO 里 cut 字段，也不会因为格式微调而失效。
echo "$IMAGE_TAG" > "$OUT_DIR/IMAGE_TAG"

# 每次都清掉旧产物。服务端按目录整体 COPY，留下上一次的旧二进制或旧 dist
# 不会报错，只会让人拿着旧版本排查半天。
if $do_api; then
  echo "==> 编译后端（linux/amd64，静态链接）"
  rm -rf "$OUT_DIR/api"
  mkdir -p "$OUT_DIR/api/config"
  # CGO_ENABLED=0 是交叉编译的关键：关掉 cgo 才能在没有 Linux 工具链的机器上产出
  # 不依赖 libc 的二进制，和 Dockerfile 里的编译参数保持一致。
  # -trimpath 去掉本机绝对路径；-s -w 去符号表，体积小三成左右。
  #
  # -p 2 是本机内存的护栏：默认并行度会同时铺开十几个 compile.exe，
  # 提交内存（页面文件）不够时报 "The paging file is too small" 或
  # "out of memory allocating heap arena map"，看起来像卡死。压到两个并行
  # 只慢几十秒，但每次都能跑完。GOGC 一并调低，让每个编译进程更早回收。
  export GOGC=60
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -p 2 -trimpath -ldflags="-s -w" -o "$OUT_DIR/api/forum-server" ./cmd/server
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -p 2 -trimpath -ldflags="-s -w" -o "$OUT_DIR/api/forum-migrate" ./cmd/migrate
  # 镜像里那份配置是默认值，生产实际用的那份从宿主机挂进去。
  cp config/config.docker.yaml "$OUT_DIR/api/config/config.yaml"
  # artifact 版 Dockerfile 只做 COPY，构建时从上下文目录里取。
  cp deploy/Dockerfile.artifact "$OUT_DIR/api/Dockerfile"
fi

if $do_web; then
  echo "==> 构建前端静态文件"
  # 用本机 Node 22 构建，与 CI 的 node-version 保持一致，
  # 版本不同时产物可能带了目标环境不支持的语法。
  (cd web && npm run build)
  rm -rf "$OUT_DIR/web"
  mkdir -p "$OUT_DIR/web"
  cp -r web/dist "$OUT_DIR/web/dist"
  cp deploy/nginx.docker.conf "$OUT_DIR/web/default.conf"
  cp deploy/web.Dockerfile.artifact "$OUT_DIR/web/Dockerfile"
fi

if $make_tar; then
  echo "==> 打包传输用压缩包"
  # -C 到仓库根目录，让包里的路径就是 .build/api、.build/web，
  # 服务器上直接解到 /opt/forum 即可，不用再挪文件。
  #
  # 只打本次真正生成的目录：--api-only 时如果连 .build/web 一起打进去，
  # 服务器会解出一份上一次的前端产物，看起来「前端改了没生效」。
  tar_paths=(".build/BUILD_INFO" ".build/IMAGE_TAG")
  if $do_api; then
    tar_paths+=(".build/api")
  fi
  if $do_web; then
    tar_paths+=(".build/web")
  fi
  tar -czf "$OUT_DIR/forum-linux-amd64.tar.gz" -C "$REPO_DIR" "${tar_paths[@]}"
fi

echo
echo "==> 产物清单（本次标签：$IMAGE_TAG）"
du -sh "$OUT_DIR/api" "$OUT_DIR/web" 2>/dev/null || true
ls -lh "$OUT_DIR/forum-linux-amd64.tar.gz" 2>/dev/null || true
echo
echo "下一步：把压缩包传到服务器并解到项目根目录（见 docs/10 第 13.5 节）"
echo "  scp $OUT_DIR/forum-linux-amd64.tar.gz root@<服务器IP>:/tmp/"
echo
echo "然后在服务器上执行"
echo "  tar -xzf /tmp/forum-linux-amd64.tar.gz -C /opt/forum"
echo "  cd /opt/forum && sed -i '/^IMAGE_TAG=/d' .env && echo \"IMAGE_TAG=\$(cat .build/IMAGE_TAG)\" >> .env"
# export 必须单独一行：写成 `COMPOSE_FILE=... docker compose build && docker compose up`
# 的话，环境变量只作用在第一条命令上，up 会退回只读根 compose——2026-11-24
# 就是这样在一台 2C2G 的机器上把整机拖死的。服务器 .env 里现在也写了一份，
# 这里显式 export 一遍是为了兼容还没升级 .env 的机器。
echo "  cd /opt/forum && export COMPOSE_FILE=/opt/forum/docker-compose.yml:/opt/forum/deploy/prod/docker-compose.prod.yml:/opt/forum/deploy/prod/docker-compose.artifact.yml"
echo "  docker compose build api web"
echo "  docker compose up -d --no-deps --no-build api"
echo "  docker compose up -d --no-deps --force-recreate --no-build web"
echo
# 台球物理服务是 Python，不在这份产物里，第一次部署（或 pool-sim/ 有改动）时单独构建。
# 不提示的话，up -d --no-build 会去找一个根本不存在的镜像，报 pull access denied。
echo "首次部署还有一步（台球物理服务，只在服务器上构建）："
echo "  cd /opt/forum && docker compose build pool-sim"

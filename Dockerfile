# 后端镜像：Go 编译出静态二进制，再塞进最小运行镜像。
#
# 为什么先编译再拷进 alpine，而不是直接用 golang 镜像跑 go run：
#   - golang 镜像 800MB 起步，构建工具链在运行期完全用不上；
#   - CGO_ENABLED=0 编出来的二进制不依赖 libc，换任意 linux 基础镜像都能跑。
#
# 前端不在这里构建：静态文件由 nginx 镜像托管（见 deploy/web.Dockerfile），
# 一个容器只跑一个进程，日志、重启、横向扩容都简单。

FROM golang:1.26-alpine AS build

WORKDIR /src

# 先只拷依赖清单再 download：只要 go.mod/go.sum 没变，这一层就能命中缓存，
# 改业务代码时不用重新拉一遍模块。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# -trimpath 去掉本机绝对路径，-s -w 去掉符号表与调试信息，
# 二进制大约能小三分之一，代价是线上 panic 栈没有行号，需要时用不带 -s 的版本复现。
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/forum-server ./cmd/server \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/forum-migrate ./cmd/migrate

FROM alpine:3.20

# ca-certificates：调大模型接口走 HTTPS，没有根证书会直接握手失败。
# tzdata：日志与 created_at 都按本地时区渲染，容器默认是 UTC。
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 forum

WORKDIR /app

COPY --from=build /out/forum-server /out/forum-migrate /app/
COPY config/config.docker.yaml /app/config/config.yaml

# 用非 root 跑：容器被拿到 shell 时，攻击者也只能动 /app 下这几个文件。
USER forum

EXPOSE 8080

# 配置文件由 CONFIG_PATH 指定，其余敏感项（数据库密码、JWT secret、模型 key）
# 一律走环境变量覆盖，镜像里不留任何密钥。
ENV CONFIG_PATH=/app/config/config.yaml \
    GIN_MODE=release

# 迁移不放在启动脚本里：多副本同时起来会并发建表。
# 首次部署先跑 docker compose run --rm migrate，之后正常启动 api。
CMD ["/app/forum-server"]

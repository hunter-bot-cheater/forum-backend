# 前端镜像：node 构建出静态文件，再交给 nginx 托管并反代 /api。
#
# 与 deploy/nginx.conf.example（裸机部署）的区别只在 server_name 与 upstream：
# 这里 upstream 是 compose 里的服务名 api，端口 8080 不出容器网络。

FROM node:22-alpine AS build

WORKDIR /web

# 只拷锁文件先装依赖：源码改动不会让这一层失效。
# npm ci 按 lock 精确安装，构建结果可复现；npm install 会顺手升级次版本。
COPY web/package.json web/package-lock.json ./
RUN npm ci

COPY web/ ./
# VITE_API_BASE_URL 保持默认 /api：前端与接口同源，浏览器不会触发跨域。
RUN npm run build

FROM nginx:1.27-alpine

COPY deploy/nginx.docker.conf /etc/nginx/conf.d/default.conf
COPY --from=build /web/dist /usr/share/nginx/html

EXPOSE 80

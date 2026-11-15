# 常用开发命令
.PHONY: run run-release build test tidy swagger web-dev web-build web-preview

run:   ## 启动服务
	go run ./cmd/server

run-release: ## 以 release 模式启动后端，关闭 Swagger
	GIN_MODE=release go run ./cmd/server

build: ## 编译
	go build -o bin/forum-server ./cmd/server

test:  ## 跑全部测试
	go test ./...

tidy:  ## 整理依赖
	go mod tidy

swagger: ## 重新生成 API 文档
	swag init -g cmd/server/main.go -o api/swagger --parseInternal

web-dev: ## 启动前端开发服务，端口 5173
	cd web && npm run dev

web-build: ## 构建前端静态产物到 web/dist
	cd web && npm run build

web-preview: ## 构建并预览前端产物，端口由 vite.config.ts 固定为 5173
	cd web && npm run build && npm run preview

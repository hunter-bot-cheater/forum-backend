# 常用开发命令
.PHONY: run build test tidy swagger

run:   ## 启动服务
	go run ./cmd/server

build: ## 编译
	go build -o bin/forum-server ./cmd/server

test:  ## 跑全部测试
	go test ./...

tidy:  ## 整理依赖
	go mod tidy

swagger: ## 重新生成 API 文档
	swag init -g cmd/server/main.go -o api/swagger --parseInternal

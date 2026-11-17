# 常用开发命令
.PHONY: run run-release build build-release migrate test test-integration test-cover lint tidy swagger web-dev web-build web-preview pack pack-tar backup up down

run:   ## 启动服务
	go run ./cmd/server

run-release: ## 以 release 模式启动后端，关闭 Swagger
	GIN_MODE=release go run ./cmd/server

build: ## 编译服务端与迁移命令到 bin/
	go build -o bin/forum-server ./cmd/server
	go build -o bin/forum-migrate ./cmd/migrate

build-release: ## 编译 Linux 版可执行文件，用于上传服务器（本机是 Windows 时用这个）
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/forum-server-linux ./cmd/server
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/forum-migrate-linux ./cmd/migrate

migrate: ## 手工执行一次建表（生产 auto_migrate=false 时用；本机也可用）
	go run ./cmd/migrate

test:  ## 跑全部测试；没设 TEST_MYSQL_DSN 时自动跳过需要 MySQL 的集成测试
	go test ./...

test-integration: ## 跑集成测试，需要先 export TEST_MYSQL_DSN（见 README「测试」一节）
	@test -n "$$TEST_MYSQL_DSN" || (echo "请先设置 TEST_MYSQL_DSN，例如：export TEST_MYSQL_DSN=\"root:密码@tcp(127.0.0.1:3306)/forum_test?parseTime=true&loc=Local&charset=utf8mb4\""; exit 1)
	go test -p 1 -count=1 ./...

test-cover: ## 看覆盖率；同样需要 TEST_MYSQL_DSN 才能覆盖到 service 层
	go test -p 1 -cover ./...

lint: ## 代码检查：gofmt + golangci-lint（需先装 golangci-lint v2，规则见 .golangci.yml）
	@unformatted=$$(gofmt -l .); \
	if [ -n "$$unformatted" ]; then \
		echo "以下文件没有用 gofmt 格式化："; \
		echo "$$unformatted"; \
		exit 1; \
	fi
	golangci-lint run ./...

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

pack: ## 本机编译 Linux 产物到 .build/，服务器只需十几秒打镜像（见 docs/10 13.5）
	bash scripts/pack-linux.sh

pack-tar: ## 同上，再打一个 .build/forum-linux-amd64.tar.gz 方便 scp
	bash scripts/pack-linux.sh --tar

backup: ## 备份数据库到 backups/，保留 7 天（KEEP_DAYS 可改）
	bash scripts/backup.sh

up: ## 容器方式启动整套服务（首次需先 docker compose run --rm migrate）
	docker compose up -d

down: ## 停掉容器，保留数据卷
	docker compose down

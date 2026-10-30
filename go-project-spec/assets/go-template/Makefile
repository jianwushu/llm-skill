APP      := app
MAIN     := ./cmd/server
BIN      := bin
VERSION  ?= dev

.PHONY: dev web-dev web-build build build-linux build-linux-arm64 tidy clean

## dev: 本地运行后端（前端未构建时 / 返回 503 提示；联调请走 vite :5173 代理）
dev:
	go run $(MAIN)

## web-dev: 前端开发服务器（:5173，/api 代理到后端）
web-dev:
	cd web && npm run dev

## web-build: 前端构建，产物直接输出到 internal/webfs/dist（embed 唯一来源）
web-build:
	cd web && npm run build

## build: 构建单二进制（先构建前端再编译 Go，CGO_ENABLED=0）
build: web-build
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN)/$(APP) $(MAIN)

## build-linux: 交叉编译 linux/amd64 单文件
build-linux: web-build
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN)/$(APP)-linux-amd64 $(MAIN)

## build-linux-arm64: 交叉编译 linux/arm64 单文件
build-linux-arm64: web-build
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o $(BIN)/$(APP)-linux-arm64 $(MAIN)

tidy:
	go mod tidy

clean:
	rm -rf $(BIN)

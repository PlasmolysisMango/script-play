SHELL := /bin/bash
.DEFAULT_GOAL := help

SERVER_DIR := server
WEB_DIR := web

## help: 列出所有目标
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/^## //' | column -t -s ':'

## server: 编译后端到 server/bin/script-play
server:
	cd $(SERVER_DIR) && go build -o bin/script-play ./cmd/server

## test: 后端 vet + 单元测试
test:
	cd $(SERVER_DIR) && go vet ./... && go test ./...

## check-scripts: 校验 scripts/ 下所有剧本（与导入时同一套校验）
check-scripts:
	cd $(SERVER_DIR) && go run ./cmd/scriptcheck ../scripts/*/script.json

## run: 本地启动后端（mock provider，8080）
run:
	cd $(SERVER_DIR) && SP_LLM_PROVIDER=$${SP_LLM_PROVIDER:-mock} SP_WEB_DIR=$${SP_WEB_DIR:-../web/dist} go run ./cmd/server

## dev-server: 同上，但热重载靠 go run
dev-server: run

## web-install: 安装前端依赖
web-install:
	cd $(WEB_DIR) && npm install

## web-dev: 前端开发服务器（5173，/api 与 /ws 代理到 8080）
web-dev:
	cd $(WEB_DIR) && npm run dev

## web-check: 前端类型检查 + 单元测试
web-check:
	cd $(WEB_DIR) && npx tsc --noEmit && npx vitest run

## web-build: 生产构建（2 核机器上较慢，CI 会做）
web-build:
	cd $(WEB_DIR) && npm run build

## web-publish: 构建并发布前端到 nginx 站点目录（/usr/share/nginx/script-play）
web-publish: web-build
	rm -rf /usr/share/nginx/script-play.new
	cp -a $(WEB_DIR)/dist /usr/share/nginx/script-play.new
	mv /usr/share/nginx/script-play /usr/share/nginx/script-play.old 2>/dev/null || true
	mv /usr/share/nginx/script-play.new /usr/share/nginx/script-play
	rm -rf /usr/share/nginx/script-play.old

## voices: 浏览器语音能力自检（Web Speech API，无需下载模型）
voices:
	@echo "ASR  : Chrome/Edge 的 webkitSpeechRecognition；其它浏览器自动回退为文本输入"
	@echo "TTS  : speechSynthesis（系统音色，无需模型）"
	@echo "离线方案 sherpa-onnx WASM 尚未接入，见 README 的差异说明"

## fmt: gofmt 后端
fmt:
	cd $(SERVER_DIR) && gofmt -w ./cmd ./internal

.PHONY: help server test check-scripts run dev-server web-install web-dev web-check web-build web-publish voices fmt

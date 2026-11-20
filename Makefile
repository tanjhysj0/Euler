.PHONY: build test lint fmt tidy web-install web-test web-build test-e2e dev-api dev-web harbor-test

# Harbor 评测连接的本机数据库；可用同名环境变量覆盖。
EULER_DATABASE_URL ?= postgres://euler:euler@127.0.0.1:5432/euler?sslmode=disable
EULER_LOG ?= /tmp/euler-harbor/euler.log
# Harbor 创建临时模型 profile 所需的默认值，可用同名环境变量覆盖。
EULER_MODEL_BASE_URL ?= https://api.deepseek.com
EULER_MODEL ?= deepseek-v4-flash
EULER_REASONING_EFFORT ?= high

build:
	npm --prefix web ci && npm --prefix web run build
	go build -o bin/euler ./cmd/euler

test:
	go test ./...

lint:
	golangci-lint run --timeout=5m ./...

fmt:
	gofmt -w ./cmd ./internal

tidy:
	go mod tidy

web-install:
	npm install --prefix web

web-test:
	npm --prefix web test -- --run

web-build:
	npm --prefix web run build

test-e2e:
	npm --prefix web run test:e2e

dev-api:
	go run ./cmd/euler

dev-web:
	npm --prefix web run dev

# 重建二进制并重启 Euler，健康检查通过后运行 Harbor 评测。
# 默认跑完整编码评测；HARBOR_MODE=smoke 改为冒烟门禁。
harbor-test:
	@set -e; \
		mkdir -p "$$(dirname '$(EULER_LOG)')"; \
		echo "==> 重建 Euler 二进制"; \
		go build -o bin/euler ./cmd/euler; \
		echo "==> 重启 Euler 服务"; \
		lsof -ti tcp:8080 | xargs kill 2>/dev/null || true; \
		for i in $$(seq 1 10); do \
			curl -sf --max-time 1 http://127.0.0.1:8080/health >/dev/null || break; \
			sleep 1; \
		done; \
		if curl -sf --max-time 1 http://127.0.0.1:8080/health >/dev/null; then \
			echo "  端口 8080 仍被占用，重启失败" >&2; \
			exit 1; \
		fi; \
		if [ -z "$${LANGFUSE_SECRET_KEY:-}" ] && [ -f "$$HOME/.zprofile" ]; then \
			set -a; . "$$HOME/.zprofile" >/dev/null 2>&1 || true; set +a; \
		fi; \
		EULER_DATABASE_URL='$(EULER_DATABASE_URL)' nohup \
			./harbor_eval/scripts/start-euler.sh > '$(EULER_LOG)' 2>&1 & \
		pid=$$!; \
		for i in $$(seq 1 30); do \
			if curl -sf --max-time 2 http://127.0.0.1:8080/health >/dev/null; then \
				echo "  服务已就绪 (pid=$$pid)"; break; \
			fi; \
			if [ "$$i" -eq 30 ]; then \
				echo "  服务启动超时，最近日志：" >&2; \
				tail -n 20 '$(EULER_LOG)' >&2; \
				exit 1; \
			fi; \
			sleep 1; \
		done; \
		echo "==> 运行 Harbor (mode=$${HARBOR_MODE:-coding})"; \
		export EULER_MODEL_BASE_URL='$(EULER_MODEL_BASE_URL)'; \
		export EULER_MODEL='$(EULER_MODEL)'; \
		export EULER_REASONING_EFFORT='$(EULER_REASONING_EFFORT)'; \
		if [ "$${HARBOR_MODE:-coding}" = "smoke" ]; then \
			./harbor_eval/scripts/run-smoke.sh; \
		else \
			./harbor_eval/scripts/run-coding.sh; \
		fi

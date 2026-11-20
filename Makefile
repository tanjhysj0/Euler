.PHONY: build test lint fmt tidy web-install web-test web-build test-e2e dev-api dev-web harbor-test

# Harbor 评测连接的本机数据库；可用同名环境变量覆盖。
EULER_DATABASE_URL ?= postgres://euler:euler@127.0.0.1:5432/euler?sslmode=disable
EULER_LOG ?= /tmp/euler-harbor/euler.log
# Harbor 创建临时模型 profile 所需的默认值，可用同名环境变量覆盖。
EULER_MODEL_BASE_URL ?= https://api.deepseek.com
EULER_MODEL ?= deepseek-v4-flash
EULER_REASONING_EFFORT ?= high

# harbor-test 默认覆盖的编码数据集；可用命令行或环境变量覆盖。
HARBOR_DATASETS ?= programbench devopsgym tb3-preview-v2 terminal-bench-3-test swe-gen-js swe-marathon android-bench swe-gen-rust LiteCoder-rl swe-gen-cpp swe-gen-java mimo-v26-code swe-gen-go aider-polyglot algotune mimo-v26-terminal humanevalfix bigcodebench-hard-complete tb21-code-debug binary-audit
HARBOR_PER_DATASET ?= 1
# 并发 trial 数；每个 trial 使用独立工作区子目录，因此可以并行。
HARBOR_CONCURRENCY ?= 1

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
		echo "==> 运行 Harbor"; \
		export EULER_MODEL_BASE_URL='$(EULER_MODEL_BASE_URL)'; \
		export EULER_MODEL='$(EULER_MODEL)'; \
		export EULER_REASONING_EFFORT='$(EULER_REASONING_EFFORT)'; \
		if [ "$${HARBOR_MODE:-multi}" = "smoke" ]; then \
			./harbor_eval/scripts/run-smoke.sh; \
		elif [ "$${HARBOR_MODE:-multi}" = "coding" ]; then \
			./harbor_eval/scripts/run-coding.sh; \
		else \
			./harbor_eval/scripts/prepare-workspace.sh; \
			run_id=$$(date +%Y%m%d-%H%M%S); \
			job_name="coding-multi-$$run_id"; \
			results_dir="harbor_eval/results/$$run_id"; \
			cfg="$${TMPDIR:-/tmp}/euler-harbor-multi-$$run_id.yaml"; \
			{ \
				echo "job_name: $$job_name"; \
				echo "jobs_dir: harbor_eval/results"; \
				echo "n_concurrent_trials: $(HARBOR_CONCURRENCY)"; \
				echo "datasets:"; \
				for ds in $(HARBOR_DATASETS); do \
					echo "  - path: harbor_eval/tasks/_downloads/$$ds"; \
					echo "    n_tasks: $(HARBOR_PER_DATASET)"; \
				done; \
				echo "agents:"; \
				echo "  - import_path: agents.euler_agent:EulerAgent"; \
				echo "environment:"; \
				echo "  type: docker"; \
				echo "  mounts:"; \
				echo "    - type: bind"; \
				echo "      source: /tmp/euler-harbor/workspaces"; \
				echo "      target: /mnt/euler-workspaces"; \
			} > "$$cfg"; \
			echo "  数据集: $$(echo $(HARBOR_DATASETS) | wc -w | tr -d ' ') 个，每个 $(HARBOR_PER_DATASET) 个任务，并发 $(HARBOR_CONCURRENCY)"; \
			PYTHONPATH="$$PWD/harbor_eval$${PYTHONPATH:+:$$PYTHONPATH}" \
				harbor run -c "$$cfg" -y --job-name "$$job_name" -o "$$results_dir"; \
			job_dir="$$results_dir/$$job_name"; \
			echo "==> 结果目录: $$job_dir"; \
			find "$$job_dir" -name reward.txt -exec cat {} \; 2>/dev/null | \
				awk '{total++; if ($$1 == "1") passed++} END {printf "==> 通过 %d/%d\n", passed, total}'; \
		fi

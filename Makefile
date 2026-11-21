.PHONY: build test lint fmt tidy web-install web-test web-build test-e2e test-smoke dev-api dev-web harbor-test harbor-test-easy harbor-test-medium harbor-test-hard harbor-test-all agent-dist

# Harbor 评测使用的默认模型；可用同名环境变量覆盖。
EULER_MODEL_BASE_URL ?= https://api.deepseek.com
EULER_MODEL ?= deepseek-v4-flash
EULER_REASONING_EFFORT ?= high

# harbor-test 默认覆盖的编码数据集；可用命令行或环境变量覆盖。
# 注意：programbench 与 swe-marathon 的任务声明了 [agent] network_mode，
# 需要 Docker 侧的 egress-control（依赖内核 CONFIG_NFT_FIB_INET）；Docker
# Desktop 的内核不支持，会在任务校验阶段直接失败。需要这些数据集时，
# 在支持 nftables 的 Linux Docker 主机上通过 HARBOR_TASKS 单独指定。
HARBOR_DATASETS ?= devopsgym tb3-preview-v2 terminal-bench-3-test swe-gen-js android-bench swe-gen-rust LiteCoder-rl swe-gen-cpp swe-gen-java mimo-v26-code swe-gen-go aider-polyglot algotune mimo-v26-terminal humanevalfix bigcodebench-hard-complete tb21-code-debug binary-audit
HARBOR_PER_DATASET ?= 1
# 并发 trial 数。本机 Docker VM 为 4 核 / 3.8GB，而评测任务普遍声明 1 CPU；
# 实测单容器峰值约 1 核、常驻内存约 130MB，agent 大部分时间在等模型。
# 取 4 即“一个 trial 占一个核”的安全上限，同时给镜像构建和验证器留出余量；
# 纯运行（镜像已缓存）时可临时提到 5-6。内存不是瓶颈。
HARBOR_CONCURRENCY ?= 4
# 单个 trial 的 agent 阶段硬超时（秒），避免重型任务拖长整批测试。
# agent 自身预算默认 600s，再加 120s 容器内 exec 余量和 60s 结果回传余量。
HARBOR_AGENT_TIMEOUT ?= 780
# 可选：精确指定任务（dataset/task，空格分隔）；设置后忽略精选清单。
HARBOR_TASKS ?=
# 精选任务清单（每行一个仓库相对路径）；HARBOR_TASKS 为空时使用。
HARBOR_TASKS_FILE ?= harbor_eval/tasks/selected-tasks.txt
HARBOR_SELECTED ?= $(shell grep -v '^\#' $(HARBOR_TASKS_FILE) 2>/dev/null | sed '/^$$/d')
# 按难度分组的精选清单，各 10 个任务；程序执行时间上限分别为 30s / 30s / 60s。
HARBOR_EASY_TASKS_FILE ?= harbor_eval/tasks/selected-easy.txt
HARBOR_MEDIUM_TASKS_FILE ?= harbor_eval/tasks/selected-medium.txt
HARBOR_HARD_TASKS_FILE ?= harbor_eval/tasks/selected-hard.txt
# 容器内 agent 二进制的输出目录。
AGENT_DIST ?= bin/agent-dist

build:
	npm --prefix web ci && npm --prefix web run build
	go build -o bin/euler ./cmd/euler

# 交叉编译容器内运行的一次性 agent；容器可能是 amd64 或 arm64。
agent-dist:
	@mkdir -p $(AGENT_DIST)
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
		-o $(AGENT_DIST)/euler-agent-linux-amd64 ./cmd/euler-agent
	GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
		-o $(AGENT_DIST)/euler-agent-linux-arm64 ./cmd/euler-agent

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

# 真实模型冒烟：真实 DeepSeek + 真实数据库，只跑 1 个端到端用例，
# 用于验证 mock E2E 覆盖不到的流式响应与真实工具调用。
test-smoke:
	@if [ -z "$${OPENAI_API_KEY:-}" ] && [ -f "$$HOME/.zprofile" ]; then \
		set -a; . "$$HOME/.zprofile" >/dev/null 2>&1 || true; set +a; \
	fi; \
	test -n "$${OPENAI_API_KEY:-}" || (echo "缺少 OPENAI_API_KEY" >&2; exit 1); \
	OPENAI_API_KEY="$${OPENAI_API_KEY}" npm --prefix web run test:smoke

dev-api:
	go run ./cmd/euler

dev-web:
	npm --prefix web run dev

# 构建容器内 agent 二进制并运行 Harbor 评测；agent 在任务容器内直接执行，
# 不需要宿主机 Euler 服务或工作区挂载。
harbor-test:
	@set -e; \
		if [ -z "$${LANGFUSE_SECRET_KEY:-}" ] && [ -f "$$HOME/.zprofile" ]; then \
			set -a; . "$$HOME/.zprofile" >/dev/null 2>&1 || true; set +a; \
		fi; \
		if [ -z "$${EULER_MODEL_API_KEY:-}" ] && [ -z "$${OPENAI_API_KEY:-}" ]; then \
			echo "缺少模型密钥：请设置 EULER_MODEL_API_KEY 或 OPENAI_API_KEY" >&2; \
			exit 1; \
		fi; \
		echo "==> 构建容器内 agent 二进制"; \
		$(MAKE) agent-dist; \
		export EULER_MODEL_BASE_URL='$(EULER_MODEL_BASE_URL)'; \
		export EULER_MODEL='$(EULER_MODEL)'; \
		export EULER_REASONING_EFFORT='$(EULER_REASONING_EFFORT)'; \
		model_host=$$(printf '%s' '$(EULER_MODEL_BASE_URL)' | sed -E 's#^[a-z]+://##; s#/.*$$##'); \
		allow_args="--allow-agent-host $$model_host"; \
		langfuse_url="$${LANGFUSE_HOST:-$${LANGFUSE_BASE_URL:-}}"; \
		if [ -n "$$langfuse_url" ]; then \
			langfuse_host=$$(printf '%s' "$$langfuse_url" | sed -E 's#^[a-z]+://##; s#/.*$$##'); \
			allow_args="$$allow_args --allow-agent-host $$langfuse_host"; \
		fi; \
		if [ "$${HARBOR_MODE:-multi}" = "smoke" ]; then \
			./harbor_eval/scripts/run-smoke.sh; \
		elif [ "$${HARBOR_MODE:-multi}" = "coding" ]; then \
			./harbor_eval/scripts/run-coding.sh; \
		else \
			echo "==> 运行 Harbor"; \
			run_id=$$(date +%Y%m%d-%H%M%S); \
			job_name="coding-multi-$$run_id"; \
			results_dir="harbor_eval/results/$$run_id"; \
			cfg="$${TMPDIR:-/tmp}/euler-harbor-multi-$$run_id.yaml"; \
			{ \
				echo "job_name: $$job_name"; \
				echo "jobs_dir: harbor_eval/results"; \
				echo "n_concurrent_trials: $(HARBOR_CONCURRENCY)"; \
				if [ -n "$(HARBOR_TASKS)" ]; then \
					echo "tasks:"; \
					for pick in $(HARBOR_TASKS); do \
						echo "  - path: harbor_eval/tasks/_downloads/$$pick"; \
					done; \
				elif [ -n "$(HARBOR_SELECTED)" ]; then \
					echo "tasks:"; \
					for path in $(HARBOR_SELECTED); do \
						echo "  - path: $$path"; \
					done; \
				else \
					echo "datasets:"; \
					for ds in $(HARBOR_DATASETS); do \
						echo "  - path: harbor_eval/tasks/_downloads/$$ds"; \
						echo "    n_tasks: $(HARBOR_PER_DATASET)"; \
					done; \
				fi; \
				echo "agents:"; \
				echo "  - import_path: agents.euler_agent:EulerAgent"; \
				echo "    override_timeout_sec: $(HARBOR_AGENT_TIMEOUT)"; \
				echo "environment:"; \
				echo "  type: docker"; \
			} > "$$cfg"; \
			if [ -n "$(HARBOR_TASKS)" ]; then \
				echo "  任务: $(HARBOR_TASKS)，并发 $(HARBOR_CONCURRENCY)"; \
			elif [ -n "$(HARBOR_SELECTED)" ]; then \
				echo "  精选任务: $$(echo $(HARBOR_SELECTED) | wc -w | tr -d ' ') 个，每个 agent 上限 $(HARBOR_AGENT_TIMEOUT)s，并发 $(HARBOR_CONCURRENCY)"; \
			else \
				echo "  数据集: $$(echo $(HARBOR_DATASETS) | wc -w | tr -d ' ') 个，每个 $(HARBOR_PER_DATASET) 个任务，并发 $(HARBOR_CONCURRENCY)"; \
			fi; \
			PYTHONPATH="$$PWD/harbor_eval$${PYTHONPATH:+:$$PYTHONPATH}" \
				harbor run -c "$$cfg" -y $$allow_args \
				--job-name "$$job_name" -o "$$results_dir"; \
			job_dir="$$results_dir/$$job_name"; \
			echo "==> 结果目录: $$job_dir"; \
			find "$$job_dir" -name reward.txt -exec cat {} \; 2>/dev/null | \
				awk '{total++; if ($$1 == "1") passed++} END {printf "==> 通过 %d/%d\n", passed, total}'; \
		fi

# 按难度分组运行精选任务，复用 harbor-test 的构建与结果汇总逻辑。
harbor-test-easy:
	@$(MAKE) --no-print-directory harbor-test HARBOR_TASKS_FILE=$(HARBOR_EASY_TASKS_FILE)

harbor-test-medium:
	@$(MAKE) --no-print-directory harbor-test HARBOR_TASKS_FILE=$(HARBOR_MEDIUM_TASKS_FILE)

harbor-test-hard:
	@$(MAKE) --no-print-directory harbor-test HARBOR_TASKS_FILE=$(HARBOR_HARD_TASKS_FILE)

# 依次跑完三组；任意一组失败都会中止。
harbor-test-all: harbor-test-easy harbor-test-medium harbor-test-hard

.PHONY: build test lint fmt tidy web-install web-test web-build test-e2e test-smoke dev-api dev-restart dev-restart-all dev-reload dev-db-up dev-db-stop dev-db-restart dev-web-restart dev-web-stop dev-stop dev-web start stop restart harbor-test harbor-test-run harbor-test-easy harbor-test-medium harbor-test-hard harbor-test-all harbor-test-fast harbor-check-selected harbor-download harbor-clean agent-dist

# 本地调试服务的端口与配置文件；EULER_CONFIG 留空时使用默认的 ~/.euler/config.yaml。
EULER_PORT ?= 8080
# 前端开发服务器端口，与 vite.config.ts 的 server.port 保持一致。
WEB_PORT ?= 5173
EULER_CONFIG ?=
# 本地开发库地址，默认与 compose.yaml 的凭据一致；同名环境变量优先。
EULER_DATABASE_URL ?= postgres://euler:euler@127.0.0.1:5432/euler?sslmode=disable

# Harbor 评测使用的默认模型；可用同名环境变量覆盖。
EULER_MODEL_BASE_URL ?= https://api.deepseek.com
EULER_MODEL ?= deepseek-v4-flash
EULER_REASONING_EFFORT ?= high
# 模型请求的空闲超时：首 token 或流中途超过该时长没有数据即失败。
EULER_MODEL_TIMEOUT ?= 10s
# 单次模型请求的总时长上限。
EULER_MODEL_REQUEST_TIMEOUT ?= 5m

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
# harbor-test（以及兼容别名 harbor-test-all）定死的三组精选清单，
# 各 10 个任务（共 30 个）；不读取 HARBOR_DATASETS / HARBOR_TASKS /
# selected-tasks.txt，避免任务集合漂移。
HARBOR_FIXED_EASY := harbor_eval/tasks/selected-easy.txt
HARBOR_FIXED_MEDIUM := harbor_eval/tasks/selected-medium.txt
HARBOR_FIXED_HARD := harbor_eval/tasks/selected-hard.txt
HARBOR_FIXED_FILES := $(HARBOR_FIXED_EASY) $(HARBOR_FIXED_MEDIUM) $(HARBOR_FIXED_HARD)
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

# 端到端测试：使用独立的 18080 端口与 euler_test 库，不会影响本地调试服务。
# 但它验证的是当前源码；若同时开着手工调试的界面，跑完后请执行 make dev-restart。
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

# 本地调试服务：手工用 Web 界面测试时默认开启 Langfuse 追踪，
# 未配置密钥时静默跳过。E2E 使用自己的配置文件与变量，不受影响。
dev-api:
	@if [ -z "$${LANGFUSE_SECRET_KEY:-}" ] && [ -f "$$HOME/.zprofile" ]; then \
		set -a; . "$$HOME/.zprofile" >/dev/null 2>&1 || true; set +a; \
	fi; \
	EULER_DATABASE_URL='$(EULER_DATABASE_URL)' \
		go run ./cmd/euler $(if $(EULER_CONFIG),-config $(EULER_CONFIG),)

# 重启本地调试服务：停掉占用端口上的旧进程，再用当前代码重新启动。
# 改完代码跑过测试后执行一次，避免手工界面还在用旧二进制。
dev-restart:
	@pid=$$(lsof -nP -iTCP:$(EULER_PORT) -sTCP:LISTEN -t 2>/dev/null | head -1); \
	if [ -n "$$pid" ]; then echo "停止旧服务 (pid $$pid)"; kill "$$pid"; sleep 1; fi; \
	if [ -z "$${LANGFUSE_SECRET_KEY:-}" ] && [ -f "$$HOME/.zprofile" ]; then \
		set -a; . "$$HOME/.zprofile" >/dev/null 2>&1 || true; set +a; \
	fi; \
	EULER_DATABASE_URL='$(EULER_DATABASE_URL)' \
		nohup go run ./cmd/euler $(if $(EULER_CONFIG),-config $(EULER_CONFIG),) \
		> /tmp/euler-dev.log 2>&1 & \
	for i in $$(seq 1 60); do \
		if curl -sf -o /dev/null http://127.0.0.1:$(EULER_PORT)/health; then \
			echo "服务已就绪 http://127.0.0.1:$(EULER_PORT)，日志 /tmp/euler-dev.log"; \
			exit 0; \
		fi; \
		if grep -q "startup failed" /tmp/euler-dev.log 2>/dev/null; then \
			echo "服务启动失败，日志：" >&2; tail -20 /tmp/euler-dev.log >&2; exit 1; \
		fi; \
		sleep 1; \
	done; \
	echo "服务启动超时，最近日志：" >&2; tail -20 /tmp/euler-dev.log >&2; exit 1

# 前端有改动时使用：重新构建嵌入产物后再重启后端，避免 UI 仍是旧 dist。
dev-reload: web-build dev-restart

# 启动本地开发数据库（compose 中的 postgres）；重复执行是幂等的，
# --wait 会等到 healthcheck 通过，避免后端先于数据库就绪。
dev-db-up:
	@docker compose up -d --wait postgres

# 数据库、后端（8080）与 Vite 开发服务器（5173）一起启动/重启。
dev-restart-all: dev-db-up dev-restart dev-web-restart

# 重启前端开发服务器（5173）：停掉旧进程后以固定端口重新启动。
dev-web-restart:
	@pid=$$(lsof -nP -iTCP:$(WEB_PORT) -sTCP:LISTEN -t 2>/dev/null | head -1); \
	if [ -n "$$pid" ]; then echo "停止旧前端 (pid $$pid)"; kill "$$pid"; sleep 1; fi; \
	nohup npm --prefix web run dev -- --port $(WEB_PORT) --strictPort \
		> /tmp/euler-web.log 2>&1 & \
	for i in $$(seq 1 60); do \
		if curl -sf -o /dev/null http://127.0.0.1:$(WEB_PORT)/; then \
			echo "前端已就绪 http://127.0.0.1:$(WEB_PORT)，日志 /tmp/euler-web.log"; \
			exit 0; \
		fi; \
		if grep -q "EADDRINUSE" /tmp/euler-web.log 2>/dev/null; then \
			echo "前端启动失败，端口被占用：" >&2; tail -10 /tmp/euler-web.log >&2; exit 1; \
		fi; \
		sleep 1; \
	done; \
	echo "前端启动超时，最近日志：" >&2; tail -20 /tmp/euler-web.log >&2; exit 1

# 停止本地调试服务。
dev-stop:
	@pid=$$(lsof -nP -iTCP:$(EULER_PORT) -sTCP:LISTEN -t 2>/dev/null | head -1); \
	if [ -n "$$pid" ]; then kill "$$pid" && echo "已停止 (pid $$pid)"; \
	else echo "端口 $(EULER_PORT) 上没有运行中的服务"; fi

dev-web:
	npm --prefix web run dev

# 停止前端开发服务器（5173）。
dev-web-stop:
	@pid=$$(lsof -nP -iTCP:$(WEB_PORT) -sTCP:LISTEN -t 2>/dev/null | head -1); \
	if [ -n "$$pid" ]; then kill "$$pid" && echo "已停止前端 (pid $$pid)"; \
	else echo "端口 $(WEB_PORT) 上没有运行中的前端"; fi

# 停止数据库；容器尚未创建时也视为成功，保证 make stop 能完整执行。
dev-db-stop:
	@docker compose stop postgres >/dev/null 2>&1 || true
	@echo "数据库已停止"

# 重启数据库，并等待 healthcheck 通过后再返回。
dev-db-restart:
	@docker compose restart postgres
	@docker compose up -d --wait postgres

# 一键启动：数据库 + 后端（8080）+ 前端（5173）；已在运行的服务会先停止再启动。
start: dev-db-up dev-restart dev-web-restart

# 一键停止：前端、后端、数据库。
stop: dev-web-stop dev-stop dev-db-stop

# 一键重建并启动：重新构建前后端，再重启数据库与两个服务。
restart: build dev-db-restart dev-restart dev-web-restart

# 清理 Harbor 评测残留：先删掉挂在评测网络上的容器，再删除评测网络。
# Harbor 正常结束会自行 teardown，但启动或结果下载阶段抛异常会跳过清理；
# 残留网络会持续占用 Docker 地址池，最终导致
# “all predefined address pools have been fully subnetted”。
harbor-clean:
	@for net in $$(docker network ls --format '{{.Name}}' | grep '__env_'); do \
		for cid in $$(docker network inspect "$$net" \
			-f '{{range .Containers}}{{.Name}} {{end}}' 2>/dev/null); do \
			docker rm -f "$$cid" >/dev/null 2>&1 || true; \
		done; \
		docker network rm "$$net" >/dev/null 2>&1 || true; \
	done

# 内部 target：构建容器内 agent 二进制并运行单批 Harbor 评测；agent 在任务容器内
# 直接执行，不需要宿主机 Euler 服务或工作区挂载。
# 运行前先兜底清理历史残留，结束后（无论成功失败）再清理一次，保证可重复执行。
harbor-test-run: harbor-clean
	@set -e; \
		trap '$(MAKE) --no-print-directory harbor-clean >/dev/null 2>&1 || true' EXIT; \
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
		export EULER_MODEL_TIMEOUT='$(EULER_MODEL_TIMEOUT)'; \
		export EULER_MODEL_REQUEST_TIMEOUT='$(EULER_MODEL_REQUEST_TIMEOUT)'; \
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

# 按难度分组运行定死的精选任务；HARBOR_TASKS 置空，避免外部同名变量改变任务集合。
harbor-test-easy:
	@$(MAKE) --no-print-directory harbor-test-run HARBOR_TASKS= HARBOR_TASKS_FILE=$(HARBOR_FIXED_EASY)

harbor-test-medium:
	@$(MAKE) --no-print-directory harbor-test-run HARBOR_TASKS= HARBOR_TASKS_FILE=$(HARBOR_FIXED_MEDIUM)

harbor-test-hard:
	@$(MAKE) --no-print-directory harbor-test-run HARBOR_TASKS= HARBOR_TASKS_FILE=$(HARBOR_FIXED_HARD)

# 校验定死的 30 个任务（每组 10 个），清单被误改时直接失败。
harbor-check-selected:
	@set -e; \
	total=0; \
	for file in $(HARBOR_FIXED_FILES); do \
		n=$$(grep -hv '^\#' "$$file" | sed '/^$$/d' | wc -l | tr -d ' '); \
		if [ "$$n" -ne 10 ]; then \
			echo "任务清单 $$file 应为 10 个任务，实际 $$n 个" >&2; \
			exit 1; \
		fi; \
		total=$$((total + n)); \
	done; \
	echo "==> 任务清单校验通过：$$total 个任务"

# 按定死清单补齐缺失的任务目录，任务名取 _downloads 后的 <dataset>/<task>。
harbor-download:
	@set -e; \
	for file in $(HARBOR_FIXED_FILES); do \
		for path in $$(grep -hv '^\#' "$$file" | sed '/^$$/d'); do \
			if [ -f "$$path/task.toml" ]; then continue; fi; \
			command -v harbor >/dev/null 2>&1 || { \
				echo "缺少 harbor 命令，无法自动下载任务 $$path" >&2; exit 1; \
			}; \
			pick=$${path#harbor_eval/tasks/_downloads/}; \
			echo "==> 下载缺失任务：$$pick"; \
			harbor download "$$pick" -o harbor_eval/tasks/_downloads; \
			if [ ! -f "$$path/task.toml" ]; then \
				echo "下载后仍缺少任务目录：$$path" >&2; exit 1; \
			fi; \
		done; \
	done

# 默认评测：定死跑三组共 30 个任务；先校验清单并补齐缺失任务，再按组依次执行，
# 任意一组失败都会中止。
harbor-test: harbor-check-selected harbor-download
	@$(MAKE) --no-print-directory harbor-test-easy
	@$(MAKE) --no-print-directory harbor-test-medium
	@$(MAKE) --no-print-directory harbor-test-hard

# 兼容旧用法：三组共 30 个任务。
harbor-test-all: harbor-test

# 快速回归：只跑 selected-tasks.txt 里的 10 个任务。
harbor-test-fast:
	@$(MAKE) --no-print-directory harbor-test-run HARBOR_TASKS= HARBOR_TASKS_FILE=harbor_eval/tasks/selected-tasks.txt

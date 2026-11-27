.PHONY: build agent-dist test lint fmt tidy check-env test-smoke web-install web-test web-build test-e2e dev-api dev-restart dev-web-restart dev-stop dev-web-stop dev-reload dev-restart-all dev-web start stop restart harbor-clean harbor-check-selected harbor-download harbor-test-run harbor-test-easy harbor-test-medium harbor-test-hard harbor-test harbor-test-all harbor-test-fast release-snapshot release package-macos package-windows

# Windows 使用批处理入口，其他平台使用 Bash 脚本。
ifeq ($(OS),Windows_NT)
BUILD_SCRIPT := scripts\build.cmd
DEV_SCRIPT := scripts\dev.cmd
else
BUILD_SCRIPT := ./scripts/build.sh
DEV_SCRIPT := ./scripts/dev.sh
endif

# 本地调试服务的端口与配置文件；EULER_CONFIG 留空时使用默认的 ~/.euler/config.yaml。
EULER_PORT ?= 8080
# 前端开发服务器端口，与 vite.config.ts 的 server.port 保持一致。
WEB_PORT ?= 5173
EULER_CONFIG ?=
# 数据库方言：sqlite（默认，单文件，无需服务）或 postgres（实例请自行准备）。
# 同名环境变量优先；Make 默认留空，由开发脚本设置平台对应的默认值。
EULER_DATABASE_DRIVER ?=
# 本地开发库地址：默认值随方言与平台变化；同名环境变量优先。
#   - sqlite:   单文件，默认用户主目录下的 .euler/euler.db
#   - postgres: 默认连本地 127.0.0.1:5432（实例需自行准备）
EULER_DATABASE_URL ?=

# Harbor 评测的模型请求超时预算；可用同名环境变量覆盖。
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
# harbor-test（以及兼容别名 harbor-test-all）定死的三组精选清单，
# 各 10 个任务（共 30 个）；不读取 HARBOR_DATASETS / HARBOR_TASKS /
# selected-tasks.txt，避免任务集合漂移。
HARBOR_FIXED_EASY := harbor_eval/tasks/selected-easy.txt
HARBOR_FIXED_MEDIUM := harbor_eval/tasks/selected-medium.txt
HARBOR_FIXED_HARD := harbor_eval/tasks/selected-hard.txt
# 容器内 agent 二进制的输出目录。
AGENT_DIST ?= bin/agent-dist

# 将 Make 默认值与命令行覆盖传递给构建、开发和评测脚本。
export EULER_PORT WEB_PORT EULER_CONFIG
export EULER_DATABASE_DRIVER EULER_DATABASE_URL
export AGENT_DIST
export EULER_MODEL EULER_MODEL_BASE_URL EULER_REASONING_EFFORT
export EULER_MODEL_TIMEOUT EULER_MODEL_REQUEST_TIMEOUT
export HARBOR_DATASETS HARBOR_PER_DATASET HARBOR_CONCURRENCY HARBOR_AGENT_TIMEOUT
export HARBOR_TASKS HARBOR_TASKS_FILE

build:
	@$(BUILD_SCRIPT) all

# 交叉编译容器内运行的一次性 agent；容器可能是 amd64 或 arm64。
agent-dist:
	@$(BUILD_SCRIPT) agent-dist

# Windows 安装包的目标架构；版本号由命令行传入，例如
# make package-windows VERSION=1.2.3 ARCH=arm64
VERSION ?=
ARCH ?= amd64

# 本地生成快照发布产物（交叉编译 + 归档 + deb/rpm），不会上传。
release-snapshot:
	goreleaser release --snapshot --clean --skip=publish

# 按当前 tag 正式发布到 GitHub；需要 GITHUB_TOKEN 与签名相关环境变量。
release:
	goreleaser release --clean

# 本机生成 macOS DMG；未提供证书时跳过签名与公证。
package-macos: build
	@./packaging/macos/package.sh

# Windows 本机生成 MSI 安装器；需要 WiX v4 与已编译的 bin\euler.exe。
package-windows:
	@powershell -NoProfile -ExecutionPolicy Bypass -File packaging/windows/package.ps1 -Version "$(VERSION)" -Arch "$(ARCH)"

test:
	go test ./...

lint:
	golangci-lint run --timeout=5m ./...

fmt:
	gofmt -w ./cmd ./internal

tidy:
	go mod tidy

# 一键启动前检查 Node.js、npm 与 Go；缺少环境时仅提示安装，不修改系统环境。
check-env:
	@$(DEV_SCRIPT) check-env

# 真实模型冒烟：真实 DeepSeek + 真实数据库，只跑 1 个端到端用例，
# 用于验证 mock E2E 覆盖不到的流式响应与真实工具调用。
test-smoke:
	@$(DEV_SCRIPT) test-smoke

web-install:
	npm --prefix web ci

web-test:
	npm --prefix web test -- --run

web-build:
	npm --prefix web run build

# 端到端测试：使用独立的 18080 端口与 euler_test 库，不会影响本地调试服务。
# 但它验证的是当前源码；若同时开着手工调试的界面，跑完后请执行 make dev-restart。
test-e2e:
	npm --prefix web run test:e2e

# 本地调试服务：手工用 Web 界面测试时默认开启 Langfuse 追踪，
# 未配置密钥时静默跳过。E2E 使用自己的配置文件与变量，不受影响。
dev-api:
	@$(DEV_SCRIPT) api

# 重启本地调试服务：停掉旧进程，再用当前代码重新启动。
dev-restart:
	@$(DEV_SCRIPT) restart-api

# 重启前端开发服务器（5173）。
dev-web-restart:
	@$(DEV_SCRIPT) restart-web

# 停止本地调试服务。
dev-stop:
	@$(DEV_SCRIPT) stop-api

dev-web:
	npm --prefix web run dev

# 停止前端开发服务器（5173）。
dev-web-stop:
	@$(DEV_SCRIPT) stop-web

# 前端有改动时使用：重新构建嵌入产物后再重启后端，避免 UI 仍是旧 dist。
dev-reload:
	@$(DEV_SCRIPT) reload

# 后端（8080）与 Vite 开发服务器（5173）一起重启。
dev-restart-all:
	@$(DEV_SCRIPT) restart-dev

# 一键启动：检查环境，安装前端依赖并构建，再启动后端与前端。
start:
	@$(DEV_SCRIPT) start

# 一键停止：前端与后端。
stop:
	@$(DEV_SCRIPT) stop

# 一键重建并启动：重新构建前后端，再重启两个服务。
restart:
	@$(DEV_SCRIPT) restart

# 清理 Harbor 评测残留的容器与网络。
harbor-clean:
	@./harbor_eval/scripts/clean.sh

# 校验定死的 30 个任务（每组 10 个），清单被误改时直接失败。
harbor-check-selected:
	@python3 harbor_eval/scripts/tasks.py validate

# 按定死清单补齐缺失的任务目录。
harbor-download:
	@python3 harbor_eval/scripts/tasks.py download

# 运行单批 Harbor 评测；脚本负责清理、agent 构建、运行与结果汇总。
harbor-test-run:
	@./harbor_eval/scripts/run-multi.sh

# 按难度分组运行定死的精选任务；HARBOR_TASKS 置空，避免外部同名变量改变任务集合。
harbor-test-easy:
	@HARBOR_TASKS= HARBOR_TASKS_FILE=$(HARBOR_FIXED_EASY) ./harbor_eval/scripts/run-multi.sh

harbor-test-medium:
	@HARBOR_TASKS= HARBOR_TASKS_FILE=$(HARBOR_FIXED_MEDIUM) ./harbor_eval/scripts/run-multi.sh

harbor-test-hard:
	@HARBOR_TASKS= HARBOR_TASKS_FILE=$(HARBOR_FIXED_HARD) ./harbor_eval/scripts/run-multi.sh

# 默认评测：先校验清单并补齐缺失任务，再按简单、中等、困难顺序各跑一组，
# 任意一组失败都会中止。
harbor-test: harbor-check-selected harbor-download
	@HARBOR_TASKS= HARBOR_TASKS_FILE=$(HARBOR_FIXED_EASY) ./harbor_eval/scripts/run-multi.sh
	@HARBOR_TASKS= HARBOR_TASKS_FILE=$(HARBOR_FIXED_MEDIUM) ./harbor_eval/scripts/run-multi.sh
	@HARBOR_TASKS= HARBOR_TASKS_FILE=$(HARBOR_FIXED_HARD) ./harbor_eval/scripts/run-multi.sh

# 兼容旧用法：三组共 30 个任务。
harbor-test-all: harbor-test

# 快速回归：只跑 selected-tasks.txt 里的 10 个任务。
harbor-test-fast:
	@HARBOR_TASKS= HARBOR_TASKS_FILE=harbor_eval/tasks/selected-tasks.txt ./harbor_eval/scripts/run-multi.sh

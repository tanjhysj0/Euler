#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

EULER_PORT="${EULER_PORT:-8080}"
WEB_PORT="${WEB_PORT:-5173}"
EULER_CONFIG="${EULER_CONFIG:-}"
EULER_DATABASE_DRIVER="${EULER_DATABASE_DRIVER:-sqlite}"
if [[ -z "${EULER_DATABASE_URL:-}" ]]; then
	if [[ "$EULER_DATABASE_DRIVER" == "postgres" ]]; then
		EULER_DATABASE_URL="postgres://euler:euler@127.0.0.1:5432/euler?sslmode=disable"
	else
		EULER_DATABASE_URL="$HOME/.euler/euler.db"
	fi
fi
export EULER_DATABASE_DRIVER EULER_DATABASE_URL

STATE_DIR="${EULER_DEV_STATE_DIR:-${XDG_STATE_HOME:-$HOME/.local/state}/euler/dev}"
API_BINARY="$STATE_DIR/euler-dev"
API_PID_FILE="$STATE_DIR/euler-dev.pid"
API_START_FILE="$STATE_DIR/euler-dev.start"
API_LOG="$STATE_DIR/euler-dev.log"
WEB_PID_FILE="$STATE_DIR/euler-web.pid"
WEB_START_FILE="$STATE_DIR/euler-web.start"
WEB_LOG="$STATE_DIR/euler-web.log"

usage() {
	cat >&2 <<'EOF'
用法：scripts/dev.sh <命令>
命令：check-env、test-smoke、api、restart-api、restart-web、stop-api、stop-web、
      reload、restart-dev、start、stop、restart
EOF
}

check_env() {
	local failed=0
	local node_major node_version npm_version go_version

	if ! command -v node >/dev/null 2>&1; then
		echo "未检测到 Node.js，请先安装 Node.js 20+ 并将 node 加入 PATH" >&2
		failed=1
	elif ! node_version="$(node --version 2>/dev/null)"; then
		echo "无法执行 node --version，请确认已正确安装 Node.js 20+" >&2
		failed=1
	elif ! node_major="$(node -p 'process.versions.node.split(".")[0]' 2>/dev/null)"; then
		echo "无法确定 Node.js 版本，请确认已正确安装 Node.js 20+" >&2
		failed=1
	elif [[ ! "$node_major" =~ ^[0-9]+$ ]] || ((10#$node_major < 20)); then
		echo "Node.js 版本过低或无法识别（当前 $node_version），请先升级到 Node.js 20+" >&2
		failed=1
	fi

	if ! command -v npm >/dev/null 2>&1; then
		echo "未检测到 npm，请先安装 npm 并加入 PATH（通常随 Node.js 一起安装）" >&2
		failed=1
	elif ! npm_version="$(npm --version 2>/dev/null)"; then
		echo "无法执行 npm --version，请确认已正确安装 npm" >&2
		failed=1
	fi
	if ! command -v go >/dev/null 2>&1; then
		echo "未检测到 Go，请先安装 Go 1.26+ 并将 go 加入 PATH" >&2
		failed=1
	elif ! go_version="$(go version 2>/dev/null)"; then
		echo "无法执行 go version，请确认已正确安装 Go 1.26+" >&2
		failed=1
	fi
	if ((failed != 0)); then
		return 1
	fi
	printf '环境检查通过：Node.js %s，npm %s，%s\n' \
		"$node_version" "$npm_version" "$go_version"
}

show_log() {
	local log_file="$1"
	if [[ -f "$log_file" ]]; then
		tail -n 20 "$log_file" >&2 || true
	fi
}

cleanup_metadata() {
	local name="$1"
	local pid_file="$2"
	local start_file="$3"
	if ! rm -f -- "$pid_file" "$start_file"; then
		printf '清理%s的进程元数据失败\n' "$name" >&2
		return 1
	fi
}

process_start_token() {
	local raw
	local -a fields
	if ! raw="$(TZ=UTC0 LC_ALL=C ps -p "$1" -o lstart= 2>/dev/null)"; then
		return 1
	fi
	read -r -a fields <<< "$raw"
	if ((${#fields[@]} == 0)); then
		return 1
	fi
	printf '%s\n' "${fields[*]}"
}

wait_for_exit() {
	local name="$1"
	local pid="$2"
	local deadline=$((SECONDS + 10))

	while ((SECONDS < deadline)); do
		if ! kill -0 "$pid" 2>/dev/null; then
			return 0
		fi
		sleep 1
	done
	if ! kill -0 "$pid" 2>/dev/null; then
		return 0
	fi
	printf '%s进程在停止信号发出后 10 秒内未退出（PID %s）\n' "$name" "$pid" >&2
	return 1
}

terminate_process() {
	local name="$1"
	local pid="$2"
	if ! kill "$pid" 2>/dev/null; then
		if ! kill -0 "$pid" 2>/dev/null; then
			return 0
		fi
		printf '无法向%s进程发送停止信号（PID %s）\n' "$name" "$pid" >&2
		return 1
	fi
	wait_for_exit "$name" "$pid"
}

stop_process() {
	local name="$1"
	local pid_file="$2"
	local start_file="$3"
	local pid=""
	local expected=""
	local actual=""

	if [[ ! -f "$pid_file" ]]; then
		if [[ -f "$start_file" ]]; then
			printf '%s的 PID 文件缺失，仅清理陈旧启动身份文件\n' "$name"
			cleanup_metadata "$name" "$pid_file" "$start_file"
			return
		fi
		printf '%s未运行（PID 文件不存在）\n' "$name"
		return 0
	fi
	if ! IFS= read -r pid < "$pid_file" || [[ ! "$pid" =~ ^[1-9][0-9]*$ ]]; then
		printf '%s的 PID 文件内容无效，未停止任何进程，仅清理陈旧元数据\n' "$name" >&2
		cleanup_metadata "$name" "$pid_file" "$start_file"
		return
	fi
	if [[ ! -f "$start_file" ]]; then
		printf '%s缺少启动身份文件，未停止 PID %s，仅清理陈旧元数据\n' "$name" "$pid" >&2
		cleanup_metadata "$name" "$pid_file" "$start_file"
		return
	fi
	if ! IFS= read -r expected < "$start_file" || [[ -z "$expected" ]]; then
		printf '%s的启动身份文件无效，未停止 PID %s，仅清理陈旧元数据\n' "$name" "$pid" >&2
		cleanup_metadata "$name" "$pid_file" "$start_file"
		return
	fi
	if ! kill -0 "$pid" 2>/dev/null; then
		printf '%s进程已退出（PID %s），清理元数据\n' "$name" "$pid"
		cleanup_metadata "$name" "$pid_file" "$start_file"
		return
	fi
	if ! actual="$(process_start_token "$pid")"; then
		printf '无法读取%s进程的启动时间，未停止 PID %s并保留元数据\n' "$name" "$pid" >&2
		return 1
	fi
	if [[ "$actual" != "$expected" ]]; then
		printf '%s的 PID %s 启动身份不匹配，可能已复用；未停止该进程，仅清理陈旧元数据\n' \
			"$name" "$pid" >&2
		cleanup_metadata "$name" "$pid_file" "$start_file"
		return
	fi
	if ! terminate_process "$name" "$pid"; then
		printf '%s停止失败，保留 PID 与启动身份文件\n' "$name" >&2
		return 1
	fi
	if ! cleanup_metadata "$name" "$pid_file" "$start_file"; then
		return 1
	fi
	printf '已停止%s（PID %s）\n' "$name" "$pid"
}

stop_api() {
	stop_process "后端" "$API_PID_FILE" "$API_START_FILE"
}

stop_web() {
	stop_process "前端" "$WEB_PID_FILE" "$WEB_START_FILE"
}

stop_all() {
	local failed=0
	if ! stop_web; then
		failed=1
	fi
	if ! stop_api; then
		failed=1
	fi
	if ((failed != 0)); then
		echo "部分开发进程停止失败，请根据上述提示处理" >&2
		return 1
	fi
}

port_in_use() {
	(exec 3<>"/dev/tcp/127.0.0.1/$1") >/dev/null 2>&1
}

# 返回监听指定端口的进程 PID；无法确定时输出空字符串。
listener_pid() {
	local port="$1"
	local pid=""
	if command -v lsof >/dev/null 2>&1; then
		pid="$(lsof -nP -iTCP:"$port" -sTCP:LISTEN -t 2>/dev/null | head -1 || true)"
	elif command -v ss >/dev/null 2>&1; then
		pid="$(ss -ltnpH "sport = :$port" 2>/dev/null |
			sed -n 's/.*pid=\([0-9][0-9]*\).*/\1/p' | head -1 || true)"
	fi
	printf '%s' "$pid"
}

# 只识别本项目遗留的进程：旧版 make 用 go run 启动的后端，或本项目目录下的 Vite。
is_project_legacy_process() {
	local pid="$1"
	local kind="$2"
	local command_line=""
	local executable=""

	command_line="$(ps -p "$pid" -o command= 2>/dev/null || true)"
	if [[ -z "$command_line" ]]; then
		return 1
	fi
	executable="${command_line%% *}"
	executable="${executable##*/}"
	case "$kind" in
	api)
		[[ "$executable" == "euler" || "$executable" == "euler-dev" ]]
		;;
	web)
		[[ "$command_line" == *vite* && "$command_line" == *"$ROOT_DIR"* ]]
		;;
	*)
		return 1
		;;
	esac
}

# 端口被占用时仅接管本项目遗留进程；其他进程一律提示后退出。
reclaim_port() {
	local name="$1"
	local port="$2"
	local kind="$3"
	local pid=""
	local deadline

	pid="$(listener_pid "$port")"
	if [[ -z "$pid" ]]; then
		printf '%s启动前检测到端口 %s 已有监听者，但无法确定监听进程，未启动新进程\n' \
			"$name" "$port" >&2
		return 1
	fi
	if ! is_project_legacy_process "$pid" "$kind"; then
		printf '%s启动前检测到端口 %s 已被其他进程占用（PID %s），未启动新进程\n' \
			"$name" "$port" "$pid" >&2
		printf '请先停止该进程，或用 EULER_PORT / WEB_PORT 指定其他端口\n' >&2
		return 1
	fi
	printf '检测到本项目遗留的%s进程（PID %s），先停止以释放端口 %s\n' "$name" "$pid" "$port"
	if ! terminate_process "$name" "$pid"; then
		return 1
	fi
	deadline=$((SECONDS + 10))
	while ((SECONDS < deadline)); do
		if ! port_in_use "$port"; then
			return 0
		fi
		sleep 1
	done
	printf '端口 %s 在停止遗留进程后仍被占用，未启动新进程\n' "$port" >&2
	return 1
}

# 启动前保证端口可用：未被占用直接通过，本项目遗留进程先接管，其他进程报错。
ensure_port_available() {
	local name="$1"
	local port="$2"
	local kind="$3"
	if port_in_use "$port"; then
		reclaim_port "$name" "$port" "$kind"
	fi
}

write_process_metadata() {
	local pid="$1"
	local token="$2"
	local pid_file="$3"
	local start_file="$4"
	if ! printf '%s\n' "$token" > "$start_file"; then
		return 1
	fi
	if ! printf '%s\n' "$pid" > "$pid_file"; then
		return 1
	fi
}

register_process() {
	local name="$1"
	local pid="$2"
	local pid_file="$3"
	local start_file="$4"
	local token=""

	if ! token="$(process_start_token "$pid")"; then
		printf '无法记录%s新进程的启动时间，将清理该进程（PID %s）\n' "$name" "$pid" >&2
		if ! terminate_process "$name" "$pid"; then
			printf '%s新进程清理失败，请手工检查 PID %s\n' "$name" "$pid" >&2
		fi
		return 1
	fi
	if ! write_process_metadata "$pid" "$token" "$pid_file" "$start_file"; then
		printf '写入%s的 PID 或启动身份文件失败，将清理新进程（PID %s）\n' "$name" "$pid" >&2
		if terminate_process "$name" "$pid"; then
			cleanup_metadata "$name" "$pid_file" "$start_file" || true
		else
			printf '%s新进程清理失败，请手工检查 PID %s\n' "$name" "$pid" >&2
		fi
		return 1
	fi
}

wait_for_service() {
	local name="$1"
	local pid="$2"
	local url="$3"
	local log_file="$4"
	local pid_file="$5"
	local start_file="$6"
	local deadline=$((SECONDS + 60))

	while ((SECONDS < deadline)); do
		if ! kill -0 "$pid" 2>/dev/null; then
			printf '%s进程提前退出（PID %s），最近日志：\n' "$name" "$pid" >&2
			show_log "$log_file"
			cleanup_metadata "$name" "$pid_file" "$start_file" || true
			return 1
		fi
		if curl -sf --max-time 1 -o /dev/null "$url"; then
			if kill -0 "$pid" 2>/dev/null; then
				printf '%s已就绪 %s，日志 %s\n' "$name" "$url" "$log_file"
				return 0
			fi
		fi
		sleep 1
	done
	printf '%s启动超时（约 60 秒），最近日志：\n' "$name" >&2
	show_log "$log_file"
	if ! stop_process "$name" "$pid_file" "$start_file"; then
		printf '%s超时后的进程清理失败\n' "$name" >&2
	fi
	return 1
}

api() {
	ensure_port_available "后端" "$EULER_PORT" api
	if [[ -n "$EULER_CONFIG" ]]; then
		go run ./cmd/euler -config "$EULER_CONFIG"
	else
		go run ./cmd/euler
	fi
}

restart_api() {
	local pid
	local temp_binary="$API_BINARY.tmp.$$"

	mkdir -p -- "$STATE_DIR"
	if ! go build -o "$temp_binary" ./cmd/euler; then
		echo "后端构建失败，旧服务保持运行" >&2
		rm -f -- "$temp_binary" || true
		return 1
	fi
	if ! stop_api; then
		rm -f -- "$temp_binary" || true
		return 1
	fi
	if ! ensure_port_available "后端" "$EULER_PORT" api; then
		rm -f -- "$temp_binary" || true
		return 1
	fi
	if ! mv -f -- "$temp_binary" "$API_BINARY"; then
		echo "替换后端开发二进制失败" >&2
		rm -f -- "$temp_binary" || true
		return 1
	fi
	: > "$API_LOG"
	if [[ -n "$EULER_CONFIG" ]]; then
		nohup "$API_BINARY" -config "$EULER_CONFIG" >"$API_LOG" 2>&1 &
	else
		nohup "$API_BINARY" >"$API_LOG" 2>&1 &
	fi
	pid=$!
	if ! register_process "后端" "$pid" "$API_PID_FILE" "$API_START_FILE"; then
		show_log "$API_LOG"
		return 1
	fi
	wait_for_service "后端" "$pid" "http://127.0.0.1:$EULER_PORT/health" \
		"$API_LOG" "$API_PID_FILE" "$API_START_FILE"
}

restart_web() {
	local pid

	mkdir -p -- "$STATE_DIR"
	if ! stop_web; then
		return 1
	fi
	if ! ensure_port_available "前端" "$WEB_PORT" web; then
		return 1
	fi
	: > "$WEB_LOG"
	nohup node web/node_modules/vite/bin/vite.js web \
		--host 127.0.0.1 --port "$WEB_PORT" --strictPort >"$WEB_LOG" 2>&1 &
	pid=$!
	if ! register_process "前端" "$pid" "$WEB_PID_FILE" "$WEB_START_FILE"; then
		show_log "$WEB_LOG"
		return 1
	fi
	wait_for_service "前端" "$pid" "http://127.0.0.1:$WEB_PORT/" \
		"$WEB_LOG" "$WEB_PID_FILE" "$WEB_START_FILE"
}

if (($# != 1)); then
	usage
	exit 2
fi

case "$1" in
	check-env)
		check_env
		;;
	test-smoke)
		if [[ -z "${OPENAI_API_KEY:-}" ]]; then
			echo "缺少 OPENAI_API_KEY，请先设置后再运行真实模型冒烟测试" >&2
			exit 1
		fi
		npm --prefix web run test:smoke
		;;
	api)
		api
		;;
	restart-api)
		restart_api
		;;
	restart-web)
		restart_web
		;;
	stop-api)
		stop_api
		;;
	stop-web)
		stop_web
		;;
	reload)
		npm --prefix web run build
		restart_api
		;;
	restart-dev)
		restart_api
		restart_web
		;;
	start)
		check_env
		npm --prefix web ci
		npm --prefix web run build
		restart_api
		restart_web
		;;
	stop)
		stop_all
		;;
	restart)
		check_env
		"$ROOT_DIR/scripts/build.sh" all
		restart_api
		restart_web
		;;
	*)
		echo "未知命令：$1" >&2
		usage
		exit 2
		;;
esac

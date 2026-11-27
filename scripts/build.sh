#!/usr/bin/env bash
set -euo pipefail

# 从脚本位置定位仓库根目录，确保可从任意工作目录执行。
SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "$SCRIPT_DIR/.." && pwd)"
cd "$ROOT_DIR"

if (( $# > 1 )); then
	printf '错误：参数过多，仅支持无参数、all 或 agent-dist。\n' >&2
	exit 1
fi

target="${1:-all}"
case "$target" in
	all)
		npm --prefix web ci
		npm --prefix web run build
		mkdir -p bin
		go build -o bin/euler ./cmd/euler
		;;
	agent-dist)
		# 输出目录可由 AGENT_DIST 覆盖，默认放在 bin/agent-dist。
		agent_dist="${AGENT_DIST:-bin/agent-dist}"
		mkdir -p "$agent_dist"
		GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
			-o "$agent_dist/euler-agent-linux-amd64" ./cmd/euler-agent
		GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" \
			-o "$agent_dist/euler-agent-linux-arm64" ./cmd/euler-agent
		;;
	*)
		printf '错误：未知构建目标“%s”，仅支持 all 和 agent-dist。\n' "$target" >&2
		exit 1
		;;
esac

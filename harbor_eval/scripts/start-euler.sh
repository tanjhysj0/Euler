#!/usr/bin/env bash
set -euo pipefail
# 以指定预算启动 Euler；配置里同时固定 shell PATH 前缀，使宿主 shell 使用
# 与判分容器一致的 Python 3.13。默认 10 轮、5 分钟。
max_rounds=10
max_duration=5m
model_timeout=180s
shell_timeout=45s
reasoning_effort=high
listen="127.0.0.1:8080"
runtime_bin="${EULER_RUNTIME_BIN:-/tmp/euler-harbor/runtime-bin}"
print_only=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --max-rounds) max_rounds="$2"; shift 2 ;;
    --max-duration) max_duration="$2"; shift 2 ;;
    --model-timeout) model_timeout="$2"; shift 2 ;;
    --reasoning-effort) reasoning_effort="$2"; shift 2 ;;
    --shell-timeout) shell_timeout="$2"; shift 2 ;;
    --listen) listen="$2"; shift 2 ;;
    --runtime-bin) runtime_bin="$2"; shift 2 ;;
    --print-config) print_only=1; shift ;;
    *) echo "未知参数: $1" >&2; exit 1 ;;
  esac
done

config_text="listen: ${listen}
shell_path_prefix: ${runtime_bin}
shell_timeout: ${shell_timeout}
model_timeout: ${model_timeout}
model_reasoning_effort: ${reasoning_effort}
agent:
  max_model_rounds: ${max_rounds}
  max_tool_calls: 40
  max_duration: ${max_duration}
  max_output_bytes: 65536
"

if [[ "$print_only" -eq 1 ]]; then
  printf '%s' "$config_text"
  exit 0
fi

if [[ -z "${EULER_DATABASE_URL:-}" ]]; then
  echo "缺少 EULER_DATABASE_URL" >&2
  exit 1
fi

config_path="$(mktemp -t euler-config-XXXXXX).yaml"
printf '%s' "$config_text" > "$config_path"
exec ./bin/euler -config "$config_path"

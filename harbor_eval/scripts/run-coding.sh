#!/usr/bin/env bash
set -euo pipefail
# 在任务容器内直接运行 Euler agent 二进制的本地冒烟任务。
repo_root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$repo_root"
export PYTHONPATH="$repo_root/harbor_eval${PYTHONPATH:+:$PYTHONPATH}"
export EULER_MODEL_BASE_URL="${EULER_MODEL_BASE_URL:-https://api.deepseek.com}"
export EULER_MODEL="${EULER_MODEL:-deepseek-v4-flash}"
export EULER_REASONING_EFFORT="${EULER_REASONING_EFFORT:-high}"

command -v harbor >/dev/null 2>&1 || { echo "未找到 harbor 命令" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { echo "未找到 docker 命令" >&2; exit 1; }

# 允许 list 任务在 agent 阶段访问模型与追踪服务
model_host="${EULER_MODEL_BASE_URL#*://}"; model_host="${model_host%%/*}"
allow_args=(--allow-agent-host "$model_host")
langfuse_url="${LANGFUSE_HOST:-${LANGFUSE_BASE_URL:-}}"
if [ -n "$langfuse_url" ]; then
  langfuse_host="${langfuse_url#*://}"; langfuse_host="${langfuse_host%%/*}"
  allow_args+=(--allow-agent-host "$langfuse_host")
fi

make -C "$repo_root" agent-dist

run_id=$(date +%Y%m%d-%H%M%S)
results_dir="harbor_eval/results/$run_id"
job_name="coding-euler-$run_id"

harbor run -c harbor_eval/jobs/coding-euler.yaml -y "${allow_args[@]}" \
  --job-name "$job_name" -o "$results_dir"

echo "===== 本次运行 $run_id 的 reward ====="
job_dir="$results_dir/$job_name"
if [ ! -d "$job_dir" ]; then
  echo "[$job_name] 未找到结果目录: $job_dir" >&2
  exit 1
fi
found=0
failed=0
while IFS= read -r reward_file; do
  found=1
  reward="$(cat "$reward_file")"
  echo "[$job_name] reward=$reward"
  echo "  路径: $reward_file"
  echo "  mtime: $(date -r "$reward_file" '+%Y-%m-%d %H:%M:%S')"
  if [ "$reward" != "1" ]; then
    failed=1
  fi
done < <(find "$job_dir" -name 'reward.txt' -print)
if [ "$found" -eq 0 ]; then
  echo "[$job_name] 未找到 reward.txt: $job_dir" >&2
  exit 1
fi
exit "$failed"

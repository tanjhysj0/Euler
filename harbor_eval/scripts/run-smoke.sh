#!/usr/bin/env bash
set -euo pipefail
repo_root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$repo_root"
export PYTHONPATH="$repo_root/harbor_eval${PYTHONPATH:+:$PYTHONPATH}"
export EULER_BASE_URL="${EULER_BASE_URL:-http://127.0.0.1:8080}"
export EULER_HARBOR_WORKSPACE="${EULER_HARBOR_WORKSPACE:-/tmp/euler-harbor/workspace}"

# 每次运行使用唯一标识，避免 Harbor 复用历史 trial 打印过期 reward
run_id=$(date +%Y%m%d-%H%M%S)
results_dir="harbor_eval/results/$run_id"

command -v harbor >/dev/null 2>&1 || { echo "未找到 harbor 命令" >&2; exit 1; }
command -v docker >/dev/null 2>&1 || { echo "未找到 docker 命令" >&2; exit 1; }

./harbor_eval/scripts/prepare-workspace.sh
harbor run -c harbor_eval/jobs/hello-oracle.yaml -y \
  --job-name "hello-oracle-$run_id" -o "$results_dir"

./harbor_eval/scripts/prepare-workspace.sh
if ! curl -sf --max-time 5 "$EULER_BASE_URL/health" >/dev/null; then
  echo "Euler 健康检查失败：$EULER_BASE_URL/health 不可达，请先启动 Euler 服务" >&2
  exit 1
fi
harbor run -c harbor_eval/jobs/hello-euler.yaml -y \
  --job-name "hello-euler-$run_id" -o "$results_dir"

./harbor_eval/scripts/prepare-workspace.sh
harbor run -c harbor_eval/jobs/hello-nop.yaml -y \
  --job-name "hello-nop-$run_id" -o "$results_dir"

# 只汇总本次 run_id 的结果，并标注路径与 mtime 以确认新鲜度
echo "===== 本次运行 $run_id 的 reward ====="
gate_status=0
for kind in oracle euler nop; do
  case "$kind" in
    nop) expected=0 ;;
    *) expected=1 ;;
  esac
  job="hello-$kind-$run_id"
  job_dir="$results_dir/$job"
  if [ ! -d "$job_dir" ]; then
    echo "[$job] 未找到结果目录: $job_dir" >&2
    gate_status=1
    continue
  fi
  reward_file=$(find "$job_dir" -name 'reward.txt' -print -quit)
  if [ -z "$reward_file" ]; then
    echo "[$job] 未找到 reward.txt: $job_dir" >&2
    gate_status=1
    continue
  fi
  reward=$(tr -d '[:space:]' < "$reward_file")
  echo "[$job] reward=$reward"
  echo "  路径: $reward_file"
  echo "  mtime: $(date -r "$reward_file" '+%Y-%m-%d %H:%M:%S')"
  if [ "$reward" != "$expected" ]; then
    echo "[$job] reward 断言失败：期望 ${expected}，实际 $reward" >&2
    gate_status=1
  fi
done

if [ "$gate_status" -ne 0 ]; then
  echo "冒烟门禁失败：期望 Oracle=1 Euler=1 nop=0" >&2
  exit 1
fi
echo "冒烟门禁通过：Oracle=1 Euler=1 nop=0"

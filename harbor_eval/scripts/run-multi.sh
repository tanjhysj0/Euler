#!/usr/bin/env bash
set -euo pipefail

# 定位仓库根目录，确保任务路径、结果路径和模块路径保持一致。
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repo_root"
mode="${HARBOR_MODE:-multi}"

# 模型配置必须先校验，避免配置错误时触发 Docker 清理或 agent 构建。
if [ -z "${EULER_MODEL_API_KEY:-}" ] && [ -z "${OPENAI_API_KEY:-}" ]; then
  echo "缺少模型密钥：请设置 EULER_MODEL_API_KEY 或 OPENAI_API_KEY" >&2
  exit 1
fi
if [ -z "${EULER_MODEL:-}" ]; then
  echo "缺少模型名：请设置 EULER_MODEL" >&2
  exit 1
fi
if [ -z "${EULER_MODEL_BASE_URL:-}" ]; then
  echo "缺少模型 API 地址：请设置 EULER_MODEL_BASE_URL" >&2
  exit 1
fi

export PYTHONPATH="$repo_root/harbor_eval${PYTHONPATH:+:$PYTHONPATH}"
export EULER_MODEL_BASE_URL
export EULER_MODEL
export EULER_REASONING_EFFORT="${EULER_REASONING_EFFORT:-}"
export EULER_MODEL_TIMEOUT="${EULER_MODEL_TIMEOUT:-10s}"
export EULER_MODEL_REQUEST_TIMEOUT="${EULER_MODEL_REQUEST_TIMEOUT:-5m}"
export AGENT_DIST="${AGENT_DIST:-bin/agent-dist}"
HARBOR_PER_DATASET="${HARBOR_PER_DATASET:-1}"
HARBOR_CONCURRENCY="${HARBOR_CONCURRENCY:-4}"
HARBOR_AGENT_TIMEOUT="${HARBOR_AGENT_TIMEOUT:-780}"

explicit_tasks=()
selected_tasks=()
datasets=()
if [ "$mode" != "smoke" ] && [ "$mode" != "coding" ]; then
  if [ -n "${HARBOR_TASKS:-}" ]; then
    # 显式任务使用空格分隔，并优先于任何任务清单。
    read -r -a explicit_tasks <<< "$HARBOR_TASKS"
  else
    tasks_file="${HARBOR_TASKS_FILE-harbor_eval/tasks/selected-tasks.txt}"
    if [ -n "$tasks_file" ]; then
      if [ ! -f "$tasks_file" ]; then
        echo "任务清单不存在：$tasks_file" >&2
        exit 1
      fi
      # 在运行时读取清单，忽略空行和井号开头的注释。
      while IFS= read -r line || [ -n "$line" ]; do
        line="${line#"${line%%[![:space:]]*}"}"
        line="${line%"${line##*[![:space:]]}"}"
        if [ -z "$line" ] || [[ "$line" == \#* ]]; then
          continue
        fi
        selected_tasks+=("$line")
      done < "$tasks_file"
    fi
  fi

  # 没有显式任务或精选任务时，才回退到数据集抽样。
  if [ "${#explicit_tasks[@]}" -eq 0 ] && [ "${#selected_tasks[@]}" -eq 0 ]; then
    harbor_datasets="${HARBOR_DATASETS:-devopsgym tb3-preview-v2 terminal-bench-3-test swe-gen-js android-bench swe-gen-rust LiteCoder-rl swe-gen-cpp swe-gen-java mimo-v26-code swe-gen-go aider-polyglot algotune mimo-v26-terminal humanevalfix bigcodebench-hard-complete tb21-code-debug binary-audit}"
    read -r -a datasets <<< "$harbor_datasets"
  fi
fi

cfg=""
cleanup() {
  status=$?
  trap - EXIT
  if [ -n "$cfg" ]; then
    rm -f -- "$cfg" || true
  fi
  "$repo_root/harbor_eval/scripts/clean.sh" >/dev/null 2>&1 || true
  exit "$status"
}
trap cleanup EXIT

# 运行前后各清理一次，避免异常退出留下 Harbor 网络和容器。
"$repo_root/harbor_eval/scripts/clean.sh"

if [ "$mode" = "smoke" ]; then
  "$repo_root/harbor_eval/scripts/run-smoke.sh"
  exit 0
elif [ "$mode" = "coding" ]; then
  "$repo_root/harbor_eval/scripts/run-coding.sh"
  exit 0
fi

# 仅多任务模式在此构建，其他模式由各自脚本构建，避免重复。
echo "==> 构建容器内 agent 二进制"
"$repo_root/scripts/build.sh" agent-dist

model_host="${EULER_MODEL_BASE_URL#*://}"
model_host="${model_host%%/*}"
allow_args=(--allow-agent-host "$model_host")
langfuse_url="${LANGFUSE_HOST:-${LANGFUSE_BASE_URL:-}}"
if [ -n "$langfuse_url" ]; then
  langfuse_host="${langfuse_url#*://}"
  langfuse_host="${langfuse_host%%/*}"
  allow_args+=(--allow-agent-host "$langfuse_host")
fi

echo "==> 运行 Harbor"
run_id=$(date +%Y%m%d-%H%M%S)
job_name="coding-multi-$run_id"
results_dir="harbor_eval/results/$run_id"
cfg=$(mktemp "${TMPDIR:-/tmp}/euler-harbor-multi-XXXXXX")
{
  printf 'job_name: %s\n' "$job_name"
  printf 'jobs_dir: harbor_eval/results\n'
  printf 'n_concurrent_trials: %s\n' "$HARBOR_CONCURRENCY"
  if [ "${#explicit_tasks[@]}" -gt 0 ]; then
    printf 'tasks:\n'
    for pick in "${explicit_tasks[@]}"; do
      printf '  - path: harbor_eval/tasks/_downloads/%s\n' "$pick"
    done
  elif [ "${#selected_tasks[@]}" -gt 0 ]; then
    printf 'tasks:\n'
    for path in "${selected_tasks[@]}"; do
      printf '  - path: %s\n' "$path"
    done
  else
    printf 'datasets:\n'
    for dataset in "${datasets[@]}"; do
      printf '  - path: harbor_eval/tasks/_downloads/%s\n' "$dataset"
      printf '    n_tasks: %s\n' "$HARBOR_PER_DATASET"
    done
  fi
  printf 'agents:\n'
  printf '  - import_path: agents.euler_agent:EulerAgent\n'
  printf '    override_timeout_sec: %s\n' "$HARBOR_AGENT_TIMEOUT"
  printf 'environment:\n'
  printf '  type: docker\n'
} > "$cfg"

if [ "${#explicit_tasks[@]}" -gt 0 ]; then
  echo "  任务：${explicit_tasks[*]}，并发 $HARBOR_CONCURRENCY"
elif [ "${#selected_tasks[@]}" -gt 0 ]; then
  echo "  精选任务：${#selected_tasks[@]} 个，每个 agent 上限 ${HARBOR_AGENT_TIMEOUT}s，并发 $HARBOR_CONCURRENCY"
else
  echo "  数据集：${#datasets[@]} 个，每个 $HARBOR_PER_DATASET 个任务，并发 $HARBOR_CONCURRENCY"
fi

harbor run -c "$cfg" -y "${allow_args[@]}" \
  --job-name "$job_name" -o "$results_dir"
job_dir="$results_dir/$job_name"
echo "==> 结果目录：$job_dir"
find "$job_dir" -name reward.txt -exec cat {} \; 2>/dev/null | \
  awk '{total++; if ($1 == "1") passed++} END {printf "==> 通过 %d/%d\n", passed, total}'

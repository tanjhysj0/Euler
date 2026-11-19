#!/usr/bin/env bash
set -euo pipefail
repo_root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$repo_root"
export PYTHONPATH="$repo_root/harbor_eval${PYTHONPATH:+:$PYTHONPATH}"
export EULER_BASE_URL="${EULER_BASE_URL:-http://127.0.0.1:8080}"
export EULER_HARBOR_WORKSPACE="${EULER_HARBOR_WORKSPACE:-/tmp/euler-harbor/workspace}"

./harbor_eval/scripts/prepare-workspace.sh
harbor run -c harbor_eval/jobs/hello-oracle.yaml -y

./harbor_eval/scripts/prepare-workspace.sh
harbor run -c harbor_eval/jobs/hello-euler.yaml -y

./harbor_eval/scripts/prepare-workspace.sh
harbor run -c harbor_eval/jobs/hello-nop.yaml -y

find harbor_eval/results -name 'reward*' -print -exec cat {} \;

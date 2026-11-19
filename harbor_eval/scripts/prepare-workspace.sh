#!/usr/bin/env bash
set -euo pipefail
fixed_workspace="/tmp/euler-harbor/workspace"
workspace="${EULER_HARBOR_WORKSPACE:-$fixed_workspace}"

# 删除护栏：仅剥离末尾斜杠，且仅允许固定工作区，避免误删其他目录
while [[ "$workspace" == */ && "$workspace" != "/" ]]; do
  workspace="${workspace%/}"
done
if [[ "$workspace" != "$fixed_workspace" ]]; then
  echo "拒绝删除非固定工作区: ${workspace:-<空>}" >&2
  exit 1
fi

rm -rf "$workspace"
mkdir -p "$workspace"
chmod 0777 "$workspace"
echo "workspace ready: $workspace"

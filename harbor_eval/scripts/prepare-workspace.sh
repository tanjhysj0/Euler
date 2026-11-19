#!/usr/bin/env bash
set -euo pipefail
workspace="${EULER_HARBOR_WORKSPACE:-/tmp/euler-harbor/workspace}"

# 删除护栏：仅允许 /tmp/ 下至少一级的真实子路径，拒绝空值、/tmp 本身与 .. 穿越
case "$workspace" in
  /tmp/?*) ;;
  *)
    echo "拒绝删除非 /tmp 下的工作区: ${workspace:-<空>}" >&2
    exit 1
    ;;
esac
case "$workspace" in
  *..*)
    echo "拒绝删除包含 .. 的工作区: $workspace" >&2
    exit 1
    ;;
esac

rm -rf "$workspace"
mkdir -p "$workspace"
chmod 0777 "$workspace"
echo "workspace ready: $workspace"

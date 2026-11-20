#!/usr/bin/env bash
set -euo pipefail
# 每个 trial 在根目录下使用独立子目录，因此这里只清理并重建根目录。
fixed_root="/tmp/euler-harbor/workspaces"
root="${EULER_HARBOR_WORKSPACES:-$fixed_root}"

# 删除护栏：仅剥离末尾斜杠，且仅允许固定根目录，避免误删其他目录
while [[ "$root" == */ && "$root" != "/" ]]; do
  root="${root%/}"
done
if [[ "$root" != "$fixed_root" ]]; then
  echo "拒绝删除非固定根目录: ${root:-<空>}" >&2
  exit 1
fi

rm -rf "$root"
mkdir -p "$root"
chmod 0777 "$root"
echo "workspaces root ready: $root"

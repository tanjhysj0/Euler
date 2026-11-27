#!/usr/bin/env bash
set -euo pipefail

# 定位仓库根目录，确保从任意工作目录执行都清理同一批评测资源。
repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
cd "$repo_root"

networks=$(docker network ls --format '{{.Name}}')
while IFS= read -r network; do
  case "$network" in
    *__env_*) ;;
    *) continue ;;
  esac

  echo "==> 清理 Harbor 网络：$network"
  containers=$(docker network inspect "$network" \
    --format '{{range .Containers}}{{.Name}} {{end}}' 2>/dev/null || true)
  # Docker 以空格分隔容器名称，这里需要按空格分词后逐个强制删除。
  for container_name in $containers; do
    docker rm -f "$container_name" >/dev/null 2>&1 || true
  done
  docker network rm "$network" >/dev/null 2>&1 || true
done <<< "$networks"

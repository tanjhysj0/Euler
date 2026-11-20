#!/usr/bin/env bash
set -euo pipefail
# 生成与判分环境一致的 Python 运行时垫片：把 python/python3 指到 3.13。
runtime_bin="${EULER_RUNTIME_BIN:-/tmp/euler-harbor/runtime-bin}"
python_bin=""

if command -v uv >/dev/null 2>&1; then
  python_bin="$(uv python find 3.13 2>/dev/null || true)"
fi
if [[ -z "$python_bin" ]] && command -v python3.13 >/dev/null 2>&1; then
  python_bin="$(command -v python3.13)"
fi
if [[ -z "$python_bin" ]]; then
  echo "未找到 Python 3.13，请先安装 uv 或 python3.13" >&2
  exit 1
fi

mkdir -p "$runtime_bin"
ln -sfn "$python_bin" "$runtime_bin/python3"
ln -sfn "$python_bin" "$runtime_bin/python"
echo "runtime ready: $runtime_bin -> $python_bin"

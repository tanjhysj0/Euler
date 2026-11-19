#!/usr/bin/env bash
set -euo pipefail
workspace="${EULER_HARBOR_WORKSPACE:-/tmp/euler-harbor/workspace}"
rm -rf "$workspace"
mkdir -p "$workspace"
chmod 0777 "$workspace"
echo "workspace ready: $workspace"

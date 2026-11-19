#!/usr/bin/env bash
# golangci-lint 质量门禁 Hook
# 事件：SessionStart / PostToolUse(Edit|Write) / Stop
# 依据：项目根目录 .golangci.yml
#   - SessionStart：校验配置并注入关键阈值到上下文
#   - PostToolUse：编辑 .golangci.yml 时校验；编辑 .go 时 lint 所在包
#   - Stop：结束任务前全量 lint，有 issue 则阻断并要求继续修复
# 注意：配置里 issues-exit-code=0（本地不阻断），Hook 统一用 --issues-exit-code=1 覆盖。

set -uo pipefail

PROJECT_DIR="${TRAE_PROJECT_DIR:-$(pwd)}"
CONFIG="$PROJECT_DIR/.golangci.yml"
INPUT="$(cat 2>/dev/null || true)"

# 缺依赖时静默放行，避免 Hook 自身阻断正常开发
command -v jq >/dev/null 2>&1 || exit 0
command -v golangci-lint >/dev/null 2>&1 || exit 0
[ -f "$CONFIG" ] || exit 0

jqr() { printf '%s' "$INPUT" | jq -r "$1" 2>/dev/null || true; }

# 取有效行、限行数、限列宽，避免 Hook 输出过大
clip() { awk 'NF' | head -n "${1:-40}" | cut -c1-500; }

block() {
  local reason="$1"
  jq -n --arg reason "$reason" '{decision: "block", reason: $reason}'
}

find_module_root() {
  local dir="$1"
  [ -d "$dir" ] || dir="$(dirname "$dir")"
  while [ -n "$dir" ] && [ "$dir" != "/" ]; do
    if [ -f "$dir/go.mod" ]; then
      printf '%s\n' "$dir"
      return 0
    fi
    dir="$(dirname "$dir")"
  done
  return 1
}

EVENT="$(jqr '.hook_event_name // empty')"
[ -n "$EVENT" ] || exit 0

case "$EVENT" in
  SessionStart)
    VERIFY_OUT="$(cd "$PROJECT_DIR" && golangci-lint config verify 2>&1)"
    if [ -z "$VERIFY_OUT" ]; then
      CTX="代码质量门禁已启用：golangci-lint 2.12.2 + .golangci.yml。编辑 .go 后会 lint 所在包，结束任务前会全量 lint，未通过将阻断。关键阈值：funlen<=60 行/40 语句，gocognit 与认知复杂度<=15，嵌套<=4，参数<=4、返回值<=3，行宽<=100，文件<=400 行；app/domain/infra 禁止 import gin；gosec=low/low。"
    else
      CTX="代码质量门禁已启用，但 .golangci.yml 校验失败：$(printf '%s' "$VERIFY_OUT" | clip 10)"
    fi
    jq -n --arg ctx "$CTX" '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: $ctx}}'
    ;;

  PostToolUse)
    FILE="$(jqr '.tool_input.file_path // .tool_input.filePath // .tool_input.path // empty')"
    [ -n "$FILE" ] || exit 0

    case "$FILE" in
      */.golangci.yml|*/.golangci.yaml)
        if ! VERIFY_OUT="$(cd "$PROJECT_DIR" && golangci-lint config verify 2>&1)"; then
          block "golangci-lint 配置校验失败，请修正 .golangci.yml：
$(printf '%s' "$VERIFY_OUT" | clip 20)"
        fi
        exit 0
        ;;
      *.go) ;;
      *) exit 0 ;;
    esac

    [ -f "$FILE" ] || exit 0
    MODULE="$(find_module_root "$(dirname "$FILE")")" || exit 0

    PKG_DIR="$(cd "$(dirname "$FILE")" && pwd)"
    REL="${PKG_DIR#"$MODULE"}"
    REL="${REL#/}"
    [ -n "$REL" ] || REL="."
    PKG_ARG="./${REL}"
    [ "$REL" = "." ] && PKG_ARG="."

    OUT="$(cd "$MODULE" && golangci-lint run --issues-exit-code=1 --timeout=2m "$PKG_ARG" 2>&1)"
    RC=$?
    [ "$RC" -eq 0 ] && exit 0

    # RC=1 表示有 issue：仅当报告涉及本次编辑文件时阻断，避免被包内历史问题干扰
    if [ "$RC" -eq 1 ]; then
      BASENAME="$(basename "$FILE")"
      case "$OUT" in
        *"$BASENAME"*) ;;
        *) exit 0 ;;
      esac
    fi

    block "golangci-lint 在本次修改后报告问题（package ${PKG_ARG}，exit ${RC}），请修复后继续：
$(printf '%s' "$OUT" | clip 40)"
    ;;

  Stop)
    MODULE="$(find_module_root "$PROJECT_DIR")" || exit 0
    FIRST_GO="$(find "$MODULE" -name '*.go' -not -path '*/vendor/*' -not -path '*/.git/*' 2>/dev/null | head -n 1)"
    [ -n "$FIRST_GO" ] || exit 0

    OUT="$(cd "$MODULE" && golangci-lint run --issues-exit-code=1 --timeout=5m 2>&1)"
    RC=$?
    [ "$RC" -eq 0 ] && exit 0

    if [ "$RC" -eq 1 ]; then
      block "结束前 golangci-lint 未通过，请修复以下问题后再结束：
$(printf '%s' "$OUT" | clip 60)"
    else
      block "golangci-lint 执行失败（exit ${RC}），请先排除配置或编译错误：
$(printf '%s' "$OUT" | clip 30)"
    fi
    ;;

  *) exit 0 ;;
esac

#!/usr/bin/env python3
"""从 Euler 数据库重放一次 run 的工具调用，重建最终工作区。

用途：失败的 trial 结束后，Agent 的工作区可能已被清理，但 tool_calls 表里
仍保留 file_write / file_patch 参数。本脚本按时间顺序重放这些调用，得到
最终代码，便于离线复判或定位模型错在哪一步。

限制：shell 调用创建的文件无法重放，脚本会列出警告。
"""

from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import uuid
from pathlib import Path


class ReplayError(RuntimeError):
    """重放失败，例如补丁基线不匹配或路径越界。"""


def _resolve(root: Path, relative: str) -> Path:
    """把相对路径解析到 root 内，拒绝越界路径。"""
    candidate = (root / relative).resolve()
    base = root.resolve()
    if candidate != base and base not in candidate.parents:
        raise ReplayError(f"路径越界: {relative}")
    return candidate


def reconstruct(calls: list[dict], root: Path) -> list[str]:
    """按顺序重放工具调用，返回无法重放的告警列表。"""
    root = Path(root)
    root.mkdir(parents=True, exist_ok=True)
    warnings: list[str] = []
    for call in calls:
        name = call.get("name", "")
        args = call.get("arguments") or {}
        if name == "file_write":
            path = _resolve(root, args["path"])
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(args.get("content", ""), encoding="utf-8")
        elif name == "file_patch":
            path = _resolve(root, args["path"])
            if not path.exists():
                raise ReplayError(f"补丁目标不存在: {args['path']}")
            text = path.read_text(encoding="utf-8")
            old = args.get("old_string", "")
            if old not in text:
                raise ReplayError(f"old_string 不匹配: {args['path']}")
            path.write_text(
                text.replace(old, args.get("new_string", ""), 1),
                encoding="utf-8",
            )
        elif name == "file_delete":
            _resolve(root, args["path"]).unlink(missing_ok=True)
        elif name in {"file_read", "file_list"}:
            continue
        else:
            preview = json.dumps(args, ensure_ascii=False)[:120]
            warnings.append(f"{name} 调用无法重放，已跳过: {preview}")
    return warnings


def run_id_from_result(path: Path) -> str:
    """从 trial 的 result.json 中读取 Euler run_id；也接受 job 目录。"""
    target = Path(path)
    if target.is_dir():
        candidates = sorted(target.glob("*/result.json"))
        if not candidates:
            raise ReplayError(f"目录中没有 trial result.json: {target}")
        target = candidates[0]
    data = json.loads(target.read_text(encoding="utf-8"))
    metadata = (data.get("agent_result") or {}).get("metadata") or {}
    run_id = metadata.get("run_id")
    if not run_id:
        raise ReplayError(f"结果中没有 run_id: {target}")
    return str(uuid.UUID(str(run_id)))


def fetch_tool_calls(
    run_id: str, container: str, db_name: str, db_user: str
) -> list[dict]:
    """从 PostgreSQL 读取一次 run 的全部工具调用。"""
    run_id = str(uuid.UUID(run_id))
    sql = (
        "select coalesce(json_agg("
        "json_build_object('name', name, 'arguments', arguments_json::jsonb) "
        f"order by created_at), '[]'::json) from tool_calls where run_id = '{run_id}'"
    )
    result = subprocess.run(
        [
            "docker", "exec", container,
            "psql", "-U", db_user, "-d", db_name, "-tA", "-c", sql,
        ],
        check=False,
        capture_output=True,
        text=True,
    )
    if result.returncode != 0:
        raise ReplayError(f"查询工具调用失败: {result.stderr.strip()}")
    return json.loads(result.stdout.strip() or "[]")


def run_official_tests(task_dir: Path, work: Path) -> int:
    """在重建目录上执行官方 pytest，返回退出码。

    官方测试以 /app 为绝对路径，这里复制测试并把 /app 重写到重建目录。
    """
    tests_dir = task_dir / "tests"
    for name in ("test.py", "test_outputs.py"):
        source = tests_dir / name
        if source.exists():
            text = source.read_text(encoding="utf-8")
            text = text.replace('Path("/app/', f'Path("{work}/')
            (work / name).write_text(text, encoding="utf-8")

    bin_dir = work / ".bin"
    bin_dir.mkdir(exist_ok=True)
    python = bin_dir / "python"
    if not python.exists():
        python.symlink_to(sys.executable)

    env = {**os.environ, "PATH": f"{bin_dir}{os.pathsep}{os.environ['PATH']}"}
    return subprocess.run(
        [sys.executable, "-m", "pytest", "test_outputs.py", "-q"],
        cwd=work,
        env=env,
        check=False,
    ).returncode


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="重放 Euler run 的最终工作区")
    parser.add_argument("--run-id", help="Euler run UUID")
    parser.add_argument("--from-result", help="Harbor trial 的 result.json 路径")
    parser.add_argument("--out", required=True, help="重建输出目录")
    parser.add_argument("--db-container", default="euler-postgres-1")
    parser.add_argument("--db-name", default="euler")
    parser.add_argument("--db-user", default="euler")
    parser.add_argument(
        "--task-dir", help="可选：任务目录，给出后在重建目录执行官方 pytest"
    )
    args = parser.parse_args(argv)

    try:
        if args.from_result:
            run_id = run_id_from_result(Path(args.from_result))
        elif args.run_id:
            run_id = str(uuid.UUID(args.run_id))
        else:
            raise ReplayError("必须提供 --run-id 或 --from-result")
        calls = fetch_tool_calls(
            run_id, args.db_container, args.db_name, args.db_user
        )
    except ReplayError as exc:
        print(str(exc), file=sys.stderr)
        return 1

    work = Path(args.out).resolve()
    if work.exists():
        shutil.rmtree(work)
    try:
        warnings = reconstruct(calls, work)
    except ReplayError as exc:
        print(f"重放失败: {exc}", file=sys.stderr)
        return 1

    print(f"已重放 {len(calls)} 次工具调用 -> {work}")
    for item in warnings:
        print(f"警告: {item}", file=sys.stderr)

    if args.task_dir:
        code = run_official_tests(Path(args.task_dir), work)
        print(f"官方测试退出码: {code}")
        return code
    return 0


if __name__ == "__main__":
    sys.exit(main())

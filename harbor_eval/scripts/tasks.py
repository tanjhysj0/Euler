#!/usr/bin/env python3
"""校验固定任务清单，并补齐清单中尚未下载的任务。"""

import argparse
import shutil
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
DEFAULT_FILES = (
    "harbor_eval/tasks/selected-easy.txt",
    "harbor_eval/tasks/selected-medium.txt",
    "harbor_eval/tasks/selected-hard.txt",
)
DOWNLOADS_ARG = "harbor_eval/tasks/_downloads"
DOWNLOADS_DIR = REPO_ROOT / DOWNLOADS_ARG


class TaskError(Exception):
    """任务清单或下载过程产生的可读错误。"""


def absolute_path(path: str) -> Path:
    """把仓库相对路径转换为绝对路径。"""
    result = Path(path)
    if not result.is_absolute():
        result = REPO_ROOT / result
    return result


def read_tasks(file_name: str) -> list[str]:
    """读取清单，忽略空行和以井号开头的注释。"""
    path = absolute_path(file_name)
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise TaskError(f"无法读取任务清单 {file_name}：{exc}") from exc
    return [line.strip() for line in lines if line.strip() and not line.strip().startswith("#")]


def validate(files: list[str]) -> None:
    """确保每份清单恰好十个任务，总计三十个任务。"""
    total = 0
    for file_name in files:
        count = len(read_tasks(file_name))
        if count != 10:
            raise TaskError(f"任务清单 {file_name} 应为 10 个任务，实际 {count} 个")
        total += count
    if total != 30:
        raise TaskError(f"任务清单总计应为 30 个任务，实际 {total} 个")
    print(f"==> 任务清单校验通过：{total} 个任务")


def download(files: list[str]) -> None:
    """逐个检查任务配置，只下载缺少 task.toml 的任务。"""
    downloaded = 0
    for file_name in files:
        for entry in read_tasks(file_name):
            task_dir = absolute_path(entry)
            if (task_dir / "task.toml").is_file():
                continue
            try:
                pick = task_dir.relative_to(DOWNLOADS_DIR).as_posix()
            except ValueError as exc:
                raise TaskError(f"任务路径不在下载目录中：{entry}") from exc
            if shutil.which("harbor") is None:
                raise TaskError(f"缺少 harbor 命令，无法自动下载任务 {pick}")

            print(f"==> 下载缺失任务：{pick}")
            try:
                result = subprocess.run(
                    ["harbor", "download", pick, "-o", DOWNLOADS_ARG],
                    cwd=REPO_ROOT,
                    check=False,
                )
            except OSError as exc:
                raise TaskError(f"无法执行 harbor 下载任务 {pick}：{exc}") from exc
            if result.returncode != 0:
                raise TaskError(f"下载任务失败：{pick}（退出码 {result.returncode}）")
            if not (task_dir / "task.toml").is_file():
                raise TaskError(f"下载后仍缺少任务配置：{entry}/task.toml")
            downloaded += 1
    print(f"==> 任务下载检查完成，新下载 {downloaded} 个任务")


def parse_args() -> argparse.Namespace:
    """解析 validate 和 download 子命令。"""
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="command", required=True)
    for command, help_text in (("validate", "校验固定任务清单"), ("download", "下载缺失任务")):
        subparser = subparsers.add_parser(command, help=help_text, description=help_text)
        subparser.add_argument(
            "--files",
            nargs="+",
            default=list(DEFAULT_FILES),
            metavar="清单",
            help="指定任务清单；默认使用简单、中等、困难三份固定清单",
        )
    return parser.parse_args()


def main() -> int:
    """执行子命令，并以中文报告预期错误。"""
    args = parse_args()
    try:
        if args.command == "validate":
            validate(args.files)
        else:
            download(args.files)
    except TaskError as exc:
        print(f"错误：{exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

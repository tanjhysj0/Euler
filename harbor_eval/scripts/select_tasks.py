#!/usr/bin/env python3
"""筛选适合本机快速评测的 Harbor 任务。

筛选条件（可在命令行调整）：
  1. CPU / 内存不超过本机 Docker VM 的能力；
  2. 不使用 GPU；
  3. 网络策略为 public（本机 Docker Desktop 不支持 egress-control）；
  4. 预构建镜像必须包含 linux/arm64 清单（本机容器架构）。
输出为每行一个 `dataset/task`，供 `make harbor-test` 按目录直接加载，
避免 Harbor 扫描上万个任务目录。
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
import tomllib
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

TASKS_ROOT = Path(__file__).resolve().parents[1] / "tasks" / "_downloads"
DEFAULT_DATASETS = (
    "humanevalfix",
    "aider-polyglot",
    "bigcodebench-hard-complete",
    "LiteCoder-rl",
    "tb21-code-debug",
    "mimo-v26-code",
    "mimo-v26-terminal",
    "swe-gen-go",
    "swe-gen-rust",
    "swe-gen-cpp",
    "swe-gen-js",
    "devopsgym",
)
DEFAULT_MAX_CPUS = 4
# Docker 允许内存上限高于 VM 实际内存，只有 CPU 超过 VM 核数会被拒绝，
# 因此默认不按内存过滤（保留参数供特殊场景使用）。
DEFAULT_MAX_MEMORY_MB = 65536
# 0 表示不按声明的构建超时过滤（预构建镜像不构建，声明值也不代表实际耗时）。
DEFAULT_MAX_BUILD_SEC = 0
DEFAULT_SCAN_LIMIT = 40
DEFAULT_IMAGE_TIMEOUT = 15
IMAGE_WORKERS = 8


def load_task(path: Path) -> dict | None:
    """读取单个任务的 task.toml；解析失败视为不可用。"""
    try:
        return tomllib.loads(path.read_text(encoding="utf-8"))
    except (OSError, tomllib.TOMLDecodeError):
        return None


def image_architectures(image: str, timeout: int) -> set[str] | None:
    """查询镜像清单中的架构集合；超时或失败返回 None。"""
    try:
        result = subprocess.run(
            ["docker", "manifest", "inspect", image],
            capture_output=True,
            text=True,
            timeout=timeout,
        )
    except (OSError, subprocess.TimeoutExpired):
        return None
    if result.returncode != 0:
        return None
    try:
        data = json.loads(result.stdout)
    except ValueError:
        return None
    archs = {
        entry["platform"]["architecture"]
        for entry in data.get("manifests") or []
        if (entry.get("platform") or {}).get("architecture")
    }
    # 单架构镜像没有 manifests 列表，退回顶层 architecture 字段
    if not archs and data.get("architecture"):
        archs.add(data["architecture"])
    return archs


def supports_host_arch(archs: set[str] | None, arch: str) -> bool:
    """判断镜像是否支持目标架构；查询失败时保守放行。"""
    if archs is None:
        return True
    if arch == "arm64":
        return "arm64" in archs or "aarch64" in archs
    return arch in archs


def reject_reason(
    environment: dict, agent: dict, args: argparse.Namespace
) -> str | None:
    """返回剔除原因；None 表示通过。"""
    cpus = environment.get("cpus")
    if cpus is not None and cpus > args.max_cpus:
        return f"CPU {cpus} > {args.max_cpus}"
    memory = environment.get("memory_mb")
    if memory is not None and memory > args.max_memory_mb:
        return f"内存 {memory}MB > {args.max_memory_mb}MB"
    if environment.get("gpus"):
        return "需要 GPU"
    build_timeout = environment.get("build_timeout_sec")
    if (
        not environment.get("docker_image")
        and args.max_build_sec > 0
        and build_timeout is not None
        and build_timeout > args.max_build_sec
    ):
        return f"构建超时 {build_timeout}s > {args.max_build_sec}s"
    for policy in (environment, agent):
        mode = policy.get("network_mode")
        if mode not in (None, "public"):
            return f"网络策略 {mode} 需要 egress-control"
    return None


def select_dataset(
    dataset: str,
    args: argparse.Namespace,
    executor: ThreadPoolExecutor,
) -> tuple[list[str], list[str]]:
    """筛选一个数据集，返回（可用任务，被剔除原因）。"""
    directory = TASKS_ROOT / dataset
    if not directory.is_dir():
        return [], [f"{dataset}: 目录不存在"]
    pending: list[tuple[str, str | None]] = []
    rejected: list[str] = []
    for index, task_dir in enumerate(sorted(directory.iterdir())):
        if index >= args.scan_limit:
            break
        config_path = task_dir / "task.toml"
        if not config_path.is_file():
            continue
        if args.task_contains and args.task_contains not in task_dir.name:
            continue
        config = load_task(config_path)
        if config is None:
            rejected.append(f"{dataset}/{task_dir.name}: task.toml 解析失败")
            continue
        environment = config.get("environment") or {}
        agent = config.get("agent") or {}
        reason = reject_reason(environment, agent, args)
        if reason:
            rejected.append(f"{dataset}/{task_dir.name}: {reason}")
            continue
        pending.append((f"{dataset}/{task_dir.name}", environment.get("docker_image")))

    if not args.check_images:
        return [name for name, _ in pending], rejected

    # 架构检查统一并发执行，避免每个镜像串行等待
    images = sorted({image for _, image in pending if image})
    archs = dict(
        zip(images, executor.map(lambda image: image_architectures(image, args.image_timeout), images))
    )
    selected: list[str] = []
    for name, image in pending:
        if image and not supports_host_arch(archs.get(image), args.arch):
            rejected.append(f"{name}: 镜像 {image} 不支持 {args.arch}")
            continue
        selected.append(name)
    return selected, rejected


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--datasets", nargs="*", default=list(DEFAULT_DATASETS),
        help="要扫描的数据集目录名",
    )
    parser.add_argument("--per-dataset", type=int, default=2, help="每个数据集最多保留数")
    parser.add_argument("--scan-limit", type=int, default=DEFAULT_SCAN_LIMIT,
                        help="每个数据集最多检查的任务数")
    parser.add_argument("--task-contains", default="", help="任务目录名需包含的子串")
    parser.add_argument("--max-cpus", type=int, default=DEFAULT_MAX_CPUS)
    parser.add_argument("--max-memory-mb", type=int, default=DEFAULT_MAX_MEMORY_MB)
    parser.add_argument("--max-build-sec", type=int, default=DEFAULT_MAX_BUILD_SEC)
    parser.add_argument("--image-timeout", type=int, default=DEFAULT_IMAGE_TIMEOUT)
    parser.add_argument("--arch", default="arm64", choices=("arm64", "amd64"))
    parser.add_argument("--check-images", action="store_true",
                        help="用 docker manifest inspect 校验镜像架构")
    parser.add_argument("--out", type=Path, help="结果写入文件；默认打印到标准输出")
    parser.add_argument("--explain", action="store_true", help="把剔除原因打印到标准错误")
    args = parser.parse_args()

    results: list[str] = []
    with ThreadPoolExecutor(max_workers=IMAGE_WORKERS) as executor:
        for dataset in args.datasets:
            selected, rejected = select_dataset(dataset, args, executor)
            results.extend(selected[: args.per_dataset])
            if args.explain:
                for reason in rejected:
                    print(f"[skip] {reason}", file=sys.stderr)

    text = "\n".join(results) + ("\n" if results else "")
    if args.out:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        args.out.write_text(text, encoding="utf-8")
        print(f"已写入 {args.out}（{len(results)} 个任务）", file=sys.stderr)
    else:
        sys.stdout.write(text)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())

"""Harbor 外部 Agent：通过 HTTP 驱动宿主机 Euler。"""

from __future__ import annotations

import asyncio
import os
import posixpath
import shlex
import subprocess
from pathlib import Path

import httpx
from harbor.agents.base import BaseAgent
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

from agents.euler_client import TERMINAL_EVENTS, EulerClient

DEFAULT_BASE_URL = "http://127.0.0.1:8080"
DEFAULT_WORKSPACE = "/tmp/euler-harbor/workspace"
BASELINE_DIR = "/opt/baseline"
APPROVAL_REASON = "euler-harbor"
MAX_RECONNECTS = 3
RECONNECT_DELAY_SECONDS = 1.0
DANGEROUS_WORKDIRS = frozenset({"/", "/opt", BASELINE_DIR})


def build_instruction(instruction: str, workspace: str, workdir: str) -> str:
    """把官方指令中的容器路径映射到宿主机工作区。"""
    prefix = (
        f"你的工作区目录是 {workspace}（原任务中的 {workdir} 对应此目录）。"
    )
    return f"{prefix}\n\n{instruction}"


def validate_workdir(workdir: str) -> str:
    """校验容器工作目录，拒绝空值、相对路径与危险根路径。"""
    if not workdir or not workdir.startswith("/"):
        raise ValueError(f"invalid workdir: {workdir!r}")
    normalized = posixpath.normpath(workdir)
    if normalized in DANGEROUS_WORKDIRS or not normalized.strip("/"):
        raise ValueError(f"refusing dangerous workdir: {normalized!r}")
    return normalized


def _reconnect_delay(attempt: int) -> float:
    """按连续失败次数指数退避，避免短时间内耗尽重连预算。"""
    return RECONNECT_DELAY_SECONDS * (2 ** (attempt - 1))


def git_baseline(workspace: str) -> None:
    """在宿主机共享工作区建立 baseline commit。"""
    commands = [
        ["git", "-C", workspace, "init", "-q"],
        ["git", "-C", workspace, "config", "user.name", "euler-harbor"],
        ["git", "-C", workspace, "config", "user.email", "euler-harbor@local"],
        ["git", "-C", workspace, "add", "-A"],
        ["git", "-C", workspace, "commit", "-q", "--allow-empty", "-m", "baseline"],
    ]
    for command in commands:
        subprocess.run(command, check=True, capture_output=True, text=True)


class EulerAgent(BaseAgent):
    """把宿主机 Euler 包装成 Harbor 外部 Agent。"""

    @staticmethod
    def name() -> str:
        return "euler"

    def __init__(self, logs_dir: Path, model_name: str | None = None, **kwargs):
        super().__init__(logs_dir=logs_dir, model_name=model_name, **kwargs)
        self.base_url = os.environ.get("EULER_BASE_URL", DEFAULT_BASE_URL)
        self.workspace = os.environ.get("EULER_HARBOR_WORKSPACE", DEFAULT_WORKSPACE)
        self.profile_id = os.environ.get("EULER_PROFILE_ID") or None
        self.model_base_url = os.environ.get("EULER_MODEL_BASE_URL") or None
        self.model = os.environ.get("EULER_MODEL") or None
        self.secret_ref = os.environ.get("EULER_MODEL_KEY_ENV", "OPENAI_API_KEY")
        self.agent_timeout = float(os.environ.get("EULER_AGENT_TIMEOUT", "900"))

    def version(self) -> str:
        return "0.1.0"

    async def setup(self, environment: BaseEnvironment) -> None:
        async with EulerClient(self.base_url) as client:
            await self._wait_health(client)

    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        workdir = await self._workdir(environment)
        await self._seed_workspace(environment, workdir)
        git_baseline(self.workspace)
        full_instruction = build_instruction(instruction, self.workspace, workdir)

        async with EulerClient(self.base_url) as client:
            profile_id = await client.ensure_profile(
                self.profile_id,
                self.model_base_url,
                self.model,
                self.secret_ref,
            )
            session_id = await client.create_session(profile_id, self.workspace)
            run_id = await client.start_run(session_id, full_instruction)
            try:
                await self._consume(client, run_id)
                snapshot = await client.snapshot(run_id)
            except TimeoutError:
                await self._cancel(client, run_id)
                raise RuntimeError("euler run timed out") from None
            except Exception:
                # 任何非正常退出都先取消 run，避免宿主机留下孤儿 run
                await self._cancel(client, run_id)
                raise
            context.metadata = {
                "run_id": run_id,
                "session_id": session_id,
                "tool_calls": len(snapshot.get("tool_calls") or []),
                "approvals": len(snapshot.get("approvals") or []),
            }
            status = (snapshot.get("run") or {}).get("status")
            if status != "succeeded":
                raise RuntimeError(f"euler run ended with status={status}")

    async def _cancel(self, client: EulerClient, run_id: str) -> None:
        """尽力取消 run；取消失败时保留原始异常。"""
        try:
            await client.cancel(run_id)
        except Exception:
            pass

    async def _wait_health(self, client: EulerClient) -> None:
        for _ in range(30):
            try:
                await client.health()
                return
            except Exception:
                await asyncio.sleep(1)
        raise RuntimeError("euler health check failed")

    async def _workdir(self, environment: BaseEnvironment) -> str:
        result = await environment.exec(command="pwd")
        if result.return_code != 0:
            raise RuntimeError(f"pwd failed: {result.stderr}")
        return (result.stdout or "").strip()

    async def _seed_workspace(
        self, environment: BaseEnvironment, workdir: str
    ) -> None:
        # 先校验再拼 shell 命令，拒不安全路径
        safe_workdir = validate_workdir(workdir)
        quoted = shlex.quote(safe_workdir)
        await environment.exec(
            command=f"rm -rf {quoted}/* {quoted}/.[!.]* 2>/dev/null || true"
        )
        result = await environment.exec(command=f"cp -a {BASELINE_DIR}/. {quoted}/")
        if result.return_code != 0:
            raise RuntimeError(f"seed workspace failed: {result.stderr}")

    async def _consume(self, client: EulerClient, run_id: str) -> None:
        last_seq = 0
        failures = 0
        async with asyncio.timeout(self.agent_timeout):
            while True:
                error: Exception | None = None
                try:
                    async for event in client.events(run_id, after=last_seq):
                        # 收到任意事件即重置连续失败计数
                        failures = 0
                        last_seq = event.seq
                        if event.type == "approval.requested":
                            approval_id = str(event.payload.get("approval_id", ""))
                            if approval_id:
                                await client.decide(
                                    approval_id, True, APPROVAL_REASON
                                )
                        if event.type in TERMINAL_EVENTS:
                            return
                except (httpx.TransportError, httpx.ReadTimeout) as exc:
                    error = exc
                failures += 1
                if failures > MAX_RECONNECTS:
                    if error is not None:
                        raise RuntimeError("euler event stream failed") from error
                    raise RuntimeError(
                        "euler event stream ended without terminal event"
                    ) from None
                await asyncio.sleep(_reconnect_delay(failures))

# Euler Harbor 接入实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Harbor 通过外部 Python Agent 驱动宿主机 Euler，在官方容器任务上完成“改代码 → 官方 verifier 判分”的闭环。

**Architecture:** Harbor 负责容器、任务生命周期、隐藏测试与 reward；Python `BaseAgent` 通过 HTTP 驱动宿主机 Euler；宿主机固定工作区 bind-mount 到任务容器的 workdir，Euler 的修改直接落在官方 verifier 可见的目录。

**Tech Stack:** Harbor 0.26.0、Python 3.14、httpx、pytest、Docker 29.6、Euler HTTP API、PostgreSQL。

**Spec:** [2026-11-19-euler-harbor-integration-design.md](file:///Users/jason/go/src/Euler/docs/superpowers/specs/2026-11-19-euler-harbor-integration-design.md)

## Global Constraints

- 已在宿主机验证：`harbor 0.26.0`、`Python 3.14.6`、`Docker 29.6.1`、`uv` 可用。
- 本地目录使用 `harbor_eval/`，禁止命名为 `harbor/`，避免遮蔽已安装的 `harbor` Python 包。
- 固定工作区：`/tmp/euler-harbor/workspace`；Harbor 并发固定为 `1`。
- `EULER_HARBOR_WORKSPACE` 必须与 job YAML 的 mount `source` 完全一致；适配器只接受 `/tmp/euler-harbor/` 下的路径。
- 只下载 2 个任务：`hello-world/hello-world` 与一个 Terminal-Bench 编码任务。
- 不修改官方 `tests/`；不提交任何真实 API Key。
- Phase 1 使用 `[verifier] environment_mode = "shared"`。
- 两个任务镜像合计不得超过 4GB；超过则只保留 hello-world。
- 本计划不改 Euler 的 Go 代码；已有 Go 测试与 lint 必须保持通过。
- Python 注释使用中文。

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `harbor_eval/agents/euler_client.py` | 纯 HTTP 客户端，不依赖 Harbor，可单测 |
| `harbor_eval/agents/euler_agent.py` | Harbor `BaseAgent` 适配器 |
| `harbor_eval/tests/test_euler_client.py` | 客户端与 SSE 解析单测 |
| `harbor_eval/tests/test_euler_agent.py` | 适配器重连、取消与路径护栏单测 |
| `harbor_eval/tests/test_helpers.py` | 指令映射等纯函数单测 |
| `harbor_eval/jobs/*.yaml` | Oracle / Euler / nop 的 job 配置 |
| `harbor_eval/tasks/hello-world/` | 适配后的冒烟任务 |
| `harbor_eval/tasks/cancel-async-tasks/` | 适配后的编码任务 |
| `harbor_eval/scripts/prepare-workspace.sh` | 重建固定工作区 |
| `harbor_eval/scripts/run-smoke.sh` | 冒烟三连跑 |
| `harbor_eval/scripts/run-coding.sh` | 编码任务跑法 |
| `.gitignore` | 忽略下载、结果与缓存 |

---

### Task 1: Harbor 基线冒烟与任务下载

**Files:**
- Create: `harbor_eval/tasks/_downloads/`
- Create: `harbor_eval/results/`
- Modify: `.gitignore`

**Interfaces:**
- Produces: `harbor_eval/tasks/_downloads/` 下的原始任务目录，供 Task 2/6 复制。

- [ ] **Step 1: 建目录并确认 Harbor 可用**

```bash
mkdir -p harbor_eval/tasks/_downloads harbor_eval/results
harbor --version
docker info >/dev/null
```

Expected: 输出 `0.26.0`，Docker 正常。

- [ ] **Step 2: 下载 hello-world 原始任务**

```bash
harbor download "hello-world/hello-world" -o harbor_eval/tasks/_downloads
```

Expected: 打印下载路径，`harbor_eval/tasks/_downloads` 下出现任务目录。

若下载失败，用 Harbor 源码中的示例任务作为回退：

```bash
git clone --depth 1 --filter=blob:none --sparse \
  https://github.com/harbor-framework/harbor /tmp/harbor-src
git -C /tmp/harbor-src sparse-checkout set examples/tasks/hello-world
cp -a /tmp/harbor-src/examples/tasks/hello-world harbor_eval/tasks/_downloads/
```

- [ ] **Step 3: 定位任务目录**

```bash
task_dir=$(dirname "$(find harbor_eval/tasks/_downloads -name task.toml | head -1)")
echo "$task_dir"
find "$task_dir" -maxdepth 3 -type f | sort
```

Expected: 看到 `task.toml`、`instruction.md`、`environment/`、`tests/`，可能还有 `solution/`。

- [ ] **Step 4: 先用 Oracle 跑原始任务**

```bash
harbor run -p "$task_dir" -a oracle -n 1 -o harbor_eval/results/hello-baseline -y
find harbor_eval/results/hello-baseline -name 'reward*' -print -exec cat {} \;
```

Expected: `reward.txt` 内容为 `1`。若报“no solution”，记录该事实，Task 2 会补 `solution/solve.sh`。

- [ ] **Step 5: 更新 .gitignore**

在根 `.gitignore` 末尾追加：

```gitignore
# Harbor 评测产物
/harbor_eval/tasks/_downloads/
/harbor_eval/results/
/harbor_eval/.pytest_cache/
/harbor_eval/**/__pycache__/
```

- [ ] **Step 6: 提交**

```bash
git add .gitignore
git commit -m "chore(harbor): 初始化 Harbor 评测目录与忽略规则"
```

---

### Task 2: hello-world 任务适配与 Job 配置

**Files:**
- Create: `harbor_eval/tasks/hello-world/`（从 Task 1 的 `$task_dir` 复制）
- Create: `harbor_eval/jobs/hello-oracle.yaml`
- Create: `harbor_eval/jobs/hello-euler.yaml`
- Create: `harbor_eval/jobs/hello-nop.yaml`
- Create: `harbor_eval/tasks/hello-world/solution/solve.sh`（仅当原任务没有 solution）
- Modify: `harbor_eval/tasks/hello-world/environment/Dockerfile`
- Modify: `harbor_eval/tasks/hello-world/task.toml`

**Interfaces:**
- Produces: `harbor_eval/tasks/hello-world`，其容器内 `/opt/baseline` 为干净工作区副本；Job 配置中的 `target` 为检测出的 workdir。

- [ ] **Step 1: 复制并检查任务**

```bash
task_dir=$(dirname "$(find harbor_eval/tasks/_downloads -name task.toml | head -1)")
rm -rf harbor_eval/tasks/hello-world
cp -a "$task_dir" harbor_eval/tasks/hello-world
sed -n '1,160p' harbor_eval/tasks/hello-world/environment/Dockerfile
sed -n '1,160p' harbor_eval/tasks/hello-world/task.toml
```

Expected: 能看到 `WORKDIR`（通常为 `/app`）与 `tests/` 目录。

- [ ] **Step 2: 检测 workdir 并写入 baseline 副本**

```bash
workdir=$(grep -E '^[[:space:]]*WORKDIR[[:space:]]+' \
  harbor_eval/tasks/hello-world/environment/Dockerfile | tail -1 | awk '{print $2}')
workdir=${workdir:-/app}
echo "$workdir"
printf '\nRUN mkdir -p /opt/baseline && cp -a %s/. /opt/baseline/\n' "$workdir" \
  >> harbor_eval/tasks/hello-world/environment/Dockerfile
tail -5 harbor_eval/tasks/hello-world/environment/Dockerfile
```

Expected: 最后一行是 `RUN mkdir -p /opt/baseline && cp -a /app/. /opt/baseline/`（或检测到的 workdir）。

若 Dockerfile 末尾是 `USER <非 root>`，在这行之前再补一行 `USER root`，否则 `/opt/baseline` 可能无权限创建。

- [ ] **Step 3: 设置 task.toml**

在 `harbor_eval/tasks/hello-world/task.toml` 中确保存在以下配置；已有同名表时只补键，不要重复建表：

```toml
[environment]
workdir = "/app"

[verifier]
environment_mode = "shared"
user = "root"

[agent]
user = "root"
```

Expected: `grep -n 'environment_mode\|workdir\|user' task.toml` 能看到上述值。

- [ ] **Step 4: 补 solution（仅当缺失）**

```bash
if [ ! -f harbor_eval/tasks/hello-world/solution/solve.sh ]; then
  mkdir -p harbor_eval/tasks/hello-world/solution
  cat > harbor_eval/tasks/hello-world/solution/solve.sh <<'EOF'
#!/bin/bash
set -euo pipefail
echo 'Hello, world!' > /app/hello.txt
EOF
  chmod +x harbor_eval/tasks/hello-world/solution/solve.sh
fi
ls -l harbor_eval/tasks/hello-world/solution
```

Expected: `solve.sh` 可执行。

- [ ] **Step 5: 写三个 Job 配置**

`harbor_eval/jobs/hello-oracle.yaml`（Oracle 不加 mount，使用镜像内原始 `/app`）：

```yaml
job_name: euler-harbor-hello-oracle
jobs_dir: harbor_eval/results
n_concurrent_trials: 1
tasks:
  - path: harbor_eval/tasks/hello-world
agents:
  - name: oracle
environment:
  type: docker
```

`harbor_eval/jobs/hello-euler.yaml`：

```yaml
job_name: euler-harbor-hello-euler
jobs_dir: harbor_eval/results
n_concurrent_trials: 1
tasks:
  - path: harbor_eval/tasks/hello-world
agents:
  - import_path: agents.euler_agent:EulerAgent
environment:
  type: docker
  mounts:
    - type: bind
      source: /tmp/euler-harbor/workspace
      target: /app
```

若 Step 2 检测到的 workdir 不是 `/app`，把 `task.toml` 的 `workdir` 与这里三个 Job 配置的 `target` 都改成该值。

`harbor_eval/jobs/hello-nop.yaml`（挂空工作区，确保 reward 为 0）：

```yaml
job_name: euler-harbor-hello-nop
jobs_dir: harbor_eval/results
n_concurrent_trials: 1
tasks:
  - path: harbor_eval/tasks/hello-world
agents:
  - name: nop
environment:
  type: docker
  mounts:
    - type: bind
      source: /tmp/euler-harbor/workspace
      target: /app
```

- [ ] **Step 6: 跑 Oracle 验证适配**

```bash
mkdir -p /tmp/euler-harbor/workspace
harbor run -c harbor_eval/jobs/hello-oracle.yaml -y
find harbor_eval/results/euler-harbor-hello-oracle -name 'reward*' -print -exec cat {} \;
```

Expected: reward 为 `1`。若为 `0`，检查 `/opt/baseline` 是否为空、`tests/test.sh` 是否依赖被 baseline 复制破坏的文件。

- [ ] **Step 7: 提交**

```bash
git add harbor_eval/tasks/hello-world harbor_eval/jobs
git commit -m "feat(harbor): 适配 hello-world 任务并加入 Oracle 配置"
```

---

### Task 3: Euler HTTP 客户端

**Files:**
- Create: `harbor_eval/agents/__init__.py`
- Create: `harbor_eval/agents/euler_client.py`
- Test: `harbor_eval/tests/test_euler_client.py`

**Interfaces:**
- Produces:
  - `Event(seq: int, type: str, payload: dict)`
  - `parse_sse_data(line: str) -> Event | None`
  - `EulerClient(base_url: str, timeout: float = 60.0, transport: httpx.AsyncBaseTransport | None = None)`
  - `EulerClient.bootstrap() -> None`
  - `EulerClient.health() -> None`
  - `EulerClient.ensure_profile(profile_id, base_url, model, secret_ref) -> str`
  - `EulerClient.create_session(profile_id: str, path: str) -> str`
  - `EulerClient.start_run(session_id: str, content: str) -> str`
  - `EulerClient.decide(approval_id: str, approved: bool, reason: str) -> None`
  - `EulerClient.snapshot(run_id: str) -> dict`
  - `EulerClient.cancel(run_id: str) -> None`
  - `EulerClient.events(run_id: str, after: int = 0) -> AsyncIterator[Event]`

- [ ] **Step 1: 写失败测试**

`harbor_eval/tests/test_euler_client.py`：

```python
import asyncio

import httpx
import pytest

from agents.euler_client import EulerClient, parse_sse_data


def run(coro):
    return asyncio.run(coro)


def test_parse_sse_data_returns_event():
    event = parse_sse_data(
        'data: {"seq":3,"type":"approval.requested","payload":{"approval_id":"a1"}}'
    )
    assert event.seq == 3
    assert event.type == "approval.requested"
    assert event.payload["approval_id"] == "a1"


def test_parse_sse_data_ignores_heartbeat_and_blank():
    assert parse_sse_data(": heartbeat") is None
    assert parse_sse_data("") is None


def test_client_bootstraps_and_persists_cookie():
    seen = {}

    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/api/v1/bootstrap":
            return httpx.Response(
                204, headers={"set-cookie": "euler_local=token; Path=/"}
            )
        seen["cookie"] = request.headers.get("cookie", "")
        return httpx.Response(201, json={"id": "session-1"})

    async def scenario():
        async with EulerClient(
            "http://euler", transport=httpx.MockTransport(handler)
        ) as client:
            return await client.create_session("profile-1", "/tmp/ws")

    assert run(scenario()) == "session-1"
    assert "euler_local=token" in seen["cookie"]


def test_start_run_reads_nested_run_id():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(202, json={"run": {"id": "run-1"}, "user_message": {}})

    async def scenario():
        async with EulerClient(
            "http://euler", transport=httpx.MockTransport(handler)
        ) as client:
            return await client.start_run("session-1", "fix it")

    assert run(scenario()) == "run-1"


def test_decide_ignores_conflict():
    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/api/v1/bootstrap":
            return httpx.Response(204)
        return httpx.Response(409, json={"error": {"code": "approval_already_decided"}})

    async def scenario():
        async with EulerClient(
            "http://euler", transport=httpx.MockTransport(handler)
        ) as client:
            await client.decide("approval-1", True, "euler-harbor")

    run(scenario())


def test_ensure_profile_requires_profile_or_model():
    def handler(request: httpx.Request) -> httpx.Response:
        return httpx.Response(204)

    async def scenario():
        async with EulerClient(
            "http://euler", transport=httpx.MockTransport(handler)
        ) as client:
            await client.ensure_profile(None, None, None, "OPENAI_API_KEY")

    with pytest.raises(RuntimeError):
        run(scenario())
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd /Users/jason/go/src/Euler
PYTHONPATH=harbor_eval uv run --with pytest --with httpx --with 'harbor==0.26.0' \
  pytest harbor_eval/tests/test_euler_client.py -q
```

Expected: `ModuleNotFoundError: No module named 'agents.euler_client'`。

- [ ] **Step 3: 实现客户端**

`harbor_eval/agents/__init__.py` 为空文件。

`harbor_eval/agents/euler_client.py`：

```python
"""Euler HTTP 客户端：只依赖 httpx，不依赖 Harbor，便于单测。"""

from __future__ import annotations

import json
import time
from dataclasses import dataclass
from typing import AsyncIterator

import httpx

TERMINAL_EVENTS = frozenset(
    {"run.completed", "run.failed", "run.cancelled", "run.interrupted"}
)

_SSE_DATA_PREFIX = "data: "


@dataclass(frozen=True)
class Event:
    """一条已解析的 run 事件。"""

    seq: int
    type: str
    payload: dict


def parse_sse_data(line: str) -> Event | None:
    """解析 SSE data 行；心跳、id 行和空行返回 None。"""
    if not line.startswith(_SSE_DATA_PREFIX):
        return None
    raw = json.loads(line[len(_SSE_DATA_PREFIX):])
    return Event(
        seq=int(raw["seq"]),
        type=str(raw["type"]),
        payload=raw.get("payload") or {},
    )


class EulerClient:
    """驱动 Euler 公开 API 的异步客户端。"""

    def __init__(
        self,
        base_url: str,
        timeout: float = 60.0,
        transport: httpx.AsyncBaseTransport | None = None,
    ) -> None:
        self._client = httpx.AsyncClient(
            base_url=base_url.rstrip("/"),
            timeout=timeout,
            follow_redirects=True,
            transport=transport,
        )

    async def __aenter__(self) -> "EulerClient":
        await self.bootstrap()
        return self

    async def __aexit__(self, *exc: object) -> None:
        await self._client.aclose()

    async def bootstrap(self) -> None:
        """获取本机 cookie。"""
        response = await self._client.get("/api/v1/bootstrap")
        response.raise_for_status()

    async def health(self) -> None:
        """探测服务与数据库健康状态。"""
        response = await self._client.get("/health")
        response.raise_for_status()

    async def ensure_profile(
        self,
        profile_id: str | None,
        base_url: str | None,
        model: str | None,
        secret_ref: str,
    ) -> str:
        """复用已有 profile，或创建一个 environment 类型的 profile。"""
        if profile_id:
            return profile_id
        if not base_url or not model:
            raise RuntimeError(
                "set EULER_PROFILE_ID or EULER_MODEL_BASE_URL/EULER_MODEL"
            )
        payload = {
            "name": f"euler-harbor-{int(time.time())}",
            "base_url": base_url,
            "model": model,
            "secret_kind": "environment",
            "secret_ref": secret_ref,
        }
        response = await self._client.post("/api/v1/model-profiles", json=payload)
        response.raise_for_status()
        return str(response.json()["id"])

    async def create_session(self, profile_id: str, path: str) -> str:
        """创建绑定到宿主机工作区的会话。"""
        response = await self._client.post(
            "/api/v1/sessions", json={"profile_id": profile_id, "path": path}
        )
        response.raise_for_status()
        return str(response.json()["id"])

    async def start_run(self, session_id: str, content: str) -> str:
        """启动一次 run 并返回 run id。"""
        response = await self._client.post(
            f"/api/v1/sessions/{session_id}/runs", json={"content": content}
        )
        response.raise_for_status()
        return str(response.json()["run"]["id"])

    async def decide(self, approval_id: str, approved: bool, reason: str) -> None:
        """提交审批决策；409 表示已被决策，直接忽略。"""
        response = await self._client.post(
            f"/api/v1/approvals/{approval_id}/decision",
            json={"approved": approved, "reason": reason},
        )
        if response.status_code == 409:
            return
        response.raise_for_status()

    async def snapshot(self, run_id: str) -> dict:
        """读取 run 快照。"""
        response = await self._client.get(f"/api/v1/runs/{run_id}")
        response.raise_for_status()
        return response.json()

    async def cancel(self, run_id: str) -> None:
        """取消 run。"""
        response = await self._client.post(f"/api/v1/runs/{run_id}/cancel")
        response.raise_for_status()

    async def events(self, run_id: str, after: int = 0) -> AsyncIterator[Event]:
        """按 seq 顺序消费 SSE 事件。"""
        params = {"after": str(after)} if after > 0 else None
        async with self._client.stream(
            "GET", f"/api/v1/runs/{run_id}/events", params=params
        ) as response:
            response.raise_for_status()
            async for line in response.aiter_lines():
                event = parse_sse_data(line)
                if event is not None:
                    yield event
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd /Users/jason/go/src/Euler
PYTHONPATH=harbor_eval uv run --with pytest --with httpx --with 'harbor==0.26.0' \
  pytest harbor_eval/tests/test_euler_client.py -q
```

Expected: 6 passed。

- [ ] **Step 5: 提交**

```bash
git add harbor_eval/agents harbor_eval/tests/test_euler_client.py
git commit -m "feat(harbor): 新增 Euler HTTP 客户端与单测"
```

---

### Task 4: Euler Harbor Agent 适配器

**Files:**
- Create: `harbor_eval/agents/euler_agent.py`
- Test: `harbor_eval/tests/test_helpers.py`

**Interfaces:**
- Consumes: Task 3 的 `EulerClient`、`TERMINAL_EVENTS`。
- Produces: `agents.euler_agent:EulerAgent`，供 Job 的 `import_path` 使用；
  `build_instruction(instruction: str, workspace: str, workdir: str) -> str`。

- [ ] **Step 1: 写失败测试**

`harbor_eval/tests/test_helpers.py`：

```python
from agents.euler_agent import build_instruction


def test_build_instruction_maps_container_path_to_workspace():
    text = build_instruction("edit /app/main.py", "/tmp/ws", "/app")
    assert "/tmp/ws" in text
    assert "原任务中的 /app" in text
    assert "edit /app/main.py" in text


def test_build_instruction_keeps_original_text_last():
    text = build_instruction("fix the bug", "/tmp/ws", "/repo")
    assert text.endswith("fix the bug")
```

- [ ] **Step 2: 运行测试确认失败**

```bash
cd /Users/jason/go/src/Euler
PYTHONPATH=harbor_eval uv run --with pytest --with httpx --with 'harbor==0.26.0' \
  pytest harbor_eval/tests/test_helpers.py -q
```

Expected: `ModuleNotFoundError: No module named 'agents.euler_agent'`。

- [ ] **Step 3: 实现适配器**

`harbor_eval/agents/euler_agent.py`：

```python
"""Harbor 外部 Agent：通过 HTTP 驱动宿主机 Euler。"""

from __future__ import annotations

import asyncio
import os
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


def build_instruction(instruction: str, workspace: str, workdir: str) -> str:
    """把官方指令中的容器路径映射到宿主机工作区。"""
    prefix = (
        f"你的工作区目录是 {workspace}（原任务中的 {workdir} 对应此目录）。"
    )
    return f"{prefix}\n\n{instruction}"


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
        self.agent_timeout = float(os.environ.get("EULER_AGENT_TIMEOUT", "840"))

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
            except TimeoutError:
                await client.cancel(run_id)
                raise RuntimeError("euler run timed out") from None
            snapshot = await client.snapshot(run_id)
            context.metadata = {
                "run_id": run_id,
                "session_id": session_id,
                "tool_calls": len(snapshot.get("tool_calls") or []),
                "approvals": len(snapshot.get("approvals") or []),
            }
            status = (snapshot.get("run") or {}).get("status")
            if status != "succeeded":
                raise RuntimeError(f"euler run ended with status={status}")

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
        await environment.exec(
            command=f"rm -rf {workdir}/* {workdir}/.[!.]* 2>/dev/null || true"
        )
        result = await environment.exec(command=f"cp -a {BASELINE_DIR}/. {workdir}/")
        if result.return_code != 0:
            raise RuntimeError(f"seed workspace failed: {result.stderr}")

    async def _consume(self, client: EulerClient, run_id: str) -> None:
        last_seq = 0
        reconnects = 0
        async with asyncio.timeout(self.agent_timeout):
            while True:
                try:
                    async for event in client.events(run_id, after=last_seq):
                        last_seq = event.seq
                        if event.type == "approval.requested":
                            approval_id = str(event.payload.get("approval_id", ""))
                            if approval_id:
                                await client.decide(
                                    approval_id, True, APPROVAL_REASON
                                )
                        if event.type in TERMINAL_EVENTS:
                            return
                    reconnects += 1
                    if reconnects > MAX_RECONNECTS:
                        raise RuntimeError(
                            "euler event stream ended without terminal event"
                        )
                    await asyncio.sleep(RECONNECT_DELAY_SECONDS)
                except (httpx.TransportError, httpx.ReadTimeout):
                    reconnects += 1
                    if reconnects > MAX_RECONNECTS:
                        raise
                    await asyncio.sleep(RECONNECT_DELAY_SECONDS)
```

- [ ] **Step 4: 运行测试确认通过**

```bash
cd /Users/jason/go/src/Euler
PYTHONPATH=harbor_eval uv run --with pytest --with httpx --with 'harbor==0.26.0' \
  pytest harbor_eval/tests -q
```

Expected: 22 passed。

- [ ] **Step 5: 提交**

```bash
git add harbor_eval/agents/euler_agent.py harbor_eval/tests/test_helpers.py
git commit -m "feat(harbor): 新增 Euler BaseAgent 适配器"
```

---

### Task 5: hello-world 三连跑（Oracle / Euler / nop）

**Files:**
- Create: `harbor_eval/scripts/prepare-workspace.sh`
- Create: `harbor_eval/scripts/run-smoke.sh`

**Interfaces:**
- Consumes: Task 2 的 Job 配置、Task 4 的 `EulerAgent`。
- Produces: `harbor_eval/results` 下的三组 reward 与日志。

- [ ] **Step 1: 写工作区脚本**

`harbor_eval/scripts/prepare-workspace.sh`：

```bash
#!/usr/bin/env bash
set -euo pipefail
workspace="${EULER_HARBOR_WORKSPACE:-/tmp/euler-harbor/workspace}"
rm -rf "$workspace"
mkdir -p "$workspace"
chmod 0777 "$workspace"
echo "workspace ready: $workspace"
```

```bash
chmod +x harbor_eval/scripts/prepare-workspace.sh
```

- [ ] **Step 2: 写冒烟脚本**

`harbor_eval/scripts/run-smoke.sh`：

```bash
#!/usr/bin/env bash
set -euo pipefail
repo_root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$repo_root"
export PYTHONPATH="$repo_root/harbor_eval${PYTHONPATH:+:$PYTHONPATH}"
export EULER_BASE_URL="${EULER_BASE_URL:-http://127.0.0.1:8080}"
export EULER_HARBOR_WORKSPACE="${EULER_HARBOR_WORKSPACE:-/tmp/euler-harbor/workspace}"

./harbor_eval/scripts/prepare-workspace.sh
harbor run -c harbor_eval/jobs/hello-oracle.yaml -y

./harbor_eval/scripts/prepare-workspace.sh
harbor run -c harbor_eval/jobs/hello-euler.yaml -y

./harbor_eval/scripts/prepare-workspace.sh
harbor run -c harbor_eval/jobs/hello-nop.yaml -y

find harbor_eval/results -name 'reward*' -print -exec cat {} \;
```

```bash
chmod +x harbor_eval/scripts/run-smoke.sh
```

- [ ] **Step 3: 检查 Euler 与 PostgreSQL**

```bash
curl -sf http://127.0.0.1:8080/health
docker compose up -d postgres
```

Expected: `{"status":"ok"}`。若 Euler 未启动：

```bash
export EULER_DATABASE_URL='postgres://euler:euler@127.0.0.1:5432/euler?sslmode=disable'
read -rsp 'OPENAI_API_KEY: ' OPENAI_API_KEY
export OPENAI_API_KEY
go run ./cmd/euler &
```

模型配置二选一：
- 已有配置：`export EULER_PROFILE_ID='<uuid>'`
- 新建配置：`export EULER_MODEL_BASE_URL='<OpenAI 兼容地址>'`、`export EULER_MODEL='<模型名>'`、`export EULER_MODEL_KEY_ENV='OPENAI_API_KEY'`

- [ ] **Step 4: 跑冒烟三连**

```bash
./harbor_eval/scripts/run-smoke.sh
```

Expected:
- Oracle job：reward `1`
- Euler job：reward `1`
- nop job：reward `0`

- [ ] **Step 5: 检查 Agent 元数据**

```bash
grep -R '"run_id"' harbor_eval/results/euler-harbor-hello-euler | head
```

Expected: 能看到 `run_id`、`tool_calls`、`approvals`。

- [ ] **Step 6: 提交**

```bash
git add harbor_eval/scripts
git commit -m "test(harbor): 增加 hello-world 冒烟脚本"
```

---

### Task 6: 编码任务下载、适配与 Oracle

**Files:**
- Create: `harbor_eval/tasks/cancel-async-tasks/`
- Create: `harbor_eval/jobs/coding-oracle.yaml`
- Create: `harbor_eval/jobs/coding-euler.yaml`

**Interfaces:**
- Produces: 一个能在 shared verifier 下产出 reward 的编码任务。

- [ ] **Step 1: 下载编码任务**

```bash
harbor download "terminal-bench/cancel-async-tasks" -o harbor_eval/tasks/_downloads
```

若失败，按顺序尝试：

```bash
harbor download "terminal-bench/break-filter-js-from-html" -o harbor_eval/tasks/_downloads
harbor download "terminal-bench/fix-code-vulnerability" -o harbor_eval/tasks/_downloads
```

- [ ] **Step 2: 复制并检查**

```bash
task_dir=$(dirname "$(find harbor_eval/tasks/_downloads -name task.toml | grep -v hello-world | head -1)")
echo "$task_dir"
rm -rf harbor_eval/tasks/cancel-async-tasks
cp -a "$task_dir" harbor_eval/tasks/cancel-async-tasks
cat harbor_eval/tasks/cancel-async-tasks/task.toml
ls harbor_eval/tasks/cancel-async-tasks/environment
ls harbor_eval/tasks/cancel-async-tasks/solution 2>/dev/null || true
```

Expected: 有 `solution/solve.sh`；有 `environment/Dockerfile` 或 `environment/docker-compose.yaml`。

- [ ] **Step 3: 检测 workdir 并写 baseline**

```bash
workdir=$(grep -E '^[[:space:]]*WORKDIR[[:space:]]+' \
  harbor_eval/tasks/cancel-async-tasks/environment/Dockerfile 2>/dev/null \
  | tail -1 | awk '{print $2}')
workdir=${workdir:-/app}
echo "$workdir"
printf '\nRUN mkdir -p /opt/baseline && cp -a %s/. /opt/baseline/\n' "$workdir" \
  >> harbor_eval/tasks/cancel-async-tasks/environment/Dockerfile
tail -5 harbor_eval/tasks/cancel-async-tasks/environment/Dockerfile
```

若任务只有 `docker-compose.yaml` 而没有 Dockerfile，先从 `task.toml` 取出镜像名并生成 Dockerfile：

```bash
image=$(grep -E '^[[:space:]]*docker_image' harbor_eval/tasks/cancel-async-tasks/task.toml \
  | sed -E 's/.*"([^"]+)".*/\1/')
cat > harbor_eval/tasks/cancel-async-tasks/environment/Dockerfile <<EOF
FROM ${image}
RUN mkdir -p /opt/baseline && cp -a /app/. /opt/baseline/
EOF
```

然后删除 `task.toml` 中的 `docker_image` 行，让 Harbor 使用新的 Dockerfile。

- [ ] **Step 4: 设置 task.toml**

确保：

```toml
[environment]
workdir = "/app"

[verifier]
environment_mode = "shared"
user = "root"

[agent]
user = "root"
```

- [ ] **Step 5: 写编码 Job 配置**

`harbor_eval/jobs/coding-oracle.yaml`（Oracle 不加 mount）：

```yaml
job_name: euler-harbor-coding-oracle
jobs_dir: harbor_eval/results
n_concurrent_trials: 1
tasks:
  - path: harbor_eval/tasks/cancel-async-tasks
agents:
  - name: oracle
environment:
  type: docker
```

`harbor_eval/jobs/coding-euler.yaml`：

```yaml
job_name: euler-harbor-coding-euler
jobs_dir: harbor_eval/results
n_concurrent_trials: 1
tasks:
  - path: harbor_eval/tasks/cancel-async-tasks
agents:
  - import_path: agents.euler_agent:EulerAgent
environment:
  type: docker
  mounts:
    - type: bind
      source: /tmp/euler-harbor/workspace
      target: /app
```

若 Step 3 检测到的 workdir 不是 `/app`，把 `task.toml` 与这里 `target` 都改成该值。

- [ ] **Step 6: 跑 Oracle**

```bash
mkdir -p /tmp/euler-harbor/workspace
harbor run -c harbor_eval/jobs/coding-oracle.yaml -y
find harbor_eval/results/euler-harbor-coding-oracle -name 'reward*' -print -exec cat {} \;
```

Expected: reward `1`。若失败，检查是否缺 `solution`、是否依赖网络或 GPU；不满足条件就换候选任务。

- [ ] **Step 7: 检查磁盘**

```bash
docker system df
docker image ls --format '{{.Repository}}:{{.Tag}} {{.Size}}' | tail -20
```

Expected: 两个任务镜像合计 ≤ 4GB。超过则删除编码任务，只保留 hello-world。

- [ ] **Step 8: 提交**

```bash
git add harbor_eval/tasks/cancel-async-tasks harbor_eval/jobs/coding-oracle.yaml \
  harbor_eval/jobs/coding-euler.yaml
git commit -m "feat(harbor): 接入 Terminal-Bench 编码任务"
```

---

### Task 7: 编码任务 Euler 跑法与最终门禁

**Files:**
- Create: `harbor_eval/scripts/run-coding.sh`

**Interfaces:**
- Consumes: Task 6 的 `coding-euler.yaml`、Task 4 的 `EulerAgent`。
- Produces: 可重复执行的编码评测入口与最终验收记录。

- [ ] **Step 1: 写编码脚本**

`harbor_eval/scripts/run-coding.sh`：

```bash
#!/usr/bin/env bash
set -euo pipefail
repo_root=$(cd "$(dirname "$0")/../.." && pwd)
cd "$repo_root"
export PYTHONPATH="$repo_root/harbor_eval${PYTHONPATH:+:$PYTHONPATH}"
export EULER_BASE_URL="${EULER_BASE_URL:-http://127.0.0.1:8080}"
export EULER_HARBOR_WORKSPACE="${EULER_HARBOR_WORKSPACE:-/tmp/euler-harbor/workspace}"

./harbor_eval/scripts/prepare-workspace.sh
harbor run -c harbor_eval/jobs/coding-euler.yaml -y

find harbor_eval/results/euler-harbor-coding-euler -name 'reward*' \
  -print -exec cat {} \;
```

```bash
chmod +x harbor_eval/scripts/run-coding.sh
```

- [ ] **Step 2: 跑 Euler 编码任务**

```bash
./harbor_eval/scripts/run-coding.sh
```

Expected: 产出 reward（`0` 或 `1`），且 Agent 不崩溃。reward 为 `0` 是有效结果，不是失败。

- [ ] **Step 3: Python 单测门禁**

```bash
cd /Users/jason/go/src/Euler
PYTHONPATH=harbor_eval uv run --with pytest --with httpx --with 'harbor==0.26.0' \
  pytest harbor_eval/tests -q
```

Expected: 22 passed。

- [ ] **Step 4: Harbor 冒烟门禁**

```bash
./harbor_eval/scripts/run-smoke.sh
```

Expected: Oracle `1`、Euler `1`、nop `0`。

- [ ] **Step 5: Euler 既有 Go 门禁**

```bash
go test ./... -count=1
golangci-lint run --timeout=5m ./...
```

Expected: 全部通过，lint `0 issues`。

- [ ] **Step 6: 清理与状态检查**

```bash
rm -rf /tmp/euler-harbor
docker system df
git status --short
```

Expected: 工作区只剩本计划的源码与文档改动；下载目录与结果目录被 `.gitignore` 忽略。

- [ ] **Step 7: 提交**

```bash
git add harbor_eval/scripts/run-coding.sh
git commit -m "test(harbor): 增加编码任务运行脚本"
```

---

## 自检

- 规格覆盖：任务来源（Task 1/6）、bind mount（Task 2/6）、外部 Agent（Task 3/4）、Oracle（Task 2/6）、负向验证（Task 5）、磁盘门禁（Task 6）、最终门禁（Task 7）均有对应任务。
- 无占位符：所有代码、命令、配置均为可直接执行内容。
- 类型一致：`EulerClient` 的方法签名在 Task 3 定义，Task 4 按同一签名调用；`build_instruction` 参数顺序一致。
- 已知风险：官方任务若依赖 `/app` 以外的状态或需要网络/GPU，Task 6 会通过 Oracle 失败暴露并触发候选任务替换。

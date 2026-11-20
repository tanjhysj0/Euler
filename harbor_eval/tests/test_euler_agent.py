import asyncio
import json
from types import SimpleNamespace

import pytest

from agents.euler_agent import (
    APPROVAL_REASON,
    DEFAULT_WORKSPACES_ROOT,
    EulerAgent,
    validate_host_workspace,
    validate_workdir,
    workspace_changed,
)
from agents.euler_client import Event


class FakeClient:
    """按顺序回放事件流的假客户端，不接触真实网络。"""

    def __init__(self, streams):
        self._streams = [list(stream) for stream in streams]
        self.decisions = []
        self.calls = 0

    async def events(self, run_id, after=0):
        self.calls += 1
        stream = self._streams.pop(0) if self._streams else []
        for event in stream:
            yield event

    async def decide(self, approval_id, approved, reason):
        self.decisions.append((approval_id, approved, reason))


class FakeRunClient(FakeClient):
    """带完整 run 生命周期的假客户端，用于验证异常路径取消。"""

    def __init__(self, streams):
        super().__init__(streams)
        self.cancelled = []

    async def ensure_profile(self, profile_id, base_url, model, secret_ref):
        return "profile-1"

    async def create_session(self, profile_id, path):
        return "session-1"

    async def start_run(self, session_id, content):
        return "run-1"

    async def snapshot(self, run_id):
        return {"run": {"status": "succeeded"}}

    async def messages(self, session_id):
        return []

    async def cancel(self, run_id):
        self.cancelled.append(run_id)

    async def __aenter__(self):
        return self

    async def __aexit__(self, *exc):
        return None


class FakeEnvironment:
    """只返回固定 pwd 结果的假环境。"""

    def __init__(self):
        self.commands = []

    async def exec(self, command):
        self.commands.append(command)
        stdout = "Python 3.13.5\n" if command == "python3 --version" else "/app\n"
        return SimpleNamespace(stdout=stdout, stderr="", return_code=0)


class FakeHealthClient:
    """前 N 次健康检查抛错的假客户端。"""

    def __init__(self, failures: int):
        self.remaining = failures
        self.calls = 0

    async def health(self):
        self.calls += 1
        if self.remaining > 0:
            self.remaining -= 1
            raise RuntimeError("euler not ready")


class CancelledStreamClient(FakeRunClient):
    """事件流直接抛 CancelledError，模拟 Harbor 终止 trial。"""

    async def events(self, run_id, after=0):
        raise asyncio.CancelledError()
        yield  # pragma: no cover


def make_agent(tmp_path) -> EulerAgent:
    agent = EulerAgent(logs_dir=tmp_path)
    # 每个 trial 独占测试临时目录下的工作区子目录
    agent.workspaces_root = str(tmp_path / "workspaces")
    return agent


def test_consume_returns_after_terminal_event(tmp_path, monkeypatch):
    monkeypatch.setattr("agents.euler_agent.RECONNECT_DELAY_SECONDS", 0.0)
    client = FakeClient(
        [[Event(1, "message.delta", {}), Event(2, "run.completed", {})]]
    )

    asyncio.run(make_agent(tmp_path)._consume(client, "run-1"))

    assert client.calls == 1


def test_consume_approves_requested_approval(tmp_path, monkeypatch):
    monkeypatch.setattr("agents.euler_agent.RECONNECT_DELAY_SECONDS", 0.0)
    client = FakeClient(
        [
            [
                Event(1, "approval.requested", {"approval_id": "a1"}),
                Event(2, "run.completed", {}),
            ]
        ]
    )

    asyncio.run(make_agent(tmp_path)._consume(client, "run-1"))

    assert client.decisions == [("a1", True, APPROVAL_REASON)]


def test_consume_raises_after_reconnect_budget_exhausted(tmp_path, monkeypatch):
    monkeypatch.setattr("agents.euler_agent.RECONNECT_DELAY_SECONDS", 0.0)
    client = FakeClient([])

    with pytest.raises(RuntimeError):
        asyncio.run(make_agent(tmp_path)._consume(client, "run-1"))

    # 首次连接 + 3 次重连后仍无终态事件
    assert client.calls == 4


def test_consume_resets_budget_after_any_event(tmp_path, monkeypatch):
    monkeypatch.setattr("agents.euler_agent.RECONNECT_DELAY_SECONDS", 0.0)
    monkeypatch.setattr("agents.euler_agent.MAX_RECONNECTS", 1)
    client = FakeClient(
        [
            [Event(1, "message.delta", {})],
            [Event(2, "message.delta", {})],
            [Event(3, "run.completed", {})],
        ]
    )

    asyncio.run(make_agent(tmp_path)._consume(client, "run-1"))

    # 每个事件都重置预算，因此两次空流结束不会耗尽重连次数
    assert client.calls == 3


def test_run_cancels_run_when_consume_fails(tmp_path, monkeypatch):
    client = FakeRunClient([])
    monkeypatch.setattr("agents.euler_agent.EulerClient", lambda base_url: client)
    monkeypatch.setattr("agents.euler_agent.git_baseline", lambda workspace: None)
    monkeypatch.setattr("agents.euler_agent.MAX_RECONNECTS", 0)

    context = SimpleNamespace(metadata=None)
    with pytest.raises(RuntimeError):
        asyncio.run(make_agent(tmp_path).run("fix", FakeEnvironment(), context))

    assert client.cancelled == ["run-1"]


def test_validate_workdir_rejects_dangerous_paths():
    for path in (
        "",
        "/",
        "//",
        "/opt",
        "relative/path",
    ):
        with pytest.raises(ValueError):
            validate_workdir(path)


def test_validate_workdir_normalizes_container_path():
    assert validate_workdir("/app/") == "/app"


def test_seed_workspace_clears_copies_and_chmods_quoted_workdir(tmp_path):
    environment = FakeEnvironment()
    target = "/mnt/euler-workspaces/trial-1"

    asyncio.run(
        make_agent(tmp_path)._seed_workspace(environment, "/app dir", target)
    )

    assert environment.commands == [
        f"mkdir -p {target}",
        f"rm -rf {target}/* {target}/.[!.]* 2>/dev/null || true",
        f"cp -a '/app dir'/. {target}/",
        f"chmod -R a+rwX {target}",
    ]


def test_seed_workspace_propagates_chmod_failure(tmp_path, monkeypatch):
    environment = FakeEnvironment()
    original_exec = environment.exec

    async def fail_chmod(command):
        if command.startswith("chmod "):
            environment.commands.append(command)
            return SimpleNamespace(
                stdout="", stderr="chmod: permission denied", return_code=1
            )
        return await original_exec(command)

    monkeypatch.setattr(environment, "exec", fail_chmod)

    with pytest.raises(RuntimeError, match="permission denied"):
        asyncio.run(
            make_agent(tmp_path)._seed_workspace(
                environment, "/app", "/mnt/euler-workspaces/trial-1"
            )
        )


def test_seed_workspace_rejects_dangerous_workdir(tmp_path):
    environment = FakeEnvironment()

    with pytest.raises(ValueError):
        asyncio.run(
            make_agent(tmp_path)._seed_workspace(
                environment, "/opt", "/mnt/euler-workspaces/trial-1"
            )
        )

    assert environment.commands == []


def test_wait_health_retries_until_success(tmp_path, monkeypatch):
    monkeypatch.setattr("agents.euler_agent.HEALTH_RETRY_DELAY_SECONDS", 0.0)
    client = FakeHealthClient(failures=2)

    asyncio.run(make_agent(tmp_path)._wait_health(client))

    assert client.calls == 3


def test_run_cancels_run_and_reraises_on_cancel(tmp_path, monkeypatch):
    client = CancelledStreamClient([])
    monkeypatch.setattr("agents.euler_agent.EulerClient", lambda base_url: client)
    monkeypatch.setattr("agents.euler_agent.git_baseline", lambda workspace: None)

    context = SimpleNamespace(metadata=None)
    with pytest.raises(asyncio.CancelledError):
        asyncio.run(make_agent(tmp_path).run("fix", FakeEnvironment(), context))

    assert client.cancelled == ["run-1"]


def test_workspace_changed_assumes_change_when_git_fails(tmp_path):
    # 非 git 目录无法判定，按“有改动”处理，避免误报 no_action
    assert workspace_changed(str(tmp_path)) is True


def test_run_marks_no_action_when_run_does_nothing(tmp_path, monkeypatch):
    client = FakeRunClient([[Event(1, "run.completed", {})]])
    monkeypatch.setattr("agents.euler_agent.EulerClient", lambda base_url: client)
    monkeypatch.setattr("agents.euler_agent.git_baseline", lambda workspace: None)
    monkeypatch.setattr(
        "agents.euler_agent.workspace_changed", lambda workspace: False
    )
    agent = make_agent(tmp_path)
    context = SimpleNamespace(metadata=None)

    asyncio.run(agent.run("fix", FakeEnvironment(), context))

    assert context.metadata["outcome"] == "no_action"
    assert context.metadata["tool_calls"] == 0


def test_run_marks_completed_when_workspace_changed_without_tools(
    tmp_path, monkeypatch
):
    client = FakeRunClient([[Event(1, "run.completed", {})]])
    monkeypatch.setattr("agents.euler_agent.EulerClient", lambda base_url: client)
    monkeypatch.setattr("agents.euler_agent.git_baseline", lambda workspace: None)
    monkeypatch.setattr(
        "agents.euler_agent.workspace_changed", lambda workspace: True
    )
    agent = make_agent(tmp_path)
    context = SimpleNamespace(metadata=None)

    asyncio.run(agent.run("fix", FakeEnvironment(), context))

    assert context.metadata["outcome"] == "completed"


def test_run_marks_completed_when_tools_were_used(tmp_path, monkeypatch):
    client = FakeRunClient([[Event(1, "run.completed", {})]])

    async def snapshot(_run_id):
        return {"run": {"status": "succeeded"}, "tool_calls": [{}]}

    client.snapshot = snapshot
    monkeypatch.setattr("agents.euler_agent.EulerClient", lambda base_url: client)
    monkeypatch.setattr("agents.euler_agent.git_baseline", lambda workspace: None)
    monkeypatch.setattr(
        "agents.euler_agent.workspace_changed", lambda workspace: False
    )
    agent = make_agent(tmp_path)
    context = SimpleNamespace(metadata=None)

    asyncio.run(agent.run("fix", FakeEnvironment(), context))

    assert context.metadata["outcome"] == "completed"
    assert context.metadata["tool_calls"] == 1


def test_run_records_failed_outcome_before_raising(tmp_path, monkeypatch):
    client = FakeRunClient([[Event(1, "run.failed", {})]])

    async def snapshot(_run_id):
        return {
            "run": {
                "status": "failed",
                "error_code": "model_round_limit",
                "error_message": "model round limit exceeded",
            }
        }

    client.snapshot = snapshot
    monkeypatch.setattr("agents.euler_agent.EulerClient", lambda base_url: client)
    monkeypatch.setattr("agents.euler_agent.git_baseline", lambda workspace: None)
    monkeypatch.setattr(
        "agents.euler_agent.workspace_changed", lambda workspace: False
    )
    agent = make_agent(tmp_path)
    context = SimpleNamespace(metadata=None)

    with pytest.raises(RuntimeError):
        asyncio.run(agent.run("fix", FakeEnvironment(), context))

    assert context.metadata["outcome"] == "failed"
    assert context.metadata["error_code"] == "model_round_limit"
    assert context.metadata["error_message"] == "model round limit exceeded"


def test_run_treats_round_limit_with_output_as_delivered(tmp_path, monkeypatch):
    # 轮次耗尽但工作区已有改动时按完成收尾，不再抛出异常
    client = FakeRunClient([[Event(1, "run.failed", {})]])

    async def snapshot(_run_id):
        return {
            "run": {"status": "failed", "error_code": "model_round_limit"},
            "tool_calls": [{}],
        }

    client.snapshot = snapshot
    monkeypatch.setattr("agents.euler_agent.EulerClient", lambda base_url: client)
    monkeypatch.setattr("agents.euler_agent.git_baseline", lambda workspace: None)
    monkeypatch.setattr(
        "agents.euler_agent.workspace_changed", lambda workspace: True
    )
    agent = make_agent(tmp_path)
    context = SimpleNamespace(metadata=None)

    asyncio.run(agent.run("fix", FakeEnvironment(), context))

    assert context.metadata["outcome"] == "budget_exhausted"
    assert context.metadata["error_code"] == "model_round_limit"


def test_run_writes_trace_artifact(tmp_path, monkeypatch):
    client = FakeRunClient([[Event(1, "run.completed", {})]])

    async def messages(_session_id):
        return [{"role": "assistant", "content": "done"}]

    client.messages = messages
    monkeypatch.setattr("agents.euler_agent.EulerClient", lambda base_url: client)
    monkeypatch.setattr("agents.euler_agent.git_baseline", lambda workspace: None)
    monkeypatch.setattr(
        "agents.euler_agent.workspace_changed", lambda workspace: True
    )
    agent = make_agent(tmp_path)
    context = SimpleNamespace(metadata=None)

    asyncio.run(agent.run("fix", FakeEnvironment(), context))

    trace = json.loads((tmp_path / "euler-trace.json").read_text(encoding="utf-8"))
    assert trace["run_id"] == "run-1"
    assert trace["session_id"] == "session-1"
    assert trace["run"]["status"] == "succeeded"
    assert trace["messages"][0]["content"] == "done"
    assert trace["container_python"] == "Python 3.13.5"


def test_run_records_model_and_declared_budget(tmp_path, monkeypatch):
    monkeypatch.setenv("EULER_MODEL", "deepseek-v4-pro")
    monkeypatch.setenv("EULER_MAX_MODEL_ROUNDS", "24")
    monkeypatch.setenv("EULER_REASONING_EFFORT", "max")
    client = FakeRunClient([[Event(1, "run.completed", {})]])
    monkeypatch.setattr("agents.euler_agent.EulerClient", lambda base_url: client)
    monkeypatch.setattr("agents.euler_agent.git_baseline", lambda workspace: None)
    monkeypatch.setattr(
        "agents.euler_agent.workspace_changed", lambda workspace: True
    )
    agent = make_agent(tmp_path)
    context = SimpleNamespace(metadata=None)

    asyncio.run(agent.run("fix", FakeEnvironment(), context))

    assert context.metadata["model"] == "deepseek-v4-pro"
    assert context.metadata["max_model_rounds"] == 24
    assert context.metadata["reasoning_effort"] == "max"
    trace = json.loads((tmp_path / "euler-trace.json").read_text(encoding="utf-8"))
    assert trace["model"] == "deepseek-v4-pro"
    assert trace["max_model_rounds"] == 24
    assert trace["reasoning_effort"] == "max"


def test_validate_host_workspace_rejects_unsafe_paths():
    for path in (
        "",
        "/",
        "relative/path",
        "/Users/jason/go/src/Euler",
        "/tmp//",
        "/tmp/euler-harbor",
        "/tmp/euler-harbor/workspaces",
        "/tmp/euler-harbor/workspace",
        "/tmp/euler-harbor/other",
        "/tmp/euler-harbor/../etc",
    ):
        with pytest.raises(ValueError):
            validate_host_workspace(path)


def test_validate_host_workspace_accepts_trial_subdir_with_trailing_slash():
    assert (
        validate_host_workspace("/tmp/euler-harbor/workspaces/trial-1/")
        == "/tmp/euler-harbor/workspaces/trial-1"
    )
    assert (
        DEFAULT_WORKSPACES_ROOT == "/tmp/euler-harbor/workspaces"
    )


def test_run_rejects_host_workspace_before_session(tmp_path, monkeypatch):
    agent = make_agent(tmp_path)
    agent.workspaces_root = ""

    def fail(base_url):
        raise AssertionError("非法工作区不应创建 client")

    monkeypatch.setattr("agents.euler_agent.EulerClient", fail)

    with pytest.raises(ValueError):
        asyncio.run(
            agent.run("fix", FakeEnvironment(), SimpleNamespace(metadata=None))
        )

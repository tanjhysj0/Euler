import asyncio
from types import SimpleNamespace

import pytest

from agents.euler_agent import (
    APPROVAL_REASON,
    EulerAgent,
    validate_host_workspace,
    validate_workdir,
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
        return SimpleNamespace(stdout="/app\n", stderr="", return_code=0)


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
    return EulerAgent(logs_dir=tmp_path)


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
        "/opt/baseline",
        "relative/path",
        "/opt/../opt/baseline",
    ):
        with pytest.raises(ValueError):
            validate_workdir(path)


def test_validate_workdir_normalizes_container_path():
    assert validate_workdir("/app/") == "/app"


def test_seed_workspace_quotes_workdir(tmp_path):
    environment = FakeEnvironment()

    asyncio.run(make_agent(tmp_path)._seed_workspace(environment, "/app dir"))

    assert all("'/app dir'" in command for command in environment.commands)


def test_seed_workspace_rejects_dangerous_workdir(tmp_path):
    environment = FakeEnvironment()

    with pytest.raises(ValueError):
        asyncio.run(make_agent(tmp_path)._seed_workspace(environment, "/opt"))

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


def test_validate_host_workspace_rejects_unsafe_paths():
    for path in (
        "",
        "/",
        "relative/path",
        "/Users/jason/go/src/Euler",
        "/tmp/euler-harbor",
        "/tmp/euler-harbor/../etc",
    ):
        with pytest.raises(ValueError):
            validate_host_workspace(path)


def test_validate_host_workspace_accepts_tmp_subdir():
    assert (
        validate_host_workspace("/tmp/euler-harbor/workspace/")
        == "/tmp/euler-harbor/workspace"
    )


def test_run_rejects_host_workspace_before_session(tmp_path, monkeypatch):
    agent = make_agent(tmp_path)
    agent.workspace = "/Users/jason/go/src/Euler"

    def fail(base_url):
        raise AssertionError("非法工作区不应创建 client")

    monkeypatch.setattr("agents.euler_agent.EulerClient", fail)

    with pytest.raises(ValueError):
        asyncio.run(
            agent.run("fix", FakeEnvironment(), SimpleNamespace(metadata=None))
        )

import asyncio
import json
from types import SimpleNamespace

import pytest

from agents.euler_agent import (
    REMOTE_BINARY,
    REMOTE_CA_BUNDLE,
    REMOTE_INSTRUCTION,
    REMOTE_RESULT,
    EulerAgent,
    build_instruction,
    validate_workdir,
)


class FakeEnvironment:
    """只模拟容器内交互的假环境，不接触 Docker。"""

    def __init__(self, workdir="/app", arch="x86_64"):
        self.workdir = workdir
        self.arch = arch
        self.commands = []
        self.uploads = []
        self.exec_env = None
        self.report = None
        self.run_return_code = 0
        self.run_stderr = ""
        self.run_stdout = ""

    async def exec(self, command, cwd=None, env=None, timeout_sec=None, user=None):
        self.commands.append(command)
        if env is not None:
            self.exec_env = env
        if command == "uname -m":
            return SimpleNamespace(stdout=f"{self.arch}\n", stderr="", return_code=0)
        if command == "pwd":
            return SimpleNamespace(
                stdout=f"{self.workdir}\n", stderr="", return_code=0
            )
        if command.endswith("-version"):
            return SimpleNamespace(
                stdout="euler-agent 0.1.0\n", stderr="", return_code=0
            )
        if command.startswith(REMOTE_BINARY):
            return SimpleNamespace(
                stdout=self.run_stdout,
                stderr=self.run_stderr,
                return_code=self.run_return_code,
            )
        return SimpleNamespace(stdout="", stderr="", return_code=0)

    async def upload_file(self, source_path, target_path):
        self.uploads.append((str(source_path), target_path))

    async def download_file(self, source_path, target_path):
        assert source_path == REMOTE_RESULT
        if self.report is None:
            raise FileNotFoundError(source_path)
        target_path.parent.mkdir(parents=True, exist_ok=True)
        target_path.write_text(
            json.dumps(self.report, ensure_ascii=False), encoding="utf-8"
        )


def make_agent(tmp_path) -> EulerAgent:
    agent = EulerAgent(logs_dir=tmp_path / "logs")
    agent.dist_dir = tmp_path / "dist"
    agent.dist_dir.mkdir()
    for arch in ("amd64", "arm64"):
        (agent.dist_dir / f"euler-agent-linux-{arch}").write_text("bin")
    agent.model = "deepseek-v4-flash"
    agent.api_key = "sk-test"
    agent.max_duration_sec = 60
    return agent


def make_context():
    return SimpleNamespace(metadata=None)


def test_setup_uploads_binary_for_container_arch(tmp_path, monkeypatch):
    ca_bundle = tmp_path / "ca.pem"
    ca_bundle.write_text("ca")
    monkeypatch.setenv("EULER_TLS_CA_FILE", str(ca_bundle))
    agent = make_agent(tmp_path)
    environment = FakeEnvironment(arch="aarch64")

    asyncio.run(agent.setup(environment))

    assert environment.uploads == [
        (str(agent.dist_dir / "euler-agent-linux-arm64"), REMOTE_BINARY),
        (str(ca_bundle), REMOTE_CA_BUNDLE),
    ]
    assert f"chmod +x {REMOTE_BINARY}" in environment.commands
    assert any(command.endswith("-version") for command in environment.commands)


def test_setup_rejects_unsupported_arch(tmp_path):
    environment = FakeEnvironment(arch="riscv64")

    with pytest.raises(RuntimeError, match="unsupported container arch"):
        asyncio.run(make_agent(tmp_path).setup(environment))


def test_setup_fails_when_binary_missing(tmp_path):
    agent = make_agent(tmp_path)
    (agent.dist_dir / "euler-agent-linux-amd64").unlink()

    with pytest.raises(RuntimeError, match="make agent-dist"):
        asyncio.run(agent.setup(FakeEnvironment()))


def test_run_executes_agent_in_container_workspace(tmp_path):
    agent = make_agent(tmp_path)
    environment = FakeEnvironment(workdir="/app")
    environment.report = {
        "status": "succeeded",
        "tool_calls": [{"name": "file_write", "status": "succeeded"}],
        "output": "done",
        "error_code": "",
    }
    context = make_context()

    asyncio.run(agent.run("fix the bug", environment, context))

    run_command = next(
        command for command in environment.commands
        if command.startswith(REMOTE_BINARY)
    )
    assert "-dir /app" in run_command
    assert f"-instruction-file {REMOTE_INSTRUCTION}" in run_command
    assert context.metadata["outcome"] == "completed"
    assert context.metadata["tool_calls"] == 1
    assert context.metadata["exit_code"] == 0
    uploaded_instruction = environment.uploads[-1]
    assert uploaded_instruction[1] == REMOTE_INSTRUCTION
    text = (tmp_path / "logs" / "instruction.txt").read_text(encoding="utf-8")
    assert "/app" in text
    assert text.endswith("fix the bug")


def test_run_passes_model_timeout_and_ca_bundle_env(tmp_path, monkeypatch):
    ca_bundle = tmp_path / "ca.pem"
    ca_bundle.write_text("ca")
    monkeypatch.setenv("EULER_TLS_CA_FILE", str(ca_bundle))
    monkeypatch.setenv("EULER_MODEL_TIMEOUT", "180s")
    monkeypatch.setenv("EULER_MODEL_REQUEST_TIMEOUT", "90s")
    agent = make_agent(tmp_path)
    environment = FakeEnvironment()
    environment.report = {"status": "succeeded", "tool_calls": [{}]}

    asyncio.run(agent.setup(environment))
    asyncio.run(agent.run("fix", environment, make_context()))

    run_command = next(
        command for command in environment.commands
        if command.startswith(REMOTE_BINARY) and "-dir" in command
    )
    assert "-model-timeout 180s" in run_command
    assert "-model-request-timeout 90s" in run_command
    assert environment.exec_env["SSL_CERT_FILE"] == REMOTE_CA_BUNDLE


def test_run_uses_default_model_timeout(tmp_path, monkeypatch):
    monkeypatch.delenv("EULER_MODEL_TIMEOUT", raising=False)
    monkeypatch.delenv("EULER_MODEL_REQUEST_TIMEOUT", raising=False)
    agent = make_agent(tmp_path)
    environment = FakeEnvironment()
    environment.report = {"status": "succeeded", "tool_calls": [{}]}

    asyncio.run(agent.run("fix", environment, make_context()))

    run_command = next(
        command for command in environment.commands
        if command.startswith(REMOTE_BINARY) and "-dir" in command
    )
    assert "-model-timeout 10s" in run_command
    assert "-model-request-timeout 5m" in run_command
    assert "SSL_CERT_FILE" not in environment.exec_env


def test_run_passes_model_key_and_langfuse_env(tmp_path, monkeypatch):
    monkeypatch.setenv("LANGFUSE_HOST", "https://cloud.langfuse.com")
    monkeypatch.setenv("LANGFUSE_BASE_URL", "https://cloud.langfuse.com")
    monkeypatch.setenv("LANGFUSE_PUBLIC_KEY", "pk")
    monkeypatch.setenv("LANGFUSE_SECRET_KEY", "sk")
    agent = make_agent(tmp_path)
    environment = FakeEnvironment()
    environment.report = {"status": "succeeded", "tool_calls": [{}]}

    asyncio.run(agent.run("fix", environment, make_context()))

    assert environment.exec_env["EULER_MODEL_API_KEY"] == "sk-test"
    assert environment.exec_env["EULER_MODEL"] == "deepseek-v4-flash"
    assert environment.exec_env["LANGFUSE_HOST"] == "https://cloud.langfuse.com"
    # 也支持只配置 LANGFUSE_BASE_URL 的旧写法
    assert environment.exec_env["LANGFUSE_BASE_URL"] == "https://cloud.langfuse.com"


def test_run_omits_empty_env_values(tmp_path, monkeypatch):
    monkeypatch.delenv("LANGFUSE_HOST", raising=False)
    monkeypatch.delenv("LANGFUSE_BASE_URL", raising=False)
    monkeypatch.delenv("LANGFUSE_PUBLIC_KEY", raising=False)
    monkeypatch.delenv("LANGFUSE_SECRET_KEY", raising=False)
    agent = make_agent(tmp_path)
    agent.reasoning_effort = ""
    environment = FakeEnvironment()
    environment.report = {"status": "succeeded", "tool_calls": [{}]}

    asyncio.run(agent.run("fix", environment, make_context()))

    assert "LANGFUSE_HOST" not in environment.exec_env
    assert "EULER_REASONING_EFFORT" not in environment.exec_env


def test_run_marks_no_action_when_no_tool_calls(tmp_path):
    agent = make_agent(tmp_path)
    environment = FakeEnvironment()
    environment.report = {"status": "succeeded", "tool_calls": []}
    context = make_context()

    asyncio.run(agent.run("fix", environment, context))

    assert context.metadata["outcome"] == "no_action"


def test_run_marks_budget_exhausted_on_round_limit(tmp_path):
    agent = make_agent(tmp_path)
    environment = FakeEnvironment()
    environment.report = {
        "status": "failed",
        "tool_calls": [{}],
        "error_code": "model_round_limit",
    }
    environment.run_return_code = 1
    context = make_context()

    asyncio.run(agent.run("fix", environment, context))

    assert context.metadata["outcome"] == "budget_exhausted"
    assert context.metadata["error_code"] == "model_round_limit"


def test_run_does_not_raise_when_agent_reports_failure(tmp_path):
    agent = make_agent(tmp_path)
    environment = FakeEnvironment()
    environment.report = {
        "status": "failed",
        "tool_calls": [],
        "error_code": "model_auth",
        "error": "unauthorized",
    }
    environment.run_return_code = 1
    context = make_context()

    asyncio.run(agent.run("fix", environment, context))

    assert context.metadata["outcome"] == "failed"
    assert context.metadata["error_message"] == "unauthorized"


def test_run_raises_when_result_missing(tmp_path):
    agent = make_agent(tmp_path)
    environment = FakeEnvironment()
    environment.run_return_code = 1
    environment.run_stderr = "boom"

    with pytest.raises(RuntimeError, match="produced no result"):
        asyncio.run(agent.run("fix", environment, make_context()))


def test_run_requires_api_key(tmp_path):
    agent = make_agent(tmp_path)
    agent.api_key = ""

    with pytest.raises(RuntimeError, match="missing model api key"):
        asyncio.run(agent.run("fix", FakeEnvironment(), make_context()))


def test_run_requires_model(tmp_path):
    agent = make_agent(tmp_path)
    agent.model = ""

    with pytest.raises(RuntimeError, match="missing model"):
        asyncio.run(agent.run("fix", FakeEnvironment(), make_context()))


def test_validate_workdir_rejects_dangerous_paths():
    for path in ("", "/", "//", "/opt", "/tmp", "relative/path"):
        with pytest.raises(ValueError):
            validate_workdir(path)


def test_validate_workdir_normalizes_container_path():
    assert validate_workdir("/app/") == "/app"


def test_build_instruction_keeps_original_text_last():
    text = build_instruction("fix the bug", "/app")
    assert "/app" in text
    assert text.endswith("fix the bug")

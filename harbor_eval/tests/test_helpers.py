import os
from pathlib import Path
import subprocess

import pytest

from agents.euler_agent import build_instruction


def test_build_instruction_maps_container_path_to_workspace():
    text = build_instruction("edit /app/main.py", "/tmp/ws", "/app")
    assert "/tmp/ws" in text
    assert "原任务中的 /app" in text
    assert "edit /app/main.py" in text


def test_build_instruction_keeps_original_text_last():
    text = build_instruction("fix the bug", "/tmp/ws", "/repo")
    assert text.endswith("fix the bug")


@pytest.mark.parametrize("workspace", ["/tmp//", "/tmp/euler-harbor/other"])
def test_prepare_workspace_rejects_non_fixed_path_before_rm(tmp_path, workspace):
    script = Path(__file__).parents[1] / "scripts" / "prepare-workspace.sh"
    fake_bin = tmp_path / "bin"
    fake_bin.mkdir()
    call_log = tmp_path / "commands.log"
    for command in ("rm", "mkdir", "chmod"):
        executable = fake_bin / command
        executable.write_text(
            "#!/bin/sh\n"
            f"printf '%s %s\\n' '{command}' \"$*\" >> \"$WORKSPACE_COMMAND_LOG\"\n",
            encoding="utf-8",
        )
        executable.chmod(0o755)

    result = subprocess.run(
        ["/bin/bash", str(script)],
        capture_output=True,
        check=False,
        env={
            **os.environ,
            "EULER_HARBOR_WORKSPACE": workspace,
            "PATH": f"{fake_bin}:{os.environ['PATH']}",
            "WORKSPACE_COMMAND_LOG": str(call_log),
        },
    )

    calls = call_log.read_text(encoding="utf-8") if call_log.exists() else ""
    assert result.returncode != 0
    assert not any(line.startswith("rm ") for line in calls.splitlines())


def test_run_smoke_reward_failure_message_supports_bash_32_zh_locale():
    script = Path(__file__).parents[1] / "scripts" / "run-smoke.sh"
    failure_line = next(
        line.strip()
        for line in script.read_text(encoding="utf-8").splitlines()
        if "reward 断言失败" in line
    )
    result = subprocess.run(
        [
            "/bin/bash",
            "-u",
            "-c",
            f"job=test; expected=1; reward=0; {failure_line}",
        ],
        capture_output=True,
        check=False,
        env={**os.environ, "LC_ALL": "zh_CN.UTF-8"},
    )
    stderr = result.stderr.decode("utf-8", errors="replace")
    assert result.returncode == 0, stderr
    assert stderr == "[test] reward 断言失败：期望 1，实际 0\n"

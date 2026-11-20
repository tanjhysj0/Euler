import importlib.util
import json
from pathlib import Path

import pytest

SCRIPT = Path(__file__).parents[1] / "scripts" / "replay_run.py"


def load_replay():
    spec = importlib.util.spec_from_file_location("replay_run", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    spec.loader.exec_module(module)
    return module


def test_reconstruct_applies_write_then_patch(tmp_path):
    replay = load_replay()
    calls = [
        {"name": "file_write", "arguments": {"path": "run.py", "content": "a\nb\n"}},
        {
            "name": "file_patch",
            "arguments": {
                "path": "run.py",
                "old_string": "b",
                "new_string": "c",
            },
        },
    ]

    warnings = replay.reconstruct(calls, tmp_path)

    assert (tmp_path / "run.py").read_text() == "a\nc\n"
    assert warnings == []


def test_reconstruct_rejects_patch_on_missing_file(tmp_path):
    replay = load_replay()
    calls = [
        {
            "name": "file_patch",
            "arguments": {
                "path": "run.py",
                "old_string": "missing",
                "new_string": "x",
            },
        },
    ]

    with pytest.raises(replay.ReplayError, match="不存在"):
        replay.reconstruct(calls, tmp_path)


def test_reconstruct_rejects_path_escape(tmp_path):
    replay = load_replay()
    calls = [
        {
            "name": "file_write",
            "arguments": {"path": "../escape.py", "content": "x"},
        },
    ]

    with pytest.raises(replay.ReplayError, match="越界"):
        replay.reconstruct(calls, tmp_path)


def test_reconstruct_warns_on_unreplayable_calls(tmp_path):
    replay = load_replay()
    calls = [
        {"name": "shell", "arguments": {"command": "echo hi > x.txt"}},
        {"name": "file_write", "arguments": {"path": "run.py", "content": "x"}},
    ]

    warnings = replay.reconstruct(calls, tmp_path)

    assert any("shell" in item for item in warnings)
    assert (tmp_path / "run.py").read_text() == "x"


def test_reconstruct_deletes_file(tmp_path):
    replay = load_replay()
    calls = [
        {"name": "file_write", "arguments": {"path": "tmp.txt", "content": "x"}},
        {"name": "file_delete", "arguments": {"path": "tmp.txt"}},
    ]

    replay.reconstruct(calls, tmp_path)

    assert not (tmp_path / "tmp.txt").exists()


def test_run_id_from_result_reads_metadata(tmp_path):
    replay = load_replay()
    path = tmp_path / "result.json"
    path.write_text(
        json.dumps(
            {"agent_result": {"metadata": {"run_id": "875b3db3-5374-4a15-a6bb-279a944627e0"}}}
        ),
        encoding="utf-8",
    )

    assert (
        replay.run_id_from_result(path)
        == "875b3db3-5374-4a15-a6bb-279a944627e0"
    )


def test_run_id_from_result_accepts_job_directory(tmp_path):
    replay = load_replay()
    trial = tmp_path / "cancel-async-tasks__abc123"
    trial.mkdir()
    (trial / "result.json").write_text(
        json.dumps(
            {"agent_result": {"metadata": {"run_id": "875b3db3-5374-4a15-a6bb-279a944627e0"}}}
        ),
        encoding="utf-8",
    )

    assert (
        replay.run_id_from_result(tmp_path)
        == "875b3db3-5374-4a15-a6bb-279a944627e0"
    )


def test_run_id_from_result_rejects_missing_metadata(tmp_path):
    replay = load_replay()
    path = tmp_path / "result.json"
    path.write_text(json.dumps({"agent_result": {}}), encoding="utf-8")

    with pytest.raises(replay.ReplayError, match="run_id"):
        replay.run_id_from_result(path)

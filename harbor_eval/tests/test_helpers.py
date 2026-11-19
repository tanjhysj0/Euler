from agents.euler_agent import build_instruction


def test_build_instruction_maps_container_path_to_workspace():
    text = build_instruction("edit /app/main.py", "/tmp/ws", "/app")
    assert "/tmp/ws" in text
    assert "原任务中的 /app" in text
    assert "edit /app/main.py" in text


def test_build_instruction_keeps_original_text_last():
    text = build_instruction("fix the bug", "/tmp/ws", "/repo")
    assert text.endswith("fix the bug")

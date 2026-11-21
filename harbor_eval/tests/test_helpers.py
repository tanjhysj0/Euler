from agents.euler_agent import build_instruction


def test_build_instruction_reports_workspace():
    text = build_instruction("edit main.py", "/app")
    assert "你的工作区目录是 /app" in text


def test_build_instruction_keeps_original_text_last():
    text = build_instruction("fix the bug", "/repo")
    assert text.endswith("fix the bug")


def test_build_instruction_includes_convergence_guidance():
    text = build_instruction("fix the bug", "/repo")
    assert "验收点" in text
    assert "验收点全部通过后立即调用 submit 工具结束运行" in text
    assert "逐条列出" in text
    assert "根因" in text
    assert text.index("验收点") < text.index("fix the bug")


def test_build_instruction_discourages_extra_tests():
    text = build_instruction("fix the bug", "/repo")
    assert "不要新建超出任务要求的测试文件" in text

"""Harbor 外部 Agent：把 Euler 一次性二进制传入任务容器内直接执行。

设计目标是让 agent 进程就跑在任务容器里：工作区就是容器的工作目录，
验证器天然能看到产物，因此不再需要宿主机工作区映射或双向同步。
"""

from __future__ import annotations

import json
import os
import posixpath
import shlex
import ssl
from pathlib import Path

from harbor.agents.base import BaseAgent
from harbor.environments.base import BaseEnvironment
from harbor.models.agent.context import AgentContext

# 仓库内交叉编译产物目录；可用 EULER_AGENT_DIST 覆盖。
DEFAULT_DIST_DIR = Path(__file__).resolve().parents[2] / "bin" / "agent-dist"
# 容器内的固定路径：放在 /tmp 下，避免污染任务工作区。
REMOTE_DIR = "/tmp/euler"
REMOTE_BINARY = f"{REMOTE_DIR}/euler-agent"
REMOTE_INSTRUCTION = f"{REMOTE_DIR}/instruction.txt"
REMOTE_RESULT = f"{REMOTE_DIR}/result.json"
# 容器内 CA bundle：部分任务镜像没有 ca-certificates，需上传宿主机的信任库。
REMOTE_CA_BUNDLE = f"{REMOTE_DIR}/ca-bundle.pem"
# 单次模型请求的空闲超时：首 token 或流中途超过该时长没有数据即失败。
DEFAULT_MODEL_TIMEOUT = "10s"
# 单次模型请求的总时长上限，覆盖整个流式响应。
DEFAULT_MODEL_REQUEST_TIMEOUT = "5m"
# 宿主机常见的 CA bundle 位置，按优先级探测。
CA_BUNDLE_CANDIDATES = (
    "/etc/ssl/cert.pem",
    "/etc/ssl/certs/ca-certificates.crt",
    "/etc/pki/tls/certs/ca-bundle.crt",
)
# 容器架构别名到构建产物后缀的映射。
ARCH_ALIASES = {
    "x86_64": "amd64",
    "amd64": "amd64",
    "aarch64": "arm64",
    "arm64": "arm64",
}
DEFAULT_MODEL_BASE_URL = "https://api.deepseek.com"
# 0 表示该维度不设限；Harbor 评测只保留 10 分钟工作时长约束。
DEFAULT_MAX_ROUNDS = 0
DEFAULT_MAX_TOOL_CALLS = 0
DEFAULT_MAX_DURATION_SEC = 600
# exec 需要比 agent 自身预算更宽松，才能拿到它写出的结果而不是先被终止
EXEC_TIMEOUT_MARGIN_SEC = 120
DANGEROUS_WORKDIRS = frozenset({"/", "/opt", "/mnt", "/tmp"})
ROUND_LIMIT_CODE = "model_round_limit"
# 常见的 Langfuse 环境变量名；容器内追踪与宿主机保持同一套配置。
LANGFUSE_ENV_KEYS = (
    "LANGFUSE_HOST",
    "LANGFUSE_BASE_URL",
    "LANGFUSE_PUBLIC_KEY",
    "LANGFUSE_SECRET_KEY",
    "EULER_LANGFUSE_HOST",
    "EULER_LANGFUSE_PUBLIC_KEY",
    "EULER_LANGFUSE_SECRET_KEY",
)
EXECUTION_GUIDANCE = (
    "执行约束：\n"
    "1. 开始前列出指令中的验收点，实现后用一条端到端命令逐项确认。\n"
    "2. 工作区没有随任务提供的测试；不要新建超出任务要求的测试文件，"
    "用最小脚本或命令验证即可。\n"
    "3. 控制流只处理一次：不要重复取消或中断已经进入清理阶段的任务，清理必须能完成。\n"
    "4. 连续两次执行相同或等价命令且结果无变化时，立即更换策略。\n"
    "5. 自测失败时先写出根因，再改代码。\n"
    "6. 验收点全部通过后立即调用 submit 工具结束运行，不要重复验证同一条路径。\n"
    "7. 结束前在回复中逐条列出验收点、验证方式与结果；未验证项必须标注。"
)


def build_instruction(instruction: str, workdir: str) -> str:
    """在工作区说明之外附加执行约束，保留原始指令在末尾。"""
    prefix = f"你的工作区目录是 {workdir}，当前目录即该目录。"
    return f"{prefix}\n\n{EXECUTION_GUIDANCE}\n\n{instruction}"


def host_ca_bundle() -> Path | None:
    """返回宿主机可用的 CA bundle；优先 EULER_TLS_CA_FILE，找不到返回 None。"""
    candidates: list[Path] = []
    override = os.environ.get("EULER_TLS_CA_FILE")
    if override:
        candidates.append(Path(override))
    default = ssl.get_default_verify_paths()
    if default.cafile:
        candidates.append(Path(default.cafile))
    candidates.extend(Path(path) for path in CA_BUNDLE_CANDIDATES)
    for candidate in candidates:
        if candidate.is_file():
            return candidate
    return None


def validate_workdir(workdir: str) -> str:
    """校验容器工作目录，拒绝空值、相对路径与危险根路径。"""
    if not workdir or not workdir.startswith("/"):
        raise ValueError(f"invalid workdir: {workdir!r}")
    normalized = posixpath.normpath(workdir)
    if normalized in DANGEROUS_WORKDIRS or not normalized.strip("/"):
        raise ValueError(f"refusing dangerous workdir: {normalized!r}")
    return normalized


class EulerAgent(BaseAgent):
    """把容器内运行的 Euler 一次性二进制包装成 Harbor Agent。"""

    @staticmethod
    def name() -> str:
        return "euler"

    def __init__(self, logs_dir: Path, model_name: str | None = None, **kwargs):
        super().__init__(logs_dir=logs_dir, model_name=model_name, **kwargs)
        self.dist_dir = Path(
            os.environ.get("EULER_AGENT_DIST", str(DEFAULT_DIST_DIR))
        )
        self.model = os.environ.get("EULER_MODEL") or self.model_name or ""
        self.model_base_url = (
            os.environ.get("EULER_MODEL_BASE_URL") or DEFAULT_MODEL_BASE_URL
        )
        self.reasoning_effort = os.environ.get("EULER_REASONING_EFFORT") or ""
        self.max_model_rounds = _optional_int(
            os.environ.get("EULER_MAX_MODEL_ROUNDS"), DEFAULT_MAX_ROUNDS, allow_zero=True
        )
        self.max_tool_calls = _optional_int(
            os.environ.get("EULER_MAX_TOOL_CALLS"), DEFAULT_MAX_TOOL_CALLS,
            allow_zero=True
        )
        self.max_duration_sec = _optional_int(
            os.environ.get("EULER_MAX_DURATION_SEC"), DEFAULT_MAX_DURATION_SEC
        )
        self.api_key = (
            os.environ.get("EULER_MODEL_API_KEY")
            or os.environ.get("OPENAI_API_KEY")
            or ""
        )
        self.model_timeout = (
            os.environ.get("EULER_MODEL_TIMEOUT") or DEFAULT_MODEL_TIMEOUT
        )
        self.model_request_timeout = (
            os.environ.get("EULER_MODEL_REQUEST_TIMEOUT")
            or DEFAULT_MODEL_REQUEST_TIMEOUT
        )
        # setup 成功上传 CA bundle 后才在容器内设置 SSL_CERT_FILE。
        self._ca_bundle_uploaded = False

    def version(self) -> str:
        return "0.2.0"

    async def setup(self, environment: BaseEnvironment) -> None:
        """检测容器架构，上传对应二进制并校验可执行。"""
        arch = await self._detect_arch(environment)
        binary = self._binary_path(arch)
        result = await environment.exec(command=f"mkdir -p {REMOTE_DIR}")
        if result.return_code != 0:
            raise RuntimeError(f"create remote dir failed: {result.stderr}")
        await environment.upload_file(binary, REMOTE_BINARY)
        # 任务镜像可能缺少 CA 根证书（例如 write-compressor），上传宿主机信任库
        # 并通过 SSL_CERT_FILE 让 Go 的 TLS 校验使用它。
        ca_bundle = host_ca_bundle()
        if ca_bundle is not None:
            await environment.upload_file(ca_bundle, REMOTE_CA_BUNDLE)
            self._ca_bundle_uploaded = True
        await self._chmod(environment)
        check = await environment.exec(command=f"{shlex.quote(REMOTE_BINARY)} -version")
        if check.return_code != 0:
            raise RuntimeError(
                f"euler-agent not runnable: {check.stderr or check.stdout}"
            )

    async def run(
        self,
        instruction: str,
        environment: BaseEnvironment,
        context: AgentContext,
    ) -> None:
        if not self.api_key:
            raise RuntimeError(
                "missing model api key: set EULER_MODEL_API_KEY or OPENAI_API_KEY"
            )
        if not self.model:
            raise RuntimeError("missing model: set EULER_MODEL or pass -m")
        workspace = validate_workdir(await self._workdir(environment))
        await environment.exec(command=f"mkdir -p {REMOTE_DIR}")
        local_instruction = self._write_instruction(
            build_instruction(instruction, workspace)
        )
        await environment.upload_file(local_instruction, REMOTE_INSTRUCTION)
        command = shlex.join(
            [
                REMOTE_BINARY,
                "-instruction-file",
                REMOTE_INSTRUCTION,
                "-dir",
                workspace,
                "-result",
                REMOTE_RESULT,
                "-max-rounds",
                str(self.max_model_rounds),
                "-max-tool-calls",
                str(self.max_tool_calls),
                "-max-duration",
                f"{self.max_duration_sec}s",
                "-model-timeout",
                self.model_timeout,
                "-model-request-timeout",
                self.model_request_timeout,
            ]
        )
        try:
            result = await environment.exec(
                command=command,
                cwd=workspace,
                env=self._exec_env(),
                timeout_sec=self.max_duration_sec + EXEC_TIMEOUT_MARGIN_SEC,
            )
        except TimeoutError:
            raise RuntimeError("euler agent timed out") from None
        report = await self._download_report(environment)
        context.metadata = self._metadata(report, workspace, result)
        if report is None:
            raise RuntimeError(
                "euler-agent produced no result: "
                f"{_tail(result.stderr or result.stdout)}"
            )

    async def _detect_arch(self, environment: BaseEnvironment) -> str:
        """读取容器架构并映射为构建产物后缀。"""
        result = await environment.exec(command="uname -m")
        if result.return_code != 0:
            raise RuntimeError(f"detect arch failed: {result.stderr}")
        raw = (result.stdout or "").strip().splitlines()
        arch = ARCH_ALIASES.get(raw[-1].strip() if raw else "")
        if arch is None:
            raise RuntimeError(f"unsupported container arch: {raw!r}")
        return arch

    def _binary_path(self, arch: str) -> Path:
        """返回指定架构的本地二进制；缺失时给出构建提示。"""
        binary = self.dist_dir / f"euler-agent-linux-{arch}"
        if not binary.is_file():
            raise RuntimeError(
                f"missing agent binary {binary}; run `make agent-dist` first"
            )
        return binary

    async def _chmod(self, environment: BaseEnvironment) -> None:
        quoted = shlex.quote(REMOTE_BINARY)
        result = await environment.exec(command=f"chmod +x {quoted}")
        if result.return_code != 0:
            raise RuntimeError(f"chmod agent failed: {result.stderr}")

    async def _workdir(self, environment: BaseEnvironment) -> str:
        result = await environment.exec(command="pwd")
        if result.return_code != 0:
            raise RuntimeError(f"pwd failed: {result.stderr}")
        return (result.stdout or "").strip()

    def _write_instruction(self, text: str) -> Path:
        """把完整指令写到 trial 日志目录，便于事后复盘与上传。"""
        self.logs_dir.mkdir(parents=True, exist_ok=True)
        path = self.logs_dir / "instruction.txt"
        path.write_text(text, encoding="utf-8")
        return path

    def _exec_env(self) -> dict[str, str]:
        """组装容器内进程需要的环境变量；空值不传，避免覆盖默认。"""
        values = {
            "EULER_MODEL": self.model,
            "EULER_MODEL_BASE_URL": self.model_base_url,
            "EULER_MODEL_API_KEY": self.api_key,
            "EULER_REASONING_EFFORT": self.reasoning_effort,
            # 仅在 setup 上传成功时覆盖，容器自带信任库时保持默认探测。
            "SSL_CERT_FILE": REMOTE_CA_BUNDLE if self._ca_bundle_uploaded else "",
        }
        values.update(
            {key: os.environ.get(key, "") for key in LANGFUSE_ENV_KEYS}
        )
        return {key: value for key, value in values.items() if value}

    async def _download_report(
        self, environment: BaseEnvironment
    ) -> dict | None:
        """下载并解析结果 JSON；未生成或不可解析时返回 None。"""
        target = self.logs_dir / "result.json"
        try:
            await environment.download_file(REMOTE_RESULT, target)
        except Exception:
            return None
        try:
            return json.loads(target.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            return None

    def _metadata(
        self, report: dict | None, workspace: str, result: object
    ) -> dict:
        """把运行结果映射为 trial 元数据，失败也保留可定位的信息。"""
        metadata = {
            "agent": "euler",
            "model": self.model,
            "workspace": workspace,
            "max_model_rounds": self.max_model_rounds,
            "max_tool_calls": self.max_tool_calls,
            "exit_code": getattr(result, "return_code", None),
            "outcome": "failed",
        }
        if report is None:
            metadata["error_message"] = _tail(
                getattr(result, "stderr", "") or getattr(result, "stdout", "")
            )
            return metadata
        metadata.update(
            {
                "status": report.get("status"),
                "duration_ms": report.get("duration_ms"),
                "tool_calls": len(report.get("tool_calls") or []),
                "output_chars": len(report.get("output") or ""),
            }
        )
        if report.get("error_code"):
            metadata["error_code"] = report["error_code"]
        if report.get("error"):
            metadata["error_message"] = report["error"]
        if report.get("status") == "succeeded":
            # 零工具调用的成功属于空跑，单独标记便于筛查
            metadata["outcome"] = (
                "no_action" if not report.get("tool_calls") else "completed"
            )
        elif report.get("error_code") == ROUND_LIMIT_CODE:
            metadata["outcome"] = "budget_exhausted"
        return metadata


def _optional_int(
    value: str | None, fallback: int, *, allow_zero: bool = False
) -> int:
    """把可选环境变量解析为整数；非法值回退到默认值。

    allow_zero 为真时接受 0（例如轮次或工具调用上限用 0 表示不设限）。
    """
    if not value:
        return fallback
    try:
        parsed = int(value)
    except ValueError:
        return fallback
    if parsed > 0 or (allow_zero and parsed == 0):
        return parsed
    return fallback


def _tail(text: str, limit: int = 800) -> str:
    """截取输出末尾，避免把整段日志塞进元数据。"""
    text = (text or "").strip()
    return text if len(text) <= limit else "..." + text[-limit:]

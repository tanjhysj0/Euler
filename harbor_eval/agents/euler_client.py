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
        await self.aclose()

    async def aclose(self) -> None:
        """关闭底层 HTTP 连接池，供不使用 async with 的调用方显式释放。"""
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

    async def messages(self, session_id: str) -> list[dict]:
        """按分页读取会话的全部消息，供轨迹存档使用。"""
        items: list[dict] = []
        after = 0
        page_size = 200
        while True:
            params: dict[str, str] = {"limit": str(page_size)}
            if after > 0:
                params["after"] = str(after)
            response = await self._client.get(
                f"/api/v1/sessions/{session_id}/messages", params=params
            )
            response.raise_for_status()
            page = response.json()
            batch = page.get("messages") or []
            items.extend(batch)
            next_after = int(page.get("next_after") or 0)
            if not batch or next_after <= after:
                return items
            after = next_after

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

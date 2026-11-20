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


def test_client_pages_session_messages():
    pages = {
        0: {"messages": [{"id": "m1"}], "next_after": 1},
        1: {"messages": [{"id": "m2"}], "next_after": 0},
    }
    seen = []

    def handler(request: httpx.Request) -> httpx.Response:
        if request.url.path == "/api/v1/bootstrap":
            return httpx.Response(204)
        after = int(request.url.params.get("after", "0"))
        seen.append(after)
        return httpx.Response(200, json=pages[after])

    async def scenario():
        async with EulerClient(
            "http://euler", transport=httpx.MockTransport(handler)
        ) as client:
            return await client.messages("session-1")

    assert [item["id"] for item in run(scenario())] == ["m1", "m2"]
    assert seen == [0, 1]


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
        return httpx.Response(
            409, json={"error": {"code": "approval_already_decided"}}
        )

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

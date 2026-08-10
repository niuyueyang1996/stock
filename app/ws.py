"""WebSocket 推送：连接管理 + 跨线程广播。

FastAPI 的 ws 处理在事件循环线程；jobs 刷新/后台自动刷新是同步线程，需经
loop.call_soon_threadsafe 投递到事件循环再逐客户端 send_json。无客户端时广播为 no-op。
"""
from __future__ import annotations

import asyncio
import logging
import threading
from typing import Any

logger = logging.getLogger("ws")

_clients: set[Any] = set()
_clients_lock = threading.Lock()
_loop: asyncio.AbstractEventLoop | None = None


def set_loop(loop: asyncio.AbstractEventLoop) -> None:
    global _loop
    _loop = loop


def is_connected() -> bool:
    with _clients_lock:
        return len(_clients) > 0


async def connect(websocket: Any) -> None:
    with _clients_lock:
        _clients.add(websocket)


async def disconnect(websocket: Any) -> None:
    with _clients_lock:
        _clients.discard(websocket)


async def _send_all(message: dict) -> None:
    with _clients_lock:
        clients = list(_clients)
    if not clients:
        return
    dead = []
    for ws in clients:
        try:
            await ws.send_json(message)
        except Exception:  # noqa: BLE001 断连客户端丢弃
            dead.append(ws)
    if dead:
        with _clients_lock:
            for ws in dead:
                _clients.discard(ws)


def broadcast(message: dict) -> None:
    """从任意线程安全地推送（web/异步线程也可直接 await _send_all）。

    事件循环未建立（无客户端连过 / 已关闭）→ 忽略。
    """
    loop = _loop
    if loop is None or loop.is_closed():
        return

    def _post() -> None:
        try:
            asyncio.create_task(_send_all(message))
        except RuntimeError:
            pass  # loop 关闭竞态，忽略

    loop.call_soon_threadsafe(_post)

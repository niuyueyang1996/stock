"""Windows tray launcher for the packaged desktop application.

The module keeps its reusable helpers platform-neutral so port/state behavior can
be tested on macOS/Linux. Windows-only imports are delayed until ``main()``.
"""
from __future__ import annotations

import argparse
import json
import logging
import os
import socket
import sys
import threading
import time
import urllib.error
import urllib.request
import webbrowser
from logging.handlers import RotatingFileHandler
from pathlib import Path
from typing import Callable, Iterable

from app.config import LOG_DIR, RUNTIME_DIR
from app.version import APP_ID, APP_NAME, APP_VERSION

HOST = "127.0.0.1"
PORTS = range(8000, 8100)
STATE_FILE = RUNTIME_DIR / "launcher.json"
MUTEX_NAME = r"Local\StockAnalyzer.Singleton"
RESTART_EVENT_NAME = r"Local\StockAnalyzer.Restart"
SHUTDOWN_EVENT_NAME = r"Local\StockAnalyzer.Shutdown"

logger = logging.getLogger("launcher")


def app_url(port: int) -> str:
    return f"http://{HOST}:{port}/"


def health_url(port: int) -> str:
    return f"http://{HOST}:{port}/api/health"


def find_available_port(
    host: str = HOST, ports: Iterable[int] = PORTS
) -> int | None:
    """Return the first bindable loopback port without keeping it reserved."""
    for port in ports:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        try:
            if os.name == "nt" and hasattr(socket, "SO_EXCLUSIVEADDRUSE"):
                sock.setsockopt(socket.SOL_SOCKET, socket.SO_EXCLUSIVEADDRUSE, 1)
            sock.bind((host, int(port)))
            return int(port)
        except OSError:
            continue
        finally:
            sock.close()
    return None


def save_state(path: Path, *, pid: int, port: int) -> None:
    """Atomically publish the running instance for duplicate launchers."""
    path.parent.mkdir(parents=True, exist_ok=True)
    payload = {
        "app_id": APP_ID,
        "version": APP_VERSION,
        "pid": int(pid),
        "port": int(port),
    }
    temp = path.with_suffix(path.suffix + ".tmp")
    temp.write_text(json.dumps(payload, ensure_ascii=False), encoding="utf-8")
    os.replace(temp, path)


def load_state(path: Path = STATE_FILE) -> dict | None:
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
        if payload.get("app_id") != APP_ID:
            return None
        port = int(payload["port"])
        pid = int(payload["pid"])
        if not 1 <= port <= 65535 or pid <= 0:
            return None
        return {
            "app_id": APP_ID,
            "version": str(payload.get("version", "")),
            "pid": pid,
            "port": port,
        }
    except (OSError, ValueError, TypeError, KeyError, json.JSONDecodeError):
        return None


def remove_state(path: Path = STATE_FILE) -> None:
    try:
        path.unlink(missing_ok=True)
    except OSError:
        logger.warning("无法删除运行状态文件：%s", path, exc_info=True)


def is_our_server(port: int, timeout: float = 1.0) -> bool:
    """Verify that a port belongs to this app, not merely any HTTP server."""
    request = urllib.request.Request(
        health_url(port), headers={"User-Agent": f"{APP_ID}-launcher/{APP_VERSION}"}
    )
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            payload = json.loads(response.read().decode("utf-8"))
        return bool(payload.get("ok")) and payload.get("app_id") == APP_ID
    except (OSError, ValueError, urllib.error.URLError, json.JSONDecodeError):
        return False


def wait_until_healthy(
    port: int,
    timeout: float = 60.0,
    alive: Callable[[], bool] | None = None,
) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if is_our_server(port):
            return True
        if alive is not None and not alive():
            return False
        time.sleep(0.25)
    return False


def setup_logging() -> Path:
    LOG_DIR.mkdir(parents=True, exist_ok=True)
    log_file = LOG_DIR / "stock-analyzer.log"
    handler = RotatingFileHandler(
        log_file, maxBytes=2 * 1024 * 1024, backupCount=5, encoding="utf-8"
    )
    handler.setFormatter(
        logging.Formatter("%(asctime)s [%(name)s] %(levelname)s %(message)s")
    )
    root = logging.getLogger()
    root.setLevel(logging.INFO)
    root.handlers.clear()
    root.addHandler(handler)
    return log_file


def open_logs_folder() -> None:
    LOG_DIR.mkdir(parents=True, exist_ok=True)
    if os.name == "nt":
        os.startfile(str(LOG_DIR))  # type: ignore[attr-defined]


def show_error(message: str) -> None:
    logger.error(message)
    if os.name == "nt":
        import ctypes

        ctypes.windll.user32.MessageBoxW(  # type: ignore[attr-defined]
            None,
            f"{message}\n\n日志目录：{LOG_DIR}",
            APP_NAME,
            0x10,
        )
    else:
        print(message, file=sys.stderr)


def _kernel32():
    import ctypes

    kernel32 = ctypes.WinDLL("kernel32", use_last_error=True)
    kernel32.CreateMutexW.argtypes = [ctypes.c_void_p, ctypes.c_bool, ctypes.c_wchar_p]
    kernel32.CreateMutexW.restype = ctypes.c_void_p
    kernel32.CreateEventW.argtypes = [
        ctypes.c_void_p,
        ctypes.c_bool,
        ctypes.c_bool,
        ctypes.c_wchar_p,
    ]
    kernel32.CreateEventW.restype = ctypes.c_void_p
    kernel32.OpenEventW.argtypes = [ctypes.c_uint32, ctypes.c_bool, ctypes.c_wchar_p]
    kernel32.OpenEventW.restype = ctypes.c_void_p
    kernel32.OpenMutexW.argtypes = [ctypes.c_uint32, ctypes.c_bool, ctypes.c_wchar_p]
    kernel32.OpenMutexW.restype = ctypes.c_void_p
    kernel32.SetEvent.argtypes = [ctypes.c_void_p]
    kernel32.SetEvent.restype = ctypes.c_bool
    kernel32.CloseHandle.argtypes = [ctypes.c_void_p]
    kernel32.CloseHandle.restype = ctypes.c_bool
    kernel32.WaitForMultipleObjects.argtypes = [
        ctypes.c_uint32,
        ctypes.POINTER(ctypes.c_void_p),
        ctypes.c_bool,
        ctypes.c_uint32,
    ]
    kernel32.WaitForMultipleObjects.restype = ctypes.c_uint32
    return kernel32


def create_singleton_mutex() -> tuple[int | None, bool]:
    """Return (handle, is_primary). Only called on Windows."""
    import ctypes

    kernel32 = _kernel32()
    ctypes.set_last_error(0)
    handle = kernel32.CreateMutexW(None, False, MUTEX_NAME)
    if not handle:
        raise ctypes.WinError(ctypes.get_last_error())
    return int(handle), ctypes.get_last_error() != 183


def create_control_event(name: str) -> int:
    import ctypes

    handle = _kernel32().CreateEventW(None, False, False, name)
    if not handle:
        raise ctypes.WinError(ctypes.get_last_error())
    return int(handle)


def signal_control_event(name: str) -> bool:
    if os.name != "nt":
        return False
    kernel32 = _kernel32()
    event_modify_state = 0x0002
    handle = kernel32.OpenEventW(event_modify_state, False, name)
    if not handle:
        return False
    try:
        return bool(kernel32.SetEvent(handle))
    finally:
        kernel32.CloseHandle(handle)


def singleton_exists() -> bool:
    """Check whether the primary tray process still owns the named mutex object."""
    if os.name != "nt":
        return False
    kernel32 = _kernel32()
    synchronize = 0x00100000
    handle = kernel32.OpenMutexW(synchronize, False, MUTEX_NAME)
    if not handle:
        return False
    kernel32.CloseHandle(handle)
    return True


def wait_for_singleton_exit(timeout: float = 20.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if not singleton_exists():
            return True
        time.sleep(0.1)
    return not singleton_exists()


def close_handle(handle: int | None) -> None:
    if handle and os.name == "nt":
        _kernel32().CloseHandle(handle)


class ServerController:
    """Own one Uvicorn server thread while the tray process remains alive."""

    def __init__(self, state_file: Path = STATE_FILE):
        self.state_file = state_file
        self.server = None
        self.thread: threading.Thread | None = None
        self.port: int | None = None
        self.lock = threading.RLock()

    def start(self) -> bool:
        import uvicorn

        with self.lock:
            if self.port and is_our_server(self.port):
                return True
            self._stop_unlocked()
            remaining = list(PORTS)
            while remaining:
                port = find_available_port(ports=remaining)
                if port is None:
                    break
                remaining = [candidate for candidate in remaining if candidate > port]
                config = uvicorn.Config(
                    "app.main:app",
                    host=HOST,
                    port=port,
                    log_config=None,
                    access_log=False,
                )
                self.server = uvicorn.Server(config)
                self.port = port
                save_state(self.state_file, pid=os.getpid(), port=port)
                self.thread = threading.Thread(
                    target=self.server.run,
                    name="stock-analyzer-server",
                    daemon=True,
                )
                self.thread.start()
                if wait_until_healthy(port, timeout=60, alive=self.thread.is_alive):
                    logger.info("服务已启动：%s", app_url(port))
                    return True
                port_was_taken = find_available_port(ports=[port]) is None
                if port_was_taken:
                    logger.warning("端口 %d 被其他程序占用，尝试下一个端口", port)
                else:
                    logger.error("服务在端口 %d 初始化失败", port)
                self._stop_unlocked()
                if not port_was_taken:
                    # The port is free again, so startup failed inside the app rather
                    # than because of a bind race. Retrying 99 ports would only hide
                    # the real database/import error and flood the log.
                    break
            return False

    def _stop_unlocked(self) -> None:
        server = self.server
        thread = self.thread
        if server is not None:
            server.should_exit = True
        if thread is not None and thread.is_alive():
            thread.join(timeout=10)
        if thread is not None and thread.is_alive() and server is not None:
            server.force_exit = True
            thread.join(timeout=3)
        self.server = None
        self.thread = None
        self.port = None
        remove_state(self.state_file)

    def stop(self) -> None:
        with self.lock:
            self._stop_unlocked()
            logger.info("服务已停止")

    def restart(self, *, open_after: bool = True) -> bool:
        with self.lock:
            self._stop_unlocked()
            ok = self.start()
        if ok and open_after:
            self.open_home()
        return ok

    def open_home(self) -> bool:
        with self.lock:
            port = self.port
            healthy = bool(port and is_our_server(port))
        if not healthy:
            healthy = self.restart(open_after=False)
            port = self.port
        if healthy and port:
            webbrowser.open(app_url(port), new=2)
            return True
        return False


def _make_tray_image():
    from PIL import Image, ImageDraw

    image = Image.new("RGBA", (64, 64), (28, 100, 242, 255))
    draw = ImageDraw.Draw(image)
    draw.rounded_rectangle((2, 2, 61, 61), radius=13, fill=(28, 100, 242, 255))
    draw.line((13, 46, 25, 34, 35, 39, 51, 18), fill="white", width=5)
    for x, height in ((14, 12), (27, 20), (40, 28), (51, 38)):
        draw.rectangle((x - 3, 51 - height, x + 2, 51), fill=(255, 255, 255, 215))
    return image


def _wait_for_existing(timeout: float = 60.0) -> int | None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        state = load_state()
        if state and is_our_server(state["port"]):
            return int(state["port"])
        time.sleep(0.25)
    return None


def open_existing_instance() -> bool:
    port = _wait_for_existing()
    if port is None and signal_control_event(RESTART_EVENT_NAME):
        port = _wait_for_existing()
    if port is None:
        show_error("程序已在运行，但服务暂时无法访问。请从托盘菜单选择“重启服务”。")
        return False
    webbrowser.open(app_url(port), new=2)
    return True


def run_primary(mutex_handle: int) -> int:
    import ctypes

    import pystray

    setup_logging()
    remove_state()
    controller = ServerController()
    restart_event = create_control_event(RESTART_EVENT_NAME)
    shutdown_event = create_control_event(SHUTDOWN_EVENT_NAME)
    icon = None

    def background(
        action: Callable[[], object], failure: str = "操作失败，请查看日志。"
    ) -> None:
        def run_action() -> None:
            try:
                action()
            except Exception:  # noqa: BLE001 tray callbacks must never terminate the UI loop
                logger.exception(failure)
                show_error(failure)

        threading.Thread(target=run_action, daemon=True).start()

    def start_and_open() -> None:
        try:
            if not controller.start():
                show_error("启动失败：8000–8099 端口均不可用，或服务初始化失败。")
                return
            controller.open_home()
        except Exception:  # noqa: BLE001 show a friendly GUI error instead of losing the worker thread
            logger.exception("启动服务时发生异常")
            show_error("启动失败，请打开日志目录查看详细原因。")

    def restart_server() -> None:
        try:
            if not controller.restart(open_after=True):
                show_error("重启失败，请查看日志后重试。")
        except Exception:  # noqa: BLE001
            logger.exception("重启服务时发生异常")
            show_error("重启失败，请查看日志后重试。")

    def exit_app() -> None:
        controller.stop()
        if icon is not None:
            icon.stop()

    menu = pystray.Menu(
        pystray.MenuItem(
            "打开首页",
            lambda _icon, _item: background(controller.open_home),
            default=True,
        ),
        pystray.MenuItem("重启服务", lambda _icon, _item: background(restart_server)),
        pystray.MenuItem("打开日志目录", lambda _icon, _item: open_logs_folder()),
        pystray.Menu.SEPARATOR,
        pystray.MenuItem("退出", lambda _icon, _item: background(exit_app)),
    )
    icon = pystray.Icon(APP_ID, _make_tray_image(), APP_NAME, menu)

    def watch_control_events() -> None:
        handles = (ctypes.c_void_p * 2)(restart_event, shutdown_event)
        while True:
            result = _kernel32().WaitForMultipleObjects(2, handles, False, 0xFFFFFFFF)
            if result == 0:
                restart_server()
            elif result == 1:
                exit_app()
                return
            else:
                logger.error("控制事件监听失败：%s", result)
                return

    threading.Thread(target=watch_control_events, name="launcher-control", daemon=True).start()

    def setup_tray(tray) -> None:  # noqa: ANN001 pystray callback
        tray.visible = True
        background(start_and_open)

    try:
        icon.run(setup=setup_tray)
        return 0
    finally:
        controller.stop()
        close_handle(restart_event)
        close_handle(shutdown_event)
        close_handle(mutex_handle)


def _control_existing(command: str) -> int:
    event = SHUTDOWN_EVENT_NAME if command == "shutdown" else RESTART_EVENT_NAME
    deadline = time.monotonic() + 10
    signaled = False
    while time.monotonic() < deadline:
        if signal_control_event(event):
            signaled = True
            break
        if not singleton_exists():
            return 0 if command == "shutdown" else 1
        time.sleep(0.1)
    if not signaled:
        return 1
    if command == "shutdown":
        return 0 if wait_for_singleton_exit() else 1
    return 0 if _wait_for_existing() is not None else 1


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(add_help=False)
    parser.add_argument("--open", action="store_true")
    parser.add_argument("--restart", action="store_true")
    parser.add_argument("--shutdown", action="store_true")
    args = parser.parse_args(argv)

    if os.name != "nt":
        print("StockAnalyzer Windows launcher can only run on Windows.", file=sys.stderr)
        return 2
    if args.shutdown:
        return _control_existing("shutdown")
    if args.restart:
        return _control_existing("restart")

    mutex_handle, is_primary = create_singleton_mutex()
    if not is_primary:
        close_handle(mutex_handle)
        return 0 if open_existing_instance() else 1
    assert mutex_handle is not None
    return run_primary(mutex_handle)


if __name__ == "__main__":
    raise SystemExit(main())

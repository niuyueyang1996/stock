"""用户可配置项：简单/高级模式、静态刷新节流、动态自动刷新间隔。

配置落 config 表（key/value），getter 缺省回落常量、数值钳制，读失败不影响调用。
镜像 app/services/ai.py 的 get_max_tokens 模式。
"""
from __future__ import annotations

from app.models.db import get_conn

# 缺省值
DEFAULT_UI_MODE = "simple"          # simple=一切自动、无刷新按钮；advanced=显示刷新按钮+全列
DEFAULT_STATIC_TTL_MINUTES = 60     # 个股打开自动刷新：静态数据 1h 内齐全则不重拉
DEFAULT_DYNAMIC_INTERVAL_SECONDS = 300  # 盘中动态数据自动刷新间隔 5 分钟（默认，防源站封 IP）

_TTL_MIN = 10
_TTL_MAX = 1440
_INTERVAL_MIN = 30
_INTERVAL_MAX = 3600


def _get_str(key: str, default: str) -> str:
    try:
        with get_conn() as c:
            row = c.execute("SELECT value FROM config WHERE key=?", (key,)).fetchone()
        if row and str(row["value"] or "").strip():
            return str(row["value"]).strip()
    except Exception:  # noqa: BLE001 读配置失败不影响调用
        pass
    return default


def _get_int(key: str, default: int, lo: int, hi: int) -> int:
    try:
        with get_conn() as c:
            row = c.execute("SELECT value FROM config WHERE key=?", (key,)).fetchone()
        if row and str(row["value"] or "").strip().isdigit():
            return max(lo, min(hi, int(row["value"])))
    except Exception:  # noqa: BLE001 读配置失败不影响调用
        pass
    return default


def get_ui_mode() -> str:
    mode = _get_str("ui_mode", DEFAULT_UI_MODE)
    return mode if mode in ("simple", "advanced") else DEFAULT_UI_MODE


def get_static_ttl_minutes() -> int:
    return _get_int("refresh_static_ttl_minutes", DEFAULT_STATIC_TTL_MINUTES,
                    _TTL_MIN, _TTL_MAX)


def get_dynamic_interval_seconds() -> int:
    return _get_int("dynamic_interval_seconds", DEFAULT_DYNAMIC_INTERVAL_SECONDS,
                    _INTERVAL_MIN, _INTERVAL_MAX)


def get_last_full_sync_date() -> str:
    """最近一次每日全量同步的日期（YYYY-MM-DD）；从未同步过返回空串。"""
    return _get_str("last_full_sync_date", "")


def set_last_full_sync_date(today: str) -> None:
    """记录当日已做每日全量同步（与启动预热共用，避免同日双跑）。"""
    with get_conn() as c:
        c.execute(
            "INSERT INTO config(key, value) VALUES('last_full_sync_date', ?) "
            "ON CONFLICT(key) DO UPDATE SET value=excluded.value",
            (today,),
        )


def set_refresh_settings(mode: str | None = None,
                         static_ttl_minutes: int | None = None,
                         dynamic_interval_seconds: int | None = None) -> None:
    """批量写三个用户配置（校验由 API 层负责）。"""
    with get_conn() as c:
        for key, value in (
            ("ui_mode", mode),
            ("refresh_static_ttl_minutes", static_ttl_minutes),
            ("dynamic_interval_seconds", dynamic_interval_seconds),
        ):
            if value is None:
                continue
            c.execute(
                "INSERT INTO config(key, value) VALUES(?, ?) "
                "ON CONFLICT(key) DO UPDATE SET value=excluded.value",
                (key, str(value)),
            )

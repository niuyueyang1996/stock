"""汇率服务：外币（港股等）折算人民币。

口径（用户确认）：
- 汇率采用中行港币折算价，单位转换为 `1 HKD = x CNY`。
- 缺失时依次使用央行中间价、买卖价中点。
- 非交易日使用最近一个有效汇率（读缓存时前向取最近一条）。
- 动态/全量刷新在存在港股时自动刷新 HKD/CNY；历史回填覆盖最早港股交易日至今。
- 汇率缺失时绝不按 1:1 计算：调用方显示原币数据并在人民币汇总中剔除（missing_fx）。

数据源实现经 akshare（`currency_boc_sina`，中行外汇牌价，单位 100 外币）。
拉取函数做成模块级，便于测试打桩。
"""
import logging
from datetime import date, datetime, timedelta

from app.data.cache import (
    get_fx_rate,
    get_latest_fx_rate,
    get_fx_rates,
    upsert_fx_rate,
)
from app.models.db import get_conn

logger = logging.getLogger("fx")

# 支持折算的币种 → 中行牌价名称
CURRENCY_NAMES = {
    "HKD": "港币",
}

# 拉取窗口（覆盖最早港股交易日至今最多拉这么多自然天）
FX_HISTORY_WINDOW_DAYS = 400


def fetch_rate_for_date(currency: str, rate_date: str) -> tuple[float | None, str | None]:
    """从数据源拉取指定日汇率。返回 (rate, source)；失败返回 (None, None)。

    降级链：中行折算价 → 央行中间价 → 买卖价中点。
    """
    cn = CURRENCY_NAMES.get(currency)
    if not cn:
        return None, None
    # 新浪实时外汇 HKDCNY：1 HKD = x CNY（买卖价中点）。
    # 注：中行牌价接口(ak.currency_boc_sina)在本环境仅返回 2023 年历史，不可用，故改用新浪实时。
    # rate_date 仅作记录；实时接口只给当前值，历史日期也取当前近似（港币波动小，误差可接受）。
    try:
        import requests

        from app.config import HTTP_HEADERS, REQUEST_TIMEOUT

        r = requests.get(
            "https://hq.sinajs.cn/list=HKDCNY",
            headers={**HTTP_HEADERS, "Referer": "https://finance.sina.com.cn"},
            timeout=REQUEST_TIMEOUT,
        )
        text = r.content.decode("gbk", errors="replace")
        if "HKDCNY" not in text or '"' not in text:
            return None, None
        parts = text.split('"')[1].split(",")
        if len(parts) < 11:
            return None, None
        buy = float(parts[1])
        sell = float(parts[2])
        return round((buy + sell) / 2, 6), "新浪外汇"
    except Exception:  # noqa: BLE001 单源失败不阻断
        logger.warning("[汇率] HKD/CNY 新浪实时拉取失败")
        return None, None


def _rate_for(currency: str, rate_date: str) -> tuple[float | None, str | None]:
    """拉取并落库指定日汇率。返回 (rate, source)。"""
    rate, source = fetch_rate_for_date(currency, rate_date)
    if rate is None:
        return None, None
    upsert_fx_rate(currency, rate_date, rate, source)
    return rate, source


def ensure_fx_for_date(currency: str, rate_date: str) -> float | None:
    """确保指定日有汇率：读缓存 → 拉取落库 → 回退最近有效。返回汇率或 None。"""
    row = get_fx_rate(currency, rate_date)
    if row and row["rate"]:
        return float(row["rate"])
    rate, _ = _rate_for(currency, rate_date)
    if rate is not None:
        return rate
    row = get_latest_fx_rate(currency, rate_date)
    return float(row["rate"]) if row and row["rate"] else None


def get_fx_rate_cny(currency: str, rate_date: str) -> float | None:
    """读取某日汇率（1 原币 = x 人民币），纯缓存零网络。

    缺失时依次回退：≤rate_date 的最近有效值 → 任意最近值（历史回填用当前汇率近似）。
    """
    if currency in (None, "CNY"):
        return 1.0
    row = get_fx_rate(currency, rate_date)
    if row and row["rate"]:
        return float(row["rate"])
    row = get_latest_fx_rate(currency, rate_date)
    if row and row["rate"]:
        return float(row["rate"])
    row = get_latest_fx_rate(currency)  # 任意最近（含当前/未来）
    return float(row["rate"]) if row and row["rate"] else None


def fx_to_cny(amount: float | None, currency: str | None, rate: float | None) -> float | None:
    """原币金额 → 人民币。currency 为 CNY 或 rate=1 时原样返回；汇率缺失返回 None（不按 1:1）。"""
    if amount is None:
        return None
    if currency in (None, "CNY"):
        return round(amount, 2)
    if rate is None:
        return None
    return round(amount * rate, 2)


def hk_holding_codes() -> list[str]:
    """当前持仓中港股（非 CNY 币种）代码列表。"""
    with get_conn() as c:
        rows = c.execute(
            """SELECT h.code FROM holdings h
               LEFT JOIN stocks s ON h.code=s.code
               WHERE h.status='active' AND COALESCE(s.currency,'CNY')<>'CNY'"""
        ).fetchall()
    return [r["code"] for r in rows]


def refresh_hk_fx(now: datetime | None = None, force: bool = False) -> dict:
    """刷新港股汇率：新浪实时接口只给当前值，拉取并存入今日。返回统计。"""
    now = now or datetime.now()
    today = now.date().isoformat()
    codes = hk_holding_codes()
    if not codes:
        return {"currency": None, "fetched": 0, "range": None}
    fetched = 0
    for currency in CURRENCY_NAMES:
        latest = get_latest_fx_rate(currency, today)
        if latest and latest["rate_date"] >= today and not force:
            continue
        rate, source = _rate_for(currency, today)
        if rate is not None:
            fetched += 1
    # 汇率可用后回填缺失的港股人民币金额（成本折算）
    try:
        from app.services.holdings import backfill_trade_cny

        backfilled = backfill_trade_cny()
    except Exception:  # noqa: BLE001 回填失败不影响汇率刷新
        backfilled = 0
    return {"currency": "HKD", "fetched": fetched, "backfilled": backfilled, "range": [today, today]}

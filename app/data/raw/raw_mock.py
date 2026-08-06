"""接口层：离线模拟数据（测试桩）。返回固定原始数据，供 MockProvider 组装标准模型。

合成数据无真实平台格式，provider 直接映射为标准模型（无需 normalizer）。
"""
from datetime import date, timedelta

# 固定分笔（time, amount, sign）：覆盖各档位与买卖方向，供资金流测试
MOCK_TICKS: list[tuple[str, float, int]] = [
    ("09:31:00", 100_000, 1),    # 10万 买
    ("09:31:05", 50_000, -1),    # 5万 卖
    ("09:32:00", 200_000, 1),    # 20万 买
    ("09:32:30", 30_000, -1),    # 3万 卖
    ("09:33:00", 300_000, 1),    # 30万 买
    ("09:33:30", 120_000, -1),   # 12万 卖
]


def mock_quote_fields(code: str) -> dict:
    """模拟实时行情原始字段。"""
    return {
        "code": code, "name": f"模拟股{code}", "price": 10.0, "pct_chg": 0.5,
        "prev_close": 9.95, "open": 9.96, "high": 10.1, "low": 9.9,
        "volume": 100000, "amount": 1000000, "ts": date.today().isoformat() + " 15:00:00",
    }


def mock_bars(code: str, start: str, end: str) -> list[dict]:
    """模拟日K原始行（工作日递增 0.01）。"""
    bars = []
    d = date.fromisoformat(start)
    end_d = date.fromisoformat(end)
    price = 10.0
    while d <= end_d:
        if d.weekday() < 5:
            bars.append({"date": d.isoformat(), "open": price, "high": price,
                         "low": price, "close": price, "volume": 1000, "amount": 10000})
            price += 0.01
        d += timedelta(days=1)
    return bars


def mock_valuation_point() -> dict:
    """模拟估值历史原始点。"""
    return {"date": date.today().isoformat(), "value": 10.0}


def mock_financials_fields(code: str) -> dict:
    """模拟财务原始字段（人民币口径）。"""
    return {
        "report_date": "20260331", "roe": 12.0, "roa": 4.0, "revenue_yoy": 10.0,
        "profit_yoy": 8.0, "net_profit": 1_000_000_000, "net_assets": 5_000_000_000,
        "eps": 1.0, "dv_per_share": 0.5, "payout_ratio": 50.0, "dv_report": "2025年报",
        "profit_series": [
            {"report_date": "20260331", "net_profit": 1_000_000_000, "profit_yoy": 8.0},
            {"report_date": "20251231", "net_profit": 1_000_000_000, "profit_yoy": 5.0},
        ],
        "total_shares": 1_000_000_000,
    }

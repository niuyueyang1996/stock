"""持仓交易单元测试：移动加权成本、卖出、撤销回滚、超量校验。"""
import pytest

from app.models.db import get_conn
from app.services import holdings


def _qty(code):
    with get_conn() as c:
        row = c.execute("SELECT quantity, avg_cost, status FROM holdings WHERE code=?", (code,)).fetchone()
        return dict(row) if row else None


def test_cost_adjust_increases_cost():
    """正调整：补记漏记成本 → avg_cost 上升，数量不变。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")   # 成本 10
    holdings.adjust_cost("600000", amount=200, note="补记成本")        # 加 200 → 成本 12
    h = _qty("600000")
    assert h["quantity"] == 100
    assert h["avg_cost"] == pytest.approx(12.0)


def test_cost_adjust_dividend_reduction():
    """负调整：分红除权摊薄 → avg_cost 下降，数量不变（pnl 不再虚亏）。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")   # 成本 10
    holdings.adjust_cost("600000", amount=-300, note="分红除权 3元/股")  # 减 300 → 成本 7
    h = _qty("600000")
    assert h["quantity"] == 100
    assert h["avg_cost"] == pytest.approx(7.0)


def test_cost_adjust_no_holding_rejected():
    """无持仓时无法调整成本。"""
    with pytest.raises(ValueError):
        holdings.adjust_cost("600000", amount=100, name="浦发银行")


def test_cost_adjust_replay_consistent():
    """重放法一致：调整后任意交易操作仍保持调整后的成本。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    holdings.adjust_cost("600000", amount=-100, note="分红")
    holdings.record_trade("600000", "buy", 12, 100)   # 再加仓 → 成本重算
    h = _qty("600000")
    # 调整后成本 9，加仓 100@12 → (100×9+1200)/200 = 10.5
    assert h["avg_cost"] == pytest.approx(10.5)


def test_cost_adjust_delete_reverts():
    """删除调整记录 → 成本恢复原值。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    r = holdings.adjust_cost("600000", amount=200, note="补记")
    holdings.delete_trade(r["trade_id"])
    assert _qty("600000")["avg_cost"] == pytest.approx(10.0)


def test_adjust_qty_split_shares():
    """拆股/送股：只加股不改总成本 → 每股成本摊薄。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")  # 100股@10，总成本1000
    holdings.adjust_cost("600000", delta_qty=100, note="1拆2")         # 加100股，总成本不变
    h = _qty("600000")
    assert h["quantity"] == 200
    assert h["avg_cost"] == pytest.approx(5.0)  # 1000/200


def test_update_adjust_preserves_split_qty():
    """编辑拆股 adjust 记录（改备注）不得清零 quantity，持仓股数保持。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    r = holdings.adjust_cost("600000", delta_qty=100, note="1拆2")
    tid = r["trade_id"]
    holdings.update_trade(tid, note="1拆2（备注修正）")
    with get_conn() as c:
        row = c.execute("SELECT quantity, amount, note FROM trades WHERE id=?", (tid,)).fetchone()
    assert row["quantity"] == pytest.approx(100.0)
    assert row["amount"] == pytest.approx(0.0)
    assert "备注修正" in (row["note"] or "")
    assert _qty("600000")["quantity"] == 200
    assert _qty("600000")["avg_cost"] == pytest.approx(5.0)


def test_hk_buy_fee_without_fx_not_one_to_one():
    """港股买入：amount_cny 有值但 fx_rate 缺失时，费用不得按 1:1 计入；标记 missing_fx。"""
    with get_conn() as c:
        c.execute(
            """INSERT INTO stocks(code,name,market,currency,tag) VALUES(?,?,?,?,?)
               ON CONFLICT(code) DO UPDATE SET currency='HKD'""",
            ("00700", "腾讯控股", "hk", "HKD", "港股"),
        )
        c.execute(
            """INSERT INTO trades(code,side,price,quantity,amount,fee,trade_time,note,fx_rate,amount_cny)
               VALUES(?,?,?,?,?,?,?,?,?,?)""",
            ("00700", "buy", 300.0, 100, 30000.0, 100.0, "2026-08-01 10:00:00", "测试",
             None, 27000.0),  # 有 amount_cny、无 fx_rate
        )
    from app.services.holdings import rebuild
    with get_conn() as c:
        h = rebuild("00700", c)
    assert h["missing_fx"] is True
    assert h["avg_cost_cny"] is None
    # 本金 27000 已折算，但缺汇率 → 整段人民币口径不可信，不假算费用
    assert h["total_buy_cny"] is None


def test_adjust_qty_and_amount_combined():
    """同时调整股数与成本。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    holdings.adjust_cost("600000", amount=200, delta_qty=100, note="补记+送股")
    h = _qty("600000")
    assert h["quantity"] == 200
    assert h["avg_cost"] == pytest.approx(6.0)  # (1000+200)/200


def test_adjust_zero_both_rejected():
    """金额与股数都为 0 → 拒绝。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    with pytest.raises(ValueError):
        holdings.adjust_cost("600000", amount=0, delta_qty=0)


def test_adjust_qty_to_zero_rejected():
    """调整后股数 ≤ 0 → 拒绝（重放时抛错并回滚）。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    with pytest.raises(ValueError):
        holdings.adjust_cost("600000", delta_qty=-100, note="错误减股")


def test_cost_adjust_dividend_flag_tracks_cumulative():
    """is_dividend=True 的调整计入累计分红，普通成本修正不计。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    holdings.adjust_cost("600000", amount=-300, is_dividend=True, note="除权")   # 除权 300
    holdings.adjust_cost("600000", amount=50, is_dividend=False, note="补记")    # 普通修正
    hs = {h["code"]: h for h in holdings.get_holdings(active_only=True)}
    assert hs["600000"]["total_dividend"] == pytest.approx(300.0)


def test_moving_average_cost():
    """两次买入移动加权：100@10 + 100@12 → avg_cost=11。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    holdings.record_trade("600000", "buy", 12, 100)
    h = _qty("600000")
    assert h["quantity"] == 200
    assert h["avg_cost"] == pytest.approx(11.0)


def test_buy_fee_included():
    """买入含费用计入成本：100@10 + 费5 → avg_cost=10.05。"""
    # 000001 同时是上证指数代码：模拟真实用户已持有该股（stocks 表有记录）→ 按个股放行
    from app.data.base import load_index_registry
    from app.models.db import get_conn

    with get_conn() as c:
        c.execute("INSERT INTO stocks (code, name, market) VALUES ('000001', '平安银行', 'sz')")
    load_index_registry()
    holdings.record_trade("000001", "buy", 10, 100, fee=5, name="平安银行")
    h = _qty("000001")
    assert h["avg_cost"] == pytest.approx(10.05)


def test_sell_keeps_cost():
    """卖出只减数量，成本不变。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    holdings.record_trade("600000", "buy", 12, 100)
    holdings.record_trade("600000", "sell", 15, 50)
    h = _qty("600000")
    assert h["quantity"] == 150
    assert h["avg_cost"] == pytest.approx(11.0)


def test_sell_over_quantity_rejected():
    """卖出超过持仓应报错且事务回滚（不残留半条交易）。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    with pytest.raises(ValueError):
        holdings.record_trade("600000", "sell", 15, 200)
    # 交易应未插入
    assert len(holdings.list_trades("600000")) == 1
    assert _qty("600000")["quantity"] == 100


def test_full_close_turns_closed():
    """清仓后 status 转 closed。"""
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    holdings.record_trade("600000", "sell", 15, 100)
    h = _qty("600000")
    assert h["quantity"] == 0
    assert h["status"] == "closed"


def test_delete_trade_rollback():
    """撤销首笔买入后持仓重建回退。"""
    r1 = holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    holdings.record_trade("600000", "buy", 12, 100)
    assert _qty("600000")["avg_cost"] == pytest.approx(11.0)
    holdings.delete_trade(r1["trade_id"])
    h = _qty("600000")
    assert h["quantity"] == 100
    assert h["avg_cost"] == pytest.approx(12.0)


def test_delete_trade_breaks_subsequent_sell_rejected():
    """删除某笔后导致后续卖出超量，应拒绝删除。"""
    r1 = holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    holdings.record_trade("600000", "sell", 15, 100)
    with pytest.raises(ValueError):
        holdings.delete_trade(r1["trade_id"])
    # 交易应仍存在
    assert len(holdings.list_trades("600000")) == 2


def test_new_stock_triggers_data_sync(monkeypatch):
    """开仓新股：自动同步全部数据（mock sync_stock_full 断言被调用一次）。"""
    from app.services import refresh as rmod

    called = []
    monkeypatch.setattr(rmod, "sync_stock_full", lambda code: called.append(code) or {"code": code})
    holdings.record_trade("600001", "buy", 10, 100, name="新股")
    assert called == ["600001"]


def test_existing_stock_no_resync(monkeypatch):
    """已有缓存数据的股票再交易：不再触发数据同步。"""
    holdings.record_trade("600002", "buy", 10, 100, name="旧股")  # 首笔走真同步（MockProvider）
    from app.services import refresh as rmod

    called = []
    monkeypatch.setattr(rmod, "sync_stock_full", lambda code: called.append(code) or {"code": code})
    holdings.record_trade("600002", "buy", 12, 100)
    assert called == []


def test_full_close_keeps_cache():
    """清仓只改持仓状态，原始缓存（日K/估值/分位/财务/序列）长期保留。"""
    from types import SimpleNamespace

    from app.data.base import Financials
    from app.data.cache import (
        upsert_daily_prices,
        upsert_financials,
        upsert_quantile,
        upsert_valuation,
        upsert_valuation_series,
    )
    from app.models.db import get_conn

    # 买入 + 造全套缓存
    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    bar = SimpleNamespace(date="2026-08-03", open=10.0, high=10.1, low=9.9, close=10.0, volume=100, amount=1000)
    upsert_daily_prices("600000", [bar], "mock")
    upsert_financials("600000", Financials(
        report_date="20260331", roe=20.0, roa=4.0, revenue_yoy=8.0, profit_yoy=5.0,
        net_profit=1_000_000_000, net_assets=5_000_000_000, eps=1.0,
        dv_per_share=0.5, payout_ratio=50.0, dv_report="2025年报", profit_series=[],
        total_shares=1_000_000_000,
    ))
    upsert_valuation_series("600000", "pe", "3y", [("2026-08-03", 10.0)])
    upsert_valuation_series("600000", "pb", "3y", [("2026-08-03", 1.0)])
    upsert_quantile("600000", "2026-08-03", "3y", 50.0, 50.0, 100)
    upsert_valuation("600000", "2026-08-03", pe_ttm=10.0, pb=1.0)
    with get_conn() as c:
        assert c.execute("SELECT COUNT(*) FROM daily_price_cache WHERE code='600000'").fetchone()[0] > 0

    # 清仓卖出 → 持仓 closed，但原始缓存全部保留
    holdings.record_trade("600000", "sell", 15, 100)
    h = _qty("600000")
    assert h["status"] == "closed"
    with get_conn() as c:
        for tbl in ("daily_price_cache", "daily_valuation_cache", "valuation_quantile_cache",
                    "financial_cache", "valuation_history_cache"):
            n = c.execute(f"SELECT COUNT(*) FROM {tbl} WHERE code='600000'").fetchone()[0]
            assert n > 0, f"{tbl} 应保留原始缓存"


def test_closed_stock_rebuy_no_resync(monkeypatch):
    """清仓后旧股再次买入：有交易记录 → 不视为新股 → 不重新同步（缓存保留，靠交易记录判定）。"""
    holdings.record_trade("600003", "buy", 10, 100, name="旧股")
    holdings.record_trade("600003", "sell", 15, 100)  # 清仓 → 原始缓存保留
    from app.services import refresh as rmod

    called = []
    monkeypatch.setattr(rmod, "sync_stock_full", lambda code: called.append(code) or {"code": code})
    holdings.record_trade("600003", "buy", 10, 100, trade_time="2026-08-04 10:00:02")
    assert called == []


def test_delete_trade_keeps_cache_when_closed():
    """撤销唯一买入导致清仓 → 原始缓存同样保留。"""
    from app.data.cache import upsert_valuation_series
    from app.models.db import get_conn

    holdings.record_trade("600000", "buy", 10, 100, name="浦发银行")
    upsert_valuation_series("600000", "pe", "3y", [("2026-08-03", 10.0)])
    trade = holdings.list_trades("600000")[0]

    holdings.delete_trade(trade["id"])  # 删除唯一买入 → 持仓归零 closed
    with get_conn() as c:
        n = c.execute("SELECT COUNT(*) FROM valuation_history_cache WHERE code='600000'").fetchone()[0]
        assert n > 0  # 原始缓存保留


def test_sync_financials_repairs_incomplete_cache():
    """残缺财务缓存（net_profit/net_assets/eps=None，早期同步失败产物）
    → sync_financials 自动重拉补齐，而非跳过（否则实时 PE/PB 永远算不出）。"""
    from app.data.base import Financials
    from app.data.cache import get_financials, upsert_financials
    from app.models.db import get_conn
    from app.services.refresh import sync_financials

    upsert_financials("600000", Financials(
        report_date="20260331", roe=2.32, roa=None, revenue_yoy=1.4, profit_yoy=1.5,
        net_profit=None, net_assets=None, eps=None, dv_per_share=0.42,
        payout_ratio=None, dv_report=None, profit_series=[],
    ))
    with get_conn() as c:
        assert c.execute("SELECT net_profit FROM financial_cache WHERE code='600000'").fetchone()[0] is None

    r = sync_financials("600000")
    assert r["reason"] == "ok"
    fin = get_financials("600000")
    assert fin["net_profit"] == pytest.approx(1_000_000_000)
    assert fin["net_assets"] == pytest.approx(5_000_000_000)
    assert fin["eps"] == pytest.approx(1.0)


def test_sync_financials_skips_complete_cache():
    """完整财务缓存 → 命中不重拉。"""
    from app.data.base import Financials
    from app.data.cache import upsert_financials
    from app.services.refresh import sync_financials

    upsert_financials("600000", Financials(
        report_date="20260331", roe=12.0, roa=4.0, revenue_yoy=10.0, profit_yoy=8.0,
        net_profit=1_000_000_000, net_assets=5_000_000_000, eps=1.0,
        dv_per_share=0.5, payout_ratio=50.0, dv_report="2025年报", profit_series=[],
        total_shares=1_000_000_000,
    ))
    r = sync_financials("600000")
    assert r["reason"] == "cached"


def test_parse_holdings_excel():
    """汇总持仓.xlsx 解析：单位成本优先、最新价兜底、港股也纳入。"""
    import io

    from openpyxl import Workbook

    from app.services import holdings

    wb = Workbook()
    ws = wb.active
    ws.title = "持仓数据"
    ws.append(["代码", "名称", "持有数量", "单位成本", "最新价"])
    ws.append(["600000", "浦发银行", 100, 9.8, 10.0])
    ws.append(["510300", "300ETF", 8800, 4.715, 4.651])
    ws.append(["06198", "青岛港", 5000, 6.0, 7.0])
    buf = io.BytesIO()
    wb.save(buf)
    items, skipped = holdings.parse_holdings_excel(buf.getvalue())
    assert [i["code"] for i in items] == ["600000", "510300", "06198"]
    assert items[0]["price"] == pytest.approx(9.8)
    assert skipped == []


def test_holdings_auto_tag_etf():
    """ETF/基金代码自动打 ETF 标签，并在持仓列表标记 is_etf。"""
    from app.data.base import auto_tag, is_etf_code, is_hk_code

    assert is_etf_code("510300") is True
    assert is_etf_code("600000") is False
    assert is_hk_code("06198") is True
    assert auto_tag("510300", "300ETF") == "ETF"
    assert auto_tag("06198", "青岛港") == "港股"
    assert auto_tag("600000") == "个股"

    holdings.record_trade("510300", "buy", 4.6, 100, name="300ETF")
    h = holdings.get_holdings(active_only=True)[0]
    assert h["tag"] == "ETF"
    assert h["is_etf"] is True

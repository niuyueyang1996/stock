"""API 集成测试：全部路由返回结构与错误码（离线，mock 行情）。"""
import pytest
from fastapi.testclient import TestClient

from app.main import create_app


@pytest.fixture
def client():
    # raise_server_exceptions=False：让服务端异常由全局 handler 转成 500+detail 响应（与真实 uvicorn 行为一致）
    return TestClient(create_app(), raise_server_exceptions=False)


def test_status(client):
    r = client.get("/api/status")
    assert r.status_code == 200
    body = r.json()
    assert body["ok"] is True
    assert "trade_day" in body and "source_status" in body


def test_init_and_list_holdings(client):
    r = client.post("/api/holdings", json={"items": [{"code": "600000", "name": "浦发银行", "price": 10, "quantity": 100}]})
    assert r.status_code == 200
    data = r.json()["data"]
    assert data[0]["trade_id"] > 0
    assert data[0]["daily_score"] is not None  # 自动重算当日综合评分

    r = client.get("/api/holdings")
    holdings = r.json()["data"]
    assert len(holdings) == 1
    assert holdings[0]["code"] == "600000"


def test_trade_crud_and_daily_score(client):
    r = client.post("/api/trades", json={"code": "600000", "side": "buy", "price": 10, "quantity": 100, "name": "浦发银行"})
    trade_id = r.json()["data"]["trade_id"]
    assert r.json()["data"]["daily_score"]["trades_count"] == 1

    # 卖出超量 → 400
    r = client.post("/api/trades", json={"code": "600000", "side": "sell", "price": 11, "quantity": 999})
    assert r.status_code == 400

    # 当日综合评分
    r = client.get("/api/scoring/daily")
    assert r.json()["data"]["trades_count"] == 1

    # 修改交易（数量 100→50）
    r = client.put(f"/api/trades/{trade_id}", json={"quantity": 50})
    assert r.status_code == 200
    assert r.json()["data"]["holding"]["quantity"] == pytest.approx(50)
    # 修改后综合分重算
    r = client.get("/api/scoring/daily")
    assert r.json()["data"]["trades_count"] == 1

    # 历史
    r = client.get("/api/scoring/history")
    assert len(r.json()["data"]) == 1

    # 流水
    r = client.get("/api/trades")
    trades = r.json()["data"]
    assert len(trades) == 1
    assert trades[0]["quantity"] == 50

    # 撤销
    r = client.delete(f"/api/trades/{trade_id}")
    assert r.status_code == 200
    assert client.get("/api/trades").json()["data"] == []
    # 当日无交易 → 综合分删除
    assert client.get("/api/scoring/daily").json()["data"] is None


def test_portfolio(client):
    client.post("/api/holdings", json={"items": [{"code": "600000", "name": "浦发银行", "price": 10, "quantity": 100}]})
    r = client.get("/api/portfolio")
    assert r.status_code == 200
    p = r.json()["data"]
    assert p["portfolio"]["total_value"] == pytest.approx(1000.0)  # 100股×mock价10
    assert p["portfolio"]["stocks_count"] == 1
    assert len(p["weights"]) == 1

    r = client.get("/api/portfolio/weights")
    assert len(r.json()["data"]) == 1

    r = client.get("/api/portfolio", params={"code": "600000"})
    assert r.status_code == 200
    assert r.json()["data"]["stock"]["code"] == "600000"


def test_stock_detail(client):
    r = client.get("/api/stocks/600000")
    assert r.status_code == 200
    data = r.json()["data"]
    assert data["code"] == "600000"
    assert data["quote"]["price"] == pytest.approx(10.0)
    assert "quantiles" in data and "fundflow_15m" in data


def test_expected_growth_crud(client):
    """预期年同比增速可保存并读取。"""
    r = client.put("/api/stocks/600000/expected-growth", json={"growth": -15.0})
    assert r.status_code == 200
    assert r.json()["data"]["growth"] == pytest.approx(-15.0)
    r = client.get("/api/stocks/600000/expected-growth")
    assert r.status_code == 200
    assert r.json()["data"]["growth"] == pytest.approx(-15.0)


def test_expected_revenue_growth_crud(client):
    """预期营收增速独立保存与读取。"""
    r = client.put("/api/stocks/600000/expected-revenue-growth", json={"growth": 2.5})
    assert r.status_code == 200
    assert r.json()["data"]["growth"] == pytest.approx(2.5)
    r = client.get("/api/stocks/600000/expected-revenue-growth")
    assert r.json()["data"]["growth"] == pytest.approx(2.5)


def test_stock_tag_crud(client):
    """个股标签可保存并在详情返回。"""
    r = client.put("/api/stocks/600000/tag", json={"tag": "银行"})
    assert r.status_code == 200
    assert r.json()["data"]["tag"] == "银行"
    r = client.get("/api/stocks/600000")
    assert r.status_code == 200
    assert r.json()["data"]["tag"] == "银行"


def test_import_excel_holdings(client):
    """一键导入持仓Excel：空仓时成功，非空仓时拒绝。"""
    import io

    from openpyxl import Workbook

    wb = Workbook()
    ws = wb.active
    ws.title = "持仓数据"
    ws.append(["代码", "名称", "持有数量", "单位成本", "最新价"])
    ws.append(["600000", "浦发银行", 100, 10.0, 10.0])
    buf = io.BytesIO()
    wb.save(buf)
    files = {
        "file": (
            "持仓.xlsx",
            buf.getvalue(),
            "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        )
    }
    r = client.post("/api/holdings/import-excel", files=files)
    assert r.status_code == 200
    assert len(r.json()["data"]["imported"]) == 1
    assert len(client.get("/api/holdings").json()["data"]) == 1

    r2 = client.post("/api/holdings/import-excel", files=files)
    assert r2.status_code == 400
    assert "空仓" in r2.json()["detail"]


def test_scoring_rules(client):
    r = client.get("/api/scoring/rules")
    data = r.json()["data"]
    assert abs(sum(data["buy_weights"].values()) - 1.0) < 1e-6
    assert abs(sum(data["sell_weights"].values()) - 1.0) < 1e-6

    # 权重和不为1 → 400
    r = client.put("/api/scoring/rules", json={"buy_weights": {"pe_pct": 0.5, "roe": 0.3}})
    assert r.status_code == 400

    # 合法更新
    r = client.put("/api/scoring/rules", json={"buy_weights": {"pe_pct": 0.6, "pb_pct": 0.4}})
    assert r.status_code == 200
    data = r.json()["data"]
    assert data["buy_weights"] == {"pe_pct": 0.6, "pb_pct": 0.4}


def test_unhandled_exception_returns_clear_detail(client, monkeypatch):
    """未捕获异常 → HTTP 500 + 明确 detail（前端 api.js 读 detail 展示，便于定位）。"""
    from app.services import holdings as hmod

    def boom(active_only=True):
        raise RuntimeError("测试异常：数据源连接失败")

    monkeypatch.setattr(hmod, "get_holdings", boom)
    r = client.get("/api/holdings")
    assert r.status_code == 500
    body = r.json()
    assert body["ok"] is False
    assert body["detail"].startswith("RuntimeError")
    assert "数据源连接失败" in body["detail"]


def test_stock_detail_auto_downloads_when_no_cache(client, monkeypatch):
    """GET /stocks/{code}：无缓存行情 → 自动同步全部数据后重试成功（首次查看任意 A 股即出结果）。"""
    import app.api.stocks as smod
    from app.services import quote as qmod
    from app.services import refresh as rmod

    calls = []
    monkeypatch.setattr(rmod, "sync_stock_full", lambda code: calls.append(code) or {"code": code})

    orig = qmod.get_quote
    state = {"n": 0}

    def flaky_quote(code, *a, **k):
        state["n"] += 1
        if state["n"] == 1:
            raise RuntimeError("无缓存行情")
        return orig(code, *a, **k)

    monkeypatch.setattr(qmod, "get_quote", flaky_quote)
    monkeypatch.setattr(smod, "get_quote", flaky_quote)

    r = client.get("/api/stocks/600000")
    assert r.status_code == 200
    assert calls == ["600000"]  # 自动同步被触发一次
    assert r.json()["data"]["quote"]["price"] == pytest.approx(10.0)


def test_stock_refresh_dynamic(client):
    """POST /stocks/{code}/refresh：单股动态刷新，返回 daily/valuation 结构。"""
    r = client.post("/api/stocks/600000/refresh", json={"items": ["price", "valuation"]})
    assert r.status_code == 200
    data = r.json()["data"]
    assert data["code"] == "600000"
    assert data["mode"] == "dynamic"
    assert "daily" in data and "valuation" in data and "financials" in data


def test_stock_refresh_full(client):
    """POST /stocks/{code}/refresh/full：单股全量刷新，force 覆盖。"""
    r = client.post("/api/stocks/600000/refresh/full", json={"items": ["bars", "financials", "valuation"]})
    assert r.status_code == 200
    data = r.json()["data"]
    assert data["code"] == "600000"
    assert data["mode"] == "full"
    assert "daily" in data and "valuation" in data and "financials" in data


def test_scoring_rebuild(client):
    """POST /scoring/rebuild：重建全部有交易日的综合评分，返回重建日数。"""
    client.post("/api/trades", json={"code": "600000", "side": "buy", "price": 10, "quantity": 100, "name": "浦发银行"})
    r = client.post("/api/scoring/rebuild")
    assert r.status_code == 200
    assert r.json()["data"]["rebuilt_days"] >= 1


def test_refresh_dynamic_items_filter(client):
    """POST /refresh 按 items 过滤：只刷指定内容项（价格），其余 skip。"""
    client.post("/api/holdings", json={"items": [{"code": "600000", "name": "浦发银行", "price": 10, "quantity": 100}]})
    r = client.post("/api/refresh", json={"items": ["price"]})
    assert r.status_code == 200
    data = r.json()["data"]
    assert data["mode"] == "dynamic"
    assert data["items"] == ["price"]
    assert len(data["stocks"]) == 1
    s = data["stocks"][0]
    assert "daily" in s and "valuation" in s
    assert s["valuation"]["reason"] == "skipped"  # 未勾选 valuation → 跳过


def test_data_reset_requires_confirm(client):
    """一键清空：未传 confirm 或 confirm=false → 400（防误触）。"""
    r = client.post("/api/data/reset")
    assert r.status_code == 400
    assert "confirm" in r.json()["detail"]
    r = client.post("/api/data/reset", json={"confirm": False})
    assert r.status_code == 400


def test_data_reset_clears_all(client):
    """一键清空：删除全部业务数据与缓存，保留评分权重配置（config）。"""
    client.post("/api/holdings", json={"items": [{"code": "600000", "name": "浦发银行", "price": 10, "quantity": 100}]})
    # 确认已写入业务数据
    assert len(client.get("/api/trades").json()["data"]) == 1
    assert client.get("/api/scoring/daily").json()["data"] is not None

    r = client.post("/api/data/reset", json={"confirm": True})
    assert r.status_code == 200
    assert r.json()["data"]["deleted_rows"] >= 3  # trades + holdings + stocks

    # 业务数据全空
    assert client.get("/api/trades").json()["data"] == []
    assert client.get("/api/holdings").json()["data"] == []
    assert client.get("/api/scoring/daily").json()["data"] is None

    # 评分权重配置保留
    rules = client.get("/api/scoring/rules").json()["data"]
    assert abs(sum(rules["buy_weights"].values()) - 1.0) < 1e-6

    # 清空后缓存表同步清掉（录交易+刷新产生的日K等）
    from app.models.db import get_conn

    with get_conn() as c:
        for tbl in ("daily_price_cache", "daily_valuation_cache", "financial_cache",
                    "valuation_history_cache", "portfolio_valuation_cache", "daily_scores"):
            n = c.execute(f"SELECT COUNT(*) FROM {tbl}").fetchone()[0]
            assert n == 0, f"{tbl} 应已清空"

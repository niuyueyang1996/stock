"""指数功能测试：估值转换 / 注册表冲突防护 / ETF 自动映射 / API / 指数不可交易守卫。"""
import pandas as pd
import pytest

from app.data.base import is_index_code, load_index_registry
from app.data.normalizers import normalize_index_valuation_http
from app.models.db import get_conn
from app.services import holdings
from app.services.indices import auto_map_etf_index, auto_map_holdings_etfs


# ---------- 估值转换（normalize_index_valuation_http） ----------

def test_normalize_index_valuation_http_uses_ttm():
    df = pd.DataFrame({
        "date": ["2026-08-05", "2026-08-06"],
        "ttmPe": [13.5, 13.63],
        "addLyrPe": [0.0, 0.0],
        "pb": [1.40, 1.43],
    })
    pts = normalize_index_valuation_http(df, "pe", "近一年")
    assert [p.value for p in pts] == [13.5, 13.63]
    assert [p.date for p in pts] == ["2026-08-05", "2026-08-06"]


def test_normalize_index_valuation_http_fallback_addlyrpe():
    # 恒指场景：ttmPe 全 0 仅 addLyrPe 有效 → 回退
    df = pd.DataFrame({"date": ["2026-08-06"], "ttmPe": [0.0], "addLyrPe": [9.3], "pb": [None]})
    pts = normalize_index_valuation_http(df, "pe", "近一年")
    assert [p.value for p in pts] == [9.3]
    # pb 列值全 None → []
    assert normalize_index_valuation_http(df, "pb", "近一年") == []


def test_normalize_index_valuation_http_empty_and_clip():
    # df 空 / None → []
    assert normalize_index_valuation_http(None, "pe", "近一年") == []
    assert normalize_index_valuation_http(pd.DataFrame(), "pe", "近一年") == []
    # 超出近一年（cutoff=今日-365 天）截断 + 负值剔除
    df = pd.DataFrame({
        "date": ["2024-01-01", "2025-01-01", "2026-08-06", "2026-08-05"],
        "ttmPe": [-5.0, 10.0, 12.0, 11.0],
    })
    pts = normalize_index_valuation_http(df, "pe", "近一年")
    # 2024/2025 距今 >365 截断，-5.0 剔除；保留近一年内两个正值（df 原序）
    assert [p.date for p in pts] == ["2026-08-06", "2026-08-05"]
    assert [p.value for p in pts] == [12.0, 11.0]


# ---------- 注册表与冲突防护（is_index_code） ----------

def test_is_index_code_seed_and_conflict():
    # 种子指数可识别
    assert is_index_code("000300") is True
    assert is_index_code("HSI") is True
    # 未定义 → False
    assert is_index_code("999999") is False
    # stocks 表登记 000001（平安银行）后，000001 让位给个股，不再当指数
    with get_conn() as c:
        c.execute(
            "INSERT INTO stocks (code, name, market) VALUES ('000001', '平安银行', 'sz')"
        )
    load_index_registry()
    assert is_index_code("000001") is False
    assert is_index_code("000300") is True


# ---------- ETF→指数名称自动匹配 ----------

def test_auto_map_etf_index():
    assert auto_map_etf_index("沪深300ETF华泰柏瑞") == "000300"
    assert auto_map_etf_index("中证红利ETF") == "000922"
    assert auto_map_etf_index("上证50ETF") == "000016"
    assert auto_map_etf_index("证券ETF") is None
    assert auto_map_etf_index("") is None


def test_auto_map_etf_index_longest_name_wins():
    # 多个指数名命中同一 ETF 名时取最长：智选科技龙头(6) > 智选科技(4)
    with get_conn() as c:
        c.execute(
            "INSERT INTO index_defs (code, name, symbol, pe_source, pb_source, sort_order) "
            "VALUES ('TEST01', '智选科技', 'sz000000', 'none', 'none', 99)"
        )
        c.execute(
            "INSERT INTO index_defs (code, name, symbol, pe_source, pb_source, sort_order) "
            "VALUES ('TEST02', '智选科技龙头', 'sz000000', 'none', 'none', 100)"
        )
    assert auto_map_etf_index("智选科技龙头ETF") == "TEST02"
    assert auto_map_etf_index("智选科技ETF") == "TEST01"


def test_auto_map_holdings_etfs_writes_mapping():
    with get_conn() as c:
        c.execute("INSERT INTO stocks (code, name, market) VALUES ('515080', '中证红利ETF', 'sh')")
        c.execute(
            "INSERT INTO holdings (code, quantity, avg_cost, total_buy, status) "
            "VALUES ('515080', 100, 1.0, 100.0, 'active')"
        )
    assert auto_map_holdings_etfs() == 1
    with get_conn() as c:
        row = c.execute(
            "SELECT index_code, source FROM etf_index_map WHERE etf_code='515080'"
        ).fetchone()
    assert dict(row) == {"index_code": "000922", "source": "auto"}
    # 幂等：再跑一次不再新增
    assert auto_map_holdings_etfs() == 0


# ---------- 指数不可交易守卫 ----------

def test_record_trade_index_guard():
    with pytest.raises(ValueError, match="指数不可交易"):
        holdings.record_trade("000300", "buy", 4000.0, 100)


# ---------- API ----------

def test_indices_api(client):
    r = client.get("/api/indices")
    assert r.status_code == 200
    codes = {d["code"] for d in r.json()["data"]}
    assert len(codes) >= 16
    assert "000300" in codes and "HSI" in codes
    d300 = next(d for d in r.json()["data"] if d["code"] == "000300")
    assert d300["name"] == "沪深300"
    assert d300["pe_source"] == "legu"
    # 恒指仅 PE 源，PB 无估值
    hsi = next(d for d in r.json()["data"] if d["code"] == "HSI")
    assert hsi["pb_source"] == "none"


def test_index_detail_api(client):
    r = client.get("/api/indices/000300")
    assert r.status_code == 200
    d = r.json()["data"]
    assert d["is_index"] is True
    assert d["is_etf"] is False
    assert d["financials"] is None
    assert "valuation_history" in d
    assert d["partial_missing"] == []
    # 未定义指数 → 404
    assert client.get("/api/indices/999999").status_code == 404


def test_index_series_route_not_swallowed(client):
    r = client.get("/api/indices/series?codes=000300,HSI")
    assert r.status_code == 200
    d = r.json()["data"]
    assert "periods" in d
    assert d["default"] == "3y"


def test_etf_map_api(client):
    # 种子映射 510300→000300
    r = client.get("/api/indices/etf-map/510300")
    assert r.status_code == 200
    d = r.json()["data"]
    assert d["index_code"] == "000300"
    assert d["index_name"] == "沪深300"
    assert d["source"] == "manual"
    # PUT 新增映射
    r2 = client.put("/api/indices/etf-map/510880", json={"index_code": "000300"})
    assert r2.status_code == 200
    assert r2.json()["data"]["index_code"] == "000300"
    # PUT index_code=null 清空
    r3 = client.put("/api/indices/etf-map/510880", json={"index_code": None})
    assert r3.status_code == 200
    assert r3.json()["data"] is None
    # 非法指数 → 400
    r4 = client.put("/api/indices/etf-map/510300", json={"index_code": "999999"})
    assert r4.status_code == 400


def test_etf_map_auto_api(client):
    r = client.get("/api/indices/etf-map/auto", params={"etf_name": "沪深300ETF华泰柏瑞"})
    assert r.status_code == 200
    d = r.json()["data"]
    assert d["suggest_index_code"] == "000300"
    assert d["suggest_index_name"] == "沪深300"

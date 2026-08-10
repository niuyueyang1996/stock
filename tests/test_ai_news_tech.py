"""消息面 / 技术面 AI 测试：as_of 注入、时效空态、剥离规则、技术面 bars 截断、
批量落库（单只失败不中断、tags/codes 互斥）、迁移建表、HTML 门控。全程 mock chat_json，离线。"""
from datetime import datetime

import pytest

from app.models.db import get_conn
from app.services import ai as ai_mod
from app.services import ai as ai_svc
from app.services import holdings


def _activate_mock_model():
    m = ai_mod.save_model("mock", "http://localhost", "k", "mock-model")
    return ai_mod.activate_model(m["id"])


def _fake_chat(monkeypatch, payload):
    monkeypatch.setattr(ai_mod, "chat_json", lambda cfg, system, user, **kw: payload)


def _asof_today():
    """与后端口径一致：非交易日回落到最近交易日（锚点，周末不会红）。"""
    from app.market.calendar import resolve_trade_day

    return resolve_trade_day(None)[0]


def _seed_trade(code="600000", tag=None):
    ts = datetime.now().strftime("%Y-%m-%d %H:%M:%S.%f")
    holdings.record_trade(code, "buy", 10.0, 100, name="模拟股" + code, trade_time=ts)
    if tag:
        holdings.set_tag(code, tag)


def _seed_price(code, trade_date, close, pct_change=0.0):
    with get_conn() as c:
        c.execute(
            """INSERT OR REPLACE INTO daily_price_cache
               (code, trade_date, open, high, low, close, volume, amount, pct_change, is_closed)
               VALUES(?,?,?,?,?,?,?,?,?,1)""",
            (code, trade_date, close, close, close, close, 10000, 1000000, pct_change),
        )


# ============================================================ 技术面 bars（as-of 锚点截断）

def test_build_technical_bars_empty_no_error():
    """无日K → 返回空列表不抛错（调用方按该维缺数处理）。"""
    assert ai_svc.build_technical_bars("600000", f"{_asof_today()}T10:00:00+08:00") == []


def test_build_technical_bars_truncates_to_trade_day():
    """as_of 截断：未来日期不进结果；尾部 limit 根；字段齐全。"""
    anchor = _asof_today()
    _seed_price("600000", anchor, 10.5, 1.0)
    _seed_price("600000", "2999-12-31", 99.0)      # 远超 as_of 的未来行，绝不该出现
    bars = ai_svc.build_technical_bars("600000", f"{anchor}T15:21:00+08:00")
    assert bars
    assert bars[-1]["date"] == anchor
    assert all(b["date"] <= anchor for b in bars)
    assert all(99.0 not in (b["close"],) for b in bars)
    b = bars[0]
    assert {"date", "open", "high", "low", "close", "volume", "pct_change"} <= set(b.keys())


def test_build_technical_bars_limit_tail():
    """limit 只取尾部 N 根（升序）。"""
    # 用最近 3 个真实交易日（避免 anchor-1/-2 落到周末被非交易日过滤剔掉）
    from conftest import last_n_trade_days

    ds = last_n_trade_days(3)
    d0, d1, anchor = ds[0], ds[1], ds[2]
    _seed_price("600000", d0, 9.0)
    _seed_price("600000", d1, 9.5)
    _seed_price("600000", anchor, 10.0)
    bars = ai_svc.build_technical_bars("600000", f"{anchor}T10:00:00+08:00", limit=2)
    assert len(bars) == 2
    assert [b["date"] for b in bars] == [d1, anchor]


def test_build_technical_bars_many():
    """批量版：一次查多只；无数据的 code 返回空列表。"""
    anchor = _asof_today()
    _seed_price("600000", anchor, 10.5)
    m = ai_svc.build_technical_bars_many(["600000", "600001"], f"{anchor}T10:00:00+08:00")
    assert m["600000"][-1]["date"] == anchor
    assert m["600001"] == []


# ============================================================ 规整规则

def test_normalize_news_strip_and_fallback():
    """无 event_date / 自标过时 item 被剥离；stance 非法兜底 neutral；合法 item 保留。"""
    n = ai_svc._normalize_news({"stance": "weird"})
    assert n["stance"] == "neutral"
    assert ai_svc._normalize_news({})["stance"] == "neutral"
    assert ai_svc._normalize_news({"stance": "bearish"})["stance"] == "bearish"
    n2 = ai_svc._normalize_news({
        "items": [
            {"headline": "ok", "event_date": "2026-08-01", "impact": "利多", "summary": "s"},
            {"headline": "no-date", "impact": "利多", "summary": "不应出现"},          # 无日期
            {"headline": "stale", "event_date": "2020-01-01", "stale": True},          # 自标过时
            {"headline": "expired", "event_date": "2020-01-01", "expired": True},      # 自标过期
            "not-a-dict",
        ],
        "risks": ["政策风险", "", "  "],
    })
    assert len(n2["items"]) == 1 and n2["items"][0]["headline"] == "ok"
    assert n2["items"][0]["event_date"] == "2026-08-01"
    assert n2["risks"] == ["政策风险"]
    assert n2["html"] == ""          # 缺省空串
    # 全被剥离 → items 空（合法结果）
    n3 = ai_svc._normalize_news({"items": [{"headline": "no-date"}]})
    assert n3["items"] == []


def test_normalize_technical_fallback():
    """趋势枚举兜底 range；关键位/信号数组容错。"""
    n = ai_svc._normalize_technical({"trend_short": "weird"})
    assert n["trend_short"] == "range" and n["trend_mid"] == "range"
    n2 = ai_svc._normalize_technical({
        "trend_short": "up", "trend_mid": "down",
        "key_levels": {"support": [10.0, 9.5], "resistance": [11.0]},
        "signals": ["站稳10元", ""], "invalidation": "跌破9.5", "summary": "短期上行",
    })
    assert n2["trend_short"] == "up" and n2["trend_mid"] == "down"
    assert n2["key_levels"]["support"] == ["10.0", "9.5"]
    assert n2["key_levels"]["resistance"] == ["11.0"]
    assert n2["signals"] == ["站稳10元"] and n2["invalidation"] == "跌破9.5"
    assert n2["html"] == ""


# ============================================================ analyze_news / analyze_technical

def test_analyze_news_success_and_asof_injection(monkeypatch):
    _activate_mock_model()
    captured = {}
    payload = {
        "stance": "bullish",
        "summary": "近期利好为主",
        "items": [
            {"headline": "中标大单", "event_date": "2026-08-01", "impact": "利多", "summary": "s"},
            {"headline": "无日期事件", "impact": "利多", "summary": "不应出现"},
            {"headline": "过期旧闻", "event_date": "2020-01-01", "stale": True},
        ],
        "risks": ["政策风险"],
        "omit_reason": "",
    }
    monkeypatch.setattr(ai_mod, "chat_json",
                        lambda cfg, system, user, **kw: captured.update(user=user) or payload)
    r = ai_svc.analyze_news("600000")
    assert r["code"] == "600000" and r["source"] == "single"
    assert "as_of_datetime" in captured["user"]          # as_of 注入
    assert r["as_of"] and "+" in r["as_of"]              # 带时区 ISO
    rep = r["report"]
    assert rep["stance"] == "bullish"
    assert len(rep["items"]) == 1 and rep["items"][0]["headline"] == "中标大单"
    assert rep["risks"] == ["政策风险"]
    got = ai_svc.get_stock_news_report("600000")         # 落库读回
    assert got and got["stance"] == "bullish" and len(got["items"]) == 1
    assert got["source"] == "single" and got["as_of"] == r["as_of"]


def test_analyze_news_omit_reason_success(monkeypatch):
    """时效空态：items 空 + omit_reason → 任务成功、落库、GET 可读、字段齐全（非错误）。"""
    _activate_mock_model()
    _fake_chat(monkeypatch, {
        "stance": "neutral", "summary": "", "items": [], "risks": [],
        "omit_reason": "近期无公开可确认的新信息", "html": "",
    })
    r = ai_svc.analyze_news("600000")
    assert r["report"]["items"] == []
    assert r["report"]["omit_reason"]
    got = ai_svc.get_stock_news_report("600000")
    assert got and got["items"] == [] and got["omit_reason"] == "近期无公开可确认的新信息"
    assert got["stance"] == "neutral" and got["html"] == ""


def test_analyze_news_no_model():
    with pytest.raises(ValueError, match="AI 模型"):
        ai_svc.analyze_news("600000")


def test_analyze_technical_success(monkeypatch):
    _activate_mock_model()
    anchor = _asof_today()
    _seed_price("600000", anchor, 10.5)
    captured = {}
    payload = {"trend_short": "up", "trend_mid": "range", "summary": "短期上行",
               "key_levels": {"support": ["10.0"], "resistance": ["11.0"]},
               "signals": ["放量站上10.5"], "invalidation": "跌破10"}
    monkeypatch.setattr(ai_mod, "chat_json",
                        lambda cfg, system, user, **kw: captured.update(user=user) or payload)
    r = ai_svc.analyze_technical("600000")
    assert r["report"]["trend_short"] == "up"
    assert "bars" in captured["user"] and "as_of_datetime" in captured["user"]
    assert "weekly_bars" in captured["user"] and "monthly_bars" in captured["user"]
    got = ai_svc.get_stock_tech_report("600000")
    assert got and got["key_levels"]["resistance"] == ["11.0"]
    assert got["signals"] == ["放量站上10.5"] and got["invalidation"] == "跌破10"


def test_analyze_technical_no_bars_ok(monkeypatch):
    """无日K 照常落库（AI 明说不下结论），不报错。"""
    _activate_mock_model()
    _fake_chat(monkeypatch, {"trend_short": "range", "trend_mid": "range",
                             "summary": "无日K数据，无法分析技术面",
                             "key_levels": {}, "signals": [], "invalidation": ""})
    r = ai_svc.analyze_technical("600000")
    assert r["report"]["summary"] == "无日K数据，无法分析技术面"
    assert ai_svc.get_stock_tech_report("600000") is not None


def test_analyze_technical_no_model():
    with pytest.raises(ValueError, match="AI 模型"):
        ai_svc.analyze_technical("600000")


# ============================================================ HTML 门控

def test_analyze_news_intensity_html_gating(monkeypatch):
    """消息面：深入 → system 含 HTML 要求 + schema 保留 html；快速/普通 → 无 html 字段。"""
    _activate_mock_model()
    captured = {}
    monkeypatch.setattr(ai_mod, "chat_json",
                        lambda cfg, system, user, **kw: captured.update(system=system, user=user)
                        or {"stance": "neutral", "items": []})
    ai_svc.analyze_news("600000", intensity="normal")
    assert "HTML 深度分析强制规范" not in captured["system"]
    assert '"html"' not in captured["user"]
    ai_svc.analyze_news("600000", intensity="deep")
    assert "HTML 深度分析强制规范" in captured["system"]
    assert '"html"' in captured["user"]


def test_analyze_technical_intensity_html_gating(monkeypatch):
    _activate_mock_model()
    captured = {}
    monkeypatch.setattr(ai_mod, "chat_json",
                        lambda cfg, system, user, **kw: captured.update(system=system, user=user)
                        or {"trend_short": "range", "trend_mid": "range", "key_levels": {}})
    ai_svc.analyze_technical("600000", intensity="fast")
    assert '"html"' not in captured["user"]
    ai_svc.analyze_technical("600000", intensity="deep")
    assert "HTML 深度分析强制规范" in captured["system"]
    assert '"html"' in captured["user"]


def test_news_html_persist_roundtrip(monkeypatch):
    """深入生成的 html 落库并可读回（F5 后📄按钮恢复）。"""
    _activate_mock_model()
    _fake_chat(monkeypatch, {"stance": "bullish", "summary": "s", "items": [],
                             "risks": [], "html": "<html>消息面报告</html>"})
    ai_svc.analyze_news("600000", intensity="deep")
    got = ai_svc.get_stock_news_report("600000")
    assert got and got["html"] == "<html>消息面报告</html>"


# ============================================================ 组合批量

def test_analyze_batch_news_persists(monkeypatch):
    """批量消息面：source='batch' 落库；只落库组合内 code；list map 正确。"""
    _activate_mock_model()
    _seed_trade("600000", tag="红利")
    _seed_trade("600001", tag="红利")
    _fake_chat(monkeypatch, {"stocks": [
        {"code": "600000", "stance": "bullish", "summary": "s1",
         "items": [{"headline": "h", "event_date": "2026-08-01", "impact": "利多", "summary": ""}], "risks": []},
        {"code": "600001", "stance": "neutral", "summary": "s2", "items": [],
         "risks": [], "omit_reason": "无新信息"},
        {"code": "999999", "stance": "bullish", "summary": "不在组合"},      # 不在成员 → 忽略
    ]})
    r = ai_svc.analyze_batch_news(tags=["红利"])
    assert r["count"] == 2 and len(r["reports"]) == 2
    got = ai_svc.get_stock_news_report("600000")
    assert got["source"] == "batch" and got["stance"] == "bullish"
    assert got["items"] == [{"headline": "h", "event_date": "2026-08-01", "impact": "利多", "summary": ""}]
    m = ai_svc.list_news_reports(["600000", "600001", "999999"])
    assert m["600000"]["stance"] == "bullish"
    assert m["600001"]["omit_reason"] == "无新信息"
    assert "999999" not in m


def test_analyze_batch_news_single_failure_continues(monkeypatch):
    """单只落库异常不影响其他只。"""
    _activate_mock_model()
    _seed_trade("600000", tag="红利")
    _seed_trade("600001", tag="红利")
    orig_upsert = ai_svc._upsert_news_report

    def flaky(code, as_of, source, report, model_name=""):
        if code == "600000":
            raise RuntimeError("boom")
        return orig_upsert(code, as_of, source, report, model_name)

    monkeypatch.setattr(ai_svc, "_upsert_news_report", flaky)
    _fake_chat(monkeypatch, {"stocks": [
        {"code": "600000", "stance": "bullish", "summary": "s1", "items": []},
        {"code": "600001", "stance": "bearish", "summary": "s2", "items": []},
    ]})
    r = ai_svc.analyze_batch_news(tags=["红利"])
    assert r["count"] == 1
    assert ai_svc.get_stock_news_report("600001")["stance"] == "bearish"
    assert ai_svc.get_stock_news_report("600000") is None


def test_analyze_batch_news_no_model():
    with pytest.raises(ValueError, match="AI 模型"):
        ai_svc.analyze_batch_news(tags=None)


def test_analyze_batch_news_no_holdings():
    _activate_mock_model()
    with pytest.raises(ValueError, match="暂无"):
        ai_svc.analyze_batch_news(tags=None)


def test_analyze_batch_technical_persists(monkeypatch):
    """批量技术面：逐只落库 source='batch'；含 as_of 注入。"""
    _activate_mock_model()
    _seed_trade("600000", tag="红利")
    _seed_trade("600001", tag="红利")
    _seed_price("600000", _asof_today(), 10.5)
    captured = {}
    payload = {"stocks": [
        {"code": "600000", "trend_short": "up", "trend_mid": "range", "summary": "s1",
         "key_levels": {"support": ["10"], "resistance": ["11"]}, "signals": [], "invalidation": "x"},
        {"code": "600001", "trend_short": "down", "trend_mid": "down", "summary": "s2",
         "key_levels": {}, "signals": [], "invalidation": ""},
    ]}
    monkeypatch.setattr(ai_mod, "chat_json",
                        lambda cfg, system, user, **kw: captured.update(user=user) or payload)
    r = ai_svc.analyze_batch_technical(tags=["红利"])
    assert r["count"] == 2
    assert "as_of_datetime" in captured["user"] and "bars" in captured["user"]
    assert "weekly_bars" in captured["user"] and "monthly_bars" in captured["user"]
    got = ai_svc.get_stock_tech_report("600001")
    assert got["source"] == "batch" and got["trend_short"] == "down"
    m = ai_svc.list_tech_reports(["600000", "600001"])
    assert m["600000"]["trend_short"] == "up" and m["600000"]["source"] == "batch"


# ============================================================ 批量深入：整组合 HTML（coherence 表）

def test_analyze_batch_news_deep_html_persists(monkeypatch):
    """批量消息面深入：schema 保留 html + system 含 HTML 要求；整组合 summary/html 落 coherence 表并可读。"""
    _activate_mock_model()
    _seed_trade("600000", tag="红利")
    _seed_trade("600001", tag="红利")
    captured = {}
    payload = {
        "summary": "整体消息面偏稳",
        "stocks": [
            {"code": "600000", "stance": "bullish", "summary": "s1", "items": [], "risks": []},
            {"code": "600001", "stance": "neutral", "summary": "s2", "items": [], "risks": []},
        ],
        "html": "<html>整组合消息面</html>",
    }
    monkeypatch.setattr(ai_mod, "chat_json",
                        lambda cfg, system, user, **kw: captured.update(system=system, user=user) or payload)
    r = ai_svc.analyze_batch_news(tags=["红利"], intensity="deep")
    assert "HTML 深度分析强制规范" in captured["system"]
    assert '"html"' in captured["user"]
    assert r["html"] == "<html>整组合消息面</html>" and r["summary"] == "整体消息面偏稳"
    coh = ai_svc.get_news_coherence("portfolio", "红利")
    assert coh and coh["html"] == "<html>整组合消息面</html>"
    assert coh["summary"] == "整体消息面偏稳"
    # 普通强度也会写最新 coherence（覆盖旧深入）：schema 无 html 要求，但 summary 可落库
    payload2 = {
        "summary": "普通强度最新一句话",
        "stocks": [{"code": "600000", "stance": "bullish", "summary": "s1", "items": []}],
    }
    monkeypatch.setattr(ai_mod, "chat_json",
                        lambda cfg, system, user, **kw: captured.update(system=system, user=user) or payload2)
    r_norm = ai_svc.analyze_batch_news(tags=["红利"], intensity="normal")
    assert '"html"' not in captured["user"]
    assert "HTML 深度分析强制规范" not in captured["system"]
    assert r_norm["summary"] == "普通强度最新一句话" and r_norm["html"] == ""
    coh2 = ai_svc.get_news_coherence("portfolio", "红利")
    assert coh2 and coh2["summary"] == "普通强度最新一句话"
    assert coh2["html"] == ""   # 最新普通跑覆盖深入，html 为空（普通未生成）
    assert coh2["as_of"] == r_norm["as_of"]


def test_analyze_batch_tech_deep_html_persists(monkeypatch):
    """批量技术面深入：整组合 summary/html 落 coherence 表并可读。"""
    _activate_mock_model()
    _seed_trade("600000", tag="红利")
    _seed_price("600000", _asof_today(), 10.5)
    payload = {
        "summary": "整体偏震荡",
        "stocks": [{"code": "600000", "trend_short": "range", "trend_mid": "range", "summary": "s1",
                    "key_levels": {}, "signals": [], "invalidation": ""}],
        "html": "<html>整组合技术面</html>",
    }
    _fake_chat(monkeypatch, payload)
    r = ai_svc.analyze_batch_technical(tags=["红利"], intensity="deep")
    assert r["html"] == "<html>整组合技术面</html>"
    coh = ai_svc.get_tech_coherence("portfolio", "红利")
    assert coh and coh["html"] == "<html>整组合技术面</html>"
    assert coh["summary"] == "整体偏震荡"
    # 普通强度也会写最新 coherence（覆盖旧深入）
    _fake_chat(monkeypatch, {
        "summary": "普通最新一句话",
        "stocks": [{"code": "600000", "trend_short": "up", "trend_mid": "range", "summary": "s1",
                    "key_levels": {}, "signals": [], "invalidation": ""}],
    })
    r_norm = ai_svc.analyze_batch_technical(tags=["红利"], intensity="normal")
    assert r_norm["summary"] == "普通最新一句话" and r_norm["html"] == ""
    coh2 = ai_svc.get_tech_coherence("portfolio", "红利")
    assert coh2 and coh2["summary"] == "普通最新一句话"
    assert coh2["html"] == ""
    assert coh2["as_of"] == r_norm["as_of"]


def test_coherence_key_normalization():
    """tags/codes 排序归一同 key（与资金流批量同口径），F5 精确匹配；顺序无关。"""
    a = ai_svc._coherence_key(["红利", "科技"])
    b = ai_svc._coherence_key(["科技", "红利"])
    assert a == b and a[0] == "portfolio"          # 顺序无关 → 同 key
    assert a[1] == ",".join(sorted(["红利", "科技"]))
    assert ai_svc._coherence_key(None) == ("portfolio", "全部")
    assert ai_svc._coherence_key(None, ["600000", "000300"]) == ("indices", "000300,600000")


# ============================================================ API 层

def test_api_news_analysis_no_model(client):
    r = client.post("/api/ai/news-analysis", json={"code": "600000"})
    assert r.status_code == 400
    assert "AI 模型" in r.json()["detail"]
    r2 = client.post("/api/ai/tech-analysis", json={"code": "600000"})
    assert r2.status_code == 400


def test_api_news_analysis_success(client, monkeypatch):
    from conftest import post_job

    _activate_mock_model()
    _fake_chat(monkeypatch, {"stance": "bullish", "summary": "s", "items": [], "risks": []})
    start, snap = post_job(client, "/api/ai/news-analysis", {"code": "600000"})
    assert start["async"] is True and snap["ok"] is True
    rep = client.get("/api/ai/news-report/600000")
    assert rep.status_code == 200
    assert rep.json()["data"]["stance"] == "bullish"


def test_api_tech_analysis_success(client, monkeypatch):
    from conftest import post_job

    _activate_mock_model()
    _seed_price("600000", _asof_today(), 10.5)
    _fake_chat(monkeypatch, {"trend_short": "up", "trend_mid": "range", "summary": "s",
                             "key_levels": {}, "signals": [], "invalidation": ""})
    start, snap = post_job(client, "/api/ai/tech-analysis", {"code": "600000"})
    assert start["async"] is True and snap["ok"] is True
    rep = client.get("/api/ai/tech-report/600000")
    assert rep.status_code == 200
    assert rep.json()["data"]["trend_short"] == "up"


def test_api_news_batch_success(client, monkeypatch):
    from conftest import post_job

    _activate_mock_model()
    _seed_trade("600000", tag="红利")
    _fake_chat(monkeypatch, {"stocks": [
        {"code": "600000", "stance": "bullish", "summary": "s", "items": [], "risks": []},
    ]})
    start, snap = post_job(client, "/api/ai/news-batch", {"tags": "红利"})
    assert start["async"] is True and snap["ok"] is True
    rep = client.get("/api/ai/news-reports?codes=600000,600001")
    assert rep.status_code == 200
    assert rep.json()["data"]["600000"]["stance"] == "bullish"


def test_api_tech_batch_success(client, monkeypatch):
    from conftest import post_job

    _activate_mock_model()
    _seed_trade("600000", tag="红利")
    _fake_chat(monkeypatch, {"stocks": [
        {"code": "600000", "trend_short": "up", "trend_mid": "range", "summary": "s",
         "key_levels": {}, "signals": [], "invalidation": ""},
    ]})
    start, snap = post_job(client, "/api/ai/tech-batch", {"tags": "红利"})
    assert start["async"] is True and snap["ok"] is True
    rep = client.get("/api/ai/tech-report/600000")
    assert rep.status_code == 200 and rep.json()["data"]["trend_short"] == "up"


def test_api_news_tech_batch_mutual_exclusion(client):
    """tags 与 codes 同传返回 400（对齐 fundflow-batch）。"""
    _activate_mock_model()
    r = client.post("/api/ai/news-batch", json={"tags": "红利", "codes": "000300"})
    assert r.status_code == 400
    assert "只能二选一" in r.json()["detail"]
    r2 = client.post("/api/ai/tech-batch", json={"tags": "红利", "codes": "000300"})
    assert r2.status_code == 400


def test_api_news_tech_reports_empty(client):
    r = client.get("/api/ai/news-reports?codes=600000")
    assert r.status_code == 200 and r.json()["data"] == {}
    r2 = client.get("/api/ai/tech-reports?codes=600000")
    assert r2.status_code == 200 and r2.json()["data"] == {}
    r3 = client.get("/api/ai/news-report/600000")
    assert r3.status_code == 200 and r3.json()["data"] is None


# ============================================================ 迁移建表

def test_news_tech_tables_created_on_init():
    """旧库（无新表）执行 init_db 后四张表存在（幂等补建，含整组合 coherence 表）。"""
    from app.models import db

    with db.get_conn() as c:
        c.execute("DROP TABLE ai_news_reports")
        c.execute("DROP TABLE ai_tech_reports")
        c.execute("DROP TABLE ai_news_coherence_reports")
        c.execute("DROP TABLE ai_tech_coherence_reports")
    db.init_db()
    with db.get_conn() as c:
        tables = {r["name"] for r in c.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()}
        for t in ("ai_news_reports", "ai_tech_reports",
                  "ai_news_coherence_reports", "ai_tech_coherence_reports"):
            assert t in tables


def test_api_news_tech_coherence(client, monkeypatch):
    """coherence GET 端点：无数据返回 null；深批后返回整组合 html。"""
    from urllib.parse import quote

    _activate_mock_model()
    _seed_trade("600000", tag="红利")
    _fake_chat(monkeypatch, {
        "summary": "整体平稳", "stocks": [
            {"code": "600000", "stance": "bullish", "summary": "s", "items": [], "risks": []},
        ], "html": "<html>整组合消息面</html>",
    })
    ai_svc.analyze_batch_news(tags=["红利"], intensity="deep")
    r = client.get("/api/ai/news-coherence?scope=portfolio&scope_key=" + quote("红利"))
    assert r.status_code == 200
    d = r.json()["data"]
    assert d and d["html"] == "<html>整组合消息面</html>"
    # 未分析的组合 → null
    r2 = client.get("/api/ai/news-coherence?scope=portfolio&scope_key=" + quote("科技"))
    assert r2.status_code == 200 and r2.json()["data"] is None
    r3 = client.get("/api/ai/tech-coherence?scope=portfolio")
    assert r3.status_code == 200


# ============================================================ 消息面新闻抓取（akshare → 缓存 → AI 注入）

def _fake_news_ak(monkeypatch, content="内容A" * 100):
    """打桩 raw_news.ak：stock_news_em 返回新闻 DataFrame。"""
    import pandas as pd

    rows = [
        {"发布时间": "2026-08-08 15:53:07", "新闻标题": "茅台又涨价了",
         "新闻内容": content, "文章来源": "红星资本局", "新闻链接": "http://example.com/1"},
        {"发布时间": "2026-08-07 09:00:00", "新闻标题": "公司公告分红",
         "新闻内容": "内容B", "文章来源": "公司公告", "新闻链接": "http://example.com/2"},
    ]

    class _Ak:
        def stock_news_em(self, symbol=""):
            return pd.DataFrame(rows)

    monkeypatch.setattr("app.data.raw.raw_news.ak", _Ak())
    return rows


def test_ensure_stock_news_fetch_and_cache(monkeypatch):
    """首次调用：akshare 抓取 → 入库全文；返回截断正文、按时间倒序。"""
    _fake_news_ak(monkeypatch)
    news = ai_svc._ensure_stock_news("600519")
    assert len(news) == 2
    assert news[0]["time"] == "2026-08-08 15:53:07" and news[0]["title"] == "茅台又涨价了"
    assert len(news[0]["content"]) <= ai_svc._NEWS_CONTENT_MAX          # 返回正文已截断
    from app.data.cache import get_stock_news

    cached = get_stock_news("600519", limit=10)
    assert len(cached) == 2 and len(cached[0]["content"]) == 300        # 库中存全文


def test_ensure_stock_news_ttl(monkeypatch):
    """TTL 内不重复抓取（零网络）；过期后重抓。"""
    from datetime import timedelta

    _fake_news_ak(monkeypatch)
    ai_svc._ensure_stock_news("600519")
    # 新鲜：把 ak 换成会抛错的桩，验证不再触发抓取
    monkeypatch.setattr(
        "app.data.raw.raw_news.ak",
        type("_FailAk", (), {"stock_news_em": lambda *a, **k: (_ for _ in ()).throw(RuntimeError("不应抓取"))})(),
    )
    news = ai_svc._ensure_stock_news("600519")
    assert len(news) == 2
    # 过期：fetched_at 改为 7 小时前 → 重新抓取
    _fake_news_ak(monkeypatch)
    with get_conn() as c:
        c.execute(
            "UPDATE stock_news_cache SET fetched_at=? WHERE code=?",
            ((datetime.now() - timedelta(hours=7)).isoformat(timespec="seconds"), "600519"),
        )
    news2 = ai_svc._ensure_stock_news("600519")
    assert len(news2) == 2


def test_ensure_stock_news_fetch_fail_fallback(monkeypatch):
    """抓取失败：有旧缓存 → 返回旧数据兜底；无缓存 → []。"""
    from datetime import timedelta

    from app.data.cache import upsert_stock_news

    upsert_stock_news("600000", [
        {"time": "2026-08-01 10:00:00", "title": "旧闻", "content": "旧内容",
         "source": "x", "url": "u"},
    ])
    with get_conn() as c:
        c.execute(
            "UPDATE stock_news_cache SET fetched_at=? WHERE code=?",
            ((datetime.now() - timedelta(hours=7)).isoformat(timespec="seconds"), "600000"),
        )
    monkeypatch.setattr(
        "app.data.raw.raw_news.ak",
        type("_FailAk", (), {"stock_news_em": lambda *a, **k: None})(),
    )
    news = ai_svc._ensure_stock_news("600000")
    assert len(news) == 1 and news[0]["title"] == "旧闻"                # 旧缓存兜底
    assert ai_svc._ensure_stock_news("600001") == []                    # 无缓存 → 空


def test_analyze_news_injects_news(monkeypatch):
    """消息面分析：user JSON 注入 news（含标题/时间/来源），AI 可基于新闻正文。"""
    _activate_mock_model()
    _fake_news_ak(monkeypatch)
    captured = {}
    payload = {"stance": "bullish", "summary": "近期利好", "items": [], "risks": []}
    monkeypatch.setattr(ai_mod, "chat_json",
                        lambda cfg, system, user, **kw: captured.update(user=user) or payload)
    r = ai_svc.analyze_news("600519")
    assert r["report"]["stance"] == "bullish"
    assert "news" in captured["user"] and "茅台又涨价了" in captured["user"]
    assert "2026-08-08" in captured["user"]


def test_analyze_batch_news_injects_news(monkeypatch):
    """批量消息面：每只标的 user JSON 注入 news。"""
    _activate_mock_model()
    _seed_trade("600000", tag="红利")
    _seed_trade("600001", tag="红利")
    _fake_news_ak(monkeypatch)
    captured = {}
    payload = {"summary": "整体平稳", "stocks": [
        {"code": "600000", "stance": "bullish", "summary": "s1", "items": [], "risks": []},
        {"code": "600001", "stance": "neutral", "summary": "s2", "items": [], "risks": []},
    ]}
    monkeypatch.setattr(ai_mod, "chat_json",
                        lambda cfg, system, user, **kw: captured.update(user=user) or payload)
    r = ai_svc.analyze_batch_news(tags=["红利"])
    assert r["count"] == 2
    assert "news" in captured["user"] and "茅台又涨价了" in captured["user"]

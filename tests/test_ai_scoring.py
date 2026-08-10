"""AI 评分测试：标签偏好 draft/confirmed、组合 AI 打分（画像哈希/stale/评级直传）、
每日 AI 打分（偏好去重/trade_id 对齐/自动触发），全程 mock chat_json、离线。"""
import pytest
from datetime import date, datetime

from app.models.db import get_conn
from app.services import ai as ai_mod
from app.services import ai_scoring as svc
from app.services import holdings

# conftest 会把 maybe_auto_score_daily 打桩为「只失效不后台打分」（防线程逃逸到真实库/网络）；
# 这里在导入期捕获真实函数，供验证线程行为的测试还原。
_REAL_MAYBE_AUTO = svc.maybe_auto_score_daily


def _activate_mock_model():
    """建一个激活的 mock 模型（无网络）。"""
    m = ai_mod.save_model("mock", "http://localhost", "k", "mock-model")
    return ai_mod.activate_model(m["id"])


def _fake_chat(monkeypatch, payload):
    """把 ai.chat_json 替换为返回固定 payload 的函数。"""
    monkeypatch.setattr(ai_mod, "chat_json", lambda cfg, system, user: payload)


def _seed_trade(code="600000", qty=100, price=10.0, side="buy", name="浦发银行", trade_time=None):
    """录一笔交易（无激活模型 → 不触发后台打分线程；微秒时间戳避免 UNIQUE 冲突）。返回 trade_id。"""
    ts = trade_time or datetime.now().strftime("%Y-%m-%d %H:%M:%S.%f")
    return holdings.record_trade(code, side, price, qty, name=name, trade_time=ts)["trade_id"]


# ============================================================ 标签偏好

def test_tag_pref_draft_and_confirm_and_delete():
    raw = "喜欢低估值高股息"
    row = svc.upsert_tag_pref("红利", raw)
    assert row["status"] == "draft" and row["prompt"] is None
    assert "红利" not in svc.confirmed_prefs()

    row2 = svc.upsert_tag_pref("红利", raw, prompt="完整指引：优先股息率≥4%…")
    assert row2["status"] == "confirmed"
    assert svc.confirmed_prefs()["红利"] == "完整指引：优先股息率≥4%…"

    svc.delete_tag_pref("红利")
    assert svc.get_tag_pref("红利") is None


def test_tag_pref_rejects_empty():
    with pytest.raises(ValueError):
        svc.upsert_tag_pref("红利", "   ")


def test_expand_tag_prompt_persists_draft(monkeypatch):
    _activate_mock_model()
    _fake_chat(monkeypatch, {"prompt": "AI 补全的完整评分指引…"})
    row = svc.expand_tag_prompt("红利", "喜欢高股息")
    assert row["status"] == "draft"
    assert row["prompt"] == "AI 补全的完整评分指引…"
    # 未确认不进入打分
    assert "红利" not in svc.confirmed_prefs()
    svc.confirm_tag_pref("红利")
    assert svc.confirmed_prefs()["红利"]


def test_expand_tag_prompt_no_model_raises():
    with pytest.raises(ValueError):
        svc.expand_tag_prompt("红利", "喜欢高股息")


def test_confirm_without_prompt_raises():
    svc.upsert_tag_pref("红利", "喜欢高股息")   # draft，无 prompt
    with pytest.raises(ValueError):
        svc.confirm_tag_pref("红利")


# ============================================================ 组合 AI

def test_portfolio_profile_hash_stable_and_changes():
    _seed_trade("600000", qty=100)
    h1 = svc.portfolio_profile_hash()
    assert svc.portfolio_profile_hash() == h1          # 相同状态稳定
    # 加仓 → 哈希变化
    _seed_trade("600000", qty=100)
    h2 = svc.portfolio_profile_hash()
    assert h2 != h1
    # 新增已确认偏好 → 哈希变化
    svc.upsert_tag_pref("个股", "喜欢低估值", prompt="指引")
    h3 = svc.portfolio_profile_hash()
    assert h3 != h2
    # 标签筛选视角不同 → 哈希不同
    assert svc.portfolio_profile_hash(tags=["红利"]) != svc.portfolio_profile_hash(tags=["个股"])
    # 纯读：再算一次稳定
    assert svc.portfolio_profile_hash() == h3


def test_portfolio_profile_hash_changes_on_stock_tag():
    """改个股标签后组合画像哈希变化（报告会标 stale）。"""
    _seed_trade("600000", qty=100)
    holdings.set_tag("600000", "红利")
    h1 = svc.portfolio_profile_hash()
    holdings.set_tag("600000", "科技")
    h2 = svc.portfolio_profile_hash()
    assert h2 != h1


def test_normalize_portfolio_report_clamps_and_passes_rating():
    r = svc._normalize_portfolio_report({"score": 999, "rating": "B", "summary": "s",
                                         "advice": ["a"], "risks": "不是列表", "reasons": [],
                                         "html": "<html>详细报告</html>"})
    assert r["score"] == 100.0
    assert r["grade"] == "B"
    assert r["rating"] == "B"            # 评级直传，不按分数换算
    assert r["rating_name"] == "良好"
    assert r["grade_name"] == "良好"
    assert r["action"] == "hold"
    assert r["risk"] == 50
    assert set(r["dimensions"]) == set(svc._PORTFOLIO_DIMS)
    assert r["risks"] == ["不是列表"]
    assert r["html"] == "<html>详细报告</html>"   # AI 生成的 HTML 报告原样透传
    # 非法评级兜底 C、坏分数回退 50、缺 html → 空
    r2 = svc._normalize_portfolio_report({"score": "abc", "rating": "Z"})
    assert r2["score"] == 50.0 and r2["grade"] == "C" and r2["rating"] == "C" and r2["html"] == ""


def test_normalize_portfolio_dimensions_presence():
    r = svc._normalize_portfolio_report({
        "score": 70, "grade": "B", "action": "add", "risk": 40,
        "dimensions": {
            "fundamentals": {"score": 80, "grade": "A", "analysis": "稳"},
            "structure": {"score": 60, "grade": "C", "analysis": "集中"},
        },
    })
    assert r["dimensions"]["fundamentals"]["score"] == 80
    assert r["dimensions"]["structure"]["grade"] == "C"
    assert r["dimensions"]["news"] == {}
    assert r["dimensions"]["technical"] == {}
    assert r["action"] == "add"


def test_build_portfolio_context_has_technical_news_meta():
    _seed_trade("600000", qty=100)
    ctx = svc.build_portfolio_context()
    assert "as_of_datetime" in ctx
    assert "technical" in ctx and isinstance(ctx["technical"], list)
    assert "news_meta" in ctx
    assert ctx["news_meta"]["as_of_datetime"] == ctx["as_of_datetime"]
    assert any(s["code"] == "600000" for s in ctx["news_meta"]["stocks"])
    assert "news_reports" in ctx and "tech_reports" in ctx


def test_score_portfolio_persist_and_stale(monkeypatch):
    _seed_trade("600000", qty=100)
    _activate_mock_model()
    _fake_chat(monkeypatch, {"score": 82, "rating": "B", "summary": "均衡",
                             "advice": ["继续持有"], "risks": ["集中度"], "reasons": ["估值不高"],
                             "analysis": "详细"})
    result = svc.score_portfolio()
    assert result["report"]["score"] == 82.0
    assert result["report"]["rating"] == "B"

    rep = svc.get_portfolio_report()
    assert rep is not None
    assert rep["stale"] is False
    assert rep["report"]["summary"] == "均衡"

    # 持仓变化 → 报告标记 stale
    _seed_trade("600000", qty=100)
    rep2 = svc.get_portfolio_report()
    assert rep2["stale"] is True

    svc.invalidate_portfolio()
    assert svc.get_portfolio_report() is None


def test_score_portfolio_no_model_raises():
    with pytest.raises(ValueError):
        svc.score_portfolio()


def test_portfolio_scores_stored_per_combination(monkeypatch):
    """不同标签组合各自存一份，互不覆盖；同一组合画像变化标 stale。"""
    _seed_trade("600000", qty=100)
    holdings.set_tag("600000", "红利")
    _seed_trade("600519", qty=10, name="贵州茅台")
    holdings.set_tag("600519", "科技")
    svc.upsert_tag_pref("红利", "高股息", prompt="红利指引")
    svc.upsert_tag_pref("科技", "高成长", prompt="科技指引")
    _activate_mock_model()

    def fake_chat(cfg, system, user):
        if '"selected_tags": ["红利"]' in user:
            return {"score": 70, "rating": "B", "summary": "红利组合", "advice": [], "risks": [], "reasons": []}
        return {"score": 60, "rating": "C", "summary": "全选", "advice": [], "risks": [], "reasons": []}

    monkeypatch.setattr(ai_mod, "chat_json", fake_chat)

    svc.score_portfolio(tags=["红利"])
    svc.score_portfolio(tags=None)   # 全部组合
    # 各自读回，互不覆盖
    g1 = svc.get_portfolio_report(tags=["红利"])
    g2 = svc.get_portfolio_report(tags=None)
    assert g1["report"]["score"] == 70.0 and g1["stale"] is False
    assert g2["report"]["score"] == 60.0 and g2["stale"] is False
    # 分开存储：两张行
    with get_conn() as c:
        n = c.execute("SELECT COUNT(*) FROM ai_portfolio_reports").fetchone()[0]
    assert n == 2
    # 加仓科技股 → 只影响「全部」组合；红利组合独立保留且非 stale
    _seed_trade("600519", qty=10)
    g1b = svc.get_portfolio_report(tags=["红利"])
    assert g1b["report"]["score"] == 70.0 and g1b["stale"] is False
    g_all = svc.get_portfolio_report(tags=None)
    assert g_all["report"]["score"] == 60.0 and g_all["stale"] is True


def test_score_portfolio_tags_filter(monkeypatch):
    # 两个标签：个股 + 红利（手动给代码 A 标红利）
    _seed_trade("600000", qty=100)          # 默认标签 个股
    holdings.set_tag("600000", "红利")
    _seed_trade("600519", qty=10, name="贵州茅台")
    holdings.set_tag("600519", "科技")
    svc.upsert_tag_pref("红利", "高股息", prompt="红利指引")
    svc.upsert_tag_pref("科技", "高成长", prompt="科技指引")
    _activate_mock_model()
    _fake_chat(monkeypatch, {"score": 70, "rating": "B", "summary": "s", "advice": [], "risks": [], "reasons": []})
    r = svc.score_portfolio(tags=["红利"])
    # 筛选视角只带该标签已确认偏好 → 上下文 tag_prefs 仅 红利
    assert r["report"]["score"] == 70.0


# ============================================================ 每日 AI

def test_build_daily_context_pref_dedup():
    tid1 = _seed_trade("600000", qty=100)
    tid2 = _seed_trade("600000", qty=50)
    svc.upsert_tag_pref("个股", "低估值", prompt="个股指引")
    ctx = svc.build_daily_context(date.today().isoformat())
    assert len(ctx["trades"]) == 2
    assert {t["trade_id"] for t in ctx["trades"]} == {tid1, tid2}
    # 同标签两笔 → 偏好只带 1 条
    assert len(ctx["tag_prefs"]) == 1
    assert ctx["tag_prefs"]["个股"] == "个股指引"
    # 无指引标签：注明按一般纪律
    svc.delete_tag_pref("个股")
    ctx2 = svc.build_daily_context(date.today().isoformat())
    assert "按一般投资纪律" in ctx2["tag_prefs"]["个股"]


def test_normalize_daily_report_alignment():
    trades = [
        {"trade_id": 1, "code": "600000", "name": "A", "tag": "个股", "side": "buy",
         "price": 10.0, "quantity": 100, "amount": 1000.0, "amount_cny": 1000.0,
         "fee": 0.0, "trade_time": "2026-08-06 10:00:00"},
        {"trade_id": 2, "code": "600519", "name": "B", "tag": "科技", "side": "sell",
         "price": 100.0, "quantity": 10, "amount": 1000.0, "amount_cny": 1000.0,
         "fee": 0.0, "trade_time": "2026-08-06 10:05:00"},
    ]
    # AI 只给了 trade 1 的逐笔、漏了 trade 2，且多给了一个未知 id
    r = svc._normalize_daily_report(
        {"score": 72, "grade": "B", "action": "cautious", "risk": 45, "summary": "s",
         "advice": [], "risks": [], "reasons": [],
         "html": "<html>复盘</html>",
         "trades": [{"trade_id": 1, "score": 85, "grade": "A", "action": "repeat", "comment": "好", "analysis": ""},
                    {"trade_id": 99, "score": 10, "rating": "D", "comment": "未知", "analysis": ""}]},
        trades,
    )
    # 每笔真实交易各一条、按输入顺序；未知 id 丢弃；漏掉的按默认分/评级补
    assert [t["trade_id"] for t in r["trades"]] == [1, 2]
    assert r["grade"] == "B" and r["action"] == "cautious"
    assert r["trades"][0]["score"] == 85.0 and r["trades"][0]["grade"] == "A"
    assert r["trades"][0]["rating"] == "A" and r["trades"][0]["action"] == "repeat"
    assert r["trades"][1]["score"] == 50.0 and r["trades"][1]["grade"] == "C"
    assert r["trades"][1]["action"] == "cautious"  # 缺省兜底
    assert "code" in r["trades"][0] and "amount_cny" in r["trades"][0]   # 显示字段并入
    assert r["html"] == "<html>复盘</html>"                                # HTML 报告透传
    assert set(r["dimensions"]) == set(svc._DAILY_DIMS)


def test_score_daily_persist_and_read(monkeypatch):
    # 用今天打分 → mock 已收盘（收盘守卫放行）
    monkeypatch.setattr("app.market.calendar.is_market_closed", lambda now: True)
    tid = _seed_trade("600000", qty=100)
    _activate_mock_model()
    _fake_chat(monkeypatch, {
        "score": 75, "rating": "B", "summary": "ok", "advice": [], "risks": [], "reasons": [],
        "trades": [{"trade_id": tid, "score": 80, "rating": "A", "comment": "符合偏好", "analysis": ""}],
    })
    d = date.today().isoformat()
    result = svc.score_daily(d)
    assert result is not None
    assert result["report"]["score"] == 75.0
    assert result["report"]["trades"][0]["comment"] == "符合偏好"

    rep = svc.get_daily_report(d)
    assert rep["report"]["trades"][0]["trade_id"] == tid
    # 左目录：该日 ai 摘要存在
    days = svc.list_daily_days()
    assert any(x["score_date"] == d and x["ai"]["rating"] == "B" for x in days)


def test_score_daily_no_trades_returns_none(monkeypatch):
    _activate_mock_model()
    _fake_chat(monkeypatch, {})
    # 无交易日：score_daily 返回 None 且不落库
    d = "2026-01-01"
    assert svc.score_daily(d) is None
    assert svc.get_daily_report(d) is None


def test_maybe_auto_score_daily_no_model_invalidates(monkeypatch):
    # 还原真实 maybe_auto_score_daily，验证「无激活模型 → 只失效、不起线程」
    monkeypatch.setattr(svc, "maybe_auto_score_daily", _REAL_MAYBE_AUTO)
    tid = _seed_trade("600000", qty=100)
    d = date.today().isoformat()
    # 预置一个旧报告 → 无模型时 maybe_auto_score_daily 应删掉它（宁可"未评分"不留陈旧）
    with get_conn() as c:
        c.execute("INSERT INTO ai_daily_reports(score_date, report_json, model_name) VALUES(?,?,?)",
                  (d, '{"score":10}', "old"))
    svc.maybe_auto_score_daily(d)
    assert svc.get_daily_report(d) is None


def test_maybe_auto_score_daily_with_model_spawns(monkeypatch):
    # 还原真实 maybe_auto_score_daily，验证「有激活模型且当日有交易 → 入队 AI 车道打分」
    # 今天是打分目标 → mock 已收盘，放行入队（否则盘中守卫会只失效不打）
    monkeypatch.setattr("app.market.calendar.is_market_closed", lambda now: True)
    monkeypatch.setattr(svc, "maybe_auto_score_daily", _REAL_MAYBE_AUTO)
    _seed_trade("600000", qty=100)
    d = date.today().isoformat()
    _activate_mock_model()
    calls = []

    # 用同步 start_simple 替身，保证确定性地执行入队任务（不依赖 worker 线程调度）
    from app.services import job_runners

    def _sync_start(kind, label, fn, step=None):
        fn()
        return "mock-job"

    monkeypatch.setattr(job_runners, "start_simple", _sync_start)
    monkeypatch.setattr(svc, "score_daily", lambda date_: calls.append(date_) or {"score_date": date_, "report": {}})

    svc.maybe_auto_score_daily(d)
    assert calls == [d]


# ============================================================ 收盘守卫 + catchup

def test_score_daily_intraday_today_rejected(monkeypatch):
    # 盘中（未收盘）打今天 → ValueError；历史日期不受限
    monkeypatch.setattr("app.market.calendar.is_market_closed", lambda now: False)
    _seed_trade("600000", qty=100)
    _activate_mock_model()
    with pytest.raises(ValueError):
        svc.score_daily(date.today().isoformat())


def test_score_daily_historical_allowed_intraday(monkeypatch):
    # 盘中也可打历史日期（数据已定格）
    monkeypatch.setattr("app.market.calendar.is_market_closed", lambda now: False)
    _seed_trade("600000", qty=100, trade_time="2026-01-05 10:00:00.000000")
    _activate_mock_model()
    _fake_chat(monkeypatch, {"score": 70, "rating": "B", "summary": "s", "advice": [], "risks": [], "reasons": []})
    r = svc.score_daily("2026-01-05")
    assert r is not None and r["report"]["score"] == 70.0


def test_catchup_pending_daily_spawns_after_close(monkeypatch):
    # 已收盘 + 当日有交易 + 无报告 → 入队 AI 车道补打一次
    monkeypatch.setattr("app.market.calendar.is_market_closed", lambda now: True)
    _seed_trade("600000", qty=100)
    _activate_mock_model()
    calls = []

    # 用同步 start_simple 替身（自动打分已改入队 LANE_AI，不再裸起 threading.Thread），
    # 保证确定性地执行入队任务而不污染全局 worker 线程。
    from app.services import job_runners

    def _sync_start(kind, label, fn, step=None):
        fn()
        return "mock-job"

    monkeypatch.setattr(job_runners, "start_simple", _sync_start)
    monkeypatch.setattr(svc, "score_daily", lambda d: calls.append(d) or {"score_date": d, "report": {}})
    svc.catchup_pending_daily()
    assert calls == [date.today().isoformat()]


def test_catchup_pending_daily_skips_intraday(monkeypatch):
    # 未收盘（盘中）→ 不触发补打
    monkeypatch.setattr("app.market.calendar.is_market_closed", lambda now: False)
    _seed_trade("600000", qty=100)
    _activate_mock_model()
    calls = []
    monkeypatch.setattr(svc, "score_daily", lambda d: calls.append(d))
    svc.catchup_pending_daily()
    assert calls == []


def test_catchup_pending_daily_skips_when_report_exists(monkeypatch):
    # 已收盘 + 当日已有 AI 报告 → 不重复打
    monkeypatch.setattr("app.market.calendar.is_market_closed", lambda now: True)
    _seed_trade("600000", qty=100)
    d = date.today().isoformat()
    with get_conn() as c:
        c.execute("INSERT INTO ai_daily_reports(score_date, report_json, model_name) VALUES(?,?,?)",
                  (d, '{"score":10}', "old"))
    calls = []
    monkeypatch.setattr(svc, "score_daily", lambda dd: calls.append(dd))
    svc.catchup_pending_daily()
    assert calls == []


def test_catchup_pending_daily_skips_no_trades(monkeypatch):
    # 已收盘 + 当日无交易 → 不触发
    monkeypatch.setattr("app.market.calendar.is_market_closed", lambda now: True)
    _activate_mock_model()
    calls = []
    monkeypatch.setattr(svc, "score_daily", lambda d: calls.append(d))
    svc.catchup_pending_daily()
    assert calls == []


# ============================================================ asof 因子快照

def test_stock_factors_asof_snapshot(monkeypatch):
    # as_of 非空：用当日 close 口径估值 + 资金流五档 + 30日摘要；asof 标记正确
    _seed_trade("600000", qty=100)
    d = "2026-08-06"
    monkeypatch.setattr("app.data.cache.get_daily_price",
                        lambda code, td: {"code": code, "trade_date": td, "close": 12.5, "pct_change": 1.5})
    flow_row = {"code": "600000", "trade_date": d, "netamount": 1000000.0, "main_net": 800000.0,
                "main_net_pct": 8.0, "super_large_net": 500000.0, "large_net": 300000.0,
                "medium_net": -100000.0, "small_net": 50000.0, "xs_net": 20000.0,
                "p15": 100.0, "p40": 500.0, "p75": 2000.0, "p95": 10000.0}
    monkeypatch.setattr("app.data.cache.get_fundflow_asof", lambda code, as_of: flow_row)
    monkeypatch.setattr("app.data.cache.get_daily_fundflows", lambda code, start, end: [])
    f = svc._stock_factors("600000", as_of=d)
    assert f["asof"] == d and f["asof_fallback"] is False
    assert f["pct_chg"] == 1.5
    assert f["fundflow_main_net"] == 800000.0
    assert f["fundflow_super_large_net"] == 500000.0
    assert f["fundflow_date"] == d
    assert "as_of_datetime" in f and f["as_of_datetime"].startswith(d)
    assert isinstance(f.get("bars"), list)


def test_stock_factors_asof_fallback(monkeypatch):
    # 当日无行情缓存 → 回退当前值并标 asof_fallback=true
    _seed_trade("600000", qty=100)
    d = "2026-08-06"
    monkeypatch.setattr("app.data.cache.get_daily_price", lambda code, td: None)
    f = svc._stock_factors("600000", as_of=d)
    assert f["asof"] == d and f["asof_fallback"] is True


# ============================================================ API 层

def test_api_prefs_crud(client):
    r = client.put("/api/ai-scoring/prefs/红利", json={"raw_pref": "喜欢高股息"})
    assert r.status_code == 200
    assert r.json()["data"]["status"] == "draft"
    r = client.get("/api/ai-scoring/prefs")
    data = r.json()["data"]
    assert any(p["tag"] == "红利" for p in data["prefs"])
    assert data["configured"] is False
    r = client.delete("/api/ai-scoring/prefs/红利")
    assert r.status_code == 200


def test_api_portfolio_no_model(client):
    g = client.get("/api/ai-scoring/portfolio").json()["data"]
    assert g["configured"] is False and g["report"] is None
    r = client.post("/api/ai-scoring/portfolio")
    assert r.status_code == 400
    assert "AI 模型" in r.json()["detail"]


def test_api_daily_no_model(client, monkeypatch):
    # 用今天 → mock 已收盘，让守卫放行、走到「未配置模型」分支（否则盘中先报交易时段）
    monkeypatch.setattr("app.market.calendar.is_market_closed", lambda now: True)
    _seed_trade("600000", qty=100)
    r = client.post("/api/ai-scoring/daily", json={"date": date.today().isoformat()})
    assert r.status_code == 400
    assert "AI 模型" in r.json()["detail"]


def test_api_portfolio_post_success(client, monkeypatch):
    from conftest import post_job

    _seed_trade("600000", qty=100)
    _activate_mock_model()
    _fake_chat(monkeypatch, {"score": 82, "rating": "B", "summary": "s", "advice": [], "risks": [], "reasons": []})
    start, snap = post_job(client, "/api/ai-scoring/portfolio")
    assert start["async"] is True and snap["ok"] is True
    # GET 读回（report 内含 report 子对象 + stale）
    g = client.get("/api/ai-scoring/portfolio").json()["data"]
    assert g["report"]["report"]["score"] == 82.0
    assert g["report"]["stale"] is False


def test_api_daily_post_success(client, monkeypatch):
    from conftest import post_job

    # 用今天打分 → mock 已收盘，放行收盘守卫
    monkeypatch.setattr("app.market.calendar.is_market_closed", lambda now: True)
    tid = _seed_trade("600000", qty=100)
    _activate_mock_model()
    _fake_chat(monkeypatch, {
        "score": 75, "rating": "B", "summary": "ok", "advice": [], "risks": [], "reasons": [],
        "trades": [{"trade_id": tid, "score": 80, "rating": "A", "comment": "符合偏好", "analysis": ""}],
    })
    d = date.today().isoformat()
    start, snap = post_job(client, "/api/ai-scoring/daily", {"date": d})
    assert start["async"] is True and snap["ok"] is True
    g = client.get(f"/api/ai-scoring/daily?date={d}").json()["data"]
    assert g["report"]["score"] == 75.0
    assert g["report"]["trades"][0]["trade_id"] == tid
    assert g["report"]["trades"][0]["rating"] == "A"
    # 明细表字段并入
    assert g["report"]["trades"][0]["code"] == "600000"


def test_api_daily_get_after_score(client, monkeypatch):
    from conftest import post_job

    # 用今天打分 → mock 已收盘，放行收盘守卫
    monkeypatch.setattr("app.market.calendar.is_market_closed", lambda now: True)
    tid = _seed_trade("600000", qty=100)
    _activate_mock_model()
    _fake_chat(monkeypatch, {
        "score": 75, "rating": "B", "summary": "ok", "advice": [], "risks": [], "reasons": [],
        "trades": [{"trade_id": tid, "score": 80, "rating": "A", "comment": "好", "analysis": ""}],
    })
    d = date.today().isoformat()
    post_job(client, "/api/ai-scoring/daily", {"date": d})
    g = client.get("/api/ai-scoring/daily", params={"date": d}).json()["data"]
    assert g["configured"] is True
    assert g["day"]["trades_count"] == 1
    assert g["report"]["score"] == 75.0


# ============================================================ 分析强度 → HTML 报告门控

def test_score_portfolio_intensity_html_gating(monkeypatch):
    """组合打分：HTML 深度报告要求仅「深入」追加；普通不含且 schema 去掉 html。"""
    _seed_trade("600000", qty=100)
    _activate_mock_model()
    captured = {}
    monkeypatch.setattr(ai_mod, "chat_json",
                        lambda cfg, system, user, **kw: captured.update(system=system, user=user)
                        or {"score": 82, "rating": "B", "summary": "均衡", "advice": [], "risks": [], "reasons": []})
    svc.score_portfolio(intensity="normal")
    assert "HTML 深度分析强制规范" not in captured["system"]
    assert '"html"' not in captured["user"]
    svc.score_portfolio(intensity="deep")
    assert "HTML 深度分析强制规范" in captured["system"]
    assert '"html"' in captured["user"]


def test_score_daily_intensity_html_gating(monkeypatch):
    """每日打分：HTML 深度报告要求仅「深入」追加；普通不含且 schema 去掉 html。"""
    monkeypatch.setattr("app.market.calendar.is_market_closed", lambda now: True)
    _seed_trade("600000", qty=100)
    _activate_mock_model()
    captured = {}
    monkeypatch.setattr(ai_mod, "chat_json",
                        lambda cfg, system, user, **kw: captured.update(system=system, user=user)
                        or {"score": 80, "rating": "B", "summary": "不错", "advice": [], "risks": [], "reasons": [],
                            "trades": [{"trade_id": 1, "score": 80, "rating": "B", "comment": "ok"}]})
    d = date.today().isoformat()
    svc.score_daily(d, intensity="normal")
    assert "HTML 深度分析强制规范" not in captured["system"]
    assert '"html"' not in captured["user"]
    svc.score_daily(d, intensity="deep")
    assert "HTML 深度分析强制规范" in captured["system"]
    assert '"html"' in captured["user"]

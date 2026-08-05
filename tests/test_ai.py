"""AI 诊股测试：模型 CRUD/切换、诊股落库、API 层。mock AI 接口，全程离线。"""
import pytest

from app.services import ai as ai_svc

_FAKE_AI_OUTPUT = {
    "rating": "A",
    "risk_score": 30,
    "dimensions": {
        "cyclicality": {"score": 60, "analysis": "周期一般", "risk": "medium", "data_source": "provided"},
        "moat": {"score": 80, "analysis": "品牌强", "risk": "low", "data_source": "supplemented"},
        "fundamentals": {"score": 75, "analysis": "盈利稳定", "risk": "low", "data_source": "provided"},
        "growth": {"score": 70, "analysis": "增长稳健", "risk": "low", "data_source": "provided"},
        "dividend": {"score": 65, "analysis": "分红稳定", "risk": "low", "data_source": "provided"},
        "valuation": {"score": 80, "analysis": "估值偏低", "risk": "low", "data_source": "provided"},
        "competition": {"score": 60, "analysis": "竞争一般", "risk": "medium", "data_source": "supplemented"},
    },
    "cross_analysis": {
        "cycle_trap": {"detected": False, "impact_score": 0, "explanation": ""},
        "value_trap": {"detected": True, "impact_score": -20, "explanation": "低估值但增长停滞，警惕价值陷阱"},
        "dividend_trap": {"detected": False, "impact_score": 0, "explanation": ""},
    },
    "expected_growth": {
        "net_profit": 15.0,
        "net_profit_reason": "净利增速依据（全中文）",
        "revenue": -3.0,
        "revenue_reason": "营收增速依据（全中文）",
    },
    "summary": "整体值得关注",
    "reasons": ["估值低", "增长稳健"],
    "detail": "## 报告详情\n各维度分析…",
}


def _add_active_model():
    m = ai_svc.save_model("DeepSeek", "https://api.deepseek.com/v1", "sk-test", "deepseek-chat")
    ai_svc.activate_model(m["id"])
    return m


# ---------- 模型 CRUD / 切换 ----------

def test_save_and_activate_model():
    m = ai_svc.save_model("DeepSeek", "https://api.deepseek.com/v1", "sk-test", "deepseek-chat")
    assert m["id"] > 0
    assert m["base_url"] == "https://api.deepseek.com/v1"  # 去尾 /
    a = ai_svc.activate_model(m["id"])
    assert a["is_active"] == 1
    assert ai_svc.get_active_model()["name"] == "DeepSeek"


def test_activate_only_one():
    m1 = ai_svc.save_model("A", "https://a/v1", "k", "a")
    m2 = ai_svc.save_model("B", "https://b/v1", "k", "b")
    ai_svc.activate_model(m1["id"])
    ai_svc.activate_model(m2["id"])
    assert ai_svc.get_active_model()["id"] == m2["id"]
    # 唯一激活
    actives = [m for m in ai_svc.list_models() if m["is_active"]]
    assert len(actives) == 1


def test_delete_model():
    m = _add_active_model()
    ai_svc.delete_model(m["id"])
    assert ai_svc.get_active_model() is None


def test_save_requires_fields():
    with pytest.raises(ValueError):
        ai_svc.save_model("", "https://x/v1", "k", "m")


# ---------- 诊股 ----------

def test_analyze_stock_persists_report(monkeypatch):
    _add_active_model()
    monkeypatch.setattr(ai_svc, "chat_json", lambda *a, **k: _FAKE_AI_OUTPUT)
    result = ai_svc.analyze_stock("600000")
    assert result["report"]["rating"] == "A"
    assert result["report"]["risk_score"] == 30
    assert set(result["report"]["dimensions"]) == set(ai_svc.DIMENSIONS)
    # 交叉陷阱分析落库
    assert result["report"]["cross_analysis"]["value_trap"]["detected"] is True
    assert result["report"]["cross_analysis"]["value_trap"]["impact_score"] == -20
    assert result["report"]["dimensions"]["moat"]["data_source"] == "supplemented"
    # 预期年同比增速落库
    assert result["report"]["expected_growth"]["net_profit"] == 15.0
    assert result["report"]["expected_growth"]["revenue"] == -3.0
    assert result["report"]["expected_growth"]["net_profit_reason"] == "净利增速依据（全中文）"
    # 落库后读取
    saved = ai_svc.get_report("600000")
    assert saved is not None
    assert saved["report"]["rating"] == "A"
    assert saved["model_name"] == "DeepSeek"


def test_analyze_no_active_model():
    with pytest.raises(ValueError):
        ai_svc.analyze_stock("600000")


def test_normalize_handles_bad_input():
    """AI 输出缺失/越界 → 规整到合法范围；非列表 reasons 保留为单条。"""
    bad = {"rating": "X", "risk_score": 999, "dimensions": {}, "reasons": "not-a-list"}
    n = ai_svc._normalize_report(bad)
    assert n["rating"] == "C"
    assert n["risk_score"] == 100
    assert n["reasons"] == ["not-a-list"]
    # 缺失 reasons → 空列表
    n2 = ai_svc._normalize_report({"rating": "B", "risk_score": 0})
    assert n2["reasons"] == []
    assert n2["dimensions"]["moat"]["risk"] == "medium"


def test_normalize_expected_growth():
    """预期增速规整：缺失→None；越界 clamp；非数值→None；负值合法保留。"""
    # 越界 clamp 上限、非数值 → None、reason 兜底空串
    n = ai_svc._normalize_report({"expected_growth": {"net_profit": 99999, "revenue": "abc"}})
    assert n["expected_growth"]["net_profit"] == 10000.0
    assert n["expected_growth"]["revenue"] is None
    assert n["expected_growth"]["net_profit_reason"] == ""
    # 缺失 → 全 None，结构仍在
    n2 = ai_svc._normalize_report({"rating": "B"})
    assert n2["expected_growth"]["net_profit"] is None
    assert n2["expected_growth"]["revenue"] is None
    assert n2["expected_growth"]["revenue_reason"] == ""
    # 负值合法保留
    n3 = ai_svc._normalize_report({"expected_growth": {"net_profit": -30.0}})
    assert n3["expected_growth"]["net_profit"] == -30.0
    # 非 dict（如字符串）→ 全 None，不抛异常
    n4 = ai_svc._normalize_report({"expected_growth": "oops"})
    assert n4["expected_growth"]["net_profit"] is None


# ---------- API 层 ----------

def test_ai_models_api(client):
    r = client.post("/api/ai/models", json={"name": "DS", "base_url": "https://x/v1", "api_key": "k", "model": "m"})
    assert r.status_code == 200
    mid = r.json()["data"]["id"]
    r = client.get("/api/ai/models")
    assert r.json()["data"]["models"][0]["name"] == "DS"
    r = client.post(f"/api/ai/models/{mid}/activate")
    assert r.json()["data"]["is_active"] == 1
    r = client.delete(f"/api/ai/models/{mid}")
    assert r.status_code == 200


def test_ai_report_api_no_model_400(client):
    r = client.post("/api/stocks/600000/ai-report")
    assert r.status_code == 400


def test_ai_report_api_success(client, monkeypatch):
    client.post("/api/ai/models", json={"name": "DS", "base_url": "https://x/v1", "api_key": "k", "model": "m"})
    models = client.get("/api/ai/models").json()["data"]["models"]
    client.post(f"/api/ai/models/{models[0]['id']}/activate")
    monkeypatch.setattr(ai_svc, "chat_json", lambda *a, **k: _FAKE_AI_OUTPUT)
    r = client.post("/api/stocks/600000/ai-report")
    assert r.status_code == 200
    assert r.json()["data"]["report"]["rating"] == "A"
    assert r.json()["data"]["report"]["expected_growth"]["net_profit"] == 15.0
    # 读已存报告
    r2 = client.get("/api/stocks/600000/ai-report")
    assert r2.status_code == 200
    assert r2.json()["data"]["report"]["rating"] == "A"

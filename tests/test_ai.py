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
        "fundflow": {"score": 62, "analysis": "主力当日小幅净流入", "risk": "low", "data_source": "provided"},
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


# AI 偶发把 dimensions 键名写成中文（300308 中际旭创真实复现：全部 analysis 丢失）
_CHINESE_KEY_AI_OUTPUT = {
    "rating": "B",
    "risk_score": 75,
    "dimensions": {
        "周期性": {"score": 45, "analysis": "行业处于AI算力景气高点，盈利有周期性回落风险", "risk": "high", "data_source": "provided"},
        "护城河": {"score": 85, "analysis": "全球光模块龙头，800G/1.6T技术领先", "risk": "low", "data_source": "provided"},
        "基本面": {"score": 78, "analysis": "盈利强劲增长，ROE 42%", "risk": "low", "data_source": "provided"},
        "增长": {"score": 70, "analysis": "收入高增但增速将回落", "risk": "medium", "data_source": "provided"},
        "股息": {"score": 30, "analysis": "股息率仅0.14%，股东回报弱", "risk": "high", "data_source": "provided"},
        "估值": {"score": 35, "analysis": "PE 74、PB 31 处历史高位", "risk": "high", "data_source": "provided"},
        "同业竞争": {"score": 80, "analysis": "竞争格局良好，头部集中", "risk": "low", "data_source": "provided"},
        "资金面": {"score": 55, "analysis": "当日主力净流入 1.2 亿，近5日持续流入", "risk": "medium", "data_source": "provided"},
    },
    "cross_analysis": {
        "cycle_trap": {"detected": True, "impact_score": -15, "explanation": "盈利与营收处于AI景气高点，需求透支，警惕周期回落"},
        "value_trap": {"detected": False, "impact_score": 0, "explanation": ""},
        "dividend_trap": {"detected": False, "impact_score": 0, "explanation": ""},
    },
    "expected_growth": {
        "net_profit": 40.0,
        "net_profit_reason": "净利增速依据（全中文）",
        "revenue": 35.0,
        "revenue_reason": "营收增速依据（全中文）",
    },
    "summary": "公司盈利强劲、成长突出，但估值处历史高位",
    "reasons": ["全球光模块龙头", "业绩增长迅速", "估值处历史高位", "行业竞争格局良好"],
    "detail": "## 详细报告\n各维度分析…",
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
    bad = {"rating": "X", "risk_score": 999, "dimensions": {}, "reasons": "not-a-list",
           "html": "<html>诊股报告</html>"}
    n = ai_svc._normalize_report(bad)
    assert n["rating"] == "C"
    assert n["risk_score"] == 100
    assert n["reasons"] == ["not-a-list"]
    assert n["html"] == "<html>诊股报告</html>"    # AI 生成的 HTML 报告原样透传
    # 缺失 reasons → 空列表；缺 html → 空
    n2 = ai_svc._normalize_report({"rating": "B", "risk_score": 0})
    assert n2["reasons"] == []
    assert n2["dimensions"]["moat"]["risk"] == "medium"
    assert n2["html"] == ""


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


def test_normalize_maps_chinese_dimension_keys():
    """AI 把 dimensions 键名写成中文（300308 真实复现）→ 映射回英文键，analysis 不再丢失。"""
    n = ai_svc._normalize_report(_CHINESE_KEY_AI_OUTPUT)
    dims = n["dimensions"]
    assert dims["cyclicality"]["analysis"] == "行业处于AI算力景气高点，盈利有周期性回落风险"
    assert dims["moat"]["score"] == 85
    assert dims["growth"]["risk"] == "medium"
    assert dims["valuation"]["analysis"] == "PE 74、PB 31 处历史高位"
    assert dims["competition"]["data_source"] == "provided"
    # 其他字段不受影响
    assert n["expected_growth"]["net_profit"] == 40.0
    assert n["cross_analysis"]["cycle_trap"]["detected"] is True
    assert n["rating"] == "B"


def test_analyze_stock_chinese_dimension_keys(monkeypatch):
    """analyze_stock 遇中文维度键名：落库报告各维度 analysis 完整（修复 300308 问题）。"""
    _add_active_model()
    monkeypatch.setattr(ai_svc, "chat_json", lambda *a, **k: _CHINESE_KEY_AI_OUTPUT)
    result = ai_svc.analyze_stock("300308")
    dims = result["report"]["dimensions"]
    assert all(dims[k]["analysis"] for k in ai_svc.DIMENSIONS)
    assert dims["growth"]["score"] == 70
    assert dims["cyclicality"]["risk"] == "high"
    assert dims["moat"]["analysis"].startswith("全球光模块龙头")


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
    from conftest import post_job

    client.post("/api/ai/models", json={"name": "DS", "base_url": "https://x/v1", "api_key": "k", "model": "m"})
    models = client.get("/api/ai/models").json()["data"]["models"]
    client.post(f"/api/ai/models/{models[0]['id']}/activate")
    monkeypatch.setattr(ai_svc, "chat_json", lambda *a, **k: _FAKE_AI_OUTPUT)
    start, snap = post_job(client, "/api/stocks/600000/ai-report")
    assert start["async"] is True
    assert snap["ok"] is True
    # 读已存报告
    r2 = client.get("/api/stocks/600000/ai-report")
    assert r2.status_code == 200
    assert r2.json()["data"]["report"]["rating"] == "A"
    assert r2.json()["data"]["report"]["expected_growth"]["net_profit"] == 15.0


def test_reasoning_effort_default_and_config():
    """思考级别缺省 high（用户可在 AI 配置里选到 max），可经 config 表 ai_reasoning_effort 覆盖。"""
    from app.models.db import get_conn

    assert ai_svc.get_reasoning_effort() == "high"
    with get_conn() as c:
        c.execute(
            "INSERT INTO config(key,value) VALUES('ai_reasoning_effort','max') "
            "ON CONFLICT(key) DO UPDATE SET value=excluded.value"
        )
    assert ai_svc.get_reasoning_effort() == "max"
    with get_conn() as c:
        c.execute("DELETE FROM config WHERE key='ai_reasoning_effort'")
    assert ai_svc.get_reasoning_effort() == "high"


def test_reasoning_api(client):
    """思考级别 GET/PUT：默认 high，可改 low/high/max，非法值 400。"""
    g = client.get("/api/ai/reasoning").json()["data"]
    assert g["effort"] == "high"
    r = client.put("/api/ai/reasoning", json={"effort": "max"})
    assert r.status_code == 200
    assert r.json()["data"]["effort"] == "max"
    r2 = client.put("/api/ai/reasoning", json={"effort": "ultra"})
    assert r2.status_code == 400
    r3 = client.put("/api/ai/reasoning", json={"effort": "high"})
    assert r3.json()["data"]["effort"] == "high"


def test_ai_prompts_api(client):
    """GET /ai/prompts：返回 5 个入口的可编辑重点要求块（非完整 system，不含 JSON schema）。"""
    r = client.get("/api/ai/prompts")
    assert r.status_code == 200
    d = r.json()["data"]
    assert set(d) == {"stock", "fundflow", "batch", "portfolio", "daily"}
    for k, v in d.items():
        assert isinstance(v, str) and v
        assert "请输出严格 JSON" not in v      # 不含 schema 结构，用户只看可编辑要求
    assert "共振" in d["batch"]                # 批量块保留共振/虹吸/分化措辞


def test_ai_report_custom_prompt(client, monkeypatch):
    """诊股 body.system_prompt 作为「用户附加要求」追加到默认指令后；无 body 用默认。"""
    from conftest import post_job

    client.post("/api/ai/models", json={"name": "DS", "base_url": "https://x/v1", "api_key": "k", "model": "m"})
    models = client.get("/api/ai/models").json()["data"]["models"]
    client.post(f"/api/ai/models/{models[0]['id']}/activate")
    captured = {}
    monkeypatch.setattr(ai_svc, "chat_json",
                        lambda cfg, system, user, **kw: captured.update(system=system) or _FAKE_AI_OUTPUT)
    # 带自定义要求
    post_job(client, "/api/stocks/600000/ai-report", {"system_prompt": "重点看股息"})
    assert captured["system"] == ai_svc._SYSTEM_PROMPT + "\n\n[用户附加要求]\n重点看股息"
    # 不带 body → 默认指令
    post_job(client, "/api/stocks/600000/ai-report")
    assert captured["system"] == ai_svc._SYSTEM_PROMPT


def test_analyze_stock_intensity_html_gating(monkeypatch):
    """HTML 深度报告要求仅「深入」追加：normal/fast 不含且 schema 去 html；deep 含 + 保留 html。"""
    _add_active_model()
    captured = {}
    monkeypatch.setattr(ai_svc, "chat_json",
                        lambda cfg, system, user, **kw: captured.update(system=system, user=user) or _FAKE_AI_OUTPUT)
    # normal：无 HTML 要求、无强度指令、schema 无 html
    ai_svc.analyze_stock("600000", intensity="normal")
    assert "HTML 深度分析强制规范" not in captured["system"]
    assert "[分析强度]" not in captured["system"]
    assert '"html"' not in captured["user"]
    # fast：仍无 HTML 要求，但附 [分析强度]
    ai_svc.analyze_stock("600000", intensity="fast")
    assert "HTML 深度分析强制规范" not in captured["system"]
    assert "[分析强度]" in captured["system"]
    assert '"html"' not in captured["user"]
    # deep：附 HTML 要求 + [分析强度]，schema 保留 html
    ai_svc.analyze_stock("600000", intensity="deep")
    assert "HTML 深度分析强制规范" in captured["system"]
    assert "[分析强度]" in captured["system"]
    assert '"html"' in captured["user"]

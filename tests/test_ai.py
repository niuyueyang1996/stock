"""AI 诊股测试：模型 CRUD/切换、诊股落库、API 层。mock AI 接口，全程离线。"""
import pytest

from app.services import ai as ai_svc

_FAKE_AI_OUTPUT = {
    "score": 82,
    "grade": "A",
    "action": "hold",
    "risk": 30,
    "risk_level": "low",
    "confidence": "high",
    "rating": "A",  # 兼容旧键
    "risk_score": 30,
    "dimensions": {
        "cyclicality": {"score": 60, "grade": "C", "analysis": "周期一般", "risk": "medium", "data_source": "provided"},
        "moat": {"score": 80, "grade": "A", "analysis": "品牌强", "risk": "low", "data_source": "supplemented"},
        "fundamentals": {"score": 75, "grade": "B", "analysis": "盈利稳定", "risk": "low", "data_source": "provided"},
        "growth": {"score": 70, "grade": "B", "analysis": "增长稳健", "risk": "low", "data_source": "provided"},
        "dividend": {"score": 65, "grade": "B", "analysis": "分红稳定", "risk": "low", "data_source": "provided"},
        "valuation": {"score": 80, "grade": "A", "analysis": "估值偏低", "risk": "low", "data_source": "provided"},
        "competition": {"score": 60, "grade": "C", "analysis": "竞争一般", "risk": "medium", "data_source": "supplemented"},
        "fundflow": {"score": 62, "grade": "C", "analysis": "主力当日小幅净流入", "risk": "low", "data_source": "provided"},
        "news": {"score": 58, "grade": "C", "analysis": "近期无重大消息", "risk": "medium", "data_source": "supplemented"},
        "technical": {"score": 55, "grade": "C", "analysis": "震荡整理", "risk": "medium", "data_source": "provided"},
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
    "score": 68,
    "grade": "B",
    "action": "watch",
    "risk": 75,
    "rating": "B",
    "risk_score": 75,
    "dimensions": {
        "周期性": {"score": 45, "grade": "C", "analysis": "行业处于AI算力景气高点，盈利有周期性回落风险", "risk": "high", "data_source": "provided"},
        "护城河": {"score": 85, "grade": "A", "analysis": "全球光模块龙头，800G/1.6T技术领先", "risk": "low", "data_source": "provided"},
        "基本面": {"score": 78, "grade": "B", "analysis": "盈利强劲增长，ROE 42%", "risk": "low", "data_source": "provided"},
        "增长": {"score": 70, "grade": "B", "analysis": "收入高增但增速将回落", "risk": "medium", "data_source": "provided"},
        "股息": {"score": 30, "grade": "D", "analysis": "股息率仅0.14%，股东回报弱", "risk": "high", "data_source": "provided"},
        "估值": {"score": 35, "grade": "D", "analysis": "PE 74、PB 31 处历史高位", "risk": "high", "data_source": "provided"},
        "同业竞争": {"score": 80, "grade": "A", "analysis": "竞争格局良好，头部集中", "risk": "low", "data_source": "provided"},
        "资金面": {"score": 55, "grade": "C", "analysis": "当日主力净流入 1.2 亿，近5日持续流入", "risk": "medium", "data_source": "provided"},
        "消息面": {"score": 50, "grade": "C", "analysis": "无重大新消息", "risk": "medium", "data_source": "supplemented"},
        "技术面": {"score": 48, "grade": "C", "analysis": "高位震荡", "risk": "medium", "data_source": "provided"},
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


# ---------- URL / chat_json 降级 ----------

def test_openai_compat_url_with_and_without_v1():
    """DeepSeek 一键模板无 /v1，也应拼成 .../v1/chat/completions；已带 /v1 不重复。"""
    assert ai_svc._openai_compat_url("https://api.deepseek.com", "chat/completions") == (
        "https://api.deepseek.com/v1/chat/completions"
    )
    assert ai_svc._openai_compat_url("https://api.deepseek.com/v1", "chat/completions") == (
        "https://api.deepseek.com/v1/chat/completions"
    )
    assert ai_svc._openai_compat_url("https://api.deepseek.com/v1/", "models") == (
        "https://api.deepseek.com/v1/models"
    )


def test_parse_json_repairs_missing_array_close():
    """模型漏写 reasons 的 ]：本地括号修补应直接吃下，无需再打 API。"""
    # 复现 2026-08-08 组合打分失败形态
    bad = (
        '{"score": 68, "rating": "B", "summary": "ok", "advice": ["a"], '
        '"risks": ["r"], "reasons": ["组合估值优势", "资金面偏空", '
        '"组合当前浮盈5.16%，限制了进一步加分。"}'
    )
    out = ai_svc._parse_json_content(bad)
    assert out["score"] == 68
    assert out["rating"] == "B"
    assert len(out["reasons"]) == 3
    assert "浮盈" in out["reasons"][-1]


def test_parse_json_repairs_trailing_comma_and_fence():
    """围栏 + 尾逗号也能解析。"""
    raw = '```json\n{"score": 1, "reasons": ["x",],}\n```'
    assert ai_svc._parse_json_content(raw) == {"score": 1, "reasons": ["x"]}


def test_parse_json_repairs_truncated_object():
    """尾部截断少闭合括号。"""
    out = ai_svc._parse_json_content('{"score": 2, "advice": ["买"]')
    assert out["score"] == 2
    assert out["advice"] == ["买"]


def test_parse_json_escapes_raw_control_chars():
    """字符串字段里的原文换行/制表符/控制字符 → 自动转义解析（真实 DeepSeek 常见坏形态）。"""
    out = ai_svc._parse_json_content('{"summary": "第一行\n第二行", "correlation": "positive"}')
    assert out["summary"] == "第一行\n第二行"          # 原文换行被还原为字符串值里的换行
    assert out["correlation"] == "positive"
    out2 = ai_svc._parse_json_content('{"summary": "a\tb"}')
    assert out2["summary"] == "a\tb"
    # 通用控制字符（如 \x0b）也转义
    out3 = ai_svc._parse_json_content('{"summary": "a\x0bb"}')
    assert out3["summary"] == "a\x0bb"
    # 已有合法转义 \\n 不被双重转义
    out4 = ai_svc._parse_json_content('{"summary": "a\\nb"}')
    assert out4["summary"] == "a\nb"
    # 结构空白（token 间换行）不受影响
    out5 = ai_svc._parse_json_content('{\n"summary": "s"\n}')
    assert out5["summary"] == "s"


def test_chat_json_retries_on_empty_content(monkeypatch):
    """DeepSeek JSON 模式偶发空 content：首次空 → 去掉 json_object 重试成功。"""
    calls = []

    class _Resp:
        def __init__(self, payload, status=200):
            self.status_code = status
            self._payload = payload
            self.text = ""

        def json(self):
            return self._payload

    def fake_post(url, headers=None, json=None, timeout=None):
        calls.append(dict(json or {}))
        if len(calls) == 1:
            assert calls[0].get("response_format") == {"type": "json_object"}
            return _Resp({
                "choices": [{"message": {"content": ""}, "finish_reason": "stop"}],
            })
        assert "response_format" not in calls[1]
        assert "reasoning_effort" not in calls[1]
        return _Resp({
            "choices": [{"message": {"content": '{"ok": true}'}, "finish_reason": "stop"}],
        })

    monkeypatch.setattr("requests.post", fake_post)
    monkeypatch.setattr(ai_svc, "get_reasoning_effort", lambda: "high")
    out = ai_svc.chat_json(
        {"base_url": "https://api.deepseek.com", "api_key": "sk", "model": "deepseek-v4-flash"},
        "sys", "user",
    )
    assert out == {"ok": True}
    assert len(calls) == 2


def test_extract_falls_back_to_reasoning_content():
    """思考模型 content 空、reasoning_content 有值 → 取 reasoning_content。"""
    text, finish = ai_svc._extract_message_content({
        "choices": [{
            "message": {"content": "", "reasoning_content": '{"ok": true}'},
            "finish_reason": "stop",
        }],
    })
    assert text == '{"ok": true}'
    assert finish == "stop"


def test_chat_json_uses_reasoning_content_without_retry(monkeypatch):
    """content 空但 reasoning_content 有 JSON → 一次成功，不触发空内容降级重试。"""
    calls = []

    class _Resp:
        status_code = 200
        text = ""

        def json(self):
            return {
                "choices": [{
                    "message": {"content": None, "reasoning_content": '{"score": 1}'},
                    "finish_reason": "stop",
                }],
            }

    def fake_post(url, headers=None, json=None, timeout=None):
        calls.append(1)
        return _Resp()

    monkeypatch.setattr("requests.post", fake_post)
    monkeypatch.setattr(ai_svc, "get_reasoning_effort", lambda: "high")
    out = ai_svc.chat_json(
        {"base_url": "https://api.deepseek.com", "api_key": "sk", "model": "deepseek-v4-flash"},
        "sys", "user",
    )
    assert out == {"score": 1}
    assert len(calls) == 1


def test_chat_json_http_error_includes_body(monkeypatch):
    """鉴权/余额失败应带 HTTP 状态与 body，不应误报缓存过少。"""

    class _Resp:
        status_code = 401
        text = '{"error":{"message":"Invalid API key"}}'

        def json(self):
            raise AssertionError("4xx 不应再解析 JSON")

    monkeypatch.setattr("requests.post", lambda *a, **k: _Resp())
    monkeypatch.setattr(ai_svc, "get_reasoning_effort", lambda: "")
    with pytest.raises(ValueError) as ei:
        ai_svc.chat_json(
            {"base_url": "https://api.deepseek.com", "api_key": "bad", "model": "m"},
            "s", "u",
        )
    msg = str(ei.value)
    assert "401" in msg
    assert "Invalid API key" in msg
    assert "缓存" not in msg


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
    assert result["report"]["grade"] == "A"
    assert result["report"]["rating"] == "A"  # 兼容别名
    assert result["report"]["score"] == 82.0
    assert result["report"]["action"] == "hold"
    assert result["report"]["risk"] == 30
    assert result["report"]["risk_score"] == 30
    assert set(result["report"]["dimensions"]) == set(ai_svc.DIMENSIONS)
    assert len(ai_svc.DIMENSIONS) == 10
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
    assert saved["report"]["grade"] == "A"
    assert saved["report"]["rating"] == "A"
    assert saved["stale"] is False
    assert saved["model_name"] == "DeepSeek"


def test_analyze_stock_reports_progress(monkeypatch):
    """诊股分步进度：汇总→调用模型→解析→保存，逐阶段 advance 上报（修复进度条不动）。"""
    class _FakeProg:
        def __init__(self):
            self.calls = []

        def advance(self, done, total, label):
            self.calls.append((done, total, label))

    _add_active_model()
    monkeypatch.setattr(ai_svc, "chat_json", lambda *a, **k: _FAKE_AI_OUTPUT)
    prog = _FakeProg()
    ai_svc.analyze_stock("600000", prog=prog)
    assert [c[0] for c in prog.calls] == [1, 2, 3, 4]
    assert all(c[1] == 4 for c in prog.calls)
    assert [c[2] for c in prog.calls] == \
        ["汇总个股数据", "调用 AI 模型分析", "解析分析结果", "保存报告"]
    # prog=None（后端兼容调用）时静默跳过，不报错
    ai_svc.analyze_stock("600000")


def test_analyze_no_active_model():
    with pytest.raises(ValueError):
        ai_svc.analyze_stock("600000")


def test_normalize_handles_bad_input():
    """AI 输出缺失/越界 → 规整到合法范围；非列表 reasons 保留为单条。"""
    bad = {"rating": "X", "risk_score": 999, "dimensions": {}, "reasons": "not-a-list",
           "html": "<html>诊股报告</html>"}
    n = ai_svc._normalize_report(bad)
    assert n["grade"] == "C"
    assert n["rating"] == "C"
    assert n["risk"] == 100
    assert n["risk_score"] == 100
    assert n["action"] == "hold"
    assert n["reasons"] == ["not-a-list"]
    assert n["html"] == "<html>诊股报告</html>"    # AI 生成的 HTML 报告原样透传
    # 缺失 reasons → 空列表；缺 html → 空
    n2 = ai_svc._normalize_report({"rating": "B", "risk_score": 0})
    assert n2["reasons"] == []
    assert n2["dimensions"]["moat"]["risk"] == "medium"
    assert n2["dimensions"]["news"]["grade"] == "C"
    assert n2["html"] == ""


def test_normalize_grade_action_fallback():
    """grade/action 非法兜底；legacy rating/risk_score 可读。"""
    n = ai_svc._normalize_report({"grade": "Z", "action": "yolo", "score": 85, "risk": 20})
    assert n["grade"] == "C" and n["action"] == "hold"
    n2 = ai_svc._normalize_report({"rating": "A", "risk_score": 40, "action": "watch", "score": 88})
    assert n2["grade"] == "A" and n2["action"] == "watch" and n2["risk"] == 40


def test_upgrade_legacy_card():
    """老报告 rating/risk_score → grade/risk；补 action/confidence。"""
    legacy = {"rating": "B", "risk_score": 55, "summary": "旧"}
    u = ai_svc.upgrade_legacy_card(legacy)
    assert u["grade"] == "B" and u["grade_name"] == "良好"
    assert u["rating"] == "B"
    assert u["risk"] == 55 and u["risk_score"] == 55
    assert u["action"] == "hold" and u["confidence"] == "medium"
    assert u["risk_level"] == "medium"


def test_dimensions_length_ten():
    assert len(ai_svc.DIMENSIONS) == 10
    assert "news" in ai_svc.DIMENSIONS and "technical" in ai_svc.DIMENSIONS
    assert ai_svc.DIMENSION_CN["news"] == "消息面"
    assert ai_svc.DIMENSION_CN["technical"] == "技术面"


def test_build_stock_context_has_asof_and_bars():
    ctx = ai_svc.build_stock_context("600000")
    assert "as_of_datetime" in ctx
    assert isinstance(ctx.get("bars"), list)


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
    assert n["grade"] == "B"
    assert n["rating"] == "B"
    assert n["action"] == "watch"
    assert n["dimensions"]["news"]["analysis"] == "无重大新消息"
    assert n["dimensions"]["technical"]["score"] == 48


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


def test_deepseek_one_click_uses_first_available_model(client, monkeypatch):
    """一键启用 DeepSeek：模型取 available 列表第一个，不写死 deepseek-chat。"""
    monkeypatch.setattr(
        ai_svc, "list_available_models",
        lambda base_url, api_key: ["deepseek-v4-flash", "deepseek-v4-pro"],
    )
    r = client.post("/api/ai/models/available", json={
        "base_url": "https://api.deepseek.com", "api_key": "sk-test",
    })
    assert r.status_code == 200
    models = r.json()["data"]["models"]
    assert models[0] == "deepseek-v4-flash"
    r = client.post("/api/ai/models", json={
        "name": "DeepSeek",
        "base_url": "https://api.deepseek.com",
        "api_key": "sk-test",
        "model": models[0],
    })
    assert r.status_code == 200
    assert r.json()["data"]["model"] == "deepseek-v4-flash"
    mid = r.json()["data"]["id"]
    assert client.post(f"/api/ai/models/{mid}/activate").status_code == 200


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
    """GET /ai/prompts：defaults=9 个入口的可编辑重点要求块（非完整 system，不含 JSON schema）+ saved=用户自定义覆盖。"""
    r = client.get("/api/ai/prompts")
    assert r.status_code == 200
    d = r.json()["data"]
    assert set(d) == {"defaults", "saved"}
    assert d["saved"] == {}            # 尚未保存自定义覆盖
    defs = d["defaults"]
    assert set(defs) == {"stock", "fundflow", "batch", "portfolio", "daily",
                         "news", "technical", "news_batch", "tech_batch"}
    for k, v in defs.items():
        assert isinstance(v, str) and v
        assert "请输出严格 JSON" not in v      # 不含 schema 结构，用户只看可编辑要求
    assert "共振" in defs["batch"]             # 批量块保留共振/虹吸/分化措辞
    assert "消息面" in defs["news"] and "证伪条件" in defs["technical"]
    assert "十个维度" in defs["stock"] and "消息面" in defs["stock"] and "技术面" in defs["stock"]
    assert "结构集中度" in defs["portfolio"] and "标签契合" in defs["portfolio"]
    assert "可复制" in defs["daily"]


def test_ai_prompts_save_override(client):
    """PUT /ai/prompts：保存/清除用户自定义提示词覆盖；GET 后 saved 反映覆盖，defaults 不变。"""
    r = client.put("/api/ai/prompts", json={"overrides": {"portfolio": "重点看红利板块均衡度"}})
    assert r.status_code == 200
    assert r.json()["data"]["saved"] == {"portfolio": "重点看红利板块均衡度"}
    d = client.get("/api/ai/prompts").json()["data"]
    assert d["saved"]["portfolio"] == "重点看红利板块均衡度"
    assert "红利" not in d["defaults"]["portfolio"]     # 默认不被覆盖
    # 空串/None → 清除该项恢复默认
    for clear in ("", None):
        client.put("/api/ai/prompts", json={"overrides": {"portfolio": clear}})
        d = client.get("/api/ai/prompts").json()["data"]
        assert d["saved"] == {}
    # 非法 kind 也保存（后端只当键值存；弹窗只传合法 kind）
    client.put("/api/ai/prompts", json={"overrides": {"daily": "  ", "news": "只看公告"}})
    d = client.get("/api/ai/prompts").json()["data"]
    assert d["saved"] == {"news": "只看公告"}           # 空白串被剥离


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

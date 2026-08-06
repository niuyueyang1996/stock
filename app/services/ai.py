"""AI 诊股服务：OpenAI 兼容大模型接入、多模型配置、个股结构化诊股报告。

- 模型配置存 ai_models 表，切换时 is_active 唯一。
- chat_json 调 OpenAI 兼容 /v1/chat/completions，优先 response_format json_object，
  失败降级为普通文本后自行解析 JSON。
- build_stock_context 复用缓存汇总个股数据（读缓存零网络）作为结构化输入。
- analyze_stock 组装 prompt → 调 AI → 规整输出 → 存 ai_reports。
"""
import json
import logging
from datetime import datetime

from app.config import HTTP_HEADERS, REQUEST_TIMEOUT
from app.models.db import get_conn

logger = logging.getLogger("ai")

# 诊股 7 维度（固定，用于 prompt 与输出校验）
DIMENSIONS = ["cyclicality", "moat", "fundamentals", "growth", "dividend", "valuation", "competition"]
DIMENSION_CN = {
    "cyclicality": "周期性", "moat": "护城河", "fundamentals": "基本面",
    "growth": "增长", "dividend": "股息", "valuation": "估值", "competition": "同业竞争",
}
# dimensions 键名别名：AI 偶发把维度 JSON 键名写成中文（prompt 要求正文用中文维度名），映射回英文键
DIM_KEY_ALIASES = {k: (k, cn) for k, cn in DIMENSION_CN.items()}

_SYSTEM_PROMPT = (
    "你是资深股票分析师。根据给定的个股结构化数据，从周期性、护城河、基本面、增长、股息、估值、同业竞争 "
    "7 个维度分析该股，给出买入评级（A=强烈推荐/B=推荐/C=中性/D=回避）、买入风险系数（0-100，越高风险越大）与原因。\n\n"
    "数据使用规则：\n"
    "1. 优先采用系统提供的结构化数据（来自实时行情/最新财报/估值分位，具有时效性）。\n"
    "2. 若某维度数据缺失或不足（如同业竞争对比、行业景气度），可用你的领域知识补充，但必须：\n"
    "   a) 在该维度 analysis 中明确标注「[AI补充]」；\n"
    "   b) 注明补充数据的时效（如「截至2026年」），确保有时效性、不用过时数据；\n"
    "   c) 无法确认时效时明确说明。\n"
    "3. 系统提供数据缺失时，在该维度 analysis 中注明「（系统数据缺失，以下为补充）」。\n"
    "4. data_source 字段：provided=基于系统数据；supplemented=AI 补充/推断。\n"
    "5. 输出语言与键名：所有文字字段（维度 analysis、cross_analysis.explanation、summary、reasons、html、"
    "预期增速依据）一律使用简体中文，禁止混入英文单词；若在正文中提及维度，用中文名"
    "（周期性、护城河、基本面、增长、股息、估值、同业竞争），不要写 growth/fundamentals 等英文。\n"
    "   JSON 键名必须保持英文、与下方输出结构完全一致，不得改成中文；尤其 dimensions 的 7 个键固定为 "
    "cyclicality/moat/fundamentals/growth/dividend/valuation/competition，禁止改名。\n"
    "6. expected_growth 字段：基于系统财务数据（最新财报同比、TTM 同比、ROE、支付率、前瞻指标）与陷阱判断，"
    "给出你对该股未来一年净利与营收年同比增速的预判（%，可为负），并用 net_profit_reason / revenue_reason 简述依据。"
    "若识别出周期陷阱，勿把历史高点同比直接外推到未来，增速应回归行业中枢。\n\n"
    "交叉分析与陷阱识别（关键，不要各维度孤立看）：\n"
    "1. 各维度存在相关性，必须交叉验证，警惕以下典型陷阱，识别后对相关维度评分与总风险显著调整：\n"
    "   a) 周期陷阱：增长/基本面表面优秀，但可能是行业周期高点（强周期股盈利顶点、需求透支）。"
    "若判断为周期陷阱，增长/基本面维度评分应显著调低，风险系数上调。\n"
    "   b) 价值陷阱：低估值看似便宜，但基本面持续恶化、盈利下滑、行业衰退或技术颠覆。"
    "若判断为价值陷阱，估值维度评分应下调，风险系数上调。\n"
    "   c) 股息陷阱：高股息率可能因股价大幅下跌或盈利下滑（分红不可持续）而虚高。"
    "需结合派息率、盈利趋势、现金流判断；若分红不可持续，股息维度评分应下调。\n"
    "2. cross_analysis 输出每个陷阱是否识别、对得分的调整量（impact_score 负数=下调）、以及识别依据（结合哪些维度数据）。\n"
    "3. 最终 rating / risk_score / summary 必须综合陷阱判断，不可仅按孤立维度加权。\n"
    "额外要求：生成一份完整、独立、可读性强的 HTML 诊股报告（字段 html），供用户新开页面查看。"
    "已提供的行情/财务/估值等原始数据用户在本应用已可查看，HTML 中**不要原样罗列数据表格**；"
    "把篇幅全部用于**深入分析**：商业模式与护城河、竞争格局、盈利质量、增长驱动、估值判断、"
    "主要风险与陷阱、买卖逻辑与操作建议，多给有洞察力的判断与推演而非数据复述。"
    "要求：自包含单文件、内联 CSS、不引用任何外部资源、不得包含 <script> 或任何可执行代码；"
    "简体中文；结构清晰、排版美观、层次分明。\n"
    "严格输出 JSON，不要输出任何额外文字。"
)

_OUTPUT_SCHEMA = {
    "rating": "A|B|C|D",
    "risk_score": "0-100 整数，越高风险越大",
    "dimensions": {
        k: {
            "score": "0-100 该维度健康度/吸引力评分",
            "analysis": "该维度分析（若为补充数据需标注[AI补充]及时效，如「截至2026年」）",
            "risk": "low|medium|high",
            "data_source": "provided（系统数据）|supplemented（AI补充，analysis 内需标注[AI补充]）",
        }
        for k in DIMENSIONS
    },
    "cross_analysis": {
        "cycle_trap": {"detected": "是否周期陷阱（true/false）", "impact_score": "对得分调整（负数=下调，如-20）", "explanation": "识别依据（结合哪些维度数据）"},
        "value_trap": {"detected": "是否价值陷阱（true/false）", "impact_score": "对得分调整（负数=下调）", "explanation": "识别依据"},
        "dividend_trap": {"detected": "是否股息陷阱（true/false）", "impact_score": "对得分调整（负数=下调）", "explanation": "识别依据"}
    },
    "expected_growth": {
        "net_profit": "预期净利年同比增速(%)，基于基本面综合判断，可为负",
        "net_profit_reason": "净利增速依据（全中文，结合财报/TTM/ROE/支付率/陷阱判断）",
        "revenue": "预期营收年同比增速(%)",
        "revenue_reason": "营收增速依据（全中文）",
    },
    "summary": "一句话总体结论（需综合陷阱判断）",
    "reasons": ["买入/回避的主要理由数组"],
    "html": "完整独立 HTML 诊股报告源代码（自包含、内联 CSS、无外部依赖、无脚本、简体中文，聚焦深入分析，不罗列原始数据）",
}


# ---------- 模型配置 ----------

def list_models() -> list[dict]:
    with get_conn() as c:
        rows = c.execute("SELECT * FROM ai_models ORDER BY id").fetchall()
    return [dict(r) for r in rows]


def get_active_model() -> dict | None:
    with get_conn() as c:
        row = c.execute("SELECT * FROM ai_models WHERE is_active=1 LIMIT 1").fetchone()
    return dict(row) if row else None


def save_model(name: str, base_url: str, api_key: str, model: str, model_id: int | None = None) -> dict:
    """新增或更新模型。base_url 去除尾部 /。返回保存后的模型行。"""
    name = (name or "").strip()
    base_url = (base_url or "").strip().rstrip("/")
    api_key = (api_key or "").strip()
    model = (model or "").strip()
    if not all([name, base_url, api_key, model]):
        raise ValueError("名称/base_url/api_key/model 均必填")
    now = datetime.now().isoformat(timespec="seconds")
    with get_conn() as c:
        if model_id:
            c.execute(
                "UPDATE ai_models SET name=?, base_url=?, api_key=?, model=?, updated_at=? WHERE id=?",
                (name, base_url, api_key, model, now, model_id),
            )
            row = c.execute("SELECT * FROM ai_models WHERE id=?", (model_id,)).fetchone()
        else:
            cur = c.execute(
                """INSERT INTO ai_models(name, base_url, api_key, model, is_active, created_at, updated_at)
                   VALUES(?,?,?,?,0,?,?)""",
                (name, base_url, api_key, model, now, now),
            )
            row = c.execute("SELECT * FROM ai_models WHERE id=?", (cur.lastrowid,)).fetchone()
    if not row:
        raise ValueError("模型不存在")
    return dict(row)


def delete_model(model_id: int) -> None:
    with get_conn() as c:
        c.execute("DELETE FROM ai_models WHERE id=?", (model_id,))


def activate_model(model_id: int) -> dict:
    """切换当前模型（清空其他 is_active）。"""
    with get_conn() as c:
        c.execute("UPDATE ai_models SET is_active=0")
        cur = c.execute("UPDATE ai_models SET is_active=1 WHERE id=?", (model_id,))
        if cur.rowcount == 0:
            raise ValueError("模型不存在")
        row = c.execute("SELECT * FROM ai_models WHERE id=?", (model_id,)).fetchone()
    return dict(row)


# ---------- 可用模型列表（从提供商获取） ----------

def list_available_models(base_url: str, api_key: str) -> list[str]:
    """调 {base_url}/models 列出该提供商可用模型名。失败抛 ValueError。"""
    import requests

    url = base_url.rstrip("/") + "/models"
    headers = {**HTTP_HEADERS, "Authorization": f"Bearer {api_key.strip()}"}
    try:
        resp = requests.get(url, headers=headers, timeout=REQUEST_TIMEOUT)
        resp.raise_for_status()
        data = resp.json()
        return [str(m.get("id")) for m in data.get("data", []) if m.get("id")]
    except Exception as e:  # noqa: BLE001
        raise ValueError(f"获取模型列表失败（请检查 Base URL 与 API Key）: {e}")


# ---------- OpenAI 兼容调用 ----------

# AI 调用超时（诊股生成长报告较慢，放宽）
AI_REQUEST_TIMEOUT = 180


def get_reasoning_effort() -> str:
    """当前 AI 思考级别（config 表 ai_reasoning_effort，缺省 high=最高）。"""
    try:
        from app.config import AI_REASONING_EFFORT

        with get_conn() as c:
            row = c.execute("SELECT value FROM config WHERE key='ai_reasoning_effort'").fetchone()
        if row and str(row["value"] or "").strip():
            return str(row["value"]).strip()
        return AI_REASONING_EFFORT
    except Exception:  # noqa: BLE001 读配置失败不影响调用
        return ""


def chat_json(model_cfg: dict, system: str, user: str) -> dict:
    """调 OpenAI 兼容接口并解析 JSON 输出。失败抛 ValueError。输入输出均打印日志。

    - 附带 reasoning_effort（思考级别，默认 high 最高）；provider 不支持会被忽略。
    - 降级路径（json_object / reasoning_effort 失败）会移除两者重试。
    """
    import requests

    base_url = model_cfg["base_url"].rstrip("/")
    url = base_url + "/chat/completions"
    payload = {
        "model": model_cfg["model"],
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "temperature": 0.4,
        "response_format": {"type": "json_object"},
    }
    effort = get_reasoning_effort()
    if effort:
        payload["reasoning_effort"] = effort
    # 打印输入日志（截断超长）
    logger.info("[AI] 请求 %s model=%s | reasoning=%s | system=%s | user=%s",
                url, model_cfg["model"], effort or "-", system[:300], user[:2000])
    headers = {**HTTP_HEADERS, "Authorization": f"Bearer {model_cfg['api_key']}"}
    try:
        resp = requests.post(url, headers=headers, json=payload, timeout=AI_REQUEST_TIMEOUT)
        resp.raise_for_status()
        content = resp.json()["choices"][0]["message"]["content"]
    except Exception as e:  # noqa: BLE001 优先 json_object，失败降级普通文本
        logger.warning("[AI] json_object 调用失败，降级普通文本：%s", e)
        try:
            payload.pop("response_format", None)
            payload.pop("reasoning_effort", None)   # provider 不支持时一并移除
            resp = requests.post(url, headers=headers, json=payload, timeout=AI_REQUEST_TIMEOUT)
            resp.raise_for_status()
            content = resp.json()["choices"][0]["message"]["content"]
        except Exception as e2:  # noqa: BLE001
            raise ValueError(f"AI 接口调用失败: {e2}")
    # 打印输出日志（截断）
    logger.info("[AI] 响应 %s | content=%s", url, str(content or "")[:2000])
    # 提取 JSON（可能带 ```json 围栏）
    txt = str(content or "").strip()
    if not txt:
        raise ValueError("AI 返回空内容（请求可能被拒绝或该股缓存数据过少）")
    if txt.startswith("```"):
        txt = txt.strip("`").strip()
        if txt.lower().startswith("json"):
            txt = txt[4:].strip()
    try:
        return json.loads(txt)
    except (ValueError, TypeError) as e:
        raise ValueError(f"AI 输出解析失败: {e}")


# ---------- 个股数据汇总（结构化输入） ----------

def build_stock_context(code: str) -> dict:
    """汇总该股缓存数据（读缓存零网络）为结构化 JSON。"""
    ctx = {"code": code}
    from app.analysis.portfolio import compute_portfolio
    from app.analysis.valuation import compute_live, get_quantiles
    from app.data.cache import get_daily_fundflow, get_financials
    from app.services.quote import get_quote

    quote = {}
    price = None
    try:
        q = get_quote(code)
        quote = {k: q.get(k) for k in ("price", "pct_chg", "prev_close", "open", "high", "low", "amount", "ts")}
        price = q.get("price")
    except Exception:  # noqa: BLE001
        pass
    ctx["quote"] = quote

    try:
        live = compute_live(code, price) or {}
        ctx["valuation"] = {
            "pe_ttm": live.get("pe"), "pb": live.get("pb"),
            "fwd_pe": live.get("fwd_pe"), "fwd_pb": live.get("fwd_pb"),
            "fwd_pb_confidence": live.get("fwd_pb_confidence"),
            "dv_ratio": live.get("dv_ratio"), "total_mv": live.get("total_mv"),
            "roe_ttm": live.get("roe_ttm"), "profit_yoy_ttm": live.get("profit_yoy_ttm"),
            "revenue_yoy_ttm": live.get("revenue_yoy_ttm"),
        }
        ctx["growth"] = {
            "ttm_net_profit": live.get("ttm_net_profit"),
            "expected_growth": live.get("expected_growth"),
            "expected_growth_source": live.get("expected_growth_source"),
            "expected_payout": live.get("expected_payout"),
        }
    except Exception:  # noqa: BLE001
        pass

    try:
        fin = get_financials(code)
        if fin:
            ctx["financials"] = {
                "report_date": fin["report_date"], "net_profit": fin["net_profit"],
                "net_assets": fin["net_assets"], "last_year_net_assets": fin["last_year_net_assets"],
                "eps": fin["eps"], "dv_per_share": fin["dv_per_share"],
                "payout_ratio": fin["payout_ratio"], "total_shares": fin["total_shares"],
                "roe": fin["roe"], "profit_yoy": fin["profit_yoy"], "revenue_yoy": fin["revenue_yoy"],
            }
    except Exception:  # noqa: BLE001
        pass

    try:
        ql = get_quantiles(code)
        ctx["quantiles"] = {
            p: {"pe_pct": ql[p].get("pe_pct"), "pb_pct": ql[p].get("pb_pct")}
            for p in ("1y", "3y", "5y") if p in ql
        }
    except Exception:  # noqa: BLE001
        pass

    try:
        flow = get_daily_fundflow(code)
        if flow:
            ctx["fundflow"] = {"main_net": flow["main_net"], "main_net_pct": flow["main_net_pct"]}
    except Exception:  # noqa: BLE001
        pass

    # 组合上下文（若该股在持仓中）
    try:
        p = compute_portfolio()
        stock = next((s for s in p["stocks"] if s["code"] == code), None)
        if stock:
            ctx["portfolio"] = {
                "weight_pct": stock.get("weight"),
                "cost": stock.get("avg_cost_cny") or stock.get("avg_cost"),
                "value_cny": stock.get("value_cny"),
                "pnl_pct": stock.get("pnl_pct"),
                "day_pnl": stock.get("day_pnl"),
                "is_etf": stock.get("is_etf"),
            }
    except Exception:  # noqa: BLE001
        pass
    return ctx


# ---------- 诊股 ----------

def _normalize_report(data: dict) -> dict:
    """校验/规整 AI 输出为统一结构，缺失字段补默认。"""
    rating = str(data.get("rating", "C")).upper()
    if rating not in ("A", "B", "C", "D"):
        rating = "C"
    try:
        risk = max(0, min(100, int(float(data.get("risk_score", 50)))))
    except (TypeError, ValueError):
        risk = 50
    dims = {}
    raw_dims = data.get("dimensions") or {}
    if not isinstance(raw_dims, dict):
        raw_dims = {}
    for k in DIMENSIONS:
        # AI 偶发把维度 JSON 键名写成中文，映射回英文键（英文优先，中文兜底）
        d = next((raw_dims[a] for a in DIM_KEY_ALIASES[k] if isinstance(raw_dims.get(a), dict)), {})
        try:
            score = max(0, min(100, int(float(d.get("score", 50)))))
        except (TypeError, ValueError):
            score = 50
        risk_lv = str(d.get("risk", "medium")).lower()
        if risk_lv not in ("low", "medium", "high"):
            risk_lv = "medium"
        src = str(d.get("data_source", "provided")).lower()
        dims[k] = {
            "score": score, "analysis": str(d.get("analysis", "")), "risk": risk_lv,
            "data_source": "supplemented" if src == "supplemented" else "provided",
        }
    raw_reasons = data.get("reasons") or []
    if not isinstance(raw_reasons, list):
        raw_reasons = [str(raw_reasons)] if raw_reasons else []
    reasons = [str(x) for x in raw_reasons]
    # 交叉陷阱分析
    raw_cross = data.get("cross_analysis") or {}
    traps = {}
    for key in ("cycle_trap", "value_trap", "dividend_trap"):
        t = raw_cross.get(key) or {}
        detected = bool(t.get("detected"))
        try:
            impact = max(-100, min(100, int(float(t.get("impact_score", 0)))))
        except (TypeError, ValueError):
            impact = 0
        traps[key] = {
            "detected": detected,
            "impact_score": impact,
            "explanation": str(t.get("explanation", "")),
        }
    # 预期年同比增速（AI 给出，可被用户应用到可配置项）；缺失/非数值 → None，数值 clamp 防离谱
    eg = data.get("expected_growth")
    if not isinstance(eg, dict):
        eg = {}

    def _num(v):
        if v is None or v == "" or isinstance(v, bool):
            return None
        try:
            return max(-100.0, min(10000.0, float(v)))
        except (TypeError, ValueError):
            return None

    np_growth = _num(eg.get("net_profit"))
    rev_growth = _num(eg.get("revenue"))
    exp_growth = {
        "net_profit": round(np_growth, 2) if np_growth is not None else None,
        "net_profit_reason": str(eg.get("net_profit_reason") or ""),
        "revenue": round(rev_growth, 2) if rev_growth is not None else None,
        "revenue_reason": str(eg.get("revenue_reason") or ""),
    }
    return {
        "rating": rating,
        "risk_score": risk,
        "dimensions": dims,
        "cross_analysis": traps,
        "expected_growth": exp_growth,
        "summary": str(data.get("summary", "")),
        "reasons": reasons,
        "html": str(data.get("html") or ""),   # AI 生成的完整 HTML 诊股报告（可空 → 前端不显示入口）
    }


def analyze_stock(code: str) -> dict:
    """触发诊股：用激活模型分析并落库，返回规整报告 + 元信息。"""
    model_cfg = get_active_model()
    if not model_cfg:
        raise ValueError("尚未配置/启用任何 AI 模型（点右上角「🤖 AI」配置）")
    ctx = build_stock_context(code)
    user = (
        "个股结构化数据：\n" + json.dumps(ctx, ensure_ascii=False, default=str) + "\n\n"
        "请输出严格 JSON，结构如下：\n" + json.dumps(_OUTPUT_SCHEMA, ensure_ascii=False)
    )
    try:
        raw = chat_json(model_cfg, _SYSTEM_PROMPT, user)
    except Exception as e:  # noqa: BLE001
        raise ValueError(str(e))
    report = _normalize_report(raw)
    from app.data.cache import get_financials

    fin = get_financials(code)
    name = None
    if fin:
        with get_conn() as c:
            row = c.execute("SELECT name FROM stocks WHERE code=?", (code,)).fetchone()
            name = row["name"] if row else None
    now = datetime.now().isoformat(timespec="seconds")
    with get_conn() as c:
        c.execute(
            """INSERT INTO ai_reports(code, name, report_json, model_name, created_at, updated_at)
               VALUES(?,?,?,?,?,?)
               ON CONFLICT(code) DO UPDATE SET
                 name=excluded.name, report_json=excluded.report_json,
                 model_name=excluded.model_name, updated_at=excluded.updated_at""",
            (code, name, json.dumps(report, ensure_ascii=False), model_cfg["name"], now, now),
        )
    logger.info("[AI诊股] %s 生成报告：%s（%s）", code, report["rating"], model_cfg["name"])
    return {"code": code, "model_name": model_cfg["name"], "created_at": now, "report": report}


def get_report(code: str) -> dict | None:
    """读取已存报告。"""
    with get_conn() as c:
        row = c.execute("SELECT * FROM ai_reports WHERE code=?", (code,)).fetchone()
    if not row:
        return None
    d = dict(row)
    d["report"] = json.loads(d.pop("report_json"))
    return d

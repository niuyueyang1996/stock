"""AI 诊股服务：OpenAI 兼容大模型接入、多模型配置、个股结构化诊股报告。

- 模型配置存 ai_models 表，切换时 is_active 唯一。
- chat_json 调 OpenAI 兼容 /v1/chat/completions，优先 response_format json_object，
  失败降级为普通文本后自行解析 JSON。
- build_stock_context 复用缓存汇总个股数据（读缓存零网络）作为结构化输入。
- analyze_stock 组装 prompt → 调 AI → 规整输出 → 存 ai_reports。
"""
import json
import logging
from datetime import date, datetime, timedelta

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
    "[数据使用规范]\n"
    "1. 系统提供的所有结构化字段都必须纳入分析（行情/估值/财务/分位/资金流/持仓），"
    "不得只挑少数指标或只看总分——漏用数据视为不合格。\n"
    "2. 字段带 *_source / *_confidence 后缀表示可靠度：user=你的输入、ttm=滚动口径、latest_report=最新财报、"
    "zero_conservative=零增长保守假设；confidence 为 high/medium/low/invalid。来源为 user 的优先采信；"
    "zero_conservative / low / invalid 的须保守解读并在报告注明。\n"
    "3. *_static 后缀=按去年年报口径，无后缀/ttm=滚动口径；两者明显背离时，分析差异原因（如新并购、周期顶回落），不得只报其一。\n"
    "4. 资金流字段为净流入（元，正=流入负=流出）；五档 super=超大单/large=大单/medium=中单/small=小单/xs=特小单，"
    "main=主力（超大+大单）。当日分时 15 分钟序列（intraday_15m：每窗口五档净额，main=单窗口主力/cum=累计主力）"
    "与近30日累计反映资金趋势，须结合看。\n"
    "5. 若某维度数据缺失或不足（如同业竞争对比、行业景气度），可用你的领域知识补充，但必须：\n"
    "   a) 在该维度 analysis 中明确标注「[AI补充]」；\n"
    "   b) 注明补充数据的时效（如「截至2026年」），确保有时效性、不用过时数据；\n"
    "   c) 无法确认时效时明确说明；字段为 null 表示系统无此数据，不得臆造数值。\n"
    "6. data_source 字段：provided=基于系统数据；supplemented=AI 补充/推断。\n"
    "7. 输出语言与键名：所有文字字段（维度 analysis、cross_analysis.explanation、summary、reasons、html、"
    "预期增速依据）一律使用简体中文，禁止混入英文单词；若在正文中提及维度，用中文名"
    "（周期性、护城河、基本面、增长、股息、估值、同业竞争），不要写 growth/fundamentals 等英文。\n"
    "   JSON 键名必须保持英文、与下方输出结构完全一致，不得改成中文；尤其 dimensions 的 7 个键固定为 "
    "cyclicality/moat/fundamentals/growth/dividend/valuation/competition，禁止改名。\n"
    "8. expected_growth 字段：基于系统财务数据（最新财报同比、TTM 同比、ROE、支付率、前瞻指标）与陷阱判断，"
    "给出你对该股未来一年净利与营收年同比增速的预判（%，可为负），并用 net_profit_reason / revenue_reason 简述依据。"
    "若识别出周期陷阱，勿把历史高点同比直接外推到未来，增速应回归行业中枢。\n\n"
    "[资金面分析要求]\n"
    "系统提供了该股当日主力净流入（main_net/main_net_pct）、五档结构（超大/大/中/小/特小单）、"
    "当日分时 15 分钟五档序列（intraday_15m：super/large/medium/small/xs 每窗口净额，main=主力/cum=累计，"
    "观察全天各档吸筹/出货节奏）、近30日完整逐日五档/买卖盘序列（daily：每日期含五档 netamount/main_net/"
    "super_large_net/large_net/medium_net/small_net/xs_net 与 buy_amount/sell_amount，升序，days 为覆盖天数，"
    "net_30d/net_5d 为累计速览）——这些必须纳入分析：\n"
    "- 结合当日主力净额与占比判断当前资金态度（吸筹/出货/分歧）；\n"
    "- 五档结构揭示资金类型（超大/大单为主 vs 散户小单为主）；\n"
    "- 近30日累计/最近5日趋势判断资金的中期流向；\n"
    "- 资金面结论须融入基本面与估值判断（如「估值低但主力持续流出」应下调吸引力），"
    "并在 reasons 与 HTML 报告中体现；数据为 null 时用 [AI补充] 说明。\n\n"
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
    "同时生成一份完整、独立、可读性强的 HTML 诊股报告（字段 html），供用户新开页面查看。\n"
    "[HTML 深度分析强制规范]\n"
    "1. HTML 正文（简体中文）必须 ≥1000 字，必须有实质分析；禁止只列要点、禁止泛泛复述 prompt 结构、禁止空话套话，浅尝辄止视为不合格重写。\n"
    "2. 结构必须覆盖：核心结论 / 商业模式与护城河 / 财务与盈利质量 / 成长驱动 / "
    "资金面（主力/五档/近30日资金流）/ 估值判断（含前瞻）/ 风险与陷阱 / 情景推演 / 操作建议分级。\n"
    "3. 每个判断必须带具体数字与上下文（如「当前 PE 12.3 处近 1 年约 15% 分位，前瞻 PE 仅 9.8，"
    "但增长来源为 zero_conservative 保守假设」）；禁止「估值偏低」「基本面扎实」这类无数字断言。\n"
    "4. 操作建议分级：加仓/持有/减仓/清仓四档，每档给出触发条件，不得含糊。\n"
    "5. HTML 为独立成文，离开本应用页面即可读懂；已提供的行情/财务/估值等原始数据用户在本应用已可查看，"
    "HTML 中**不得原样罗列数据表格**；把篇幅全部用于深入分析与洞察。\n"
    "6. 质量自检：写完后逐段检查——这段是否提供了用户在数据表上看不到的洞察？若没有，重写。\n"
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
    "html": "完整独立 HTML 诊股报告源代码（自包含、内联 CSS、无外部依赖、无脚本、简体中文、≥1000字深度正文，聚焦深入分析，不罗列原始数据）",
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
    from app.data.cache import get_daily_fundflow, get_daily_fundflows, get_financials, get_fundflow_min
    from app.data.fundflow import intraday_window_series
    from app.services.quote import get_quote

    quote = {}
    price = None
    try:
        q = get_quote(code)
        quote = {k: q.get(k) for k in ("price", "pct_chg", "prev_close", "open", "high", "low", "amount", "volume", "ts")}
        price = q.get("price")
    except Exception:  # noqa: BLE001
        pass
    ctx["quote"] = quote

    # 完整 compute_live 返回（约 35 字段：pe/pb/ps 三种口径、静态与 TTM、前瞻指标、增长来源等）
    try:
        ctx["valuation"] = compute_live(code, price) or {}
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

    # 资金流：当日五档全量 + 近30日历史摘要 + 当日分时 15 分钟主力净流入序列
    try:
        flow = get_daily_fundflow(code)
        if flow:
            ctx["fundflow"] = {
                "date": flow["trade_date"],
                "netamount": flow["netamount"],
                "main_net": flow["main_net"],
                "main_net_pct": flow["main_net_pct"],
                "super_large_net": flow["super_large_net"],
                "large_net": flow["large_net"],
                "medium_net": flow["medium_net"],
                "small_net": flow["small_net"],
                "xs_net": flow["xs_net"],
                "bands": {"p15": flow["p15"], "p40": flow["p40"], "p75": flow["p75"], "p95": flow["p95"]},
            }
            ff = ctx["fundflow"]
            try:
                end = flow["trade_date"]
                start = (date.fromisoformat(end) - timedelta(days=45)).isoformat()
                rows = get_daily_fundflows(code, start, end)
                nets = [float(r["netamount"]) for r in rows if r["netamount"] is not None]
                if nets:
                    ff["days"] = len(nets)
                    ff["net_30d"] = round(sum(nets), 0)
                    ff["net_5d"] = round(sum(nets[-5:]), 0)
                    # 完整逐日五档 + 买卖盘（按日期升序，跨天结构/趋势判断用）
                    ff["daily"] = [
                        {
                            "date": r["trade_date"],
                            "netamount": r["netamount"],
                            "main_net": r["main_net"],
                            "super_large_net": r["super_large_net"],
                            "large_net": r["large_net"],
                            "medium_net": r["medium_net"],
                            "small_net": r["small_net"],
                            "xs_net": r["xs_net"],
                            "buy_amount": r["buy_amount"],
                            "sell_amount": r["sell_amount"],
                        }
                        for r in rows
                    ]
            except Exception:  # noqa: BLE001
                pass
            try:
                mins = get_fundflow_min(code, flow["trade_date"])
                series = intraday_window_series(mins, 15)
                if series:
                    ff["intraday_15m"] = series
            except Exception:  # noqa: BLE001
                pass
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


def analyze_stock(code: str, system_prompt: str | None = None) -> dict:
    """触发诊股：用激活模型分析并落库，返回规整报告 + 元信息。

    system_prompt 非 None 时作为「用户附加要求」追加到默认指令后（前端弹窗可编辑）。
    """
    model_cfg = get_active_model()
    if not model_cfg:
        raise ValueError("尚未配置/启用任何 AI 模型（点右上角「🤖 AI」配置）")
    ctx = build_stock_context(code)
    user = (
        "个股结构化数据：\n" + json.dumps(ctx, ensure_ascii=False, default=str) + "\n\n"
        "请输出严格 JSON，结构如下：\n" + json.dumps(_OUTPUT_SCHEMA, ensure_ascii=False)
    )
    system = _SYSTEM_PROMPT
    if system_prompt:
        system = f"{system}\n\n[用户附加要求]\n{system_prompt}"
    try:
        raw = chat_json(model_cfg, system, user)
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


# ---------- 资金流 AI 实时分析（个股/组合，按选定窗口，无 HTML） ----------

_FUNDFLOW_ANALYSIS_SYSTEM = (
    "你是资深盘口资金分析师。系统提供某标的（个股或组合）在选定时间窗内的资金流与股价数据，"
    "你要快速判断：各档资金动向、资金与股价的相关性/背离、主力资金在做什么、发生了什么、要注意什么。\n"
    "[数据说明]\n"
    "1. 窗口分两类：\n"
    "   - 分钟窗口（window 如 '15m'）：points 为当日分时序列。个股/组合为五档净流入（元，正=流入负=流出）："
    "super=超大单、large=大单、medium=中单、small=小单、xs=特小单；main=主力（super+large）；"
    "cum=截至该窗口的累计主力净流入；buy=该窗口买盘成交额/sell=卖盘成交额（与天窗口 buy_amount/sell_amount 同义）；"
    "price=同窗口股价。\n"
    "   - 指数（mode='index'）无逐笔成交、无分时五档资金流，points 为分时量价："
    "price=窗口末分钟收盘价、volume=窗口累计成交量、amount=窗口累计成交额、"
    "cum=截至该窗口累计量、cum_amount=截至该窗口累计成交额、"
    "day_pct=本窗口量占全天%、cum_pct=累计量占全天%——据此判断量价/量额关系："
    "放量上攻（量增价升）/缩量回调（量缩价跌）/量价背离（价升量缩或价跌量增）、"
    "成交额与量同步或背离（放量但额不增、价涨量缩额滞）。\n"
    "   - 天窗口（window 如 '1d'/'7d'/'30d'）：points 为多日逐日五档序列（'7d'/'30d' 为每 N 个交易日聚合桶），"
    "每个点含 date 起止标签、五档净额 netamount/main_net/super_large_net/large_net/medium_net/small_net/xs_net、"
    "buy_amount=买盘成交额/sell_amount=卖盘成交额；price=该点收盘价、pct_chg=当日涨跌幅（聚合桶取末交易日）。"
    "total_net=窗口内累计净流入、day_net=最近交易日净流入。\n"
    "   - 指数天窗口（mode='index'）无五档，points 为逐日量价：close=收盘价、volume=当日成交量、"
    "amount=当日成交额、pct_chg=涨跌幅（聚合桶 volume/amount 求和、close/pct_chg 取末交易日）——据此判断量价/量额关系。\n"
    "2. bands 为今日各档单笔成交金额区间（自适应分档）："
    "特小单<下界、小单/中单/大单为区间、超大单>上界——据此理解每档对应的资金体量（单位：元）。\n"
    "3. prev_close=昨收（有则用于判断涨跌）。\n"
    "[分析要求]\n"
    "1. 各档解读：对比五档走势，判断是主力（超大+大单）主导还是散户（小+特小单）主导；"
    "超大单持续净流入 → 机构资金介入；超大单出/小单接 → 派发迹象。"
    "注意主力可能拆单（大单拆成多笔中小单分批买卖），档位净额会被伪装——须结合每窗口 buy/sell 买卖盘成交额与节奏识别："
    "买盘额持续放大但各档净额分散 → 疑似拆单吸筹；卖盘额放大而股价滞涨 → 拆单出货。\n"
    "2. correlation 五分类（判断「资金×股价」）："
    "资金与股价同步涨 → positive（同涨）；同步跌 → negative（同跌）；"
    "资金持续流入但股价不涨/下跌 → bottom_divergence（底背离，有承接吸筹，看涨信号）；"
    "资金持续流出但股价仍在上涨/抗跌 → top_divergence（顶背离，派发风险，看跌信号）；"
    "信号弱或矛盾 → neutral（中性）。"
    "（天窗口看逐日净流入与涨跌幅 pct_chg；分钟窗口看累计主力 cum 与分时价。）\n"
    "3. 主力行为：从节奏判断吸筹（尾盘/近日放量流入）/出货（拉高派发、早盘冲高后持续流出）/分歧（时进时出）。\n"
    "4. 结合 day_net/total_net 与累计走势给出一句话结论。\n"
    "5. 输出简体中文，简明扼要、可直接阅读；这是快报，不要长篇大论，不要 HTML。\n"
    "严格输出 JSON，不要输出任何额外文字。"
)

_FUNDFLOW_ANALYSIS_SCHEMA = {
    "summary": "一句话结论（发生了什么）",
    "correlation": "positive(同涨)|negative(同跌)|top_divergence(顶背离,资金流出股价仍涨)|bottom_divergence(底背离,资金流入股价不涨)|neutral(中性)",
    "divergence": [{"ts": "背离时段", "detail": "背离描述（资金 vs 股价）"}],
    "main_force": "主力资金行为解读（吸筹/出货/分歧，结合分时节奏）",
    "rhythm": "全天资金节奏（早盘/午盘/尾盘各自表现）",
    "alerts": ["要注意的风险点数组"],
    "conclusion": "简明操作提示（要注意什么、怎么做）",
}

# ---------- 组合批量资金 AI 分析：所有持仓个股一次分析（省 token），逐只精简输出 ----------

_BATCH_FUNDFLOW_SYSTEM = (
    "你是资深盘口资金分析师。系统给出组合内多只标的（个股或指数）的资金流与股价数据（列表 stocks），"
    "你要对每只标的快速判断：资金与股价的相关性/背离、主力资金在做什么、要注意什么；"
    "并额外给出组合整体的资金面相关性判断（coherence）。\n"
    "[数据说明]\n"
    "1. stocks 为列表，每只含：code/name/tag/weight_pct（组合权重）、price/pct_chg（当前价与涨跌幅）、"
    "day_net/day_main_net（当日净流入/主力净流入，元，正=流入负=流出）、points（按选定窗口的资金序列："
    "分钟窗口为当日分时序列。个股含 super/large/medium/small/xs 五档、main=主力、cum=累计、"
    "buy/sell=买盘/卖盘成交额、price=同窗口股价；指数（mode='index'）无五档（无逐笔成交），"
    "points 为分时量价：price=窗口末收盘、volume=窗口累计量、amount=窗口累计成交额、"
    "cum=累计量、cum_amount=累计成交额、day_pct/cum_pct=窗口/累计量占全天%"
    "——据此判断量额/量价关系（成交额放大上攻/萎缩回调/量额背离、成交额与量背离），判断资金强弱优先看金额（成交额）。"
    "天窗口为多日逐日/聚合桶，"
    "含 netamount/main_net/各档净额/buy_amount/sell_amount、price/pct_chg 收盘价与涨跌幅；"
    "指数（mode='index'）天窗口无五档，points 为逐日量价 close/volume/amount/pct_chg）。\n"
    "2. correlation 五分类判断「资金×股价」：资金与股价同步涨 → positive（同涨）；同步跌 → negative（同跌）；"
    "资金持续流入但股价不涨/下跌 → bottom_divergence（底背离，有承接吸筹，看涨信号）；"
    "资金持续流出但股价仍在上涨/抗跌 → top_divergence（顶背离，派发风险，看跌信号）；信号弱或不一致 → neutral（中性）。\n"
    "3. 判断依据：看逐点资金 vs 股价走势。注意主力可能拆单（大单拆成中小单分批买卖），"
    "档位净额会被伪装——须结合 buy/sell 买卖盘成交额识别真实意图（买盘额放大但各档净额分散 → 疑似拆单吸筹；"
    "卖盘额放大而股价滞涨 → 拆单出货），勿仅凭档位净额下结论。\n"
    "[组合相关性 coherence]\n"
    "看完每只后，站在组合整体逐窗口横向对比各标的，判断资金面格局（三种情形都可能）：\n"
    "1. 共振：多只同一方向（同流入/同流出、同放量/同缩量），呈板块或市场级资金联动；\n"
    "2. 跷跷板/虹吸：资金从一只转移到另一只——卖掉 A 的钱去买 B，通常两边都放量（A 放量流出、B 放量流入），"
    "核心是成交金额的此消彼长，而不是一边缩量；资金偏向谁、谁被抽走，看逐窗口成交额与净流入方向；\n"
    "3. 分化：各标的资金方向、节奏差异大，无统一主线。\n"
    "必须逐窗口对比，优先看金额（成交额）：指数看 amount/cum_amount 逐窗口成交额变化（谁的资金在放大/萎缩、"
    "净流入还是净流出），个股看各档净额与买卖盘成交额；成交量只作辅助，判断资金强弱与虹吸以金额为准。"
    "输出 correlation（五分类沿用：positive=整体同流入/negative=整体同流出/背离/分化或跷跷板=neutral）、"
    "summary（组合整体一句话）、points（至少 3 条具体证据，每条注明「分时窗口+标的+成交额/资金数据」，"
    "如『09:45 科创50 放量约 400 亿 vs 中证红利缩量约 120 亿，资金明显流向科创50』）、"
    "conclusion（组合层面操作提示）。\n"
    "[输出要求]\n"
    "1. 先输出 coherence（组合相关性），再输出 stocks 逐只。每只输出：code、correlation、summary（一句话结论）、"
    "main_force（主力行为，可选）、alerts（注意点，可选）。\n"
    "2. 必须覆盖 stocks 中出现的每只 code，不要遗漏；不要新增 stocks 中没有的 code。\n"
    "3. 每只精简到几句话，不展开；简体中文。\n"
    "严格输出 JSON，结构如下，不要输出任何额外文字。"
)

_BATCH_FUNDFLOW_SCHEMA = {
    "coherence": {
        "correlation": "组合相关性判断（positive|negative|top_divergence|bottom_divergence|neutral）",
        "summary": "组合整体一句话",
        "points": ["相关性/背离证据点数组"],
        "conclusion": "组合层面操作提示",
    },
    "stocks": [
        {
            "code": "标的代码",
            "correlation": "positive|negative|top_divergence|bottom_divergence|neutral（资金×股价）",
            "summary": "一句话结论",
            "main_force": "主力行为（可选）",
            "alerts": ["注意点（可选）"],
        }
    ]
}


def _minute_price_map(code: str, window: int) -> dict:
    """新浪分时（当日 1 分钟）→ {窗口 ts: 收盘价}。拉取失败返回空 dict（不阻断资金面分析）。"""
    try:
        from app.data.raw import raw_sina
        from app.instruments import get_instrument

        df = raw_sina.minute_a(get_instrument(code).symbol())
        if df is None or len(df) == 0:
            return {}
        out: dict[str, float] = {}
        for _, row in df.iterrows():
            day = row.get("day")
            if hasattr(day, "strftime"):
                hm = day.strftime("%H:%M")
            else:
                s = str(day)
                parts = s.split(" ")
                hm = (parts[1] if len(parts) > 1 else parts[0])[:5]
            if ":" not in hm:
                continue
            minute = int(hm[:2]) * 60 + int(hm[3:5])
            bstart = (minute // window) * window
            ts = f"{bstart // 60:02d}:{bstart % 60:02d}"
            try:
                out[ts] = float(row["close"])
            except (TypeError, ValueError, KeyError):
                pass
        return out
    except Exception:  # noqa: BLE001 股价拉取失败不阻断资金面分析
        return {}


def _norm_flow_window(window: int | str) -> str:
    """资金流窗口归一化为统一字符串（'1m'…'30d'）。兼容 int 旧值（15 → '15m'）。"""
    _FLOW_WINDOWS = ("1m", "5m", "15m", "30m", "1d", "7d", "30d")
    if isinstance(window, int):
        return f"{window}m" if window in (1, 5, 15, 30) else "15m"
    s = str(window).strip().lower()
    if s in _FLOW_WINDOWS:
        return s
    if s.isdigit() and int(s) in (1, 5, 15, 30):
        return f"{s}m"
    return "15m"


def _day_flow_point(r):
    """逐日资金流行 → AI 点（五档 + 买卖盘）。兼容 sqlite3.Row 与 dict。"""
    return {
        "date": r["trade_date"],
        "netamount": r["netamount"],
        "main_net": r["main_net"],
        "super_large_net": r["super_large_net"],
        "large_net": r["large_net"],
        "medium_net": r["medium_net"],
        "small_net": r["small_net"],
        "xs_net": r["xs_net"],
        "buy_amount": r["buy_amount"],
        "sell_amount": r["sell_amount"],
    }


def _bucket_day_flows(rows, bucket, price_map=None):
    """逐日五档序列按 N 个交易日聚合桶（与前端 bucketFlowDays 同构）；price_map={date:{price,pct_chg}}。
    bucket<=1 逐日原样输出；否则每 N 日求和，price/pct_chg 取桶末交易日。"""
    if not rows:
        return []
    if bucket <= 1:
        out = []
        for r in rows:
            p = _day_flow_point(r)
            pm = price_map or {}
            if r["trade_date"] in pm:
                p.update(pm[r["trade_date"]])
            out.append(p)
        return out
    out = []
    for i in range(0, len(rows), bucket):
        g = rows[i:i + bucket]
        last = g[-1]
        p = {"date": g[0]["trade_date"] + (f"~{last['trade_date'][5:]}" if len(g) > 1 else "")}
        for k in ("netamount", "main_net", "super_large_net", "large_net", "medium_net",
                  "small_net", "xs_net", "buy_amount", "sell_amount"):
            vals = [float(r[k]) for r in g if r[k] is not None]
            p[k] = sum(vals) if vals else None
        pm = price_map or {}
        if last["trade_date"] in pm:
            p.update(pm[last["trade_date"]])
        out.append(p)
    return out


def _bucket_day_prices(rows, bucket):
    """指数逐日量价（close/volume/amount）按 N 个交易日聚合桶（与 _bucket_day_flows 同构）。
    bucket<=1 逐日原样输出；否则每 N 日 volume/amount 求和、close/pct_chg 取桶末交易日。"""
    if not rows:
        return []
    rows = [dict(r) for r in rows]
    if bucket <= 1:
        return [
            {"date": r["trade_date"], "close": r.get("close"), "volume": r.get("volume"),
             "amount": r.get("amount"), "pct_chg": r.get("pct_change")}
            for r in rows
        ]
    out = []
    for i in range(0, len(rows), bucket):
        g = rows[i:i + bucket]
        last = g[-1]
        vols = [float(r["volume"]) for r in g if r["volume"] is not None]
        amts = [float(r["amount"]) for r in g if r.get("amount") is not None]
        out.append({
            "date": g[0]["trade_date"] + (f"~{last['trade_date'][5:]}" if len(g) > 1 else ""),
            "close": last.get("close"), "volume": sum(vols) if vols else None,
            "amount": sum(amts) if amts else None,
            "pct_chg": last.get("pct_change"),
        })
    return out


def _index_amount_scale(code: str) -> float:
    """指数成交额派生比例：行情实时成交额 ÷ 最新交易日量（每单位量→金额）。

    腾讯指数分时/日K只有量无额；今日真实成交额÷最新日量得到比例，
    各分钟/各日量乘该比例得成交额（今日刻度准确，历史日假设比例恒定，与组合页 combo_index_volume 同口径）。
    """
    try:
        from app.services.quote import get_quote
        from app.data.cache import get_latest_daily_price

        q = get_quote(code) or {}
        latest = get_latest_daily_price(code)
        vol = float(latest["volume"]) if latest and latest["volume"] else 0.0
        return (float(q.get("amount") or 0.0) / vol) if vol else 0.0
    except Exception:  # noqa: BLE001 缺行情/量时比例=0，points 不带成交额
        return 0.0


def build_fundflow_analysis_context(code: str, window: int | str = "15m",
                                    with_price: bool = True) -> dict:
    """个股资金流 AI 分析上下文（统一时间窗）。

    - 分钟窗口（'1m'/'5m'/'15m'/'30m'）：当日分时五档序列 + 分时股价对齐同一窗口。
    - 天窗口（'1d'/'7d'/'30d'）：多日逐日五档（'7d'/'30d' 聚合桶）+ 每日收盘价日K。
    - with_price=False：分钟模式跳过新浪分时股价拉取（联网），供批量分析多股时零网络。
    指数（is_index）：无五档 → 腾讯量价，points 带 price/volume/amount/cum/cum_amount/day_pct/cum_pct，
    成交额由 _index_amount_scale 派生（与组合页 combo_index_volume 同口径）。
    读缓存零网络（个股股价拉取失败不影响）；数据缺失时 points 为空。
    """
    from datetime import date, timedelta

    from app.data.cache import (get_daily_fundflow, get_daily_fundflows, get_daily_prices,
                                get_fundflow_min, get_index_intraday)
    from app.data.fundflow import FUNDFLOW_WINDOWS, index_intraday_window_series, intraday_window_series
    from app.instruments import get_instrument

    w = _norm_flow_window(window)
    is_day = w.endswith("d")
    today = date.today().isoformat()
    out = {"mode": "stock", "window": w, "date": today, "points": []}
    inst = get_instrument(code)
    # 指数：无逐笔成交、无五档 → 资金面全用腾讯量价（分时 mkline / 日级 fqkline volume），成交额按量派生
    if getattr(inst, "is_index", False):
        scale = _index_amount_scale(code)
        if is_day:
            start = (date.today() - timedelta(days=45)).isoformat()
            rows = [dict(r) for r in get_daily_prices(code, start, today)]
            for r in rows:
                if r.get("volume") is not None:
                    r["amount"] = float(r["volume"]) * scale
            if not rows:
                return out
            out["mode"] = "index"
            out["points"] = _bucket_day_prices(rows, int(w[:-1]))
            out["code"] = code
            return out
        raw = get_index_intraday(code, today)
        series = index_intraday_window_series(
            [{**r, "amount": (r.get("volume") or 0.0) * scale} for r in raw],
            int(w[:-1]),
        )
        if not series:
            return out
        out["mode"] = "index"
        out["points"] = series
        out["code"] = code
        return out
    # 今日五档 + 自适应分档区间（两种窗口都取今日快照，帮助理解档位体量）
    bands = None
    flow = get_daily_fundflow(code, today)
    if flow:
        p15, p40, p75, p95 = flow["p15"], flow["p40"], flow["p75"], flow["p95"]
        if None not in (p15, p40, p75, p95):
            bands = {
                "xs": f"<{p15}元", "small": f"{p15}~{p40}元", "medium": f"{p40}~{p75}元",
                "large": f"{p75}~{p95}元", "super": f">{p95}元",
            }
    if flow:
        out["day_net"] = flow["netamount"]
        out["day_main_net"] = flow["main_net"]
        if bands:
            out["bands"] = bands
    if is_day:
        start = (date.today() - timedelta(days=45)).isoformat()
        rows = get_daily_fundflows(code, start, today)
        if not rows:
            return out
        price_map = {}
        for r in get_daily_prices(code, start, today):
            price_map[r["trade_date"]] = {"price": r["close"], "pct_chg": r["pct_change"]}
        out["points"] = _bucket_day_flows(rows, int(w[:-1]), price_map)
        nets = [float(r["netamount"]) for r in rows if r["netamount"] is not None]
        out["total_net"] = round(sum(nets), 0)
        out["code"] = code
        return out
    try:
        from app.services.quote import get_quote

        q = get_quote(code)
        if q:
            out["prev_close"] = q.get("prev_close")
    except Exception:  # noqa: BLE001
        pass
    series = intraday_window_series(get_fundflow_min(code, today), int(w[:-1]))
    if not series:
        return out
    if with_price:
        pmap = _minute_price_map(code, int(w[:-1]))
        out["points"] = [
            {**p, **({"price": pmap[p["ts"]]} if p["ts"] in pmap else {})}
            for p in series
        ]
    else:
        out["points"] = series
    out["code"] = code
    return out


def _normalize_fundflow_analysis(data: dict) -> dict:
    """规整资金流分析输出：缺失字段补默认、correlation 限枚举。"""
    if not isinstance(data, dict):
        data = {}
    corr = str(data.get("correlation", "neutral")).lower()
    # 5 分类：positive(同涨)/negative(同跌)/top_divergence(顶背离)/bottom_divergence(底背离)/neutral；
    # divergence 兼容旧数据（未区分方向的背离）
    if corr not in ("positive", "negative", "top_divergence", "bottom_divergence",
                    "divergence", "neutral"):
        corr = "neutral"

    def _list(v):
        if isinstance(v, list):
            return [str(x) for x in v if str(x).strip()]
        return [str(v)] if v and str(v).strip() else []

    raw_div = data.get("divergence")
    divergence = [d for d in (raw_div if isinstance(raw_div, list) else []) if isinstance(d, dict)]
    return {
        "summary": str(data.get("summary") or ""),
        "correlation": corr,
        "divergence": divergence,
        "main_force": str(data.get("main_force") or ""),
        "rhythm": str(data.get("rhythm") or ""),
        "alerts": _list(data.get("alerts")),
        "conclusion": str(data.get("conclusion") or ""),
    }


def analyze_fundflow(code: str, window: int | str = "15m", system_prompt: str | None = None) -> dict:
    """个股 AI 资金流实时分析（带资金×股价相关性/背离），统一时间窗。

    无激活模型 → ValueError；选定窗口无资金流数据 → ValueError。
    返回 {mode, code, name, window, date, points_count, analysis}，无 HTML。
    system_prompt 非 None 时作为「用户附加要求」追加到默认指令后（前端弹窗可编辑）。
    """
    model_cfg = get_active_model()
    if not model_cfg:
        raise ValueError("尚未配置/启用任何 AI 模型（点右上角「🤖 AI」配置）")
    ctx = build_fundflow_analysis_context(code, window)
    if not ctx["points"]:
        raise ValueError("该时间窗资金流数据为空，请先刷新资金流")
    user = (
        "资金流与股价数据：\n" + json.dumps(ctx, ensure_ascii=False, default=str) + "\n\n"
        "请输出严格 JSON，结构如下：\n" + json.dumps(_FUNDFLOW_ANALYSIS_SCHEMA, ensure_ascii=False)
    )
    system = _FUNDFLOW_ANALYSIS_SYSTEM
    if system_prompt:
        system = f"{system}\n\n[用户附加要求]\n{system_prompt}"
    try:
        raw = chat_json(model_cfg, system, user)
    except Exception as e:  # noqa: BLE001
        raise ValueError(str(e))
    analysis = _normalize_fundflow_analysis(raw)
    # 落库 source='single'：按 code+window 覆盖重落库，F5 不丢；不同 window 各存一份
    try:
        _upsert_fundflow_report(code, ctx["date"], "single", ctx["window"],
                                analysis, model_cfg.get("model") or model_cfg.get("name", ""))
    except Exception:  # noqa: BLE001 落库失败不阻断本次分析
        pass
    unit = "分钟" if ctx["window"].endswith("m") else "天"
    logger.info("[AI资金流] %s %s%s分析完成：%s", code, ctx["window"], unit,
                analysis.get("correlation", ""))
    return {
        "mode": ctx["mode"], "code": code, "name": ctx.get("name"),
        "window": ctx["window"], "date": ctx["date"],
        "points_count": len(ctx["points"]),
        "analysis": analysis,
    }


# ============================================================ 组合批量资金 AI 分析

def _upsert_fundflow_report(code: str, trade_date: str, source: str, window: str,
                            analysis: dict, model_name: str = "") -> None:
    """单股资金流 AI 结果写入 ai_fundflow_reports（UPSERT，同 code+window 重评估覆盖）。

    主键 (code, trade_date, source, window)：同一股票不同时间窗各存一份，互不覆盖。
    """
    from datetime import datetime

    from app.models.db import get_conn

    now = datetime.now().isoformat(timespec="seconds")
    with get_conn() as c:
        c.execute(
            """INSERT INTO ai_fundflow_reports
                 (code, trade_date, source, window, correlation, summary, main_force, rhythm,
                  divergence, alerts, conclusion, model_name, created_at, updated_at)
               VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
               ON CONFLICT(code, trade_date, source, window) DO UPDATE SET
                 correlation=excluded.correlation, summary=excluded.summary,
                 main_force=excluded.main_force, rhythm=excluded.rhythm, divergence=excluded.divergence,
                 alerts=excluded.alerts, conclusion=excluded.conclusion,
                 model_name=excluded.model_name, updated_at=excluded.updated_at""",
            (code, trade_date, source, window,
             analysis.get("correlation"), analysis.get("summary"), analysis.get("main_force"),
             analysis.get("rhythm"),
             json.dumps(analysis.get("divergence") or [], ensure_ascii=False),
             json.dumps(analysis.get("alerts") or [], ensure_ascii=False),
             analysis.get("conclusion"), model_name, now, now),
        )


def build_batch_fundflow_context(tags: list[str] | None = None, window: int | str = "15m",
                                 codes: list[str] | None = None,
                                 weights: list[float] | None = None) -> dict:
    """批量资金流上下文：组合内（或直接指定 codes）每只有资金流数据的标的，按统一窗口的紧凑序列列表。

    窗口入口已保证 ≥15m；每只复用 build_fundflow_analysis_context(with_price=True)，
    批量与个股一致带上 window 匹配的股价序列（分钟模式逐股拉新浪分时价，失败的点内无 price 不崩）。
    - tags 模式：读持仓组合（portfolio），港股无资金流跳过（A股/ETF 参与）。
    - codes 模式：直接指定标代码（指数页多指数相关性用），按 participates_fundflow 过滤；
      weights 与 codes 对齐（等权可传 None，AI 侧按 100/n 记）。
    返回 {mode,window,date,covered,total,stocks:[]}。
    """
    from datetime import date

    from app.instruments import get_instrument
    from app.services.quote import get_quote

    w = _norm_flow_window(window)
    today = date.today().isoformat()
    if codes:
        members: list[dict] = []
        for i, code in enumerate(codes):
            inst = get_instrument(code)
            if not inst.participates_fundflow:
                continue
            name = inst.name or code
            if inst.is_index:
                from app.services.indices import get_index_def

                d = get_index_def(code)
                name = (d["name"] if d else None) or name
            weight = weights[i] if weights and i < len(weights) else round(100 / len(codes), 2)
            members.append({"code": code, "name": name, "tag": inst.tag, "weight": weight})
        mode = "indices"
    else:
        from app.analysis.portfolio import compute_portfolio

        holdings = compute_portfolio(tags=tags) or {}
        stocks = holdings.get("stocks") or []
        members = [
            {"code": s.get("code"), "name": s.get("name"), "tag": s.get("tag"),
             "weight": s.get("weight")}
            for s in stocks if s.get("code")
        ]
        mode = "portfolio"
    out = {"mode": mode, "window": w, "date": today,
           "covered": 0, "total": len(members), "stocks": []}
    for m in members:
        code = m["code"]
        try:
            ctx = build_fundflow_analysis_context(code, w, with_price=True)
        except Exception:  # noqa: BLE001 单股上下文缺失跳过
            continue
        if not ctx["points"] and not ctx.get("day_net"):
            continue  # 无资金流数据（港股/未刷新）
        q = get_quote(code) or {}
        out["stocks"].append({
            "code": code,
            "name": m["name"],
            "tag": m["tag"],
            "weight_pct": m["weight"],
            "price": q.get("price"),
            "pct_chg": q.get("pct_chg"),
            "day_net": ctx.get("day_net"),
            "day_main_net": ctx.get("day_main_net"),
            "points": ctx["points"],
        })
    out["covered"] = len(out["stocks"])
    return out


def _normalize_coherence(data) -> dict:
    """规整组合相关性输出：缺失字段补默认、correlation 限枚举。"""
    if not isinstance(data, dict):
        data = {}
    corr = str(data.get("correlation", "neutral")).lower()
    if corr not in ("positive", "negative", "top_divergence", "bottom_divergence", "neutral"):
        corr = "neutral"
    return {
        "correlation": corr,
        "summary": str(data.get("summary") or ""),
        "points": [str(x) for x in (data.get("points") or []) if str(x).strip()],
        "conclusion": str(data.get("conclusion") or ""),
    }


def _upsert_coherence_report(scope: str, scope_key: str, trade_date: str, window: str,
                             coherence: dict, model_name: str = "") -> None:
    """组合级资金相关性结果写入 ai_fundflow_coherence_reports（UPSERT，同 scope+key+日期+窗覆盖）。"""
    from datetime import datetime

    from app.models.db import get_conn

    now = datetime.now().isoformat(timespec="seconds")
    with get_conn() as c:
        c.execute(
            """INSERT INTO ai_fundflow_coherence_reports
                 (scope, scope_key, trade_date, window, correlation, summary, points,
                  conclusion, model_name, created_at, updated_at)
               VALUES(?,?,?,?,?,?,?,?,?,?,?)
               ON CONFLICT(scope, scope_key, trade_date, window) DO UPDATE SET
                 correlation=excluded.correlation, summary=excluded.summary, points=excluded.points,
                 conclusion=excluded.conclusion, model_name=excluded.model_name,
                 updated_at=excluded.updated_at""",
            (scope, scope_key, trade_date, window,
             coherence.get("correlation"), coherence.get("summary"),
             json.dumps(coherence.get("points") or [], ensure_ascii=False),
             coherence.get("conclusion"), model_name, now, now),
        )


def get_coherence_report(scope: str, scope_key: str | None = None,
                         window: str | None = None) -> dict | None:
    """读取最近一次组合级相关性报告（ai_fundflow_coherence_reports）。

    scope='indices'/'portfolio'；scope_key=逗号 codes 或逗号 tags 或 '全部'，
    传 None 时只按 scope（+window）取最近一条（前端 F5 后组合子集状态可能已变，作兜底）。
    window 指定时只在该时间窗内精确匹配；缺省取跨窗最近一条。无则 None。
    """
    from app.models.db import get_conn

    where, params = "WHERE scope=?", [scope]
    if scope_key:
        where += " AND scope_key=?"
        params.append(scope_key)
    if window:
        where += " AND window=?"
        params.append(window)
    sql = (f"SELECT scope, scope_key, trade_date, window, correlation, summary, points, conclusion, "
           f"model_name FROM ai_fundflow_coherence_reports {where} "
           f"ORDER BY trade_date DESC, updated_at DESC LIMIT 1")
    with get_conn() as c:
        row = c.execute(sql, params).fetchone()
    with get_conn() as c:
        row = c.execute(sql, params).fetchone()
    if not row:
        return None
    points = []
    try:
        points = json.loads(row["points"]) if row["points"] else []
    except Exception:  # noqa: BLE001 非 JSON 兜底空列表
        points = []
    return {
        "scope": row["scope"], "scope_key": row["scope_key"],
        "trade_date": row["trade_date"], "window": row["window"],
        "correlation": row["correlation"], "summary": row["summary"],
        "points": [str(x) for x in points if str(x).strip()],
        "conclusion": row["conclusion"], "model_name": row["model_name"],
    }


def analyze_batch_fundflow(tags: list[str] | None = None, window: int | str = "15m",
                           codes: list[str] | None = None,
                           weights: list[float] | None = None,
                           system_prompt: str | None = None) -> dict:
    """组合批量资金 AI 分析：所有有资金流数据的持仓/指数一次发给 AI（省 token），逐只精简输出并落库。

    窗口仅支持 15m 及以上（1m/5m 拒绝）；无激活模型/无有效标的 → ValueError。
    codes 提供时按指数组合相关性（scope='indices'），否则按持仓组合（scope='portfolio'）。
    返回 {mode, window, date, covered, total, stocks_count, reports, coherence}。
    system_prompt 非 None 时作为「用户附加要求」追加到默认指令后（前端弹窗可编辑）。
    """
    model_cfg = get_active_model()
    if not model_cfg:
        raise ValueError("尚未配置/启用任何 AI 模型（点右上角「🤖 AI」配置）")
    w = _norm_flow_window(window)
    if w in ("1m", "5m"):
        raise ValueError("批量分析窗口过小，请选择 15 分钟及以上")
    ctx = build_batch_fundflow_context(tags, w, codes=codes, weights=weights)
    if not ctx["stocks"]:
        raise ValueError("该组合暂无有资金流数据的标的，请先全量刷新")
    user = (
        "组合标的资金流数据（列表）：\n" + json.dumps(ctx, ensure_ascii=False, default=str) + "\n\n"
        "请输出严格 JSON，结构如下：\n" + json.dumps(_BATCH_FUNDFLOW_SCHEMA, ensure_ascii=False)
    )
    system = _BATCH_FUNDFLOW_SYSTEM
    if system_prompt:
        system = f"{system}\n\n[用户附加要求]\n{system_prompt}"
    try:
        raw = chat_json(model_cfg, system, user)
    except Exception as e:  # noqa: BLE001
        raise ValueError(str(e))
    name_map = {s["code"]: s["name"] for s in ctx["stocks"]}
    reports = []
    for item in raw.get("stocks") or []:
        if not isinstance(item, dict):
            continue
        code = str(item.get("code") or "").strip()
        if not code or code not in name_map:
            continue  # 只落库本组合内的标的
        analysis = _normalize_fundflow_analysis(item)
        try:
            _upsert_fundflow_report(code, ctx["date"], "batch", w, analysis,
                                    model_cfg.get("model") or model_cfg.get("name", ""))
        except Exception:  # noqa: BLE001 单只落库失败不阻断批量
            pass
        reports.append({
            "code": code, "name": name_map[code],
            "correlation": analysis["correlation"], "summary": analysis["summary"],
            "source": "batch",
        })
    # 组合级相关性：落库 ai_fundflow_coherence_reports，批量面板顶部展示
    coherence = _normalize_coherence(raw.get("coherence"))
    scope = "indices" if ctx["mode"] == "indices" else "portfolio"
    scope_key = ",".join(codes) if codes else (",".join(tags) if tags else "全部")
    try:
        _upsert_coherence_report(scope, scope_key, ctx["date"], w, coherence,
                                 model_cfg.get("model") or model_cfg.get("name", ""))
    except Exception:  # noqa: BLE001 组合相关性落库失败不阻断批量
        pass
    logger.info("[AI资金流-批量] %s %s%s 分析 %d/%d 只",
                scope, ctx["window"], "天" if w.endswith("d") else "分钟",
                len(reports), ctx["covered"])
    return {
        "mode": ctx["mode"], "window": w, "date": ctx["date"],
        "covered": ctx["covered"], "total": ctx["total"],
        "stocks_count": len(reports), "reports": reports,
        "coherence": coherence,
    }


# 弹窗展示给用户的可编辑重点要求。完整 system（含 JSON schema）用户看不懂，
# 只展示用户可能想改的「要求块」；用户改了才作为「用户附加要求」追加到完整指令后。
_EDITABLE_PROMPTS = {
    "stock": (
        "请从基本面、成长性、估值、股息、护城河、周期性、同业竞争七个维度分析该股，"
        "重点提示风险与交叉陷阱，给出明确评级与买卖建议。"
    ),
    "fundflow": (
        "请判断资金与股价的相关性/背离、主力资金意图（含大单拆小单的伪装），"
        "简明给出结论与注意点。"
    ),
    "batch": (
        "请逐只快速判断资金×股价相关性；对组合整体逐窗口横向对比，判断资金面联动格局"
        "（共振 / 跷跷板虹吸 / 分化），优先看成交金额，给出组合层面结论。"
    ),
    "portfolio": (
        "请从组合整体评估质量、估值、风险与成长性，给出得分（0-100）与评级（A/B/C/D）"
        "及改进建议。"
    ),
    "daily": (
        "请逐笔评估当日每笔交易合理性，再汇总当日整体操作给出评分与改进建议。"
    ),
}


def get_default_prompts() -> dict:
    """各 AI 分析入口展示给用户的「可编辑重点要求」（前端弹窗预览/编辑用）。

    完整 system（含 JSON schema）不暴露给用户；用户改了这里的内容才作为
    「用户附加要求」追加到完整指令后（见各 analyze/score 函数的组装）。
    """
    return dict(_EDITABLE_PROMPTS)


def get_stock_fundflow_report(code: str, window: str | None = None) -> dict | None:
    """该股最近落库资金流结果（跨 batch/single 取最新）。

    window 指定时只在该时间窗内精确匹配（不跨窗兜底，无该窗结果返回 None），
    用于个股页按当前选中窗口恢复展示；window=None 取跨窗最近一条（列表列）。
    无则 None。
    """
    from app.models.db import get_conn

    if window:
        sql, params = (
            """SELECT code, trade_date, source, window, correlation, summary, main_force, rhythm,
                      divergence, alerts, conclusion, model_name
               FROM ai_fundflow_reports WHERE code=? AND window=?
               ORDER BY trade_date DESC, updated_at DESC LIMIT 1""",
            (code, window),
        )
    else:
        sql, params = (
            """SELECT code, trade_date, source, window, correlation, summary, main_force, rhythm,
                      divergence, alerts, conclusion, model_name
               FROM ai_fundflow_reports WHERE code=?
               ORDER BY trade_date DESC, updated_at DESC LIMIT 1""",
            (code,),
        )
    with get_conn() as c:
        row = c.execute(sql, params).fetchone()
    if not row:
        return None

    def _loads(v):
        if not v:
            return []
        try:
            return json.loads(v)
        except Exception:  # noqa: BLE001 非 JSON 兜底空列表
            return []

    return {
        "code": row["code"], "trade_date": row["trade_date"], "source": row["source"],
        "window": row["window"], "correlation": row["correlation"],
        "summary": row["summary"], "main_force": row["main_force"], "rhythm": row["rhythm"],
        "divergence": _loads(row["divergence"]), "alerts": _loads(row["alerts"]),
        "conclusion": row["conclusion"], "model_name": row["model_name"],
    }


def list_fundflow_reports(codes: list[str]) -> dict:
    """按 codes 过滤，每只取最近一条落库资金流结果（供两页列表「资金面」列，一次拉取）。"""
    from app.models.db import get_conn

    out: dict[str, dict] = {}
    if not codes:
        return out
    ph = ",".join("?" * len(codes))
    with get_conn() as c:
        rows = c.execute(
            f"""SELECT code, trade_date, source, window, correlation, summary
                FROM ai_fundflow_reports WHERE code IN ({ph})
                ORDER BY trade_date DESC, updated_at DESC""",
            list(codes),
        ).fetchall()
    for r in rows:
        if r["code"] in out:
            continue  # 已取到该 code 最新一条
        out[r["code"]] = {
            "correlation": r["correlation"], "summary": r["summary"],
            "trade_date": r["trade_date"], "window": r["window"], "source": r["source"],
        }
    return out

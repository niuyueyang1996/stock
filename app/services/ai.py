"""AI 诊股服务：OpenAI 兼容大模型接入、多模型配置、个股结构化诊股报告。

- 模型配置存 ai_models 表，切换时 is_active 唯一。
- chat_json 调 OpenAI 兼容 /v1/chat/completions，优先 response_format json_object，
  HTTP 失败或 content 空时降级（去掉 json_object / reasoning_effort）再试；
  JSON 解析失败时本地修复 + 必要时请模型重发合法 JSON。
- build_stock_context 复用缓存汇总个股数据（读缓存零网络）作为结构化输入。
- analyze_stock 组装 prompt → 调 AI → 规整输出 → 存 ai_reports。
- 统一 ScoreCard：score/grade/action/risk/confidence + 10 维（含消息面/技术面）。
"""
import hashlib
import json
import logging
from datetime import date, datetime, timedelta

from app.config import HTTP_HEADERS, REQUEST_TIMEOUT
from app.models.db import get_conn

logger = logging.getLogger("ai")

# DeepSeek JSON Output 偶发空 content；输出预算过小也会截断。
# 组合/批量大任务（多只 + ≥1000 字 HTML）在高思考级下易被 reasoning 烧光（finish=length）。
# 实测 deepseek-v4-flash 接受 max_tokens=81920（5 倍余量，几十只持仓也够）；
# 用户可在「🤖 AI」弹窗改 config 表 ai_max_tokens / ai_request_timeout（见 get_max_tokens）。
_AI_MAX_TOKENS = 81920
_AI_MAX_TOKENS_SAFE = 16384

# 诊股 10 维度（共享 5 + 个股专属 5；固定，用于 prompt 与输出校验）
DIMENSIONS = [
    "cyclicality", "moat", "fundamentals", "growth", "dividend",
    "valuation", "competition", "fundflow", "news", "technical",
]
DIMENSION_CN = {
    "cyclicality": "周期性", "moat": "护城河", "fundamentals": "基本面",
    "growth": "增长", "dividend": "股息", "valuation": "估值", "competition": "同业竞争",
    "fundflow": "资金面", "news": "消息面", "technical": "技术面",
}
# dimensions 键名别名：AI 偶发把维度 JSON 键名写成中文（prompt 要求正文用中文维度名），映射回英文键
DIM_KEY_ALIASES = {k: (k, cn) for k, cn in DIMENSION_CN.items()}

# ---------- ScoreCard 公共定义（个股/组合/交易复用） ----------
GRADE_NAMES = {"A": "优秀", "B": "良好", "C": "一般", "D": "较差"}
ACTION_NAMES = {
    "add": "加仓", "hold": "持有", "watch": "观望", "reduce": "减仓", "exit": "清仓",
    "repeat": "可复制", "cautious": "谨慎复制", "avoid": "避免重复",
}
ACTION_STOCK_PORTFOLIO = frozenset({"add", "hold", "watch", "reduce", "exit"})
ACTION_TRADE = frozenset({"repeat", "cautious", "avoid"})
RISK_LEVELS = frozenset({"low", "medium", "high"})
SHARED_DIMENSIONS = ["fundamentals", "valuation", "fundflow", "news", "technical"]
PORTFOLIO_EXTRA_DIMENSIONS = ["structure", "tag_fit"]
TRADE_EXTRA_DIMENSIONS = ["timing", "execution", "sizing", "discipline"]
STOCK_EXTRA_DIMENSIONS = ["cyclicality", "moat", "growth", "dividend", "competition"]

_SCORE_RULES = (
    "[评分口径（强制）]\n"
    "1. score 0-100；grade 质量：A优秀/B良好/C一般/D较差。锚点：80+→A，60-79→B，40-59→C，<40→D；"
    "grade 须与 score 区间自洽，但由你直接给出，后端不做换算。\n"
    "2. action 操作建议与 grade 解耦：个股/组合用 add/hold/watch/reduce/exit；"
    "「好公司但现在贵」应 grade=A + action=watch。\n"
    "3. risk 0-100（越高风险越大）与 risk_level low|medium|high；可与 grade 解耦（高质量高风险允许）。\n"
    "4. confidence high|medium|low：缺数据时降级并写明缺什么，禁止臆造。\n"
    "5. 每维输出 {score, grade, analysis}；action 须被 advice 支撑并给触发条件。\n"
)


def check_grade(r) -> str:
    """校验 AI 质量评级 ∈ {A,B,C,D}；非法兜底 C（不做 score→grade 换算）。"""
    g = str(r or "C").upper().strip()
    return g if g in GRADE_NAMES else "C"


def check_action(a, allowed) -> str:
    """校验 action ∈ allowed；非法时个股/组合兜底 hold，交易兜底 cautious。"""
    v = str(a or "").lower().strip()
    if v in allowed:
        return v
    # 交易复盘集合含 cautious 不含 hold；个股/组合反之
    return "cautious" if "cautious" in allowed and "hold" not in allowed else "hold"


def check_risk_level(v) -> str:
    """校验 risk_level ∈ low|medium|high；非法兜底 medium。"""
    lv = str(v or "medium").lower().strip()
    return lv if lv in RISK_LEVELS else "medium"


def risk_level_from_score(risk: float) -> str:
    """由 risk 分数推导档位：<35 low，<65 medium，否则 high。"""
    try:
        r = float(risk)
    except (TypeError, ValueError):
        return "medium"
    if r < 35:
        return "low"
    if r < 65:
        return "medium"
    return "high"


def _normalize_dim_block(d, *, with_stock_extras: bool = False) -> dict:
    """规整单维 {score, grade, analysis[, risk, data_source]}。"""
    if not isinstance(d, dict):
        d = {}
    try:
        score = max(0, min(100, int(float(d.get("score", 50)))))
    except (TypeError, ValueError):
        score = 50
    out = {
        "score": score,
        "grade": check_grade(d.get("grade") or d.get("rating")),
        "analysis": str(d.get("analysis", "")),
    }
    if with_stock_extras:
        out["risk"] = check_risk_level(d.get("risk"))
        src = str(d.get("data_source", "provided")).lower()
        out["data_source"] = "supplemented" if src == "supplemented" else "provided"
    return out


def upgrade_legacy_card(report: dict) -> dict:
    """内存兼容：老字段映射到 ScoreCard 新键；不回写 DB、不改入参原对象语义外字段。

    rating→grade、rating_name→grade_name、risk_score→risk；补 action/confidence 缺省。
    """
    if not isinstance(report, dict):
        return {}
    out = dict(report)
    grade = check_grade(out.get("grade") or out.get("rating"))
    out["grade"] = grade
    out["grade_name"] = GRADE_NAMES[grade]
    out["rating"] = grade  # 兼容旧前端
    out["rating_name"] = GRADE_NAMES[grade]
    if "risk" not in out and "risk_score" in out:
        try:
            out["risk"] = max(0, min(100, int(float(out["risk_score"]))))
        except (TypeError, ValueError):
            out["risk"] = 50
    elif "risk" in out:
        try:
            out["risk"] = max(0, min(100, int(float(out["risk"]))))
        except (TypeError, ValueError):
            out["risk"] = 50
    if "risk_score" not in out and "risk" in out:
        out["risk_score"] = out["risk"]
    if "risk_level" not in out or out.get("risk_level") not in RISK_LEVELS:
        out["risk_level"] = risk_level_from_score(out.get("risk", 50))
    if "action" not in out or not out.get("action"):
        out["action"] = "hold"
    act = str(out["action"]).lower().strip()
    if act not in ACTION_NAMES:
        act = "hold"
        out["action"] = act
    out["action_name"] = ACTION_NAMES.get(act, ACTION_NAMES["hold"])
    conf = str(out.get("confidence") or "medium").lower().strip()
    out["confidence"] = conf if conf in ("high", "medium", "low") else "medium"
    if "score" in out:
        try:
            out["score"] = max(0, min(100, float(out["score"])))
        except (TypeError, ValueError):
            pass
    return out


_SYSTEM_PROMPT = (
    "你是资深股票分析师。根据给定的个股结构化数据，从周期性、护城河、基本面、增长、股息、估值、同业竞争、"
    "资金面、消息面、技术面 10 个维度分析该股，给出总分 score、质量评级 grade（A=优秀/B=良好/C=一般/D=较差）、"
    "操作建议 action（add/hold/watch/reduce/exit，与 grade 解耦）、风险分 risk（0-100）与原因。\n\n"
    f"{_SCORE_RULES}\n"
    # _ASOF_RULES 在模块后部定义；analyze 时与消息/技术共用同一时效规则（见下方拼接处）
    "[数据使用规范]\n"
    "1. 系统提供的所有结构化字段都必须纳入分析（行情/估值/财务/分位/资金流/日K bars/消息摘要/持仓），"
    "不得只挑少数指标或只看总分——漏用数据视为不合格。\n"
    "2. 字段带 *_source / *_confidence 后缀表示可靠度：user=你的输入、ttm=滚动口径、latest_report=最新财报、"
    "zero_conservative=零增长保守假设；confidence 为 high/medium/low/invalid。来源为 user 的优先采信；"
    "zero_conservative / low / invalid 的须保守解读并在报告注明。\n"
    "3. *_static 后缀=按去年年报口径，无后缀/ttm=滚动口径；两者明显背离时，分析差异原因（如新并购、周期顶回落），不得只报其一。\n"
    "4. 资金流字段为净流入（元，正=流入负=流出）；五档 super=超大单/large=大单/medium=中单/small=小单/xs=特小单，"
    "main=主力（超大+大单）。当日分时 15 分钟序列（intraday_15m：每窗口五档净额，main=单窗口主力/cum=累计主力）"
    "与近30日累计反映资金趋势，须结合看。\n"
    "5. bars 为截至 as_of_datetime 的日K；news_report/tech_report 若存在则优先采信其摘要（stance/trend/summary），"
    "过时或 omit_reason 说明缺数时勿臆造。消息面无正文库时按 as_of 用领域知识补充并标 [AI补充]。\n"
    "6. 若某维度数据缺失或不足（如同业竞争对比、行业景气度），可用你的领域知识补充，但必须：\n"
    "   a) 在该维度 analysis 中明确标注「[AI补充]」；\n"
    "   b) 注明补充数据的时效（如「截至2026年」），确保有时效性、不用过时数据；\n"
    "   c) 无法确认时效时明确说明；字段为 null 表示系统无此数据，不得臆造数值。\n"
    "7. data_source 字段：provided=基于系统数据；supplemented=AI 补充/推断。\n"
    "8. 输出语言与键名：所有文字字段（维度 analysis、cross_analysis.explanation、summary、reasons、html、"
    "预期增速依据）一律使用简体中文，禁止混入英文单词；若在正文中提及维度，用中文名"
    "（周期性、护城河、基本面、增长、股息、估值、同业竞争、资金面、消息面、技术面），不要写 growth/fundamentals 等英文。\n"
    "   JSON 键名必须保持英文、与下方输出结构完全一致，不得改成中文；尤其 dimensions 的 10 个键固定为 "
    "cyclicality/moat/fundamentals/growth/dividend/valuation/competition/fundflow/news/technical，禁止改名。\n"
    "9. expected_growth 字段：基于系统财务数据（最新财报同比、TTM 同比、ROE、支付率、前瞻指标）与陷阱判断，"
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
    "3. 最终 score / grade / action / risk / summary 必须综合陷阱判断，不可仅按孤立维度加权。\n"
    "严格输出 JSON，不要输出任何额外文字。"
)


# HTML 深度报告要求：仅「深入」强度追加（快速/普通不要求生成 HTML 报告）
_HTML_REQUIREMENT = (
    "同时生成一份完整、独立、可读性强的 HTML 诊股报告（字段 html），供用户新开页面查看。\n"
    "[HTML 深度分析强制规范]\n"
    "1. HTML 正文（简体中文）必须 ≥1000 字，必须有实质分析；禁止只列要点、禁止泛泛复述 prompt 结构、禁止空话套话，浅尝辄止视为不合格重写。\n"
    "2. 结构必须覆盖：核心结论 / 商业模式与护城河 / 财务与盈利质量 / 成长驱动 / "
    "资金面（主力/五档/近30日资金流）/ 消息面与技术面 / 估值判断（含前瞻）/ 风险与陷阱 / 情景推演 / 操作建议分级。\n"
    "3. 每个判断必须带具体数字与上下文（如「当前 PE 12.3 处近 1 年约 15% 分位，前瞻 PE 仅 9.8，"
    "但增长来源为 zero_conservative 保守假设」）；禁止「估值偏低」「基本面扎实」这类无数字断言。\n"
    "4. 操作建议分级：加仓/持有/观望/减仓/清仓，每档给出触发条件，不得含糊；与顶层 action 一致。\n"
    "5. HTML 为独立成文，离开本应用页面即可读懂；已提供的行情/财务/估值等原始数据用户在本应用已可查看，"
    "HTML 中**不得原样罗列数据表格**；把篇幅全部用于深入分析与洞察。\n"
    "6. 质量自检：写完后逐段检查——这段是否提供了用户在数据表上看不到的洞察？若没有，重写。\n"
    "要求：自包含单文件、内联 CSS、不引用任何外部资源、不得包含 <script> 或任何可执行代码；"
    "简体中文；结构清晰、排版美观、层次分明。\n"
)

_OUTPUT_SCHEMA = {
    "score": "0-100 总分",
    "grade": "A|B|C|D 质量评级",
    "action": "add|hold|watch|reduce|exit",
    "risk": "0-100 风险分",
    "risk_level": "low|medium|high",
    "confidence": "high|medium|low",
    "dimensions": {
        k: {
            "score": "0-100 该维度健康度/吸引力评分",
            "grade": "A|B|C|D",
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


# ---------- OpenAI 兼容 URL ----------

def _openai_compat_url(base_url: str, path: str) -> str:
    """拼 OpenAI 兼容端点。base_url 可带或不带 /v1，避免 /v1/v1。

    例：https://api.deepseek.com → .../v1/chat/completions
        https://api.openai.com/v1 → .../v1/chat/completions
    """
    base = (base_url or "").strip().rstrip("/")
    path = (path or "").lstrip("/")
    if base.endswith("/v1"):
        return f"{base}/{path}"
    return f"{base}/v1/{path}"


# ---------- 可用模型列表（从提供商获取） ----------

def list_available_models(base_url: str, api_key: str) -> list[str]:
    """调 OpenAI 兼容 /v1/models 列出该提供商可用模型名。失败抛 ValueError。"""
    import requests

    url = _openai_compat_url(base_url, "models")
    headers = {**HTTP_HEADERS, "Authorization": f"Bearer {api_key.strip()}"}
    try:
        resp = requests.get(url, headers=headers, timeout=REQUEST_TIMEOUT)
        resp.raise_for_status()
        data = resp.json()
        return [str(m.get("id")) for m in data.get("data", []) if m.get("id")]
    except Exception as e:  # noqa: BLE001
        raise ValueError(f"获取模型列表失败（请检查 Base URL 与 API Key）: {e}")


# ---------- OpenAI 兼容调用 ----------

# AI 调用超时（组合/批量深入 + 16384 输出预算生成更久，放宽到 300s）
AI_REQUEST_TIMEOUT = 300


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


def get_max_tokens() -> int:
    """当前 AI 输出预算（config 表 ai_max_tokens，缺省 _AI_MAX_TOKENS=81920）。

    组合/批量大任务（几十只持仓 + 整组合 HTML）需要大预算；用户可在「🤖 AI」弹窗调整。
    """
    try:
        with get_conn() as c:
            row = c.execute("SELECT value FROM config WHERE key='ai_max_tokens'").fetchone()
        if row and str(row["value"] or "").strip().isdigit():
            return max(2048, min(262144, int(row["value"])))
    except Exception:  # noqa: BLE001 读配置失败不影响调用
        pass
    return _AI_MAX_TOKENS


def get_request_timeout() -> int:
    """当前 AI 请求超时秒数（config 表 ai_request_timeout，缺省 AI_REQUEST_TIMEOUT=300）。"""
    try:
        with get_conn() as c:
            row = c.execute("SELECT value FROM config WHERE key='ai_request_timeout'").fetchone()
        if row and str(row["value"] or "").strip().isdigit():
            return max(30, min(1800, int(row["value"])))
    except Exception:  # noqa: BLE001 读配置失败不影响调用
        pass
    return AI_REQUEST_TIMEOUT


# 「分析强度」→ 追加进 system 的指令：快速=简要、深入=详尽；普通不加
_INTENSITY_INSTRUCTIONS = {
    "fast": "请快速简要分析，突出核心结论即可，不必逐项展开细节。",
    "deep": "请深入详尽分析，逐维度展开推演，给出充分依据、量化佐证与风险提示。",
}


def _intensity_instruction(intensity: str | None) -> str:
    """分析强度指令；非法/缺省（普通）→ 空串（不改动默认指令）。"""
    return _INTENSITY_INSTRUCTIONS.get(str(intensity or "").lower().strip(), "")


_DATA_RECEIVED_SCHEMA = "逐项列出系统实际传递给你的数据（维度名+数量；缺失/为空标注「无」），用于日志核对输入"
_DATA_RECEIVED_REQ = (
    "输出必须包含 data_received 字段：逐项列出系统实际传递给你的数据（如「日K 120根、周K 60根、月K 36根、"
    "财务、估值分位、资金流、新闻 5条」），缺失/为空的数据也要如实标注「无」；用于日志核对输入完整性。"
)


def _schema_user(label: str, ctx: dict, schema: dict) -> str:
    """组装 AI user 消息：输出格式（schema）写在开头与结尾双端。

    模型对 prompt 首尾信息权重更高（首因+近因效应），开头重申「只输出严格 JSON、结构完全遵循
    schema」，中间夹输入数据，结尾再给权威 schema + 严格指令——双端强化可减少跑偏/乱答。
    """
    schema = dict(schema)
    schema["data_received"] = _DATA_RECEIVED_SCHEMA
    schema_txt = json.dumps(schema, ensure_ascii=False)
    return (
        f"{label}\n\n"
        "【输出格式·开头重申】请只输出一个严格 JSON 对象，结构必须与下面完全一致，"
        "不要 markdown 围栏、不要任何额外文字：\n" + schema_txt + "\n\n"
        "【输入数据】\n" + json.dumps(ctx, ensure_ascii=False, default=str) + "\n\n"
        "【输出格式·结尾确认】请输出严格 JSON，结构如下（与开头完全一致，不要任何额外文字）：\n"
        + schema_txt
        + f"\n\n{_DATA_RECEIVED_REQ}"
    )


def _http_error_detail(resp) -> str:
    """从失败响应提取简短可读原因（状态码 + 截断 body）。"""
    status = getattr(resp, "status_code", "?")
    body = ""
    try:
        body = (resp.text or "").strip().replace("\n", " ")
    except Exception:  # noqa: BLE001
        body = ""
    if len(body) > 400:
        body = body[:400] + "…"
    return f"HTTP {status}" + (f"：{body}" if body else "")


def _extract_message_content(data: dict) -> tuple[str, str]:
    """从 chat/completions JSON 取 assistant content 与 finish_reason。

    content 可能为 null（DeepSeek JSON 模式偶发空返回；思考模式也可能只填 reasoning_content）。
    content 空白时回退 reasoning_content，避免思考模型被误判为空。
    """
    choices = data.get("choices") if isinstance(data, dict) else None
    if not choices:
        raise ValueError("AI 响应无 choices（模型名/额度/接口异常，请核对配置与余额）")
    choice0 = choices[0] if isinstance(choices[0], dict) else {}
    msg = choice0.get("message") if isinstance(choice0.get("message"), dict) else {}
    content = msg.get("content")
    text = "" if content is None else str(content)
    if not text.strip():
        reasoning = msg.get("reasoning_content")
        if reasoning is not None and str(reasoning).strip():
            text = str(reasoning)
    finish = str(choice0.get("finish_reason") or "")
    return text, finish


def _post_chat_completion(url: str, headers: dict, payload: dict):
    """POST 一次 chat/completions，返回 (content, finish_reason)。失败抛 ValueError。"""
    import requests

    timeout = get_request_timeout()
    try:
        resp = requests.post(url, headers=headers, json=payload, timeout=timeout)
    except Exception as e:  # noqa: BLE001 网络/超时
        raise ValueError(f"AI 接口网络失败: {e}") from e
    if resp.status_code >= 400:
        raise ValueError(f"AI 接口调用失败（{_http_error_detail(resp)}）")
    try:
        data = resp.json()
    except Exception as e:  # noqa: BLE001
        raise ValueError(f"AI 响应非 JSON: {e}") from e
    return _extract_message_content(data)


def _strip_code_fence(txt: str) -> str:
    """去掉 ``` / ```json 围栏。"""
    s = (txt or "").strip()
    if not s.startswith("```"):
        return s
    s = s[3:]
    if s.lower().startswith("json"):
        s = s[4:]
    s = s.lstrip("\r\n")
    if s.endswith("```"):
        s = s[:-3]
    return s.strip()


def _extract_json_blob(txt: str) -> str:
    """取首个 { 到末个 }（忽略前后废话）。"""
    start = txt.find("{")
    end = txt.rfind("}")
    if start >= 0 and end > start:
        return txt[start : end + 1]
    return txt


def _fix_trailing_commas(txt: str) -> str:
    """去掉 }, ] 前的多余逗号（模型常见笔误）。"""
    import re

    prev = None
    s = txt
    while prev != s:
        prev = s
        s = re.sub(r",(\s*[}\]])", r"\1", s)
    return s


def _normalize_json_quotes(txt: str) -> str:
    """智能引号 → 标准 ASCII 引号。"""
    return (
        txt.replace("\u201c", '"').replace("\u201d", '"')
        .replace("\u2018", "'").replace("\u2019", "'")
    )


def _escape_raw_control_chars(txt: str) -> str:
    """把字符串字面量里的原文控制字符（换行/回车/Tab）转义为 JSON 转义序列。

    JSON 不允许字符串里有原文换行；模型写多行中文文本时常见，json.loads 会报
    Invalid control character（实测复现：summary 字段含原文 \\n → 解析失败）。
    只在「引号内且非转义序列」处替换，不误伤结构空白与已有转义（如 \\\\n 保留原样）。
    """
    out: list[str] = []
    in_str = False
    escaped = False
    for ch in txt:
        if in_str:
            if escaped:
                out.append(ch)   # 转义序列的延续字符（如 \\n 的 n），原样保留
                escaped = False
            elif ch == "\\":
                out.append(ch)
                escaped = True
            elif ch == '"':
                in_str = False
                out.append(ch)
            elif ch == "\n":
                out.append("\\n")
            elif ch == "\r":
                out.append("\\r")
            elif ch == "\t":
                out.append("\\t")
            elif ord(ch) < 0x20:
                out.append("\\u{:04x}".format(ord(ch)))
            else:
                out.append(ch)
        else:
            if ch == '"':
                in_str = True
            out.append(ch)
    return "".join(out)


def _repair_brackets(txt: str) -> str:
    """按栈修补括号：漏写 ]/}、错关、尾部截断；字符串字面量内不改。

    典型坏例：`"reasons": ["a", "b"}` → 期望 ] 却遇到 }，先插入 ]。
    """
    out: list[str] = []
    stack: list[str] = []  # 期望的闭合符
    in_str = False
    escape = False
    for ch in txt:
        if in_str:
            out.append(ch)
            if escape:
                escape = False
            elif ch == "\\":
                escape = True
            elif ch == '"':
                in_str = False
            continue
        if ch == '"':
            in_str = True
            out.append(ch)
            continue
        if ch == "{":
            stack.append("}")
            out.append(ch)
            continue
        if ch == "[":
            stack.append("]")
            out.append(ch)
            continue
        if ch in "}]":
            # 错关：先补上栈顶期望的闭合符，再消费当前字符
            while stack and stack[-1] != ch:
                out.append(stack.pop())
            if stack and stack[-1] == ch:
                stack.pop()
            out.append(ch)
            continue
        out.append(ch)
    if in_str:
        out.append('"')
    while stack:
        out.append(stack.pop())
    return "".join(out)


def _try_salvage_html_field(txt: str) -> dict | None:
    """html 字段里常有未转义的双引号导致整段 JSON 崩掉。

    策略：从 `"html": "` 切开，前面当普通 JSON 解析，后面直到最后一个 `"\\s*}` 当作 html 原文。
    """
    import re

    m = re.search(r'"html"\s*:\s*"', txt)
    if not m:
        return None
    head = txt[: m.start()].rstrip().rstrip(",")
    if not head.endswith("{"):
        head = head + "}"
    else:
        head = head + "}"
    try:
        obj = json.loads(_fix_trailing_commas(head))
    except (ValueError, TypeError):
        return None
    if not isinstance(obj, dict):
        return None
    rest = txt[m.end() :]
    end = None
    for mm in re.finditer(r'"\s*\}', rest):
        end = mm
    if end is None:
        return None
    html_raw = rest[: end.start()]
    # 还原常见转义（模型若部分转义了）
    obj["html"] = (
        html_raw.replace("\\r\\n", "\n").replace("\\n", "\n")
        .replace('\\"', '"').replace("\\\\", "\\")
    )
    return obj


def _loads_json_lenient(txt: str) -> dict:
    """逐步放宽解析：原样 → 抽对象 → 修逗号/引号/括号 → 抢救 html 字段。"""
    candidates: list[str] = []
    raw = (txt or "").strip()
    if raw:
        candidates.append(raw)
    blob = _extract_json_blob(raw)
    if blob and blob not in candidates:
        candidates.append(blob)

    last_err: Exception | None = None

    def _try(variant: str) -> dict | None:
        nonlocal last_err
        try:
            obj = json.loads(variant)
            if isinstance(obj, dict):
                return obj
            raise ValueError(f"AI 输出不是 JSON 对象（got {type(obj).__name__}）")
        except (ValueError, TypeError) as e:
            last_err = e
            return None

    for base in list(candidates):
        for variant in (
            base,
            _fix_trailing_commas(base),
            _fix_trailing_commas(_normalize_json_quotes(base)),
            _fix_trailing_commas(_escape_raw_control_chars(base)),
            _fix_trailing_commas(_escape_raw_control_chars(_normalize_json_quotes(base))),
        ):
            if variant not in candidates:
                candidates.append(variant)
            got = _try(variant)
            if got is not None:
                return got

    # 括号修补（漏 ]、截断等）——在逗号/引号/控制字符修复之后再试
    for base in list(candidates):
        for repaired in (
            _fix_trailing_commas(_repair_brackets(_normalize_json_quotes(base))),
            _fix_trailing_commas(
                _escape_raw_control_chars(_repair_brackets(_normalize_json_quotes(base)))
            ),
        ):
            if repaired not in candidates:
                candidates.append(repaired)
            got = _try(repaired)
            if got is not None:
                return got

    for base in candidates:
        salvaged = _try_salvage_html_field(base)
        if salvaged is not None:
            return salvaged
        salvaged = _try_salvage_html_field(_repair_brackets(base))
        if salvaged is not None:
            return salvaged

    raise ValueError(str(last_err) if last_err else "无法解析 JSON")


def _parse_json_content(content: str) -> dict:
    """把模型返回文本解析为 dict（支持 ```json 围栏 + 常见坏 JSON 修复）。"""
    txt = _strip_code_fence(str(content or ""))
    if not txt.strip():
        raise ValueError(
            "AI 返回空内容（接口 HTTP 成功但 message.content 为空；"
            "DeepSeek JSON 模式偶发此问题，或思考级别/模型不兼容。请重试，或换模型/降低思考级别）"
        )
    try:
        return _loads_json_lenient(txt)
    except (ValueError, TypeError) as e:
        # 日志带出错附近片段，方便对照
        hint = ""
        msg = str(e)
        if "column" in msg or "char" in msg:
            import re
            m = re.search(r"char (\d+)", msg)
            if m:
                i = int(m.group(1))
                lo, hi = max(0, i - 60), min(len(txt), i + 60)
                hint = f" | near=…{txt[lo:hi]!r}…"
        logger.warning("[AI] JSON 解析失败: %s%s", e, hint)
        raise ValueError(f"AI 输出解析失败: {e}") from e


def chat_json(model_cfg: dict, system: str, user: str, effort: str | None = None,
              max_tokens: int | None = None) -> dict:
    """调 OpenAI 兼容接口并解析 JSON 输出。失败抛 ValueError。输入输出均打印日志。

    - 附带 reasoning_effort（思考级别，默认 high 最高）；provider 不支持会被忽略。
    - effort 非 None 时覆盖全局思考级别；None 用用户配置的全局思考级别（config ai_reasoning_effort）。
    - max_tokens 输出预算，缺省 _AI_MAX_TOKENS（16384，组合/批量大任务给足空间）。
    - 首次失败（HTTP 错 / content 空）会去掉 response_format 与 reasoning_effort 再试一次；
      若大 max_tokens 被提供商拒绝，重试降回 _AI_MAX_TOKENS_SAFE。
    - JSON 解析失败时再请求模型重发一轮合法 JSON（不重复整段业务推理指令）。
    """
    url = _openai_compat_url(model_cfg["base_url"], "chat/completions")
    limit = int(max_tokens or get_max_tokens())
    payload = {
        "model": model_cfg["model"],
        "messages": [
            {"role": "system", "content": system},
            {"role": "user", "content": user},
        ],
        "temperature": 0.4,
        "max_tokens": limit,
        "response_format": {"type": "json_object"},
    }
    if effort is None:
        effort = get_reasoning_effort()
    if effort:
        payload["reasoning_effort"] = effort
    # 打印输入日志（截断超长）
    logger.info("[AI] 请求 %s model=%s | reasoning=%s | max_tokens=%s | system=%s | user=%s",
                url, model_cfg["model"], effort or "-", limit, system[:300], user[:2000])
    headers = {**HTTP_HEADERS, "Authorization": f"Bearer {model_cfg['api_key']}"}

    content, finish = "", ""
    first_err: Exception | None = None
    try:
        content, finish = _post_chat_completion(url, headers, payload)
        if not str(content or "").strip():
            raise ValueError(
                f"AI 返回空内容（finish_reason={finish or '-'}；"
                "常见于 DeepSeek JSON 模式偶发空返回）"
            )
    except Exception as e:  # noqa: BLE001 优先 json_object，失败/空内容降级普通文本
        first_err = e
        logger.warning("[AI] json_object 调用失败或空内容，降级普通文本：%s", e)
        payload.pop("response_format", None)
        payload.pop("reasoning_effort", None)  # provider 不支持时一并移除
        if limit > _AI_MAX_TOKENS_SAFE:
            payload["max_tokens"] = _AI_MAX_TOKENS_SAFE   # 提供商拒绝大 max_tokens → 降级重试
            logger.warning("[AI] max_tokens %s 可能不被接受，降级 %s 重试", limit, _AI_MAX_TOKENS_SAFE)
        try:
            content, finish = _post_chat_completion(url, headers, payload)
        except Exception as e2:  # noqa: BLE001
            raise ValueError(f"AI 接口调用失败: {e2}（首次：{first_err}）") from e2

    logger.info("[AI] 响应 %s | finish=%s | content=%s",
                url, finish or "-", str(content or "")[:2000])
    try:
        parsed = _parse_json_content(content)
        if isinstance(parsed, dict) and parsed.get("data_received"):
            logger.info("[AI] data_received=%s", parsed["data_received"])
        return parsed
    except ValueError as parse_err:
        # 解析失败：请模型只重发合法 JSON（带上坏输出片段便于对照）
        logger.warning("[AI] 解析失败，请求模型重发合法 JSON：%s", parse_err)
        repair_payload = {
            "model": model_cfg["model"],
            "messages": [
                {
                    "role": "system",
                    "content": (
                        "你是 JSON 校正器。用户会给你一段不合法的模型输出。"
                        "请只输出一个合法 JSON 对象：修正语法（未转义引号、尾逗号、截断等），"
                        "保留原有字段与含义；不要 markdown 围栏，不要解释。"
                    ),
                },
                {
                    "role": "user",
                    "content": (
                        f"解析错误：{parse_err}\n\n"
                        f"请修复为合法 JSON：\n{str(content or '')[:12000]}"
                    ),
                },
            ],
            "temperature": 0.1,
            "max_tokens": _AI_MAX_TOKENS,
            "response_format": {"type": "json_object"},
        }
        try:
            content2, finish2 = _post_chat_completion(url, headers, repair_payload)
        except Exception as e_rep:  # noqa: BLE001
            # 部分 provider 不接受 response_format：再试一次无 format
            logger.warning("[AI] JSON 重发（带 format）失败，降级：%s", e_rep)
            repair_payload.pop("response_format", None)
            try:
                content2, finish2 = _post_chat_completion(url, headers, repair_payload)
            except Exception as e_rep2:  # noqa: BLE001
                raise ValueError(f"{parse_err}；重发亦失败：{e_rep2}") from e_rep2
        logger.info("[AI] 重发响应 | finish=%s | content=%s",
                    finish2 or "-", str(content2 or "")[:2000])
        parsed2 = _parse_json_content(content2)
        if isinstance(parsed2, dict) and parsed2.get("data_received"):
            logger.info("[AI] data_received=%s", parsed2["data_received"])
        return parsed2


# ---------- 个股数据汇总（结构化输入） ----------

def build_stock_context(code: str) -> dict:
    """汇总该股缓存数据（读缓存零网络）为结构化 JSON。"""
    ctx = {"code": code}
    from app.analysis.portfolio import compute_portfolio
    from app.analysis.valuation import compute_live, get_quantiles
    from app.data.cache import get_daily_fundflow, get_daily_fundflows, get_financials, get_fundflow_min
    from app.data.fundflow import FUNDFLOW_HISTORY_DAYS, intraday_window_series
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
                start = (date.fromisoformat(end) - timedelta(days=FUNDFLOW_HISTORY_DAYS)).isoformat()
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

    # 时效 + 技术面日/周/月K + 专项报告摘要（阶段二 ScoreCard）
    as_of = now_as_of_datetime()
    ctx["as_of_datetime"] = as_of
    try:
        ctx.update(build_technical_bars_multi(code, as_of))
    except Exception:  # noqa: BLE001
        ctx["bars"] = ctx["weekly_bars"] = ctx["monthly_bars"] = []
    try:
        nr = get_stock_news_report(code)
        if nr:
            ctx["news_report"] = {
                "stance": nr.get("stance"), "summary": nr.get("summary"),
                "omit_reason": nr.get("omit_reason"), "as_of": nr.get("as_of"),
            }
    except Exception:  # noqa: BLE001
        pass
    try:
        tr = get_stock_tech_report(code)
        if tr:
            ctx["tech_report"] = {
                "trend_short": tr.get("trend_short"), "trend_mid": tr.get("trend_mid"),
                "summary": tr.get("summary"), "as_of": tr.get("as_of"),
            }
    except Exception:  # noqa: BLE001
        pass
    return ctx


# ---------- 时效机制（消息面/技术面共用） ----------

def now_as_of_datetime() -> str:
    """本地时区当前时间 ISO 串（带时区，如 2026-08-09T15:21:00+08:00）。

    所有涉及「时效」的 AI 调用（消息面/技术面）都把它作为 as_of_datetime 注入 user JSON，
    由 AI 自行判定相对该时刻哪些信息/结论仍成立。
    """
    return datetime.now().astimezone().isoformat(timespec="seconds")


# 时效规则：注入所有基于公开知识/时点数据的 AI 调用。AI 必须相对 as_of_datetime 判定，
# 不得拿训练截止时间当「现在」；过时/不确信宁可不返回。阶段二的诊股/组合打分复用同一片段。
_ASOF_RULES = (
    "[时效规则（强制）]\n"
    "1. 请求里的 as_of_datetime 是当前时间，视为「现在」；不得以你的训练数据截止时间当「现在」。\n"
    "2. 输出的每条信息必须相对 as_of_datetime 仍成立、仍有时效；已过时、已发生重大变化、"
    "或无法确认是否仍成立的事件/结论一律不得输出。\n"
    "3. 禁止编造标题/日期/数字/机构名凑内容；不确定时效宁可不提。\n"
    "4. 无任何可用信息时：items 输出空数组 [] 并在 omit_reason 说明原因，不要硬凑结论。\n"
    "5. 带日期的条目必须给 event_date（YYYY-MM-DD），缺日期的条目不输出。\n"
)

# 诊股 system 在定义处尚未拿到 _ASOF_RULES；此处补上（与 _SCORE_RULES 并列）
_SYSTEM_PROMPT = f"{_SYSTEM_PROMPT}\n{_ASOF_RULES}"


def _bar_dict(r) -> dict:
    """日K缓存行 → AI 技术面点（统一字段，缺省 None 兜底）。"""
    return {
        "date": r["trade_date"],
        "open": r["open"], "high": r["high"], "low": r["low"],
        "close": r["close"], "volume": r["volume"],
        "pct_change": r["pct_change"],
    }


def _trade_day_rows(rows: list) -> list:
    """过滤非交易日（周末）行：旧版可能把周末假K写入缓存，AI 输入兜底。"""
    from app.market.calendar import is_trade_day

    return [r for r in rows if is_trade_day(r["trade_date"])]

def _tech_end_day(as_of_datetime: str) -> str:
    """as_of 时刻对应的「有效交易日」（所有技术面 bars 的终点）。

    非交易日回退最近交易日；交易日未开盘（<09:30）回退上一交易日——当日尚无K线数据，
    避免把「昨收当现价」的占位行当当日K喂给 AI。
    """
    from app.market.calendar import has_market_opened, is_trade_day, last_trade_date

    dt = datetime.fromisoformat(as_of_datetime)
    d = dt.date()
    if is_trade_day(d) and not has_market_opened(dt):
        return last_trade_date(d - timedelta(days=1)).isoformat()
    return last_trade_date(d).isoformat()



def build_technical_bars(code: str, as_of_datetime: str, limit: int = 120) -> list[dict]:
    """该股截至 as_of 的最近日K（升序，尾部 limit 根），供技术面 AI 分析。

    end = resolve_trade_day(as_of)（非交易日自动截到最近交易日），start≈800 自然日前；
    无数据返回 []（调用方按「该维缺数」处理，不抛错）。读缓存零网络。
    阶段二的打分 context 也会复用这个函数。
    """
    from app.data.cache import get_daily_prices

    end = _tech_end_day(as_of_datetime)
    start = (date.fromisoformat(end) - timedelta(days=800)).isoformat()
    rows = _trade_day_rows(get_daily_prices(code, start, end))
    return [_bar_dict(r) for r in rows[-limit:]]


def build_technical_bars_many(codes: list[str], as_of_datetime: str, limit: int = 120) -> dict[str, list]:
    """批量版：一次查多只的日K（get_daily_prices_many，避免逐只开连），返回 {code: bars}。"""
    from app.data.cache import get_daily_prices_many

    end = _tech_end_day(as_of_datetime)
    start = (date.fromisoformat(end) - timedelta(days=800)).isoformat()
    rows_map = get_daily_prices_many(list(codes), start, end)
    return {c: [_bar_dict(r) for r in _trade_day_rows(rows_map.get(c) or [])[-limit:]] for c in codes}


def build_technical_bars_multi(code: str, as_of_datetime: str,
                               daily_limit: int = 120, weekly_limit: int = 60,
                               monthly_limit: int = 36) -> dict:
    """该股截至 as_of 的日/周/月三套K线（各升序、尾部 limit 根），供技术面 AI 分析。

    日K读 daily_price_cache（约 800 自然日窗口），周/月K读腾讯增量缓存
    （weekly/monthly_price_cache）；任一为空返回空列表，调用方按「该维缺数」处理。
    读缓存零网络。诊股/评分复用本函数（缩小 limit 控 token）。
    """
    from app.data.cache import get_daily_prices, get_period_prices

    end = _tech_end_day(as_of_datetime)
    start = (date.fromisoformat(end) - timedelta(days=800)).isoformat()
    daily = [_bar_dict(r) for r in _trade_day_rows(get_daily_prices(code, start, end))]
    weekly = [_bar_dict(r) for r in _trade_day_rows(get_period_prices("weekly_price_cache", code, "1970-01-01", end))]
    monthly = [_bar_dict(r) for r in _trade_day_rows(get_period_prices("monthly_price_cache", code, "1970-01-01", end))]
    return {
        "bars": daily[-daily_limit:],
        "weekly_bars": weekly[-weekly_limit:],
        "monthly_bars": monthly[-monthly_limit:],
    }


def build_technical_bars_many_multi(codes: list[str], as_of_datetime: str,
                                    daily_limit: int = 120, weekly_limit: int = 60,
                                    monthly_limit: int = 36) -> dict[str, dict]:
    """批量版：一次查多只日/周/月K，返回 {code: {bars, weekly_bars, monthly_bars}}。"""
    from app.data.cache import get_daily_prices_many, get_period_prices_many

    end = _tech_end_day(as_of_datetime)
    start = (date.fromisoformat(end) - timedelta(days=800)).isoformat()
    daily_map = get_daily_prices_many(list(codes), start, end)
    weekly_map = get_period_prices_many("weekly_price_cache", list(codes), "1970-01-01", end)
    monthly_map = get_period_prices_many("monthly_price_cache", list(codes), "1970-01-01", end)
    return {
        c: {
            "bars": [_bar_dict(r) for r in _trade_day_rows(daily_map.get(c) or [])[-daily_limit:]],
            "weekly_bars": [_bar_dict(r) for r in _trade_day_rows(weekly_map.get(c) or [])[-weekly_limit:]],
            "monthly_bars": [_bar_dict(r) for r in _trade_day_rows(monthly_map.get(c) or [])[-monthly_limit:]],
        }
        for c in codes
    }


def _ensure_tech_kline(code: str) -> None:
    """技术面分析前兜底：周/月K缓存任一为空时增量同步一次（腾讯，约 1 秒）。

    失败不抛错——分析照常进行，AI 会按提示词明确说出缺哪个周期。
    """
    from app.data.cache import get_latest_period_price

    if (get_latest_period_price("weekly_price_cache", code) is not None
            and get_latest_period_price("monthly_price_cache", code) is not None):
        return
    try:
        from app.services.refresh import sync_kline_bars

        sync_kline_bars(code, datetime.now())
    except Exception:  # noqa: BLE001 同步失败不阻断分析
        pass


# ---------- 诊股 ----------

def _price_bucket(price) -> str | None:
    """价格档位（粗粒度，避免微小波动刷 stale）：按数量级分桶。"""
    if price is None:
        return None
    try:
        p = float(price)
    except (TypeError, ValueError):
        return None
    if p <= 0:
        return "0"
    # 约 2% 档：取 log 粗分，或简单按整数/十位
    if p < 10:
        return f"{round(p, 1):.1f}"
    if p < 100:
        return f"{int(round(p))}"
    if p < 1000:
        return f"{int(round(p / 5) * 5)}"
    return f"{int(round(p / 10) * 10)}"


def stock_report_snapshot_hash(code: str, model_name: str = "") -> str:
    """诊股新鲜度哈希：价格档位 + 财报报告期 + 资金流日期 + 模型名（不含时间戳）。"""
    from app.data.cache import get_daily_fundflow, get_financials
    from app.services.quote import get_quote

    price = None
    try:
        price = (get_quote(code) or {}).get("price")
    except Exception:  # noqa: BLE001
        pass
    report_date = None
    try:
        fin = get_financials(code)
        if fin:
            report_date = fin.get("report_date")
    except Exception:  # noqa: BLE001
        pass
    flow_date = None
    try:
        flow = get_daily_fundflow(code)
        if flow:
            flow_date = flow.get("trade_date")
    except Exception:  # noqa: BLE001
        pass
    payload = {
        "code": code,
        "price_bucket": _price_bucket(price),
        "fin_report_date": report_date,
        "fundflow_date": flow_date,
        "model": model_name or "",
    }
    raw = json.dumps(payload, ensure_ascii=False, sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(raw.encode("utf-8")).hexdigest()


def _normalize_report(data: dict) -> dict:
    """校验/规整 AI 输出为统一 ScoreCard 结构，缺失字段补默认；保留 rating/risk_score 别名。"""
    grade = check_grade(data.get("grade") or data.get("rating"))
    try:
        score = max(0, min(100, float(data.get("score", 50))))
    except (TypeError, ValueError):
        score = 50.0
    # score/grade 明显冲突只记 warning，不改数据
    if score >= 80 and grade == "D":
        logger.warning("[AI诊股] score/grade 冲突：score=%.1f grade=%s", score, grade)
    elif score < 40 and grade == "A":
        logger.warning("[AI诊股] score/grade 冲突：score=%.1f grade=%s", score, grade)
    action = check_action(data.get("action"), ACTION_STOCK_PORTFOLIO)
    risk_raw = data.get("risk", data.get("risk_score", 50))
    try:
        risk = max(0, min(100, int(float(risk_raw))))
    except (TypeError, ValueError):
        risk = 50
    risk_level = check_risk_level(data.get("risk_level")) if data.get("risk_level") else risk_level_from_score(risk)
    conf = str(data.get("confidence") or "medium").lower().strip()
    if conf not in ("high", "medium", "low"):
        conf = "medium"
    dims = {}
    raw_dims = data.get("dimensions") or {}
    if not isinstance(raw_dims, dict):
        raw_dims = {}
    for k in DIMENSIONS:
        # AI 偶发把维度 JSON 键名写成中文，映射回英文键（英文优先，中文兜底）
        d = next((raw_dims[a] for a in DIM_KEY_ALIASES[k] if isinstance(raw_dims.get(a), dict)), {})
        dims[k] = _normalize_dim_block(d, with_stock_extras=True)
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
        "score": round(score, 1),
        "grade": grade,
        "grade_name": GRADE_NAMES[grade],
        "action": action,
        "action_name": ACTION_NAMES[action],
        "risk": risk,
        "risk_level": risk_level,
        "confidence": conf,
        # 兼容旧前端/测试：rating=grade，risk_score=risk
        "rating": grade,
        "rating_name": GRADE_NAMES[grade],
        "risk_score": risk,
        "dimensions": dims,
        "cross_analysis": traps,
        "expected_growth": exp_growth,
        "summary": str(data.get("summary", "")),
        "reasons": reasons,
        "html": str(data.get("html") or ""),   # AI 生成的完整 HTML 诊股报告（可空 → 前端不显示入口）
    }


def analyze_stock(code: str, system_prompt: str | None = None,
                  intensity: str = "normal") -> dict:
    """触发诊股：用激活模型分析并落库，返回规整报告 + 元信息。

    system_prompt 非 None 时作为「用户附加要求」追加到默认指令后（前端弹窗可编辑）。
    intensity 分析强度 fast/normal/deep → 思考级别 low/全局/max（弹窗可选）。
    """
    model_cfg = get_active_model()
    if not model_cfg:
        raise ValueError("尚未配置/启用任何 AI 模型（点右上角「🤖 AI」配置）")
    ctx = build_stock_context(code)
    # 输出 schema：仅「深入」保留 html 字段（快速/普通不要求 HTML 报告）
    schema = {k: v for k, v in _OUTPUT_SCHEMA.items() if intensity == "deep" or k != "html"}
    user = _schema_user("个股结构化数据：", ctx, schema)
    system = _SYSTEM_PROMPT
    if intensity == "deep":
        system = f"{system}\n\n{_HTML_REQUIREMENT}"
    if system_prompt:
        system = f"{system}\n\n[用户附加要求]\n{system_prompt}"
    inst = _intensity_instruction(intensity)
    if inst:
        system = f"{system}\n\n[分析强度]\n{inst}"
    try:
        raw = chat_json(model_cfg, system, user)
    except Exception as e:  # noqa: BLE001
        raise ValueError(str(e))
    report = _normalize_report(raw)
    report["as_of"] = ctx.get("as_of_datetime") or now_as_of_datetime()
    report["snapshot_hash"] = stock_report_snapshot_hash(code, model_cfg["name"])
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
    logger.info("[AI诊股] %s 生成报告：%s（%s）", code, report["grade"], model_cfg["name"])
    return {"code": code, "model_name": model_cfg["name"], "created_at": now, "report": report}


def get_report(code: str) -> dict | None:
    """读取已存报告；内存 upgrade_legacy_card + stale（快照哈希对比）。"""
    with get_conn() as c:
        row = c.execute("SELECT * FROM ai_reports WHERE code=?", (code,)).fetchone()
    if not row:
        return None
    d = dict(row)
    report = json.loads(d.pop("report_json"))
    report = upgrade_legacy_card(report)
    stored = report.get("snapshot_hash")
    if stored:
        current = stock_report_snapshot_hash(code, d.get("model_name") or "")
        d["stale"] = stored != current
    else:
        d["stale"] = False
    d["report"] = report
    return d


# ---------- 资金流 AI 实时分析（个股/组合，按选定窗口，无 HTML） ----------

_FUNDFLOW_ANALYSIS_SYSTEM = (
    "你是资深盘口资金分析师。系统提供某标的（个股或组合）在选定时间窗内的资金流与股价数据，"
    "你要判断：各档资金动向、资金与股价的相关性/背离、主力资金在做什么、发生了什么、要注意什么。\n"
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
    "   - 天窗口（window 如 'day'/'week'/'month'）：points 为多日逐日五档序列"
    "（'week'=按自然周、'month'=按自然月聚合，与 K 线周线/月线对齐），"
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
)

# 快速/普通：快报式简明输出，不生成 HTML
_FUNDFLOW_QUICK_NOTE = (
    "5. 输出简体中文，简明扼要、可直接阅读；这是快报，不要长篇大论，不要 HTML。\n"
    "严格输出 JSON，不要输出任何额外文字。"
)

# HTML 深度报告要求：仅「深入」强度追加（快速/普通保持快报式）
_FUNDFLOW_HTML_REQUIREMENT = (
    "5. 同时生成一份完整、独立、可读性强的 HTML 资金面报告（字段 html），供用户新开页面查看。\n"
    "[HTML 深度分析强制规范]\n"
    "1. HTML 正文（简体中文）必须 ≥1000 字，必须有实质分析；禁止只列要点、禁止空话套话，浅尝辄止视为不合格重写。\n"
    "2. 结构必须覆盖：核心结论 / 各档资金动向（主力/散户结构、拆单识别）/ 资金×股价相关性或背离 / 分时与多日节奏 / 风险点 / 操作提示。\n"
    "3. 每个判断必须带具体数字（窗口、成交额、净流入、档位）与上下文；禁止「资金流入」「有背离」这类无数字断言。\n"
    "4. HTML 为独立成文，离开本应用页面即可读懂；不得原样罗列系统已展示的数据点表格。\n"
    "5. 质量自检：写完后逐段检查——这段是否提供了用户在数据表上看不到的洞察？若没有，重写。\n"
    "要求：自包含单文件、内联 CSS、不引用任何外部资源、不得包含 <script> 或任何可执行代码；"
    "简体中文；结构清晰、排版美观、层次分明。\n"
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
    "html": "完整独立 HTML 资金面报告源代码（自包含、内联 CSS、无外部依赖、无脚本、简体中文、≥1000字深度正文，聚焦深入分析，不罗列原始数据）",
}

# ---------- 组合批量资金 AI 分析：所有持仓个股一次分析（省 token），逐只精简输出 ----------

_BATCH_FUNDFLOW_SYSTEM = (
    "你是资深盘口资金分析师。系统给出组合内多只标的（个股或指数）的资金流与股价数据（列表 stocks），"
    "你要对每只标的判断：资金与股价的相关性/背离、主力资金在做什么、要注意什么；"
    "并额外给出组合整体的资金面相关性判断（coherence）。\n"
    "[数据说明]\n"
    "1. stocks 为列表，每只含：code/name/tag/weight_pct（组合权重）、price/pct_chg（当前价与涨跌幅）、"
    "day_net/day_main_net（当日净流入/主力净流入，元，正=流入负=流出）、points（按选定窗口的资金序列："
    "分钟窗口为当日分时序列。个股含 super/large/medium/small/xs 五档、main=主力、cum=累计、"
    "buy/sell=买盘/卖盘成交额、price=同窗口股价；指数（mode='index'）无五档（无逐笔成交），"
    "points 为分时量价：price=窗口末收盘、volume=窗口累计量、amount=窗口累计成交额、"
    "cum=累计量、cum_amount=累计成交额、day_pct/cum_pct=窗口/累计量占全天%"
    "——据此判断量额/量价关系（成交额放大上攻/萎缩回调/量额背离、成交额与量背离），判断资金强弱优先看金额（成交额）。"
    "天窗口为多日逐日（day）/自然周（week）/自然月（month）聚合，"
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
)

# 快速/普通：仅输出精简 JSON
_BATCH_FUNDFLOW_QUICK_NOTE = (
    "严格输出 JSON，结构如下，不要输出任何额外文字。"
)

# HTML 深度报告要求：仅「深入」强度追加（快速/普通保持精简 JSON）
_BATCH_FUNDFLOW_HTML_REQUIREMENT = (
    "4. 同时生成一份完整、独立、可读性强的 HTML 批量资金面报告（字段 html），供用户新开页面查看。\n"
    "[HTML 深度分析强制规范]\n"
    "1. HTML 正文（简体中文）必须 ≥1000 字，必须有实质分析；禁止只列要点、禁止空话套话，浅尝辄止视为不合格重写。\n"
    "2. 结构必须覆盖：核心结论 / 组合资金面格局（共振/跷跷板虹吸/分化）与证据 / 逐只资金×股价与主力行为 / 风险点 / 组合层面操作建议分级。\n"
    "3. 每个判断必须带具体数字（窗口、标的、成交额、净流入）与上下文；禁止「资金共振」「明显流入」这类无数字断言。\n"
    "4. HTML 为独立成文，离开本应用页面即可读懂；不得原样罗列系统已展示的逐只表格。\n"
    "5. 质量自检：写完后逐段检查——这段是否提供了用户在数据表上看不到的洞察？若没有，重写。\n"
    "要求：自包含单文件、内联 CSS、不引用任何外部资源、不得包含 <script> 或任何可执行代码；"
    "简体中文；结构清晰、排版美观、层次分明。\n"
    "严格输出 JSON，不要输出任何额外文字。"
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
    ],
    "html": "完整独立 HTML 批量资金面报告源代码（自包含、内联 CSS、无外部依赖、无脚本、简体中文、≥1000字深度正文，聚焦深入分析，不罗列原始数据）",
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
    """资金流窗口归一化为统一字符串：分钟 '1m'/'5m'/'15m'/'30m' + 天窗口 'day'/'week'/'month'。

    天窗口与 K 线周期对齐：day=日（逐日）、week=周（每 5 个交易日聚合桶）、
    month=月（每 20 个交易日聚合桶）。兼容 int 旧值（15 → '15m'）与旧天窗口名
    （'1d'→'day'、'7d'→'week'、'30d'→'month'，读取旧落库报告时归一化命中）。
    """
    _FLOW_WINDOWS = ("1m", "5m", "15m", "30m", "day", "week", "month")
    if isinstance(window, int):
        return f"{window}m" if window in (1, 5, 15, 30) else "15m"
    s = str(window).strip().lower()
    if s in _FLOW_WINDOWS:
        return s
    if s == "1d":
        return "day"
    if s == "7d":
        return "week"
    if s == "30d":
        return "month"
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


# 天窗口 → 聚合模式（与 K 线周期对齐：day=日逐日、week=自然周、month=自然月）
_DAY_MODES = {"day": "day", "week": "week", "month": "month"}
# 天窗口 → AI 发送量上限（与 K 线技术面对齐：日K 120 根 / 周K 60 根 / 月K 36 根）
_DAY_AI_LIMITS = {"day": 120, "week": 60, "month": 36}
# 天窗口 → 读取历史自然日窗口（覆盖新浪 500 条 ≈ 2 年）
_DAY_LOOKBACK_DAYS = 760


def _day_mode(w: str) -> str:
    """天窗口字符串 → 聚合模式（day/week/month；非法回退 day）。"""
    return _DAY_MODES.get(str(w).lower().strip(), "day")


def _day_ai_limit(w: str) -> int:
    """天窗口 → AI 发送桶数上限（day=120/week=60/month=36；非法回退 120）。"""
    return _DAY_AI_LIMITS.get(str(w).lower().strip(), 120)


def _natural_group_key(date_str: str, mode: str) -> str:
    """自然周/自然月分组键：week → 'YYYY-Www'（ISO 周，周一为一周起始）；month → 'YYYY-MM'。"""
    from datetime import date as _date

    d = _date.fromisoformat(str(date_str)[:10])
    if mode == "month":
        return f"{d.year:04d}-{d.month:02d}"
    iso = d.isocalendar()
    return f"{iso[0]:04d}-W{iso[1]:02d}"


def _bucket_day_flows(rows, mode, price_map=None):
    """逐日五档序列按聚合模式分组（与前端 bucketFlowDays 同构）。

    mode='day'：逐日原样；'week'：按自然周（ISO 周，周一起始）求和；'month'：按自然月求和。
    price/pct_chg 取分组末交易日；分组标签为「组首日期~组末日期（月日）」。
    """
    if not rows:
        return []
    if mode == "day":
        out = []
        for r in rows:
            p = _day_flow_point(r)
            pm = price_map or {}
            if r["trade_date"] in pm:
                p.update(pm[r["trade_date"]])
            out.append(p)
        return out
    buckets: dict[str, list] = {}
    for r in rows:
        key = _natural_group_key(r["trade_date"], mode)
        buckets.setdefault(key, []).append(r)
    out = []
    for key in sorted(buckets):
        g = buckets[key]
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


def _bucket_day_prices(rows, mode):
    """指数逐日量价（close/volume/amount）按聚合模式分组（与 _bucket_day_flows 同构）。

    mode='day'：逐日原样；'week'：按自然周求和；'month'：按自然月求和；
    volume/amount 求和、close/pct_chg 取分组末交易日。
    """
    if not rows:
        return []
    rows = [dict(r) for r in rows]
    if mode == "day":
        return [
            {"date": r["trade_date"], "close": r.get("close"), "volume": r.get("volume"),
             "amount": r.get("amount"), "pct_chg": r.get("pct_change")}
            for r in rows
        ]
    buckets: dict[str, list] = {}
    for r in rows:
        key = _natural_group_key(r["trade_date"], mode)
        buckets.setdefault(key, []).append(r)
    out = []
    for key in sorted(buckets):
        g = buckets[key]
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
    - 天窗口（'day'/'week'/'month'）：多日逐日五档（week=自然周、month=自然月聚合，与 K 线
      周线/月线对齐）+ 每日收盘价日K。发送量与 K 线对齐：day 最多 120 桶 / week 60 / month 36
      （数据源不足时有多少发多少）。
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
    from app.market.calendar import resolve_trade_day

    w = _norm_flow_window(window)
    is_day = w in ("day", "week", "month")
    today, _ = resolve_trade_day(None)
    out = {"mode": "stock", "window": w, "date": today, "points": []}
    inst = get_instrument(code)
    # 指数：无逐笔成交、无五档 → 资金面全用腾讯量价（分时 mkline / 日级 fqkline volume），成交额按量派生
    if getattr(inst, "is_index", False):
        scale = _index_amount_scale(code)
        if is_day:
            mode = _day_mode(w)
            start = (date.fromisoformat(today) - timedelta(days=_DAY_LOOKBACK_DAYS)).isoformat()
            rows = [dict(r) for r in get_daily_prices(code, start, today)]
            for r in rows:
                if r.get("volume") is not None:
                    r["amount"] = float(r["volume"]) * scale
            if not rows:
                return out
            out["mode"] = "index"
            out["points"] = _bucket_day_prices(rows, mode)
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
        mode = _day_mode(w)
        limit_buckets = _day_ai_limit(w)
        start = (date.fromisoformat(today) - timedelta(days=_DAY_LOOKBACK_DAYS)).isoformat()
        rows = get_daily_fundflows(code, start, today)
        if not rows:
            return out
        # 与 K 线对齐的发送量：只发最近 N 个桶（day=120 交易日 / week=60 周 / month=36 月）。
        # 自然周/月按每桶约 5/20 个交易日估算截断行数；数据源不足时有多少发多少。
        est_days_per_bucket = {"day": 1, "week": 5, "month": 20}.get(mode, 1)
        max_rows = limit_buckets * est_days_per_bucket
        if len(rows) > max_rows:
            rows = rows[-max_rows:]
        price_map = {}
        for r in get_daily_prices(code, start, today):
            price_map[r["trade_date"]] = {"price": r["close"], "pct_chg": r["pct_change"]}
        out["points"] = _bucket_day_flows(rows, mode, price_map)
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
        "html": str(data.get("html") or ""),   # AI「深入」模式生成的 HTML 资金面报告（可空 → 前端无 📄 按钮）
    }


def analyze_fundflow(code: str, window: int | str = "15m", system_prompt: str | None = None,
                     intensity: str = "normal") -> dict:
    """个股 AI 资金流实时分析（带资金×股价相关性/背离），统一时间窗。

    无激活模型 → ValueError；选定窗口无资金流数据 → ValueError。
    返回 {mode, code, name, window, date, points_count, analysis}，无 HTML。
    system_prompt 非 None 时作为「用户附加要求」追加到默认指令后（前端弹窗可编辑）。
    intensity 分析强度 fast/normal/deep → 思考级别 low/全局/max（弹窗可选）。
    """
    model_cfg = get_active_model()
    if not model_cfg:
        raise ValueError("尚未配置/启用任何 AI 模型（点右上角「🤖 AI」配置）")
    ctx = build_fundflow_analysis_context(code, window)
    if not ctx["points"]:
        raise ValueError("该时间窗资金流数据为空，请先刷新资金流")
    # 输出 schema：仅「深入」保留 html 字段（快速/普通快报式，不生成 HTML）
    schema = {k: v for k, v in _FUNDFLOW_ANALYSIS_SCHEMA.items() if intensity == "deep" or k != "html"}
    user = _schema_user("资金流与股价数据：", ctx, schema)
    system = _FUNDFLOW_ANALYSIS_SYSTEM
    if intensity == "deep":
        system = f"{system}\n\n{_FUNDFLOW_HTML_REQUIREMENT}"
    else:
        system = f"{system}\n\n{_FUNDFLOW_QUICK_NOTE}"
    if system_prompt:
        system = f"{system}\n\n[用户附加要求]\n{system_prompt}"
    inst = _intensity_instruction(intensity)
    if inst:
        system = f"{system}\n\n[分析强度]\n{inst}"
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
                  divergence, alerts, conclusion, html, model_name, created_at, updated_at)
               VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
               ON CONFLICT(code, trade_date, source, window) DO UPDATE SET
                 correlation=excluded.correlation, summary=excluded.summary,
                 main_force=excluded.main_force, rhythm=excluded.rhythm, divergence=excluded.divergence,
                 alerts=excluded.alerts, conclusion=excluded.conclusion, html=excluded.html,
                 model_name=excluded.model_name, updated_at=excluded.updated_at""",
            (code, trade_date, source, window,
             analysis.get("correlation"), analysis.get("summary"), analysis.get("main_force"),
             analysis.get("rhythm"),
             json.dumps(analysis.get("divergence") or [], ensure_ascii=False),
             json.dumps(analysis.get("alerts") or [], ensure_ascii=False),
             analysis.get("conclusion"), analysis.get("html"), model_name, now, now),
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
    from app.instruments import get_instrument
    from app.market.calendar import resolve_trade_day
    from app.services.quote import get_quote

    w = _norm_flow_window(window)
    today, _ = resolve_trade_day(None)
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
        "html": str(data.get("html") or ""),   # AI「深入」模式生成的批量 HTML 报告（可空）
    }


def _upsert_coherence_report(scope: str, scope_key: str, trade_date: str, window: str,
                             coherence: dict, model_name: str = "", html: str = "") -> None:
    """组合级资金相关性结果写入 ai_fundflow_coherence_reports（UPSERT，同 scope+key+日期+窗覆盖）。"""
    from datetime import datetime

    from app.models.db import get_conn

    now = datetime.now().isoformat(timespec="seconds")
    with get_conn() as c:
        c.execute(
            """INSERT INTO ai_fundflow_coherence_reports
                 (scope, scope_key, trade_date, window, correlation, summary, points,
                  conclusion, html, model_name, created_at, updated_at)
               VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
               ON CONFLICT(scope, scope_key, trade_date, window) DO UPDATE SET
                 correlation=excluded.correlation, summary=excluded.summary, points=excluded.points,
                 conclusion=excluded.conclusion, html=excluded.html, model_name=excluded.model_name,
                 updated_at=excluded.updated_at""",
            (scope, scope_key, trade_date, window,
             coherence.get("correlation"), coherence.get("summary"),
             json.dumps(coherence.get("points") or [], ensure_ascii=False),
             coherence.get("conclusion"), html, model_name, now, now),
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
           f"html, model_name FROM ai_fundflow_coherence_reports {where} "
           f"ORDER BY trade_date DESC, updated_at DESC LIMIT 1")
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
        "conclusion": row["conclusion"], "html": row["html"] or "",
        "model_name": row["model_name"],
    }


def analyze_batch_fundflow(tags: list[str] | None = None, window: int | str = "15m",
                           codes: list[str] | None = None,
                           weights: list[float] | None = None,
                           system_prompt: str | None = None,
                           intensity: str = "normal") -> dict:
    """组合批量资金 AI 分析：所有有资金流数据的持仓/指数一次发给 AI（省 token），逐只精简输出并落库。

    窗口仅支持 15m 及以上（1m/5m 拒绝）；无激活模型/无有效标的 → ValueError。
    codes 提供时按指数组合相关性（scope='indices'），否则按持仓组合（scope='portfolio'）。
    返回 {mode, window, date, covered, total, stocks_count, reports, coherence}。
    system_prompt 非 None 时作为「用户附加要求」追加到默认指令后（前端弹窗可编辑）。
    intensity 分析强度 fast/normal/deep → 思考级别 low/全局/max（弹窗可选）。
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
    # 输出 schema：仅「深入」保留 html 字段（快速/普通精简 JSON，不生成 HTML）
    schema = {k: v for k, v in _BATCH_FUNDFLOW_SCHEMA.items() if intensity == "deep" or k != "html"}
    user = _schema_user("组合标的资金流数据（列表）：", ctx, schema)
    system = _BATCH_FUNDFLOW_SYSTEM
    if intensity == "deep":
        system = f"{system}\n\n{_BATCH_FUNDFLOW_HTML_REQUIREMENT}"
    else:
        system = f"{system}\n\n{_BATCH_FUNDFLOW_QUICK_NOTE}"
    if system_prompt:
        system = f"{system}\n\n[用户附加要求]\n{system_prompt}"
    inst = _intensity_instruction(intensity)
    if inst:
        system = f"{system}\n\n[分析强度]\n{inst}"
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
    # scope_key 排序归一：同一组合无论选择顺序都唯一（选中 A,B 与 B,A 同 key，F5 才能精确匹配）
    coherence = _normalize_coherence(raw.get("coherence"))
    batch_html = str(raw.get("html") or "")   # AI「深入」模式生成的批量 HTML 报告
    scope = "indices" if ctx["mode"] == "indices" else "portfolio"
    scope_key = ",".join(sorted(codes)) if codes else (",".join(sorted(tags)) if tags else "全部")
    try:
        _upsert_coherence_report(scope, scope_key, ctx["date"], w, coherence,
                                 model_cfg.get("model") or model_cfg.get("name", ""),
                                 html=batch_html)
    except Exception:  # noqa: BLE001 组合相关性落库失败不阻断批量
        pass
    logger.info("[AI资金流-批量] %s %s%s 分析 %d/%d 只",
                scope, ctx["window"], "天" if w in ("day", "week", "month") else "分钟",
                len(reports), ctx["covered"])
    return {
        "mode": ctx["mode"], "window": w, "date": ctx["date"],
        "covered": ctx["covered"], "total": ctx["total"],
        "stocks_count": len(reports), "reports": reports,
        "coherence": coherence, "html": batch_html,
    }


# ============================================================ 消息面 / 技术面 AI（专项深入层）

# 个股新闻抓取（akshare stock_news_em，东财聚合源；本机实测可用，失败自动降级为无新闻）
_NEWS_TTL_HOURS = 6          # 新闻缓存有效期：6 小时内不重复抓取
_NEWS_SINGLE_LIMIT = 15      # 单股消息面注入条数（控 token）
_NEWS_BATCH_LIMIT = 6        # 批量消息面每只注入条数（组合多只，更省 token）
_NEWS_CONTENT_MAX = 150      # 单条正文截断字数（控 token，库中存全文）


def _ensure_stock_news(code: str, limit: int = _NEWS_SINGLE_LIMIT,
                       content_max: int = _NEWS_CONTENT_MAX,
                       force: bool = False) -> list[dict]:
    """个股近期新闻：缓存新鲜直接读库，过期/缺失用 akshare 抓取后入库。

    返回 [{time, title, content(已截断), source, url}]（按发布时间倒序）；抓取失败返回
    缓存旧数据（宁可旧闻也不空手），完全没有则 []——调用方按「该股无新闻」处理。
    """
    from app.data.cache import (
        get_latest_news_fetched_at,
        get_stock_news,
        upsert_stock_news,
    )
    from app.data.raw import raw_news

    last = get_latest_news_fetched_at(code)
    fresh = False
    if last and not force:
        try:
            fresh = (datetime.now() - datetime.fromisoformat(last)).total_seconds() < _NEWS_TTL_HOURS * 3600
        except ValueError:
            fresh = False
    if not fresh:
        items = raw_news.stock_news(code, limit=30)
        if items:
            upsert_stock_news(code, items)
    rows = get_stock_news(code, limit=limit)
    return [
        {
            "time": r["news_time"], "title": r["title"],
            "content": (r["content"] or "")[:content_max],
            "source": r["source"], "url": r["url"],
        }
        for r in rows
    ]

# 消息面：akshare 抓该股近期新闻（stock_news_cache）注入；抓取失败时退化为
# 只给 code/name/as_of，由 AI 按公开知识 + 时效规则补（与诊股「同业竞争」维缺数同理）。
_NEWS_SYSTEM = (
    "你是资深财经消息分析师。系统给出待分析标的的代码、名称、当前时间（as_of_datetime）"
    "与系统抓取的近期新闻（news 数组，按时间倒序，含 title/content/time/source/url）。\n"
    "请优先依据 news 中的新闻正文判断消息面：引用时注明日期与来源；news 为空或明显过时时，"
    "再结合你的公开知识（公司公告、行业与政策事件、财报节点等）补充，并严格遵守时效规则，"
    "不拿训练期旧闻当「最新」。\n\n"
    f"{_ASOF_RULES}\n"
    "[输出]\n"
    "1. stance 总立场：bullish（利多）/ neutral（中性）/ bearish（利空）。\n"
    "2. summary 一句话概括近期消息面主线。\n"
    "3. items 近期重要事件列表（按时间倒序）：每条 headline（标题）、event_date（YYYY-MM-DD）、"
    "impact（利多/利空/中性）、summary（简述与对股价的可能影响）。\n"
    "4. risks 消息面带来的风险点数组。\n"
    "5. 无足够新信息时：items 为空数组 + omit_reason 说明原因，不要硬凑。\n"
    "严格输出 JSON，不要输出任何额外文字。"
)

_NEWS_HTML_REQUIREMENT = (
    "同时生成一份完整、独立、可读性强的 HTML 消息面报告（字段 html），供用户新开页面查看。\n"
    "[HTML 深度分析强制规范]\n"
    "1. HTML 正文（简体中文）必须 ≥1000 字，必须有实质分析；禁止只列要点、禁止空话套话，浅尝辄止视为不合格重写。\n"
    "2. 结构必须覆盖：核心结论 / 近期事件梳理（按时间，标注日期与影响）/ 行业与政策环境 / "
    "财务节点与公告预期 / 风险与不确定性 / 操作提示。\n"
    "3. 每个判断必须带具体信息与时效（事件、日期、影响方向）；禁止「有利好」「消息面平稳」这类无依据断言；"
    "无法确认时效的信息一律不写。\n"
    "4. HTML 为独立成文，离开本应用页面即可读懂；把篇幅全部用于深入分析与洞察，"
    "不得原样罗列用户已可见的原始数据。\n"
    "5. 质量自检：写完后逐段检查——这段是否提供了用户在数据表上看不到的洞察？若没有，重写。\n"
    "要求：自包含单文件、内联 CSS、不引用任何外部资源、不得包含 <script> 或任何可执行代码；"
    "简体中文；结构清晰、排版美观、层次分明。\n"
    "严格输出 JSON，不要输出任何额外文字。"
)

_NEWS_SCHEMA = {
    "stance": "bullish(利多)|neutral(中性)|bearish(利空)",
    "summary": "一句话概括消息面主线",
    "items": [
        {
            "headline": "事件标题",
            "event_date": "YYYY-MM-DD",
            "impact": "利多/利空/中性",
            "summary": "简述与对股价的可能影响",
        }
    ],
    "risks": ["消息面风险点数组"],
    "omit_reason": "无足够新信息时填说明，否则留空",
    "html": "完整独立 HTML 消息面报告源代码（自包含、内联 CSS、无外部依赖、无脚本、简体中文、≥1000字深度正文，聚焦深入分析，不罗列原始数据）",
}

# 技术面：bars=日K、weekly_bars=周K、monthly_bars=月K（截至 as_of 交易日），
# prompt 白话表达、给关键位与证伪条件、不冒充盘中。
_TECH_SYSTEM = (
    "你是资深技术分析师。系统给出某标的最近日/周/月 K 数据（bars=日K、weekly_bars=周K、"
    "monthly_bars=月K，均按日期升序）与当前时间（as_of_datetime）。\n"
    "[数据说明]\n"
    "1. 每根 K 线：date（段末交易日）、open/high/low/close（元）、volume（成交量）、"
    "pct_change（涨跌幅%，周/月K为段末当日涨跌幅，可能为 null）。\n"
    "2. bars=最近约 120 个交易日、weekly_bars=最近约 60 周、monthly_bars=最近约 36 个月——"
    "日K看短线、周K看中期、月K看长期，必须结合三个级别判断趋势与关键位。\n"
    "3. 你解读的是「截至 as_of 对应交易日」的价格结构（K 线已截断到该交易日收盘），不要冒充盘中、"
    "不要臆测 as_of 之后的数据。若当前时间早于开盘（未开盘），K线已截断到上一交易日\n"
    "（见 bars_as_of），不要期待当日K线，也不要把未开盘日当作数据缺失。\n"
    "4. 若日/周/月任一周期为空（该股无该周期K线数据），summary 必须明确写出缺少哪个周期"
    "（如「缺少周K/月K数据」），不得假装有该周期数据；三套全空时明确说无法分析技术面，不要硬下结论。\n"
    "[输出要求]\n"
    "1. 用白话表达，不堆指标缩写——用户不熟技术指标，解释要通俗（如「股价站稳10元上方」而非「MA10金叉」）。\n"
    "2. 给出关键支撑位与压力位（key_levels，数值），以及什么情况会证伪当前判断（invalidation）。\n"
    "3. 可结合资金面/估值给出潜在矛盾提示（凭合理推断即可，勿编造具体数字）。\n"
    "4. trend_short / trend_mid 用 up（上行）/ down（下行）/ range（震荡）表达短/中期趋势。\n"
    "5. signals 为白话信号数组（每条一句话，说人话）。\n"
    "6. 无 K 线时：summary 明说无数据，signals 为空，trend 用 range。\n"
    "严格输出 JSON，不要输出任何额外文字。"
)

_TECH_HTML_REQUIREMENT = (
    "同时生成一份完整、独立、可读性强的 HTML 技术面报告（字段 html），供用户新开页面查看。\n"
    "[HTML 深度分析强制规范]\n"
    "1. HTML 正文（简体中文）必须 ≥1000 字，必须有实质分析；禁止只列要点、禁止空话套话，浅尝辄止视为不合格重写。\n"
    "2. 结构必须覆盖：核心结论 / 价格结构解读（结合日/周/月K看趋势、关键支撑压力位、量能）/ 白话信号 / "
    "证伪条件与风险 / 与资金面、估值的矛盾提示 / 操作提示。\n"
    "3. 每个判断必须带具体价位与上下文（如「近 30 日股价在 10.2~10.8 区间震荡，放量突破 10.8 前难言走强」）；"
    "禁止「趋势向好」「有支撑」这类无价位断言。\n"
    "4. HTML 为独立成文，离开本应用页面即可读懂；不得原样罗列系统已展示的数据表格。\n"
    "5. 质量自检：写完后逐段检查——这段是否提供了用户在数据表上看不到的洞察？若没有，重写。\n"
    "要求：自包含单文件、内联 CSS、不引用任何外部资源、不得包含 <script> 或任何可执行代码；"
    "简体中文；结构清晰、排版美观、层次分明。\n"
    "严格输出 JSON，不要输出任何额外文字。"
)

_TECH_SCHEMA = {
    "trend_short": "短期趋势 up(上行)|down(下行)|range(震荡)",
    "trend_mid": "中期趋势 up|down|range",
    "key_levels": {"support": ["支撑价位数组"], "resistance": ["压力价位数组"]},
    "signals": ["白话信号数组（每条约一句话，说人话）"],
    "invalidation": "证伪条件（出现什么情况说明当前判断错了）",
    "summary": "一句话结论（无K线时明说无数据）",
    "html": "完整独立 HTML 技术面报告源代码（自包含、内联 CSS、无外部依赖、无脚本、简体中文、≥1000字深度正文，聚焦深入分析，不罗列原始数据）",
}


# 组合批量：整批共用同一个 as_of_datetime，逐只精简输出并落库 source='batch'（与批量资金流同构）。
_BATCH_NEWS_SYSTEM = (
    "你是资深财经消息分析师。系统给出组合内多只标的（列表 stocks，每只含 code/name 与该股近期新闻 news 数组，"
    "按时间倒序，含 title/content/time/source/url）与当前时间（as_of_datetime）。\n"
    "请优先依据每只的 news 新闻正文判断消息面：引用时注明日期与来源；news 为空或明显过时时，"
    "再结合你的公开知识逐只补充，并严格遵守时效规则。\n\n"
    f"{_ASOF_RULES}\n"
    "[输出要求]\n"
    "1. 对 stocks 中每只 code 输出：stance（bullish/neutral/bearish）、summary（一句话结论）、"
    "items（近期事件列表，缺 event_date 或已过时的不要）、risks（注意点，可选）、"
    "omit_reason（无足够新信息时填说明，否则留空）。\n"
    "2. 必须覆盖 stocks 中出现的每只 code，不要遗漏；不要新增 stocks 中没有的 code。\n"
    "3. 每只精简到几句话，不展开；简体中文。\n"
    "严格输出 JSON，不要输出任何额外文字。"
)

# 批量「深入」强度要求整组合一份 HTML（落 ai_news_coherence_reports，scope+scope_key 按选中组合整体存）
_BATCH_NEWS_HTML_REQUIREMENT = (
    "4. 同时生成一份完整、独立、可读性强的 HTML 批量消息面报告（字段 html），"
    "面向【整个组合整体】，不是逐只拼接。供用户新开页面查看。\n"
    "[HTML 深度分析强制规范]\n"
    "1. HTML 正文（简体中文）必须 ≥1000 字，必须有实质分析；禁止只列要点、禁止空话套话。\n"
    "2. 结构必须覆盖：组合消息面格局（哪些偏多/偏空/中性及其权重）、逐只事件梳理（标注日期与影响）、"
    "行业与政策环境、风险点、组合层面操作建议分级。\n"
    "3. 每个判断必须带具体信息与时效（事件、日期、影响方向）；无法确认时效的信息一律不写。\n"
    "4. HTML 为独立成文，离开本应用页面即可读懂；不得原样罗列系统已展示的逐只表格。\n"
    "5. 质量自检：写完后逐段检查——这段是否提供了用户在数据表上看不到的洞察？若没有，重写。\n"
    "要求：自包含单文件、内联 CSS、不引用任何外部资源、不得包含 <script> 或任何可执行代码；"
    "简体中文；结构清晰、排版美观、层次分明。\n"
    "严格输出 JSON，不要输出任何额外文字。"
)

_BATCH_NEWS_SCHEMA = {
    "summary": "组合整体消息面一句话（可选）",
    "stocks": [
        {
            "code": "标的代码",
            "stance": "bullish|neutral|bearish",
            "summary": "一句话结论",
            "items": [{"headline": "事件标题", "event_date": "YYYY-MM-DD", "impact": "利多/利空/中性", "summary": "简述"}],
            "risks": ["注意点（可选）"],
            "omit_reason": "无足够新信息时填说明，否则留空",
        }
    ],
    "html": "完整独立 HTML 批量消息面报告源代码（面向整个组合整体，自包含、内联 CSS、无外部依赖、无脚本、简体中文、≥1000字深度正文）",
}

_BATCH_TECH_SYSTEM = (
    "你是资深技术分析师。系统给出组合内多只标的的最近日/周/月 K 数据（列表 stocks，每只含 code/name/"
    "bars=日K、weekly_bars=周K、monthly_bars=月K）"
    "与当前时间（as_of_datetime）。\n"
    "[数据说明]\n"
    "1. 每只标的 K 线按日期升序：date（段末交易日）、open/high/low/close（元）、volume（成交量）、"
    "pct_change（涨跌幅%，周/月K为段末当日涨跌幅，可能为 null）。组合内每只根数较少："
    "日K约 60、周K约 36、月K约 24——日K看短线、周K看中期、月K看长期，结合判断。\n"
    "2. 解读「截至 as_of 对应交易日」的价格结构，不要冒充盘中；三套 K 线均为空说明该标的无K线数据，"
    "若当前时间早于开盘（未开盘），K线已截断到上一交易日（见 bars_as_of）。三套 K 线均为空说明该标的无K线数据，\n"
    "明说无法分析，不要硬下结论。\n"
    "[输出要求]\n"
    "1. 对 stocks 中每只 code 输出：trend_short / trend_mid（up/down/range）、summary（一句话结论）、"
    "key_levels（支撑/压力价位）、signals（白话信号数组）、invalidation（证伪条件）。\n"
    "2. 必须覆盖 stocks 中出现的每只 code，不要遗漏；不要新增 stocks 中没有的 code。\n"
    "3. 每只精简到几句话；白话表达，不堆指标缩写；简体中文。\n"
    "严格输出 JSON，不要输出任何额外文字。"
)

# 批量「深入」强度要求整组合一份 HTML（落 ai_tech_coherence_reports，scope+scope_key 按选中组合整体存）
_BATCH_TECH_HTML_REQUIREMENT = (
    "4. 同时生成一份完整、独立、可读性强的 HTML 批量技术面报告（字段 html），"
    "面向【整个组合整体】，不是逐只拼接。供用户新开页面查看。\n"
    "[HTML 深度分析强制规范]\n"
    "1. HTML 正文（简体中文）必须 ≥1000 字，必须有实质分析；禁止只列要点、禁止空话套话。\n"
    "2. 结构必须覆盖：组合技术面格局 / 逐只趋势与关键价位 / 白话信号 / 风险与证伪条件 / 组合层面操作建议分级。\n"
    "3. 每个判断必须带具体价位与上下文；禁止「趋势向好」「有支撑」这类无价位断言。\n"
    "4. HTML 为独立成文，离开本应用页面即可读懂；不得原样罗列系统已展示的逐只表格。\n"
    "5. 质量自检：写完后逐段检查——这段是否提供了用户在数据表上看不到的洞察？若没有，重写。\n"
    "要求：自包含单文件、内联 CSS、不引用任何外部资源、不得包含 <script> 或任何可执行代码；"
    "简体中文；结构清晰、排版美观、层次分明。\n"
    "严格输出 JSON，不要输出任何额外文字。"
)

_BATCH_TECH_SCHEMA = {
    "summary": "组合整体技术面一句话（可选）",
    "stocks": [
        {
            "code": "标的代码",
            "trend_short": "up|down|range",
            "trend_mid": "up|down|range",
            "key_levels": {"support": ["支撑价位"], "resistance": ["压力价位"]},
            "signals": ["白话信号数组"],
            "invalidation": "证伪条件",
            "summary": "一句话结论",
        }
    ],
    "html": "完整独立 HTML 批量技术面报告源代码（面向整个组合整体，自包含、内联 CSS、无外部依赖、无脚本、简体中文、≥1000字深度正文）",
}


def _normalize_news(data) -> dict:
    """规整消息面输出：stance 限枚举（非法兜底 neutral）、剥离无 event_date 或 AI 标注过时的 item、
    html 缺省空串。items 为空是合法结果（AI 因时效放弃），照常落库。"""
    if not isinstance(data, dict):
        data = {}
    stance = str(data.get("stance", "neutral")).lower()
    if stance not in ("bullish", "neutral", "bearish"):
        stance = "neutral"

    def _list(v):
        if isinstance(v, list):
            return [str(x) for x in v if str(x).strip()]
        return [str(v)] if v and str(v).strip() else []

    items = []
    raw_items = data.get("items")
    if isinstance(raw_items, list):
        for it in raw_items:
            if not isinstance(it, dict):
                continue
            if it.get("stale") or it.get("expired"):        # AI 自标过时 → 剔除
                continue
            event_date = str(it.get("event_date") or "").strip()
            if not event_date:                              # 无日期 → 无法核对时效 → 剔除
                continue
            items.append({
                "headline": str(it.get("headline") or ""),
                "event_date": event_date,
                "impact": str(it.get("impact") or ""),
                "summary": str(it.get("summary") or ""),
            })
    return {
        "stance": stance,
        "summary": str(data.get("summary") or ""),
        "items": items,
        "risks": _list(data.get("risks")),
        "omit_reason": str(data.get("omit_reason") or ""),
        "html": str(data.get("html") or ""),
    }


def _normalize_technical(data) -> dict:
    """规整技术面输出：趋势枚举兜底 range，数值容错。"""
    if not isinstance(data, dict):
        data = {}

    def _trend(v):
        s = str(v or "range").lower()
        return s if s in ("up", "down", "range") else "range"

    def _list(v):
        if isinstance(v, list):
            return [str(x) for x in v if str(x).strip()]
        return [str(v)] if v and str(v).strip() else []

    levels = data.get("key_levels")
    support, resistance = [], []
    if isinstance(levels, dict):
        support = _list(levels.get("support"))
        resistance = _list(levels.get("resistance"))
    return {
        "trend_short": _trend(data.get("trend_short")),
        "trend_mid": _trend(data.get("trend_mid")),
        "key_levels": {"support": support, "resistance": resistance},
        "signals": _list(data.get("signals")),
        "invalidation": str(data.get("invalidation") or ""),
        "summary": str(data.get("summary") or ""),
        "html": str(data.get("html") or ""),
    }


def _stock_display_name(code: str) -> str:
    """取标的名（instruments 优先，stocks 表兜底；无则回退代码）。"""
    try:
        from app.instruments import get_instrument

        inst = get_instrument(code)
        if inst.name:
            return inst.name
    except Exception:  # noqa: BLE001
        pass
    with get_conn() as c:
        row = c.execute("SELECT name FROM stocks WHERE code=?", (code,)).fetchone()
        return row["name"] if row and row["name"] else code


def _upsert_news_report(code: str, as_of: str, source: str, report: dict, model_name: str = "") -> None:
    """个股消息面 AI 结果写入 ai_news_reports（主键 code+as_of+source，同刻重分析覆盖）。"""
    from datetime import datetime

    now = datetime.now().isoformat(timespec="seconds")
    with get_conn() as c:
        c.execute(
            """INSERT INTO ai_news_reports
                 (code, as_of, source, stance, summary, items_json, risks_json, omit_reason,
                  html, model_name, created_at, updated_at)
               VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
               ON CONFLICT(code, as_of, source) DO UPDATE SET
                 stance=excluded.stance, summary=excluded.summary,
                 items_json=excluded.items_json, risks_json=excluded.risks_json,
                 omit_reason=excluded.omit_reason, html=excluded.html,
                 model_name=excluded.model_name, updated_at=excluded.updated_at""",
            (code, as_of, source, report.get("stance"), report.get("summary"),
             json.dumps(report.get("items") or [], ensure_ascii=False),
             json.dumps(report.get("risks") or [], ensure_ascii=False),
             report.get("omit_reason"), report.get("html"), model_name, now, now),
        )


def _upsert_tech_report(code: str, as_of: str, source: str, report: dict, model_name: str = "") -> None:
    """个股技术面 AI 结果写入 ai_tech_reports。"""
    from datetime import datetime

    now = datetime.now().isoformat(timespec="seconds")
    with get_conn() as c:
        c.execute(
            """INSERT INTO ai_tech_reports
                 (code, as_of, source, trend_short, trend_mid, summary, levels_json, signals_json,
                  invalidation, html, model_name, created_at, updated_at)
               VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
               ON CONFLICT(code, as_of, source) DO UPDATE SET
                 trend_short=excluded.trend_short, trend_mid=excluded.trend_mid,
                 summary=excluded.summary, levels_json=excluded.levels_json,
                 signals_json=excluded.signals_json, invalidation=excluded.invalidation,
                 html=excluded.html, model_name=excluded.model_name, updated_at=excluded.updated_at""",
            (code, as_of, source, report.get("trend_short"), report.get("trend_mid"),
             report.get("summary"),
             json.dumps(report.get("key_levels") or {}, ensure_ascii=False),
             json.dumps(report.get("signals") or [], ensure_ascii=False),
             report.get("invalidation"), report.get("html"), model_name, now, now),
        )


def analyze_news(code: str, system_prompt: str | None = None, intensity: str = "normal") -> dict:
    """个股 AI 消息面分析：akshare 抓取近期新闻注入 + 公开知识 + 时效规则，落库 ai_news_reports。

    无激活模型 → ValueError。items 为空（AI 因时效放弃）也是合法结果，照常落库。
    system_prompt 非 None 时作为「用户附加要求」追加到默认指令后（前端弹窗可编辑）。
    intensity 分析强度 fast/normal/deep → 思考级别 low/全局/max（弹窗可选）。
    """
    model_cfg = get_active_model()
    if not model_cfg:
        raise ValueError("尚未配置/启用任何 AI 模型（点右上角「🤖 AI」配置）")
    as_of = now_as_of_datetime()
    ctx = {
        "code": code, "name": _stock_display_name(code), "as_of_datetime": as_of,
        "news": _ensure_stock_news(code),
    }
    # 输出 schema：仅「深入」保留 html 字段（快速/普通不要求 HTML 报告）
    schema = {k: v for k, v in _NEWS_SCHEMA.items() if intensity == "deep" or k != "html"}
    user = _schema_user(
        "消息面分析对象（news=系统抓取的该股近期新闻，按时间倒序；优先依据新闻正文判断，"
        "引用注明日期与来源；不足/过时再结合公开知识）：", ctx, schema)
    system = _NEWS_SYSTEM
    if intensity == "deep":
        system = f"{system}\n\n{_NEWS_HTML_REQUIREMENT}"
    if system_prompt:
        system = f"{system}\n\n[用户附加要求]\n{system_prompt}"
    inst = _intensity_instruction(intensity)
    if inst:
        system = f"{system}\n\n[分析强度]\n{inst}"
    try:
        raw = chat_json(model_cfg, system, user)
    except Exception as e:  # noqa: BLE001
        raise ValueError(str(e))
    report = _normalize_news(raw)
    try:
        _upsert_news_report(code, as_of, "single", report,
                            model_cfg.get("model") or model_cfg.get("name", ""))
    except Exception as e:  # noqa: BLE001 落库失败不阻断本次分析，但必须留痕便于排障
        logger.warning("[AI消息面] %s 落库失败：%s", code, e)
    logger.info("[AI消息面] %s 分析完成：%s", code, report["stance"])
    return {
        "code": code, "name": ctx["name"], "as_of": as_of, "source": "single",
        "model_name": model_cfg.get("model") or model_cfg.get("name", ""),
        "report": report,
    }


def analyze_technical(code: str, system_prompt: str | None = None, intensity: str = "normal") -> dict:
    """个股 AI 技术面分析：基于截至 as_of 的日/周/月K结构，落库 ai_tech_reports。

    无激活模型 → ValueError。无日K（bars 为空）时 AI 明说不下结论，照常落库。
    """
    model_cfg = get_active_model()
    if not model_cfg:
        raise ValueError("尚未配置/启用任何 AI 模型（点右上角「🤖 AI」配置）")
    as_of = now_as_of_datetime()
    _ensure_tech_kline(code)
    ctx = {
        "code": code, "name": _stock_display_name(code), "as_of_datetime": as_of,
        "bars_as_of": _tech_end_day(as_of),
        **build_technical_bars_multi(code, as_of),
    }
    schema = {k: v for k, v in _TECH_SCHEMA.items() if intensity == "deep" or k != "html"}
    user = _schema_user("技术面分析对象：", ctx, schema)
    system = _TECH_SYSTEM
    if intensity == "deep":
        system = f"{system}\n\n{_TECH_HTML_REQUIREMENT}"
    if system_prompt:
        system = f"{system}\n\n[用户附加要求]\n{system_prompt}"
    inst = _intensity_instruction(intensity)
    if inst:
        system = f"{system}\n\n[分析强度]\n{inst}"
    try:
        raw = chat_json(model_cfg, system, user)
    except Exception as e:  # noqa: BLE001
        raise ValueError(str(e))
    report = _normalize_technical(raw)
    try:
        _upsert_tech_report(code, as_of, "single", report,
                            model_cfg.get("model") or model_cfg.get("name", ""))
    except Exception as e:  # noqa: BLE001 落库失败不阻断本次分析，但必须留痕便于排障
        logger.warning("[AI技术面] %s 落库失败：%s", code, e)
    logger.info("[AI技术面] %s 分析完成：short=%s mid=%s", code,
                report["trend_short"], report["trend_mid"])
    return {
        "code": code, "name": ctx["name"], "as_of": as_of, "source": "single",
        "model_name": model_cfg.get("model") or model_cfg.get("name", ""),
        "report": report,
    }


def _batch_members(tags: list[str] | None = None, codes: list[str] | None = None) -> list[dict]:
    """批量分析成员：codes 直接指定（任意标的），否则按持仓组合（tags 筛选）。"""
    if codes:
        return [{"code": code, "name": _stock_display_name(code)} for code in codes]
    from app.analysis.portfolio import compute_portfolio

    holdings = compute_portfolio(tags=tags) or {}
    stocks = holdings.get("stocks") or []
    return [
        {"code": s.get("code"), "name": s.get("name")}
        for s in stocks if s.get("code")
    ]


def _coherence_key(tags: list[str] | None = None, codes: list[str] | None = None) -> tuple[str, str]:
    """批量组合标识（与资金流批量同口径）：codes → (indices, 排序 codes)；tags → (portfolio, 排序 tags)；缺省 '全部'。"""
    if codes:
        return "indices", ",".join(sorted(codes))
    if tags:
        return "portfolio", ",".join(sorted(tags))
    return "portfolio", "全部"


def _upsert_news_coherence(scope: str, scope_key: str, as_of: str, summary: str, html: str,
                           model_name: str = "") -> None:
    """整组合批量消息面整体输出（summary + html）写入 ai_news_coherence_reports（同 scope+key+as_of 覆盖）。"""
    now = datetime.now().isoformat(timespec="seconds")
    with get_conn() as c:
        c.execute(
            """INSERT INTO ai_news_coherence_reports
                 (scope, scope_key, as_of, summary, html, model_name, created_at, updated_at)
               VALUES(?,?,?,?,?,?,?,?)
               ON CONFLICT(scope, scope_key, as_of) DO UPDATE SET
                 summary=excluded.summary, html=excluded.html,
                 model_name=excluded.model_name, updated_at=excluded.updated_at""",
            (scope, scope_key, as_of, summary, html, model_name, now, now),
        )


def _upsert_tech_coherence(scope: str, scope_key: str, as_of: str, summary: str, html: str,
                           model_name: str = "") -> None:
    """整组合批量技术面整体输出（summary + html）写入 ai_tech_coherence_reports。"""
    now = datetime.now().isoformat(timespec="seconds")
    with get_conn() as c:
        c.execute(
            """INSERT INTO ai_tech_coherence_reports
                 (scope, scope_key, as_of, summary, html, model_name, created_at, updated_at)
               VALUES(?,?,?,?,?,?,?,?)
               ON CONFLICT(scope, scope_key, as_of) DO UPDATE SET
                 summary=excluded.summary, html=excluded.html,
                 model_name=excluded.model_name, updated_at=excluded.updated_at""",
            (scope, scope_key, as_of, summary, html, model_name, now, now),
        )


def get_news_coherence(scope: str, scope_key: str | None = None) -> dict | None:
    """读取该组合最近一次整组合批量消息面报告（ai_news_coherence_reports）。

    scope_key 精确匹配（可空 → 按 scope 取最近，F5 后组合子集状态可能已变作兜底）。
    """
    where, params = "WHERE scope=?", [scope]
    if scope_key:
        where += " AND scope_key=?"
        params.append(scope_key)
    with get_conn() as c:
        row = c.execute(
            f"SELECT scope, scope_key, as_of, summary, html, model_name "
            f"FROM ai_news_coherence_reports {where} "
            f"ORDER BY as_of DESC, updated_at DESC LIMIT 1",
            params,
        ).fetchone()
    if not row:
        return None
    return {
        "scope": row["scope"], "scope_key": row["scope_key"], "as_of": row["as_of"],
        "summary": row["summary"] or "", "html": row["html"] or "",
        "model_name": row["model_name"],
    }


def get_tech_coherence(scope: str, scope_key: str | None = None) -> dict | None:
    """读取该组合最近一次整组合批量技术面报告（ai_tech_coherence_reports）。"""
    where, params = "WHERE scope=?", [scope]
    if scope_key:
        where += " AND scope_key=?"
        params.append(scope_key)
    with get_conn() as c:
        row = c.execute(
            f"SELECT scope, scope_key, as_of, summary, html, model_name "
            f"FROM ai_tech_coherence_reports {where} "
            f"ORDER BY as_of DESC, updated_at DESC LIMIT 1",
            params,
        ).fetchone()
    if not row:
        return None
    return {
        "scope": row["scope"], "scope_key": row["scope_key"], "as_of": row["as_of"],
        "summary": row["summary"] or "", "html": row["html"] or "",
        "model_name": row["model_name"],
    }


def analyze_batch_news(tags: list[str] | None = None, codes: list[str] | None = None,
                       system_prompt: str | None = None, intensity: str = "normal") -> dict:
    """组合批量消息面 AI：所有成员一次发给 AI（省 token），逐只精简输出并落库 source='batch'。

    无激活模型/无成员 → ValueError。单只失败记日志继续不中断。
    返回 {as_of, count, reports:[{code,name,stance,summary,omit_reason}]}。
    """
    model_cfg = get_active_model()
    if not model_cfg:
        raise ValueError("尚未配置/启用任何 AI 模型（点右上角「🤖 AI」配置）")
    members = _batch_members(tags, codes)
    if not members:
        raise ValueError("该组合暂无持仓标的")
    as_of = now_as_of_datetime()
    ctx = {
        "as_of_datetime": as_of,
        "stocks": [
            {
                "code": m["code"], "name": m["name"],
                "news": _ensure_stock_news(m["code"], limit=_NEWS_BATCH_LIMIT, content_max=100),
            }
            for m in members
        ],
    }
    # 输出 schema：仅「深入」保留 html 字段（整组合整体 HTML，落 coherence 表）
    schema = {k: v for k, v in _BATCH_NEWS_SCHEMA.items() if intensity == "deep" or k != "html"}
    user = _schema_user(
        "组合内标的消息面分析（每只 news=系统抓取的近期新闻，按时间倒序；优先依据新闻正文判断，"
        "引用注明日期与来源；不足/过时再结合公开知识）：", ctx, schema)
    system = _BATCH_NEWS_SYSTEM
    if intensity == "deep":
        system = f"{system}\n\n{_BATCH_NEWS_HTML_REQUIREMENT}"
    if system_prompt:
        system = f"{system}\n\n[用户附加要求]\n{system_prompt}"
    inst = _intensity_instruction(intensity)
    if inst:
        system = f"{system}\n\n[分析强度]\n{inst}"
    try:
        raw = chat_json(model_cfg, system, user)
    except Exception as e:  # noqa: BLE001
        raise ValueError(str(e))
    name_map = {m["code"]: m["name"] for m in members}
    model_tag = model_cfg.get("model") or model_cfg.get("name", "")
    reports = []
    for item in raw.get("stocks") or []:
        if not isinstance(item, dict):
            continue
        code = str(item.get("code") or "").strip()
        if not code or code not in name_map:
            continue  # 只落库本组合内的标的
        try:
            report = _normalize_news(item)
            _upsert_news_report(code, as_of, "batch", report, model_tag)
        except Exception:  # noqa: BLE001 单只失败记日志继续不中断
            logger.warning("[AI消息面-批量] %s 落库失败，跳过", code)
            continue
        reports.append({
            "code": code, "name": name_map[code], "stance": report["stance"],
            "summary": report["summary"], "omit_reason": report["omit_reason"],
            "source": "batch",
        })
    # 整组合整体输出（summary + html）落 ai_news_coherence_reports；每次批量都写最新一条，
    # GET 按 as_of DESC 取最近——普通/深入均覆盖旧结果（用户要看最新，不保留旧深入 HTML）
    batch_summary = str(raw.get("summary") or "")
    batch_html = str(raw.get("html") or "")
    scope, scope_key = _coherence_key(tags, codes)
    try:
        _upsert_news_coherence(scope, scope_key, as_of, batch_summary, batch_html, model_tag)
    except Exception as e:  # noqa: BLE001 整组合落库失败不阻断批量
        logger.warning("[AI消息面-批量] 整组合 coherence 落库失败：%s", e)
    logger.info("[AI消息面-批量] %d/%d 只分析完成，整组合HTML=%d字",
                len(reports), len(members), len(batch_html))
    return {"as_of": as_of, "count": len(reports), "reports": reports,
            "summary": batch_summary, "html": batch_html}


def analyze_batch_technical(tags: list[str] | None = None, codes: list[str] | None = None,
                            system_prompt: str | None = None, intensity: str = "normal") -> dict:
    """组合批量技术面 AI：一次拉多只日/周/月K（组合每只少传控 token），逐只精简输出并落库 source='batch'。

    无激活模型/无成员 → ValueError。单只失败记日志继续不中断。
    返回 {as_of, count, reports:[{code,name,trend_short,trend_mid,summary}]}。
    """
    model_cfg = get_active_model()
    if not model_cfg:
        raise ValueError("尚未配置/启用任何 AI 模型（点右上角「🤖 AI」配置）")
    members = _batch_members(tags, codes)
    if not members:
        raise ValueError("该组合暂无持仓标的")
    as_of = now_as_of_datetime()
    multi_map = build_technical_bars_many_multi(
        [m["code"] for m in members], as_of, daily_limit=60, weekly_limit=36, monthly_limit=24)
    ctx = {
        "as_of_datetime": as_of,
        "bars_as_of": _tech_end_day(as_of),
        "stocks": [
            {"code": m["code"], "name": m["name"], **multi_map.get(m["code"], {})}
            for m in members
        ],
    }
    schema = {k: v for k, v in _BATCH_TECH_SCHEMA.items() if intensity == "deep" or k != "html"}
    user = _schema_user("组合内标的技术面分析（K线已截断到 as_of 对应交易日）：", ctx, schema)
    system = _BATCH_TECH_SYSTEM
    if intensity == "deep":
        system = f"{system}\n\n{_BATCH_TECH_HTML_REQUIREMENT}"
    if system_prompt:
        system = f"{system}\n\n[用户附加要求]\n{system_prompt}"
    inst = _intensity_instruction(intensity)
    if inst:
        system = f"{system}\n\n[分析强度]\n{inst}"
    try:
        raw = chat_json(model_cfg, system, user)
    except Exception as e:  # noqa: BLE001
        raise ValueError(str(e))
    name_map = {m["code"]: m["name"] for m in members}
    model_tag = model_cfg.get("model") or model_cfg.get("name", "")
    reports = []
    for item in raw.get("stocks") or []:
        if not isinstance(item, dict):
            continue
        code = str(item.get("code") or "").strip()
        if not code or code not in name_map:
            continue
        try:
            report = _normalize_technical(item)
            _upsert_tech_report(code, as_of, "batch", report, model_tag)
        except Exception:  # noqa: BLE001
            logger.warning("[AI技术面-批量] %s 落库失败，跳过", code)
            continue
        reports.append({
            "code": code, "name": name_map[code], "trend_short": report["trend_short"],
            "trend_mid": report["trend_mid"], "summary": report["summary"],
            "source": "batch",
        })
    # 整组合整体输出（summary + html）落 ai_tech_coherence_reports；每次批量都写最新一条，
    # GET 按 as_of DESC 取最近——普通/深入均覆盖旧结果（用户要看最新，不保留旧深入 HTML）
    batch_summary = str(raw.get("summary") or "")
    batch_html = str(raw.get("html") or "")
    scope, scope_key = _coherence_key(tags, codes)
    try:
        _upsert_tech_coherence(scope, scope_key, as_of, batch_summary, batch_html, model_tag)
    except Exception as e:  # noqa: BLE001 整组合落库失败不阻断批量
        logger.warning("[AI技术面-批量] 整组合 coherence 落库失败：%s", e)
    logger.info("[AI技术面-批量] %d/%d 只分析完成，整组合HTML=%d字",
                len(reports), len(members), len(batch_html))
    return {"as_of": as_of, "count": len(reports), "reports": reports,
            "summary": batch_summary, "html": batch_html}


def get_stock_news_report(code: str) -> dict | None:
    """该股最近落库消息面结果（跨 batch/single 取最新一条）。无则 None。"""
    with get_conn() as c:
        row = c.execute(
            """SELECT code, as_of, source, stance, summary, items_json, risks_json, omit_reason,
                      html, model_name
               FROM ai_news_reports WHERE code=?
               ORDER BY as_of DESC, updated_at DESC LIMIT 1""",
            (code,),
        ).fetchone()
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
        "code": row["code"], "as_of": row["as_of"], "source": row["source"],
        "stance": row["stance"], "summary": row["summary"],
        "items": _loads(row["items_json"]), "risks": _loads(row["risks_json"]),
        "omit_reason": row["omit_reason"], "html": row["html"] or "",
        "model_name": row["model_name"],
    }


def get_stock_tech_report(code: str) -> dict | None:
    """该股最近落库技术面结果（跨 batch/single 取最新一条）。无则 None。"""
    with get_conn() as c:
        row = c.execute(
            """SELECT code, as_of, source, trend_short, trend_mid, summary, levels_json, signals_json,
                      invalidation, html, model_name
               FROM ai_tech_reports WHERE code=?
               ORDER BY as_of DESC, updated_at DESC LIMIT 1""",
            (code,),
        ).fetchone()
    if not row:
        return None
    levels = {}
    try:
        levels = json.loads(row["levels_json"]) if row["levels_json"] else {}
    except Exception:  # noqa: BLE001
        levels = {}
    signals = []
    try:
        signals = json.loads(row["signals_json"]) if row["signals_json"] else []
    except Exception:  # noqa: BLE001
        signals = []
    return {
        "code": row["code"], "as_of": row["as_of"], "source": row["source"],
        "trend_short": row["trend_short"], "trend_mid": row["trend_mid"],
        "summary": row["summary"], "key_levels": levels,
        "signals": [str(x) for x in signals if str(x).strip()],
        "invalidation": row["invalidation"], "html": row["html"] or "",
        "model_name": row["model_name"],
    }


def list_news_reports(codes: list[str]) -> dict:
    """按 codes 过滤，每只取最近一条消息面结果（供组合批量面板 F5 重建，一次拉取）。"""
    out: dict[str, dict] = {}
    if not codes:
        return out
    ph = ",".join("?" * len(codes))
    with get_conn() as c:
        rows = c.execute(
            f"""SELECT code, as_of, source, stance, summary, omit_reason
                FROM ai_news_reports WHERE code IN ({ph})
                ORDER BY as_of DESC, updated_at DESC""",
            list(codes),
        ).fetchall()
    for r in rows:
        if r["code"] in out:
            continue  # 已取到该 code 最新一条
        out[r["code"]] = {
            "stance": r["stance"], "summary": r["summary"], "as_of": r["as_of"],
            "source": r["source"], "omit_reason": r["omit_reason"],
        }
    return out


def list_tech_reports(codes: list[str]) -> dict:
    """按 codes 过滤，每只取最近一条技术面结果（供组合批量面板 F5 重建，一次拉取）。"""
    out: dict[str, dict] = {}
    if not codes:
        return out
    ph = ",".join("?" * len(codes))
    with get_conn() as c:
        rows = c.execute(
            f"""SELECT code, as_of, source, trend_short, trend_mid, summary
                FROM ai_tech_reports WHERE code IN ({ph})
                ORDER BY as_of DESC, updated_at DESC""",
            list(codes),
        ).fetchall()
    for r in rows:
        if r["code"] in out:
            continue
        out[r["code"]] = {
            "trend_short": r["trend_short"], "trend_mid": r["trend_mid"],
            "summary": r["summary"], "as_of": r["as_of"], "source": r["source"],
        }
    return out


# 弹窗展示给用户的可编辑重点要求。完整 system（含 JSON schema）用户看不懂，
# 只展示用户可能想改的「要求块」；用户改了才作为「用户附加要求」追加到完整指令后。
_EDITABLE_PROMPTS = {
    "stock": (
        "请从周期性、护城河、基本面、增长、股息、估值、同业竞争、资金面、消息面、技术面"
        "十个维度分析该股；给出总分、质量评级 grade（优秀/良好/一般/较差）与操作建议 action"
        "（加仓/持有/观望/减仓/清仓，与 grade 解耦）；重点提示风险与交叉陷阱。"
    ),
    "fundflow": (
        "请判断资金与股价的相关性/背离、主力资金意图（含大单拆小单的伪装），"
        "简明给出结论与注意点。"
    ),
    "batch": (
        "请逐只判断资金×股价相关性；对组合整体逐窗口横向对比，判断资金面联动格局"
        "（共振 / 跷跷板虹吸 / 分化），优先看成交金额，并对比分时成交额与价格的关系，"
        "给出组合层面结论。"
    ),
    "portfolio": (
        "请从组合整体按共享维（基本面/估值/资金面/消息面/技术面）+ 结构集中度 + 标签契合度"
        "打分；给出总分、质量评级 grade、操作建议 action（加仓/持有/观望/减仓/清仓）与风险分，"
        "及改进建议。"
    ),
    "daily": (
        "请逐笔评估当日交易合理性（时机/价格执行/仓位/纪律），再汇总当日整体；"
        "给出总分、质量评级与操作建议（可复制/谨慎复制/避免重复）。"
    ),
    "news": (
        "请判断该股近期消息面（公司公告、行业与政策事件、财报节点等）与时效性，"
        "给出利多/中性/利空立场与近期事件列表；无足够新信息时如实说明，不要编造。"
    ),
    "technical": (
        "请用白话解读该股截至最近交易日的价格结构（趋势、支撑压力位、量能），"
        "给出关键价位与证伪条件，指出与资金面/估值的潜在矛盾；无日K则明说不下结论。"
    ),
    "news_batch": (
        "请对组合内每只标的逐一判断近期消息面与时效性，给出利多/中性/利空立场与一句话结论；"
        "无足够新信息时如实说明，不要编造。"
    ),
    "tech_batch": (
        "请对组合内每只标的用白话解读截至最近交易日的价格结构（趋势、支撑压力位、量能），"
        "给出关键价位与证伪条件；无日K则明说不下结论。"
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
        # 新旧窗口名兼容：'day'/'week'/'month' 匹配旧 '1d'/'7d'/'30d' 落库报告
        norm = _norm_flow_window(window)
        alias = {"day": "1d", "week": "7d", "month": "30d"}.get(norm)
        sql = (
            """SELECT code, trade_date, source, window, correlation, summary, main_force, rhythm,
                      divergence, alerts, conclusion, html, model_name
               FROM ai_fundflow_reports
               WHERE code=? AND (window=? OR window=?)
               ORDER BY CASE window WHEN ? THEN 0 WHEN ? THEN 1 ELSE 2 END,
                        trade_date DESC, updated_at DESC LIMIT 1"""
        )
        params = (code, norm, alias, norm, alias) if alias else (code, norm, norm, norm, norm)
    else:
        sql, params = (
            """SELECT code, trade_date, source, window, correlation, summary, main_force, rhythm,
                      divergence, alerts, conclusion, html, model_name
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
        "conclusion": row["conclusion"], "html": row["html"] or "",
        "model_name": row["model_name"],
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

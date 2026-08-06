"""AI 评分服务：标签偏好 + 组合/每日 AI 打分（彻底取代公式评分）。

- 标签偏好（tag_prefs）：用户输入简短偏好，保存时自动请求 AI 补全成完整「评分指引」
  （draft），确认后（confirmed）才用于打分。
- 组合 AI 打分：手动触发，把 compute_portfolio 全量聚合 + 各标签「评分指引」带给 AI，
  AI 直接输出 0-100 总分 + 评级（A/B/C/D）+ 总结/建议/风险/理由/详细分析。支持标签筛选。
- 每日 AI 打分：每天一次调用，AI 按每笔交易所属标签的「评分指引」逐笔打分，再综合当日总分。
  偏好提示词按当天涉及的标签去重（50 笔同标签也只带 1 条）。
- AI 评级（A/B/C/D）由 AI 直接给出，后端只做格式校验，不做 score→grade 转换。
- 复用 app/services/ai.py 的 get_active_model / chat_json（OpenAI 兼容）。
"""
import hashlib
import json
import logging
import threading
from datetime import datetime

from app.models.db import get_conn
from app.services import ai

logger = logging.getLogger("ai_scoring")

# AI 评级中文显示标签（仅展示；评级由 AI 直接给出）
_RATING_NAMES = {"A": "优秀", "B": "良好", "C": "一般", "D": "较差"}
_RATING_KEYS = set(_RATING_NAMES)

_NO_MODEL_MSG = "尚未配置/启用任何 AI 模型（点右上角「🤖 AI」配置）"


def _now() -> str:
    return datetime.now().isoformat(timespec="seconds")


def _clamp_score(v, lo: float = 0.0, hi: float = 100.0, default: float = 50.0) -> float:
    """0-100 分数钳制；坏值回退默认（AI 打分异常不致命）。"""
    if v is None or v == "" or isinstance(v, bool):
        return default
    try:
        return max(lo, min(hi, float(v)))
    except (TypeError, ValueError):
        return default


def _check_rating(r) -> str:
    """校验 AI 直接给出的评级 ∈ {A,B,C,D}；非法兜底 C（仅防脏数据，不做换算）。"""
    r = str(r or "C").upper().strip()
    return r if r in _RATING_KEYS else "C"


def _str_list(v) -> list[str]:
    if not isinstance(v, list):
        v = [v] if v else []
    return [str(x) for x in v]


# ============================================================ 标签偏好

def list_tag_prefs() -> list[dict]:
    with get_conn() as c:
        rows = c.execute("SELECT * FROM tag_prefs ORDER BY tag").fetchall()
    return [dict(r) for r in rows]


def get_tag_pref(tag: str) -> dict | None:
    with get_conn() as c:
        row = c.execute("SELECT * FROM tag_prefs WHERE tag=?", (tag,)).fetchone()
    return dict(row) if row else None


def upsert_tag_pref(tag: str, raw_pref: str, prompt: str | None = None,
                    status: str | None = None) -> dict:
    """保存标签偏好。status 显式用之；否则 prompt 非空→confirmed，否则 draft（无 prompt）。"""
    tag = (tag or "").strip()
    raw_pref = (raw_pref or "").strip()
    if not tag:
        raise ValueError("标签不能为空")
    if not raw_pref:
        raise ValueError("偏好描述不能为空")
    if status is None:
        status = "confirmed" if prompt else "draft"
    if status not in ("draft", "confirmed"):
        status = "draft"
    if prompt is not None:
        prompt = str(prompt).strip()[:2000]
    now = _now()
    model = ai.get_active_model()
    model_name = model["name"] if model else None
    with get_conn() as c:
        c.execute(
            """INSERT INTO tag_prefs(tag, raw_pref, prompt, status, model_name, created_at, updated_at)
               VALUES(?,?,?,?,?,?,?)
               ON CONFLICT(tag) DO UPDATE SET
                 raw_pref=excluded.raw_pref, prompt=excluded.prompt, status=excluded.status,
                 model_name=excluded.model_name, updated_at=excluded.updated_at""",
            (tag, raw_pref, prompt, status, model_name, now, now),
        )
        row = c.execute("SELECT * FROM tag_prefs WHERE tag=?", (tag,)).fetchone()
    return dict(row)


def delete_tag_pref(tag: str) -> None:
    with get_conn() as c:
        c.execute("DELETE FROM tag_prefs WHERE tag=?", (tag,))


def confirm_tag_pref(tag: str) -> dict:
    """确认偏好生效（仅 confirmed 用于打分）。"""
    row = get_tag_pref(tag)
    if not row or not row["prompt"]:
        raise ValueError("该标签尚无评分指引，请先 AI 补全或手动填写")
    with get_conn() as c:
        c.execute("UPDATE tag_prefs SET status='confirmed', updated_at=? WHERE tag=?", (_now(), tag))
        r = c.execute("SELECT * FROM tag_prefs WHERE tag=?", (tag,)).fetchone()
    return dict(r)


def confirmed_prefs() -> dict[str, str]:
    """已确认且有指引的偏好 {tag: prompt}，供上下文构建使用。"""
    with get_conn() as c:
        rows = c.execute(
            "SELECT tag, prompt FROM tag_prefs WHERE status='confirmed' "
            "AND prompt IS NOT NULL AND prompt<>''"
        ).fetchall()
    return {r["tag"]: r["prompt"] for r in rows}


_TAG_PREF_SYSTEM = (
    "你是投资偏好提炼助手。用户给出一段简短的投资偏好，请扩展成一份完整、自洽的「评分指引」，"
    "供 AI 给该标签下的股票/交易打分时作为准则。要求覆盖：估值态度、质量/成长、股息、风险偏好、"
    "加分项与回避项。全简体中文；输出严格 JSON。"
)
_TAG_PREF_OUTPUT = {"prompt": "完整评分指引（简体中文，≤800字）"}


def expand_tag_prompt(tag: str, raw_pref: str) -> dict:
    """请求 AI 补全偏好 → 存为 draft（待确认）。返回该行。"""
    model = ai.get_active_model()
    if not model:
        raise ValueError(_NO_MODEL_MSG)
    raw_pref = (raw_pref or "").strip()
    if not raw_pref:
        raise ValueError("偏好描述不能为空")
    user = f"标签：{tag}\n用户原始偏好：{raw_pref}\n\n请输出：\n" + json.dumps(_TAG_PREF_OUTPUT, ensure_ascii=False)
    raw = ai.chat_json(model, _TAG_PREF_SYSTEM, user)
    prompt = str(raw.get("prompt") or raw_pref).strip()
    if not prompt:
        prompt = raw_pref
    return upsert_tag_pref(tag, raw_pref, prompt=prompt, status="draft")


# ============================================================ 组合 AI

def _holdings_tuples(tags: list[str] | None = None) -> list[tuple]:
    """筛选标签内的活跃持仓 (code, round(qty,6), currency) 排序元组；tags 为空=全部。纯读。

    标签解析与 compute_portfolio/get_holdings 同口径（NULL 标签按 auto_tag 兜底），
    保证 profile_hash 与组合筛选视角一致。
    """
    from app.data.base import auto_tag

    with get_conn() as c:
        rows = c.execute(
            """SELECT h.code, h.quantity, COALESCE(h.currency,'CNY') AS currency,
                      COALESCE(s.tag,'') AS tag, COALESCE(s.name,'') AS name
               FROM holdings h LEFT JOIN stocks s ON h.code=s.code
               WHERE h.status='active'"""
        ).fetchall()
    if tags is not None:
        tag_set = set(tags)
        rows = [r for r in rows if ((r["tag"] or auto_tag(r["code"], r["name"]) or "") in tag_set)]
    out = []
    for r in rows:
        out.append((str(r["code"]), round(float(r["quantity"] or 0.0), 6), str(r["currency"])))
    return sorted(out)


def portfolio_profile_hash(tags: list[str] | None = None) -> str:
    """稳定画像哈希：持仓(筛选内)+已确认偏好(筛选内)+标签筛选+模型。绝不含时间戳/价格。"""
    prefs = confirmed_prefs()
    if tags is not None:
        tag_set = set(tags)
        prefs = {k: v for k, v in prefs.items() if k in tag_set}
    model = ai.get_active_model()
    model_key = f"{model['base_url']}|{model['model']}" if model else ""
    seed = json.dumps({
        "holdings": _holdings_tuples(tags),
        "prefs": sorted(prefs.items()),
        "tags": sorted(tags) if tags else [],
        "model": model_key,
    }, sort_keys=True, ensure_ascii=False)
    return hashlib.md5(seed.encode("utf-8")).hexdigest()[:16]


def build_portfolio_context(tags: list[str] | None = None) -> dict:
    """组合汇总好的所有信息（compute_portfolio 全量聚合）+ 各标签「评分指引」。"""
    from app.analysis.portfolio import compute_portfolio

    p = compute_portfolio(tags=tags)
    _KEEP_STOCK = (
        "code", "name", "tag", "is_etf", "currency", "weight", "value_cny", "pnl_pct",
        "day_pnl", "pe", "pb", "pe_pct", "pb_pct", "dv", "roe", "profit_yoy",
        "missing_fx", "missing",
    )
    _KEEP_TAG = (
        "tag", "stocks_count", "total_value", "weight", "pnl_pct", "pe", "pb",
        "pe_pct", "pb_pct", "dv", "roe", "profit_yoy",
    )
    prefs = confirmed_prefs()
    if tags is not None:
        tag_set = set(tags)
        prefs = {k: v for k, v in prefs.items() if k in tag_set}
    return {
        "portfolio": p["portfolio"],
        "weights": p["weights"],
        "stocks": [{k: s.get(k) for k in _KEEP_STOCK} for s in p["stocks"]],
        "tag_weights": p["tag_weights"],
        "tags": [{k: t.get(k) for k in _KEEP_TAG} for t in p.get("tags", [])],
        "all_tags": p["all_tags"],
        "selected_tags": sorted(tags) if tags else [],
        "tag_prefs": prefs,
    }


_PORTFOLIO_SYSTEM = (
    "你是资深个人投资组合分析师。系统提供当前组合的聚合数据（人民币口径）与各标签的「评分指引」。"
    "请综合打分：优先依据提供的结构化数据；各持仓所属标签的「评分指引」是评分的核心准则，"
    "不同标签的持仓按各自指引分别衡量后形成整体判断。"
    "直接给出 0-100 总分与评级（A=优秀/B=良好/C=一般/D=较差），并给出总结、建议、风险、核心理由。"
    "同时生成一份完整、独立、可读性强的 HTML 详细报告（字段 html），供用户新开页面查看。"
    "已提供的持仓明细、标签板块、估值等原始数据用户在本应用已可查看，HTML 中**不要原样罗列这些数据表格**；"
    "把篇幅全部用于**深入分析**：逐标签的深度解读、组合结构与集中度问题、指标背后的含义、"
    "风险与机会、情景推演、具体操作建议及理由，多给有洞察力的判断而非数据复述。"
    "要求：自包含单文件、内联 CSS、不引用任何外部资源、不得包含 <script> 或任何可执行代码；"
    "简体中文；结构清晰、排版美观、层次分明。"
    "输出语言：所有文字字段用简体中文。输出严格 JSON，不要任何额外文字。"
)
_PORTFOLIO_OUTPUT_SCHEMA = {
    "score": "0-100 整数总分",
    "rating": "A|B|C|D 评级（直接给出，不要按分数换算）",
    "summary": "一句话总体结论",
    "advice": ["建议数组（简体中文）"],
    "risks": ["风险数组（简体中文）"],
    "reasons": ["核心理由数组（简体中文）"],
    "html": "完整独立 HTML 详细报告源代码（自包含、内联 CSS、无外部依赖、无脚本、简体中文，聚焦深入分析，不罗列原始数据）",
}


def _normalize_portfolio_report(data: dict) -> dict:
    if not isinstance(data, dict):
        data = {}
    rating = _check_rating(data.get("rating"))
    return {
        "score": round(_clamp_score(data.get("score"), 0, 100), 1),
        "rating": rating,
        "rating_name": _RATING_NAMES[rating],
        "summary": str(data.get("summary") or ""),
        "advice": _str_list(data.get("advice")),
        "risks": _str_list(data.get("risks")),
        "reasons": _str_list(data.get("reasons")),
        "html": str(data.get("html") or ""),   # AI 生成的完整 HTML 报告（可空 → 前端不显示入口）
    }


def _tags_key(tags: list[str] | None = None) -> list[str]:
    """标签组合的规范化 key：None/空 → []（全部）。"""
    return sorted(tags) if tags else []


def score_portfolio(tags: list[str] | None = None) -> dict:
    """手动触发组合 AI 打分：AI 一次调用 → 规整 → 按画像哈希落库。

    **每个标签组合各自存一份**（tags_json 区分）：打个股不覆盖 个股+港股，也不覆盖 全部。
    """
    model = ai.get_active_model()
    if not model:
        raise ValueError(_NO_MODEL_MSG)
    ctx = build_portfolio_context(tags)
    user = (
        "组合聚合数据：\n" + json.dumps(ctx, ensure_ascii=False, default=str) + "\n\n"
        "请输出严格 JSON，结构如下：\n" + json.dumps(_PORTFOLIO_OUTPUT_SCHEMA, ensure_ascii=False)
    )
    raw = ai.chat_json(model, _PORTFOLIO_SYSTEM, user)
    report = _normalize_portfolio_report(raw)
    phash = portfolio_profile_hash(tags)
    tags_json = json.dumps(_tags_key(tags))
    now = _now()
    with get_conn() as c:
        c.execute(
            """INSERT INTO ai_portfolio_reports(profile_hash, tags_json, report_json, model_name, created_at, updated_at)
               VALUES(?,?,?,?,?,?)
               ON CONFLICT(profile_hash) DO UPDATE SET
                 tags_json=excluded.tags_json, report_json=excluded.report_json,
                 model_name=excluded.model_name, updated_at=excluded.updated_at""",
            (phash, tags_json, json.dumps(report, ensure_ascii=False), model["name"], now, now),
        )
    logger.info("[AI打分] 组合%s 完成：%s 分（%s）", f"筛选{tags}" if tags else "全部", report["score"], report["rating"])
    return {"report": report, "profile_hash": phash, "model_name": model["name"], "created_at": now}


def get_portfolio_report(tags: list[str] | None = None) -> dict | None:
    """读取该标签组合的 AI 报告（各组合分开存储）。

    - 当前画像哈希精确命中 → stale=False；
    - 同组合但画像已变化（持仓/偏好/模型变了）→ 返回旧报告 + stale=True（建议重新打分）；
    - 该组合从未打分 → None。纯读。
    """
    phash = portfolio_profile_hash(tags)
    tags_key = _tags_key(tags)
    with get_conn() as c:
        # 1) 当前画像精确命中
        row = c.execute(
            "SELECT * FROM ai_portfolio_reports WHERE profile_hash=?", (phash,)
        ).fetchone()
        if row:
            d = dict(row)
            d["report"] = json.loads(d.pop("report_json"))
            d["tags"] = json.loads(d.pop("tags_json") or "[]")
            d["profile_hash"] = d.pop("profile_hash", "")
            d["stale"] = False
            return d
        # 2) 同组合的旧画像（组合已变化）→ 标 stale
        rows = c.execute(
            "SELECT * FROM ai_portfolio_reports ORDER BY updated_at DESC"
        ).fetchall()
    for r in rows:
        try:
            same = json.loads(r["tags_json"] or "[]") == tags_key
        except (ValueError, TypeError):
            same = False
        if same:
            d = dict(r)
            d["report"] = json.loads(d.pop("report_json"))
            d["tags"] = json.loads(d.pop("tags_json") or "[]")
            d["profile_hash"] = d.pop("profile_hash", "")
            d["stale"] = d["profile_hash"] != phash
            return d
    return None


def invalidate_portfolio() -> None:
    """清空组合 AI 报告（改标签/切模型/重置时）。"""
    with get_conn() as c:
        c.execute("DELETE FROM ai_portfolio_reports")


# ============================================================ 每日交易 AI

def _trade_rows(score_date: str) -> list[dict]:
    """某交易日的 buy/sell 交易（adjust 不参与评分）。"""
    with get_conn() as c:
        rows = c.execute(
            """SELECT t.*, s.name AS name, s.tag AS tag
               FROM trades t LEFT JOIN stocks s ON t.code=s.code
               WHERE date(t.trade_time)=? AND t.side IN ('buy','sell')
               ORDER BY t.trade_time, t.id""",
            (score_date,),
        ).fetchall()
    return [dict(r) for r in rows]


def _stock_factors(code: str) -> dict:
    """该股当前估值/分位/涨跌因子（读缓存零网络；缺失留 None）。"""
    from app.analysis.valuation import compute_live
    from app.services.quote import get_quote

    f = {}
    try:
        live = compute_live(code) or {}
        for k in ("pe", "pb", "pe_pct", "pb_pct", "dv_ratio", "roe_ttm", "profit_yoy_ttm",
                  "revenue_yoy_ttm", "fwd_pe", "fwd_pb", "total_mv"):
            f[k] = live.get(k)
    except Exception:  # noqa: BLE001 单股因子缺失不阻断整日打分
        pass
    try:
        q = get_quote(code) or {}
        f["pct_chg"] = q.get("pct_chg")
    except Exception:  # noqa: BLE001
        pass
    return f


def build_daily_context(score_date: str) -> dict:
    """当日每笔交易 + 每股因子 + 标签；tag_prefs 按当天涉及标签去重（各一份）。"""
    from app.data.base import auto_tag

    rows = _trade_rows(score_date)
    prefs = confirmed_prefs()
    trades = []
    used_tags: set[str] = set()
    for r in rows:
        tag = (r.get("tag") or "").strip() or auto_tag(r["code"], r.get("name") or "")
        used_tags.add(tag)
        trades.append({
            "trade_id": r["id"],
            "code": r["code"],
            "name": r.get("name") or r["code"],
            "tag": tag,
            "side": r["side"],
            "price": r["price"],
            "quantity": r["quantity"],
            "amount": r["amount"],
            "amount_cny": r["amount_cny"],
            "fee": r["fee"] or 0.0,
            "trade_time": r["trade_time"],
            "factors": _stock_factors(r["code"]),
        })
    tag_prefs = {
        t: prefs.get(t) or "（该标签无已确认评分指引，请按一般投资纪律评判）"
        for t in sorted(used_tags)
    }
    return {"score_date": score_date, "trades": trades, "tag_prefs": tag_prefs}


_DAILY_SYSTEM = (
    "你是交易复盘分析师。系统提供某交易日的每笔买卖交易、该股估值因子，以及各标签的「评分指引」。"
    "请按每笔交易所属标签的「评分指引」逐笔评分并给出评级，再综合当日多笔交易的节奏/集中度/方向"
    "给出当日总分与评级。评分准则：不同标签的交易按各自指引分别衡量。"
    "直接给出 0-100 总分与评级（A/B/C/D），每笔交易也给出分数、评级与一句话点评。"
    "同时生成一份完整、独立、可读性强的 HTML 复盘报告（字段 html），供用户新开页面查看。"
    "已提供的每笔交易明细（价格/数量/金额/估值因子）用户在本应用已可查看，HTML 中**不要原样罗列交易数据表**；"
    "把篇幅全部用于**深入复盘**：逐笔交易质量背后的原因、买卖时机与价格判断、当日节奏与情绪、"
    "与各标签偏好的吻合度、集中度与风险、改进点与后续策略，多给有洞察力的判断而非数据复述。"
    "要求：自包含单文件、内联 CSS、不引用任何外部资源、不得包含 <script> 或任何可执行代码；"
    "简体中文；结构清晰、排版美观、层次分明。"
    "输出语言：所有文字字段用简体中文。输出严格 JSON，不要任何额外文字。"
)
_DAILY_OUTPUT_SCHEMA = {
    "score": "0-100 当日综合总分",
    "rating": "A|B|C|D 当日评级（直接给出）",
    "summary": "当日一句话总结",
    "advice": ["建议数组（简体中文）"],
    "risks": ["风险数组（简体中文）"],
    "reasons": ["核心理由数组（简体中文）"],
    "html": "完整独立 HTML 复盘报告源代码（自包含、内联 CSS、无外部依赖、无脚本、简体中文，聚焦深入分析，不罗列原始数据）",
    "trades": [
        {"trade_id": "整数，对应输入交易", "score": "0-100 该笔得分", "rating": "A|B|C|D 该笔评级",
         "comment": "该笔一句话点评"}
    ],
}


def _normalize_daily_report(data: dict, trades: list[dict]) -> dict:
    """规整 AI 每日输出：按输入交易顺序对齐（trade_id 精确匹配，缺则按位置兜底），
    并把交易显示字段并入各 trade 项，保证每笔真实交易各有一条。"""
    if not isinstance(data, dict):
        data = {}
    day_rating = _check_rating(data.get("rating"))
    by_id: dict[int, dict] = {}
    raw_trades = data.get("trades") if isinstance(data.get("trades"), list) else []
    for t in raw_trades:
        if isinstance(t, dict) and t.get("trade_id") is not None:
            try:
                by_id[int(t["trade_id"])] = t
            except (TypeError, ValueError):
                pass
    ai_trades = []
    for tr in trades:
        # 只按 trade_id 精确匹配（prompt 已要求 AI 回填 trade_id；位置兜底会把未知 id 项错配到真实交易）
        entry = by_id.get(tr["trade_id"], {})
        if not isinstance(entry, dict):
            entry = {}
        tscore = round(_clamp_score(entry.get("score"), 0, 100), 1)
        trating = _check_rating(entry.get("rating"))
        ai_trades.append({
            "trade_id": tr["trade_id"],
            "code": tr["code"], "name": tr["name"], "tag": tr["tag"],
            "side": tr["side"], "price": tr["price"], "quantity": tr["quantity"],
            "amount": tr["amount"], "amount_cny": tr["amount_cny"], "fee": tr["fee"],
            "trade_time": tr["trade_time"],
            "score": tscore, "rating": trating, "rating_name": _RATING_NAMES[trating],
            "comment": str(entry.get("comment") or ""),
        })
    return {
        "score": round(_clamp_score(data.get("score"), 0, 100), 1),
        "rating": day_rating,
        "rating_name": _RATING_NAMES[day_rating],
        "summary": str(data.get("summary") or ""),
        "advice": _str_list(data.get("advice")),
        "risks": _str_list(data.get("risks")),
        "reasons": _str_list(data.get("reasons")),
        "html": str(data.get("html") or ""),
        "trades": ai_trades,
    }


def score_daily(score_date: str) -> dict | None:
    """当日 AI 打分：一次调用逐笔+汇总 → 落库。当日无 buy/sell 交易 → 失效并返回 None。"""
    model = ai.get_active_model()
    if not model:
        raise ValueError(_NO_MODEL_MSG)
    trades = _trade_rows(score_date)
    if not trades:
        invalidate_daily(score_date)
        return None
    ctx = build_daily_context(score_date)
    user = (
        "当日交易数据：\n" + json.dumps(ctx, ensure_ascii=False, default=str) + "\n\n"
        "请输出严格 JSON，结构如下：\n" + json.dumps(_DAILY_OUTPUT_SCHEMA, ensure_ascii=False)
    )
    raw = ai.chat_json(model, _DAILY_SYSTEM, user)
    report = _normalize_daily_report(raw, ctx["trades"])
    now = _now()
    with get_conn() as c:
        c.execute(
            """INSERT INTO ai_daily_reports(score_date, report_json, model_name, trades_count, created_at, updated_at)
               VALUES(?,?,?,?,?,?)
               ON CONFLICT(score_date) DO UPDATE SET
                 report_json=excluded.report_json, model_name=excluded.model_name,
                 trades_count=excluded.trades_count, updated_at=excluded.updated_at""",
            (score_date, json.dumps(report, ensure_ascii=False), model["name"], len(trades), now, now),
        )
    logger.info("[AI打分] 当日 %s 完成：%s 分（%s）%d 笔",
                score_date, report["score"], report["rating"], len(trades))
    return {"score_date": score_date, "report": report, "model_name": model["name"], "created_at": now}


def get_daily_day(score_date: str) -> dict:
    """某日详情：交易行（不含估值因子，纯读）+ 笔数 + 净额。前端明细表直接用它渲染。"""
    from app.data.base import auto_tag

    trades = []
    net = 0.0
    for r in _trade_rows(score_date):
        tag = (r.get("tag") or "").strip() or auto_tag(r["code"], r.get("name") or "")
        amt = float(r["amount_cny"] or 0.0)
        net += amt if r["side"] == "buy" else -amt
        trades.append({
            "trade_id": r["id"], "code": r["code"], "name": r.get("name") or r["code"],
            "tag": tag, "side": r["side"], "price": r["price"], "quantity": r["quantity"],
            "amount": r["amount"], "amount_cny": r["amount_cny"], "fee": r["fee"] or 0.0,
            "trade_time": r["trade_time"],
        })
    return {"score_date": score_date, "trades_count": len(trades), "net_amount": round(net, 2), "trades": trades}


def get_daily_report(score_date: str) -> dict | None:
    with get_conn() as c:
        row = c.execute("SELECT * FROM ai_daily_reports WHERE score_date=?", (score_date,)).fetchone()
    if not row:
        return None
    d = dict(row)
    d["report"] = json.loads(d.pop("report_json"))
    return d


def list_daily_days() -> list[dict]:
    """左目录：所有有 buy/sell 交易的日期（倒序），各附 AI 报告摘要（无则 ai=None）。纯读零写。"""
    with get_conn() as c:
        dates = [r["score_date"] for r in c.execute(
            "SELECT DISTINCT date(trade_time) AS score_date FROM trades WHERE side IN ('buy','sell') "
            "ORDER BY score_date DESC"
        ).fetchall()]
        reps = {r["score_date"]: r for r in c.execute(
            "SELECT score_date, report_json, model_name, updated_at FROM ai_daily_reports"
        ).fetchall()}
        rows = c.execute(
            "SELECT date(trade_time) AS d, side, amount_cny FROM trades WHERE side IN ('buy','sell')"
        ).fetchall()
    net: dict[str, float] = {}
    cnt: dict[str, int] = {}
    for r in rows:
        d = r["d"]
        amt = float(r["amount_cny"] or 0.0)
        net[d] = net.get(d, 0.0) + (amt if r["side"] == "buy" else -amt)
        cnt[d] = cnt.get(d, 0) + 1
    days = []
    for d in dates:
        rep = reps.get(d)
        ai = None
        if rep:
            r = json.loads(rep["report_json"])
            ai = {"score": r.get("score"), "rating": r.get("rating"),
                  "rating_name": r.get("rating_name"), "model_name": rep["model_name"],
                  "updated_at": rep["updated_at"]}
        days.append({"score_date": d, "trades_count": cnt.get(d, 0),
                     "net_amount": round(net.get(d, 0.0), 2), "ai": ai})
    return days


def invalidate_daily(score_date: str) -> None:
    with get_conn() as c:
        c.execute("DELETE FROM ai_daily_reports WHERE score_date=?", (score_date,))


def _safe_score_daily(score_date: str) -> None:
    """后台线程执行体：失败只记日志，当日保持"未评分"（绝不抛到调用方）。"""
    try:
        score_daily(score_date)
    except Exception:  # noqa: BLE001
        logger.warning("[AI打分] 后台自动打分 %s 失败", score_date, exc_info=True)


def maybe_auto_score_daily(score_date: str) -> None:
    """交易变动后：先同步失效该日旧报告（保证后续失败时页面为"未评分"而非陈旧结果）；
    有激活模型且当日有交易才起 daemon 线程重打分（无模型/无交易不起线程，保证测试确定、不触发网络）。"""
    invalidate_daily(score_date)
    if ai.get_active_model() is None:
        return
    if not _trade_rows(score_date):
        return
    try:
        threading.Thread(target=_safe_score_daily, args=(score_date,), daemon=True).start()
    except Exception:  # noqa: BLE001
        logger.warning("[AI打分] 启动后台打分线程失败", exc_info=True)

"""AI 评分服务：标签偏好 + 组合/每日 AI 打分（彻底取代公式评分）。

- 标签偏好（tag_prefs）：用户输入简短偏好，保存时自动请求 AI 补全成完整「评分指引」
  （draft），确认后（confirmed）才用于打分。
- 组合 AI 打分：手动触发，把 compute_portfolio 全量聚合 + 各标签「评分指引」带给 AI，
  AI 直接输出 ScoreCard（score/grade/action/risk + 共享维+结构/标签契合）+ 总结/建议。
- 每日 AI 打分：每天一次调用，AI 按每笔交易所属标签的「评分指引」逐笔打分，再综合当日总分。
  偏好提示词按当天涉及的标签去重（50 笔同标签也只带 1 条）。
- AI 评级（A/B/C/D=质量）由 AI 直接给出，后端只做格式校验，不做 score→grade 转换。
- 复用 app/services/ai.py 的 get_active_model / chat_json / ScoreCard 公共定义。
"""
import hashlib
import json
import logging
from datetime import datetime

from app.models.db import get_conn
from app.services import ai

logger = logging.getLogger("ai_scoring")

# AI 评级中文显示标签（复用 ai.GRADE_NAMES；评级由 AI 直接给出）
_RATING_NAMES = ai.GRADE_NAMES
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
    return ai.check_grade(r)


def _str_list(v) -> list[str]:
    if not isinstance(v, list):
        v = [v] if v else []
    return [str(x) for x in v]


_PORTFOLIO_DIMS = ai.SHARED_DIMENSIONS + ai.PORTFOLIO_EXTRA_DIMENSIONS
_DAILY_DIMS = ai.TRADE_EXTRA_DIMENSIONS


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
_TAG_PREF_OUTPUT = {
    "prompt": "完整评分指引（简体中文，≤800字）",
    "data_received": "逐项列出系统实际传递给你的数据（标签、用户原始偏好），用于日志核对输入",
}


def expand_tag_prompt(tag: str, raw_pref: str) -> dict:
    """请求 AI 补全偏好 → 存为 draft（待确认）。返回该行。"""
    model = ai.get_active_model()
    if not model:
        raise ValueError(_NO_MODEL_MSG)
    raw_pref = (raw_pref or "").strip()
    if not raw_pref:
        raise ValueError("偏好描述不能为空")
    user = (
        f"标签：{tag}\n用户原始偏好：{raw_pref}\n\n请输出：\n"
        + json.dumps(_TAG_PREF_OUTPUT, ensure_ascii=False)
        + "\n\n输出必须包含 data_received 字段：逐项列出系统实际传递给你的数据（标签、用户原始偏好），"
          "用于日志核对输入完整性。"
    )
    raw = ai.chat_json(model, _TAG_PREF_SYSTEM, user)
    prompt = str(raw.get("prompt") or raw_pref).strip()
    if not prompt:
        prompt = raw_pref
    return upsert_tag_pref(tag, raw_pref, prompt=prompt, status="draft")


# ============================================================ 组合 AI

def _holdings_tuples(tags: list[str] | None = None) -> list[tuple]:
    """筛选标签内的活跃持仓 (code, round(qty,6), currency, tag) 排序元组；tags 为空=全部。纯读。

    标签解析与 compute_portfolio/get_holdings 同口径（NULL 标签按 auto_tag 兜底），
    保证 profile_hash 与组合筛选视角一致；tag 纳入哈希，改标签后报告会标 stale。
    """
    from app.instruments import get_instrument

    with get_conn() as c:
        rows = c.execute(
            """SELECT h.code, h.quantity, COALESCE(h.currency,'CNY') AS currency,
                      COALESCE(s.tag,'') AS tag, COALESCE(s.name,'') AS name
               FROM holdings h LEFT JOIN stocks s ON h.code=s.code
               WHERE h.status='active'"""
        ).fetchall()
    if tags is not None:
        tag_set = set(tags)
        rows = [r for r in rows if ((r["tag"] or get_instrument(r["code"]).tag or "") in tag_set)]
    out = []
    for r in rows:
        tag = (r["tag"] or get_instrument(r["code"]).tag or "")
        out.append((str(r["code"]), round(float(r["quantity"] or 0.0), 6), str(r["currency"]), str(tag)))
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


def _portfolio_fundflow_summary(tags: list[str] | None) -> dict:
    """组合资金流穿透摘要（当日五档汇总 + 近30日趋势 + 当日分时 15 分钟序列），供 AI 使用。缺失返回空 dict。"""
    try:
        from app.analysis.portfolio import portfolio_fundflow
        from app.data.fundflow import intraday_window_series

        ff = portfolio_fundflow(tags) or {}
        hist = ff.get("fundflow_history") or []
        nets = [float(h["netamount"]) for h in hist if h.get("netamount") is not None]
        out = {
            "latest": ff.get("fundflow_latest") or {},
            "covered": ff.get("covered", 0),
            "total": ff.get("total", 0),
            "trade_date": ff.get("trade_date"),
        }
        if nets:
            out["history_30d_net"] = round(sum(nets), 0)
            out["history_5d_net"] = round(sum(nets[-5:]), 0)
            out["history_days"] = len(nets)
            # 完整逐日五档 + 买卖盘（升序，跨天结构/趋势判断用）
            out["daily"] = [
                {
                    "date": h["trade_date"],
                    "netamount": h["netamount"],
                    "main_net": h.get("main_net"),
                    "super_large_net": h.get("super_large_net"),
                    "large_net": h.get("large_net"),
                    "medium_net": h.get("medium_net"),
                    "small_net": h.get("small_net"),
                    "xs_net": h.get("xs_net"),
                    "buy_amount": h.get("buy_amount"),
                    "sell_amount": h.get("sell_amount"),
                }
                for h in hist
            ]
        base = ff.get("fundflow_15m") or []
        if base:
            series = intraday_window_series(base, 15)
            if series:
                out["intraday_15m"] = series
        return out
    except Exception:  # noqa: BLE001 资金流摘要缺失不阻断打分
        return {}


def build_portfolio_context(tags: list[str] | None = None) -> dict:
    """组合汇总好的所有信息（compute_portfolio 全量聚合，单股/标签全字段）+ 资金流穿透
    + 技术面 bars / 消息面 meta + 可选专项报告摘要 + 各标签「评分指引」。"""
    from app.analysis.portfolio import compute_portfolio

    p = compute_portfolio(tags=tags)
    prefs = confirmed_prefs()
    if tags is not None:
        tag_set = set(tags)
        prefs = {k: v for k, v in prefs.items() if k in tag_set}
    stocks = p["stocks"]
    as_of = ai.now_as_of_datetime()
    # 按权重取前约 15 只喂 bars，控 token
    ranked = sorted(
        [s for s in stocks if s.get("code")],
        key=lambda s: float(s.get("weight") or 0),
        reverse=True,
    )[:15]
    top_codes = [s["code"] for s in ranked]
    multi_map: dict = {}
    try:
        if top_codes:
            multi_map = ai.build_technical_bars_many_multi(
                top_codes, as_of, daily_limit=40, weekly_limit=16, monthly_limit=12)
    except Exception:  # noqa: BLE001
        multi_map = {}
    technical = [
        {
            "code": s["code"], "name": s.get("name") or s["code"],
            "weight": s.get("weight"), **multi_map.get(s["code"], {}),
        }
        for s in ranked
    ]
    news_meta = {
        "as_of_datetime": as_of,
        "stocks": [{"code": s["code"], "name": s.get("name") or s["code"]} for s in stocks if s.get("code")],
    }
    all_codes = [s["code"] for s in stocks if s.get("code")]
    news_reports, tech_reports = {}, {}
    try:
        news_reports = ai.list_news_reports(all_codes) if all_codes else {}
    except Exception:  # noqa: BLE001
        pass
    try:
        tech_reports = ai.list_tech_reports(all_codes) if all_codes else {}
    except Exception:  # noqa: BLE001
        pass
    return {
        "portfolio": p["portfolio"],
        "weights": p["weights"],
        "stocks": p["stocks"],
        "tag_weights": p["tag_weights"],
        "tags": p.get("tags", []),
        "fundflow": _portfolio_fundflow_summary(tags),
        "all_tags": p["all_tags"],
        "selected_tags": sorted(tags) if tags else [],
        "tag_prefs": prefs,
        "as_of_datetime": as_of,
        "technical": technical,
        "news_meta": news_meta,
        "news_reports": news_reports,
        "tech_reports": tech_reports,
    }


_PORTFOLIO_SYSTEM = (
    "你是资深个人投资组合分析师。系统提供当前组合的聚合数据（人民币口径）、单股与标签板块全量指标、"
    "组合资金流穿透（fundflow）、技术面日/周/月K（technical）、消息面元数据（news_meta）及可选专项报告摘要"
    "（news_reports/tech_reports），以及各标签的「评分指引」。"
    "请综合打分：优先依据提供的结构化数据；已有 news_reports/tech_reports 摘要时优先采信；"
    "各持仓所属标签的「评分指引」是评分的核心准则，不同标签的持仓按各自指引分别衡量后形成整体判断。"
    "直接给出 ScoreCard：score 0-100、grade（A=优秀/B=良好/C=一般/D=较差，质量）、"
    "action（add/hold/watch/reduce/exit，与 grade 解耦）、risk 0-100、risk_level、confidence，"
    "以及 dimensions（共享维 fundamentals/valuation/fundflow/news/technical + structure/tag_fit），"
    "并给出总结、建议、风险、核心理由。"
    f"\n\n{ai._SCORE_RULES}\n{ai._ASOF_RULES}"
    "\n[数据使用规范]"
    "\n1. 系统提供的所有结构化字段都必须纳入分析（组合聚合/单股估值/标签板块/资金流穿透/技术面/"
    "消息面/覆盖率），不得只挑少数指标——漏用数据视为不合格。"
    "\n2. 字段带 *_source / *_confidence 后缀表示可靠度：user=你的输入、ttm=滚动口径、latest_report=最新财报、"
    "zero_conservative=零增长保守假设；confidence 为 high/medium/low/invalid。来源为 user 的优先采信；"
    "zero_conservative / low / invalid 的须保守解读并在报告注明。"
    "\n3. *_static 后缀=按去年年报口径，无后缀/ttm=滚动口径；两者明显背离时分析差异原因，不得只报其一。"
    "\n4. fundflow（组合资金流穿透：当日五档汇总、当日分时 15 分钟五档序列 intraday_15m（每窗口"
    "super/large/medium/small/xs 与 main=主力/cum=累计）、近30日完整逐日五档/买卖盘序列 daily（升序，含 "
    "netamount/main_net/各档净额/buy_amount/sell_amount）、累计速览 history_30d_net/history_5d_net、"
    "covered/total）必须纳入分析：评估组合整体资金面（净流入/流出、各档主力态度、全天节奏与跨日趋势），"
    "识别资金驱动的高权重板块。covered/total 表示有资金流数据的持仓占比，低于全部时按可用部分判断。"
    "\n5. technical 为按权重裁剪的持仓日/周/月K；news_meta 仅含 as_of 与 code/name——"
    "消息面/技术面须与资金面并列纳入；过时或不确信的不写进结论。"
    "\n6. missing_fx=该股缺汇率、已从人民币汇总剔除；字段为 null 表示系统无此数据，不得臆造数值；"
    "用领域知识补充时须标 [AI补充] 并注明时效。"
    "输出语言：所有文字字段用简体中文。输出严格 JSON，不要任何额外文字。"
)


# HTML 深度报告要求：仅「深入」强度追加（快速/普通不要求生成 HTML 报告）
_PORTFOLIO_HTML_REQUIREMENT = (
    "同时生成一份完整、独立、可读性强的 HTML 详细报告（字段 html），供用户新开页面查看。"
    "\n[HTML 深度分析强制规范]"
    "\n1. HTML 正文（简体中文）必须 ≥1000 字，必须有实质分析；禁止只列要点、禁止空话套话，浅尝辄止视为不合格重写。"
    "\n2. 结构必须覆盖：核心结论 / 逐标签深度解读 / 结构集中度 / 指标含义与背离 / 资金流·消息面·技术面 / "
    "波动率 / 风险与陷阱 / 情景推演 / 操作建议分级。"
    "\n3. 每个判断必须带具体数字与上下文（如「组合综合 PE 15.2，处近1年约 30% 分位，但覆盖率仅 65% 需谨慎」）；"
    "禁止「估值合理」「组合稳健」这类无数字断言。"
    "\n4. 操作建议分级：加仓/持有/观望/减仓/清仓，每档给出触发条件，不得含糊；与顶层 action 一致。"
    "\n5. HTML 为独立成文，离开本应用页面即可读懂；不得原样罗列系统已展示的持仓明细/标签板块/估值数据表。"
    "\n6. 质量自检：写完后逐段检查——这段是否提供了用户在数据表上看不到的洞察？若没有，重写。"
    "\n要求：自包含单文件、内联 CSS、不引用任何外部资源、不得包含 <script> 或任何可执行代码；"
    "简体中文；结构清晰、排版美观、层次分明。"
)
_PORTFOLIO_OUTPUT_SCHEMA = {
    "score": "0-100 整数总分",
    "grade": "A|B|C|D 质量评级（直接给出，不要按分数换算）",
    "action": "add|hold|watch|reduce|exit",
    "risk": "0-100 风险分",
    "risk_level": "low|medium|high",
    "confidence": "high|medium|low",
    "dimensions": {
        k: {"score": "0-100", "grade": "A|B|C|D", "analysis": "该维分析（简体中文）"}
        for k in _PORTFOLIO_DIMS
    },
    "summary": "一句话总体结论",
    "advice": ["建议数组（简体中文）"],
    "risks": ["风险数组（简体中文）"],
    "reasons": ["核心理由数组（简体中文）"],
    "html": "完整独立 HTML 详细报告源代码（自包含、内联 CSS、无外部依赖、无脚本、简体中文、≥1000字深度正文，聚焦深入分析，不罗列原始数据）",
}


def _normalize_portfolio_report(data: dict) -> dict:
    if not isinstance(data, dict):
        data = {}
    grade = _check_rating(data.get("grade") or data.get("rating"))
    score = round(_clamp_score(data.get("score"), 0, 100), 1)
    if score >= 80 and grade == "D":
        logger.warning("[AI打分] 组合 score/grade 冲突：score=%s grade=%s", score, grade)
    action = ai.check_action(data.get("action"), ai.ACTION_STOCK_PORTFOLIO)
    risk_raw = data.get("risk", data.get("risk_score", 50))
    risk = int(round(_clamp_score(risk_raw, 0, 100, 50)))
    risk_level = (ai.check_risk_level(data.get("risk_level"))
                  if data.get("risk_level") else ai.risk_level_from_score(risk))
    conf = str(data.get("confidence") or "medium").lower().strip()
    if conf not in ("high", "medium", "low"):
        conf = "medium"
    raw_dims = data.get("dimensions") if isinstance(data.get("dimensions"), dict) else {}
    dims = {}
    for k in _PORTFOLIO_DIMS:
        d = raw_dims.get(k)
        if isinstance(d, dict):
            dims[k] = ai._normalize_dim_block(d, with_stock_extras=False)
        else:
            dims[k] = {}
    out = {
        "score": score,
        "grade": grade,
        "grade_name": _RATING_NAMES[grade],
        "action": action,
        "action_name": ai.ACTION_NAMES[action],
        "risk": risk,
        "risk_level": risk_level,
        "confidence": conf,
        "rating": grade,
        "rating_name": _RATING_NAMES[grade],
        "risk_score": risk,
        "dimensions": dims,
        "summary": str(data.get("summary") or ""),
        "advice": _str_list(data.get("advice")),
        "risks": _str_list(data.get("risks")),
        "reasons": _str_list(data.get("reasons")),
        "html": str(data.get("html") or ""),   # AI 生成的完整 HTML 报告（可空 → 前端不显示入口）
    }
    return ai.upgrade_legacy_card(out)


def _tags_key(tags: list[str] | None = None) -> list[str]:
    """标签组合的规范化 key：None/空 → []（全部）。"""
    return sorted(tags) if tags else []


def score_portfolio(tags: list[str] | None = None, system_prompt: str | None = None,
                    intensity: str = "normal") -> dict:
    """手动触发组合 AI 打分：AI 一次调用 → 规整 → 按画像哈希落库。

    **每个标签组合各自存一份**（tags_json 区分）：打个股不覆盖 个股+港股，也不覆盖 全部。
    system_prompt 非 None 时作为「用户附加要求」追加到默认指令后（前端弹窗可编辑）。
    intensity 分析强度 fast/normal/deep → 思考级别 low/全局/max（弹窗可选）。
    """
    model = ai.get_active_model()
    if not model:
        raise ValueError(_NO_MODEL_MSG)
    ctx = build_portfolio_context(tags)
    # 输出 schema：仅「深入」保留 html 字段（快速/普通不要求 HTML 报告）
    schema = {k: v for k, v in _PORTFOLIO_OUTPUT_SCHEMA.items() if intensity == "deep" or k != "html"}
    user = ai._schema_user("组合聚合数据：", ctx, schema)
    system = _PORTFOLIO_SYSTEM
    if intensity == "deep":
        system = f"{system}\n\n{_PORTFOLIO_HTML_REQUIREMENT}"
    if system_prompt:
        system = f"{system}\n\n[用户附加要求]\n{system_prompt}"
    inst = ai._intensity_instruction(intensity)
    if inst:
        system = f"{system}\n\n[分析强度]\n{inst}"
    raw = ai.chat_json(model, system, user)
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
    logger.info("[AI打分] 组合%s 完成：%s 分（%s）", f"筛选{tags}" if tags else "全部", report["score"], report["grade"])
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
            d["report"] = ai.upgrade_legacy_card(json.loads(d.pop("report_json")))
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
            d["report"] = ai.upgrade_legacy_card(json.loads(d.pop("report_json")))
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


def _fundflow_factor(code: str, as_of: str | None) -> dict:
    """资金流因子：≤as_of 最近一条（当日）五档净流入 + 近30日完整逐日五档/买卖盘序列。缺失留 None。"""
    from datetime import date, timedelta

    from app.data.cache import get_daily_fundflow, get_daily_fundflows, get_fundflow_asof
    from app.data.fundflow import FUNDFLOW_HISTORY_DAYS

    out = {}
    try:
        row = get_fundflow_asof(code, as_of) if as_of else get_daily_fundflow(code)
        if row:
            for k in ("netamount", "main_net", "main_net_pct", "super_large_net", "large_net",
                      "medium_net", "small_net", "xs_net", "p15", "p40", "p75", "p95"):
                if row[k] is not None:
                    out[f"fundflow_{k}"] = row[k]
            out["fundflow_date"] = row["trade_date"]
    except Exception:  # noqa: BLE001 资金流缺失不阻断
        pass
    try:
        end = as_of or date.today().isoformat()
        start = (date.fromisoformat(end) - timedelta(days=FUNDFLOW_HISTORY_DAYS)).isoformat()
        rows = get_daily_fundflows(code, start, end)
        nets = [float(r["netamount"]) for r in rows if r["netamount"] is not None]
        if nets:
            out["fundflow_30d_net"] = round(sum(nets), 0)
            out["fundflow_5d_net"] = round(sum(nets[-5:]), 0)
            out["fundflow_days"] = len(nets)
            # 完整逐日五档 + 买卖盘（升序，跨天结构/趋势判断用）
            out["fundflow_daily"] = [
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
    # 当日分时 15 分钟主力净流入序列（观察全天资金节奏；历史日缺 15m 缓存时省略）
    try:
        from app.data.cache import get_fundflow_min
        from app.data.fundflow import intraday_window_series

        fdate = out.get("fundflow_date")
        if fdate:
            series = intraday_window_series(get_fundflow_min(code, fdate), 15)
            if series:
                out["fundflow_intraday_15m"] = series
    except Exception:  # noqa: BLE001
        pass
    return out


def _stock_factors(code: str, as_of: str | None = None) -> dict:
    """该股估值/分位/涨跌/资金流因子（读缓存零网络；缺失留 None）。

    as_of 非空：取该交易日收盘价口径的 asof 快照——估值用当日 close 计算、分位序列按 ≤as_of 截断
    （不用「未来」数据）、资金流取 ≤as_of 最近一条；当日无行情 → 回退当前值并标 asof_fallback=true。
    as_of 为空：当前最新。
    """
    from app.analysis.valuation import compute_live, percentile_in_series
    from app.data.cache import get_daily_price
    from app.services.quote import get_quote

    f = {"asof": as_of, "asof_fallback": False}
    price, pct_chg = None, None
    if as_of:
        try:
            row = get_daily_price(code, as_of)
            if row and row["close"]:
                price = float(row["close"])
                pct_chg = float(row["pct_change"]) if row["pct_change"] is not None else None
        except Exception:  # noqa: BLE001
            pass
    live = {}
    try:
        live = compute_live(code, price, as_of) or {}
    except Exception:  # noqa: BLE001 单股因子缺失不阻断整日打分
        pass
    if as_of and price is None:
        # 当日无行情缓存 → 回退当前最新值，并标注供 AI 保守解读
        f["asof_fallback"] = True
        try:
            live = compute_live(code) or {}
        except Exception:  # noqa: BLE001
            live = {}
    _LIVE_KEYS = (
        "price", "total_shares", "total_mv", "ttm_net_profit", "ttm_revenue",
        "pe", "pb", "pe_static", "pb_static", "ps_static", "ps_ttm", "ps_fwd",
        "dv_ratio", "dv_static", "roe_ttm", "roe_static",
        "profit_yoy_ttm", "profit_yoy_static", "revenue_yoy_ttm", "revenue_yoy_static",
        "payout_ratio", "g", "expected_growth", "expected_payout",
        "fwd_pe", "fwd_pb", "fwd_pb_confidence", "fwd_roe", "fwd_dv_ratio",
        "fwd_profit_yoy", "fwd_revenue_yoy", "pe_pct", "pb_pct", "fwd_pe_pct", "fwd_pb_pct",
    )
    for k in _LIVE_KEYS:
        f[k] = live.get(k)
    # 1y 分位已在 compute_live 内；3y/5y 分位用序列截断补算（不依赖当日落库）
    if live.get("pe") is not None:
        for period in ("3y", "5y"):
            try:
                f[f"pe_pct_{period}"] = percentile_in_series(code, "pe", period, live["pe"], as_of)
                f[f"pb_pct_{period}"] = percentile_in_series(code, "pb", period, live["pb"], as_of)
            except Exception:  # noqa: BLE001
                pass
    if pct_chg is None:
        try:
            pct_chg = (get_quote(code) or {}).get("pct_chg")
        except Exception:  # noqa: BLE001
            pass
    f["pct_chg"] = pct_chg
    f.update(_fundflow_factor(code, as_of))
    # 技术面日K + as_of（与资金流同级，供交易复盘 technical/news 维）
    as_of_dt = as_of or ai.now_as_of_datetime()
    # as_of 可能是 YYYY-MM-DD；补齐成可被 resolve_trade_day 吃的串
    if as_of and len(str(as_of)) == 10:
        as_of_dt = f"{as_of}T15:00:00"
    f["as_of_datetime"] = as_of_dt if as_of else ai.now_as_of_datetime()
    try:
        f.update(ai.build_technical_bars_multi(
            code, f["as_of_datetime"], daily_limit=120, weekly_limit=60, monthly_limit=36))
    except Exception:  # noqa: BLE001
        f["bars"] = f["weekly_bars"] = f["monthly_bars"] = []
    return f


def _holding_snapshot(code: str) -> dict | None:
    """该股当前持仓位置（权重/成本/盈亏/今日盈亏/累计分红）；不在仓返回 None。"""
    try:
        from app.analysis.portfolio import compute_portfolio

        p = compute_portfolio() or {}
        st = next((s for s in p.get("stocks", []) if s["code"] == code), None)
        if not st:
            return None
        return {
            "in_portfolio": True,
            "weight_pct": st.get("weight"),
            "avg_cost": st.get("avg_cost"),
            "value_cny": st.get("value_cny"),
            "pnl_pct": st.get("pnl_pct"),
            "day_pnl": st.get("day_pnl"),
            "total_dividend": st.get("total_dividend"),
            "tag": st.get("tag"),
        }
    except Exception:  # noqa: BLE001 持仓快照缺失不阻断
        return None


def build_daily_context(score_date: str) -> dict:
    """当日每笔交易 + 每股 asof 因子 + 持仓位置 + 标签；tag_prefs 按当天涉及标签去重（各一份）。"""
    from app.instruments import get_instrument

    rows = _trade_rows(score_date)
    prefs = confirmed_prefs()
    trades = []
    used_tags: set[str] = set()
    for r in rows:
        tag = (r.get("tag") or "").strip() or get_instrument(r["code"]).tag
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
            "factors": _stock_factors(r["code"], as_of=score_date),
            "holding": _holding_snapshot(r["code"]),
        })
    tag_prefs = {
        t: prefs.get(t) or "（该标签无已确认评分指引，请按一般投资纪律评判）"
        for t in sorted(used_tags)
    }
    return {"score_date": score_date, "trades": trades, "tag_prefs": tag_prefs}


_DAILY_SYSTEM = (
    "你是资深交易复盘分析师。系统提供某交易日的每笔买卖交易、该股当日 asof 因子（含日/周/月K bars）与持仓位置、"
    "当日/近30日资金流，以及各标签的「评分指引」。"
    "请按每笔交易所属标签的「评分指引」逐笔评分并给出质量评级与操作建议，再综合当日多笔交易的节奏/集中度/方向"
    "给出当日总分与评级。评分准则：不同标签的交易按各自指引分别衡量。"
    "直接给出 ScoreCard：score 0-100、grade（A/B/C/D 质量）、action（repeat/cautious/avoid）、"
    "risk、risk_level、confidence，以及 dimensions（timing/execution/sizing/discipline）；"
    "每笔交易也给出分数、grade、action 与一句话点评。"
    f"\n\n{ai._SCORE_RULES}\n{ai._ASOF_RULES}"
    "\n[数据使用规范]"
    "\n1. 系统提供的所有结构化字段都必须纳入分析（交易明细/估值/分位/资金流/日周月K bars/持仓位置），不得只挑少数指标——漏用数据视为不合格。"
    "\n2. 每股 factors 为该笔交易「当日」的 asof 快照（当日收盘价口径的估值/分位/资金流/日周月K）——"
    "不要用当前时点的数据评价历史交易；字段含 asof_fallback=true 表示当日数据缺失、已回退当前值，须保守解读。"
    "\n3. holding 字段是该股在你组合中的当前位置（权重/成本/盈亏/今日盈亏/累计分红，不在仓为 null）——"
    "评估每笔买卖对组合的意义（加仓集中度、摊薄成本、兑现盈亏）时必须结合。"
    "\n4. 资金流字段为净流入（元，正=流入负=流出）；五档 super=超大单/large=大单/medium=中单/small=小单/xs=特小单，"
    "main=主力（超大+大单）。fundflow_intraday_15m（当日 15 分钟五档序列，每窗口含各档净额与累计主力）"
    "反映交易当日各档资金节奏，fundflow_daily（完整逐日五档/买卖盘升序序列）与累计速览 "
    "fundflow_30d_net / 5d_net 反映资金中期趋势——用于判断交易当日/近期的资金环境。"
    "\n5. factors 的 bars/weekly_bars/monthly_bars 与 as_of_datetime 用于技术面/消息面环境判断；过时不写。"
    "\n6. 字段为 null 表示系统无此数据，不得臆造数值；用领域知识补充时须标 [AI补充] 并注明时效。"
    "输出语言：所有文字字段用简体中文。输出严格 JSON，不要任何额外文字。"
)


# HTML 深度报告要求：仅「深入」强度追加（快速/普通不要求生成 HTML 报告）
_DAILY_HTML_REQUIREMENT = (
    "同时生成一份完整、独立、可读性强的 HTML 复盘报告（字段 html），供用户新开页面查看。"
    "\n[HTML 深度分析强制规范]"
    "\n1. HTML 正文（简体中文）必须 ≥1000 字，必须有实质分析；禁止只列要点、禁止空话套话，浅尝辄止视为不合格重写。"
    "\n2. 结构必须覆盖：核心结论 / 逐笔质量归因（为什么这笔好/差）/ 买卖时机与价格 / 当日节奏与情绪 / "
    "与标签偏好吻合度 / 集中度与风险 / 改进与后续策略。"
    "\n3. 每个判断必须带具体数字与上下文（如「该笔买入价 12.3，当日 PE 11 处于近1年约20%分位，"
    "但 asof 数据缺失已回退当前值」）；禁止「价格合理」「买入时机好」这类无数字断言。"
    "\n4. 操作建议分级：可复制/谨慎复制/避免重复，每档给出触发条件，不得含糊；与顶层/逐笔 action 一致。"
    "\n5. HTML 为独立成文，离开本应用页面即可读懂；不得原样罗列系统已展示的交易数据表。"
    "\n6. 质量自检：写完后逐段检查——这段是否提供了用户在数据表上看不到的洞察？若没有，重写。"
    "\n要求：自包含单文件、内联 CSS、不引用任何外部资源、不得包含 <script> 或任何可执行代码；"
    "简体中文；结构清晰、排版美观、层次分明。"
)
_DAILY_OUTPUT_SCHEMA = {
    "score": "0-100 当日综合总分",
    "grade": "A|B|C|D 当日质量评级（直接给出）",
    "action": "repeat|cautious|avoid",
    "risk": "0-100 风险分",
    "risk_level": "low|medium|high",
    "confidence": "high|medium|low",
    "dimensions": {
        k: {"score": "0-100", "grade": "A|B|C|D", "analysis": "该维分析（简体中文）"}
        for k in _DAILY_DIMS
    },
    "summary": "当日一句话总结",
    "advice": ["建议数组（简体中文）"],
    "risks": ["风险数组（简体中文）"],
    "reasons": ["核心理由数组（简体中文）"],
    "html": "完整独立 HTML 复盘报告源代码（自包含、内联 CSS、无外部依赖、无脚本、简体中文、≥1000字深度正文，聚焦深入分析，不罗列原始数据）",
    "trades": [
        {"trade_id": "整数，对应输入交易", "score": "0-100 该笔得分", "grade": "A|B|C|D 该笔质量评级",
         "action": "repeat|cautious|avoid", "comment": "该笔一句话点评"}
    ],
}


def _normalize_daily_report(data: dict, trades: list[dict]) -> dict:
    """规整 AI 每日输出：按输入交易顺序对齐（trade_id 精确匹配，缺则按位置兜底），
    并把交易显示字段并入各 trade 项，保证每笔真实交易各有一条。"""
    if not isinstance(data, dict):
        data = {}
    day_grade = _check_rating(data.get("grade") or data.get("rating"))
    day_action = ai.check_action(data.get("action"), ai.ACTION_TRADE)
    risk_raw = data.get("risk", data.get("risk_score", 50))
    risk = int(round(_clamp_score(risk_raw, 0, 100, 50)))
    risk_level = (ai.check_risk_level(data.get("risk_level"))
                  if data.get("risk_level") else ai.risk_level_from_score(risk))
    conf = str(data.get("confidence") or "medium").lower().strip()
    if conf not in ("high", "medium", "low"):
        conf = "medium"
    raw_dims = data.get("dimensions") if isinstance(data.get("dimensions"), dict) else {}
    dims = {}
    for k in _DAILY_DIMS:
        d = raw_dims.get(k)
        if isinstance(d, dict):
            dims[k] = ai._normalize_dim_block(d, with_stock_extras=False)
        else:
            dims[k] = {}
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
        tgrade = _check_rating(entry.get("grade") or entry.get("rating"))
        taction = ai.check_action(entry.get("action"), ai.ACTION_TRADE)
        ai_trades.append({
            "trade_id": tr["trade_id"],
            "code": tr["code"], "name": tr["name"], "tag": tr["tag"],
            "side": tr["side"], "price": tr["price"], "quantity": tr["quantity"],
            "amount": tr["amount"], "amount_cny": tr["amount_cny"], "fee": tr["fee"],
            "trade_time": tr["trade_time"],
            "score": tscore,
            "grade": tgrade, "grade_name": _RATING_NAMES[tgrade],
            "action": taction, "action_name": ai.ACTION_NAMES[taction],
            "rating": tgrade, "rating_name": _RATING_NAMES[tgrade],
            "comment": str(entry.get("comment") or ""),
        })
    out = {
        "score": round(_clamp_score(data.get("score"), 0, 100), 1),
        "grade": day_grade,
        "grade_name": _RATING_NAMES[day_grade],
        "action": day_action,
        "action_name": ai.ACTION_NAMES[day_action],
        "risk": risk,
        "risk_level": risk_level,
        "confidence": conf,
        "rating": day_grade,
        "rating_name": _RATING_NAMES[day_grade],
        "risk_score": risk,
        "dimensions": dims,
        "summary": str(data.get("summary") or ""),
        "advice": _str_list(data.get("advice")),
        "risks": _str_list(data.get("risks")),
        "reasons": _str_list(data.get("reasons")),
        "html": str(data.get("html") or ""),
        "trades": ai_trades,
    }
    return ai.upgrade_legacy_card(out)


def score_daily(score_date: str, system_prompt: str | None = None,
                intensity: str = "normal") -> dict | None:
    """当日 AI 打分：一次调用逐笔+汇总 → 落库。当日无 buy/sell 交易 → 失效并返回 None。

    收盘守卫：仅限制「今天」——盘中不允许对今日打分；历史日期随时可打。
    system_prompt 非 None 时作为「用户附加要求」追加到默认指令后（前端弹窗可编辑）。
    intensity 分析强度 fast/normal/deep → 思考级别 low/全局/max（弹窗可选）。
    """
    _guard_today(score_date)
    model = ai.get_active_model()
    if not model:
        raise ValueError(_NO_MODEL_MSG)
    trades = _trade_rows(score_date)
    if not trades:
        invalidate_daily(score_date)
        return None
    ctx = build_daily_context(score_date)
    # 输出 schema：仅「深入」保留 html 字段（快速/普通不要求 HTML 报告）
    schema = {k: v for k, v in _DAILY_OUTPUT_SCHEMA.items() if intensity == "deep" or k != "html"}
    user = ai._schema_user("当日交易数据：", ctx, schema)
    system = _DAILY_SYSTEM
    if intensity == "deep":
        system = f"{system}\n\n{_DAILY_HTML_REQUIREMENT}"
    if system_prompt:
        system = f"{system}\n\n[用户附加要求]\n{system_prompt}"
    inst = ai._intensity_instruction(intensity)
    if inst:
        system = f"{system}\n\n[分析强度]\n{inst}"
    raw = ai.chat_json(model, system, user)
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
                score_date, report["score"], report["grade"], len(trades))
    return {"score_date": score_date, "report": report, "model_name": model["name"], "created_at": now}


def get_daily_day(score_date: str) -> dict:
    """某日详情：交易行（不含估值因子，纯读）+ 笔数 + 净额。前端明细表直接用它渲染。"""
    from app.instruments import get_instrument

    trades = []
    net = 0.0
    for r in _trade_rows(score_date):
        tag = (r.get("tag") or "").strip() or get_instrument(r["code"]).tag
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
    d["report"] = ai.upgrade_legacy_card(json.loads(d.pop("report_json")))
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
        ai_sum = None
        if rep:
            r = ai.upgrade_legacy_card(json.loads(rep["report_json"]))
            ai_sum = {
                "score": r.get("score"),
                "grade": r.get("grade"), "grade_name": r.get("grade_name"),
                "rating": r.get("rating") or r.get("grade"),
                "rating_name": r.get("rating_name") or r.get("grade_name"),
                "model_name": rep["model_name"], "updated_at": rep["updated_at"],
            }
        days.append({"score_date": d, "trades_count": cnt.get(d, 0),
                     "net_amount": round(net.get(d, 0.0), 2), "ai": ai_sum})
    return days


def invalidate_daily(score_date: str) -> None:
    with get_conn() as c:
        c.execute("DELETE FROM ai_daily_reports WHERE score_date=?", (score_date,))


def _safe_score_daily(score_date: str) -> None:
    """后台执行体：失败只记日志，当日保持"未评分"（绝不抛到调用方）。"""
    try:
        score_daily(score_date)
    except Exception:  # noqa: BLE001
        logger.warning("[AI打分] 后台自动打分 %s 失败", score_date, exc_info=True)


def _enqueue_daily_score(score_date: str) -> None:
    """把自动打分入队到 AI 车道（LANE_AI，AI_WORKERS 并发，/status/jobs 可见可取消）。

    不再裸起线程（绕过队列曾导致并发无上限、排队不可见）。入队失败只记日志，
    当日保持"未评分"（后续可手动或由 catchup 重试）。
    """
    from app.services.job_runners import start_simple

    def work():
        _safe_score_daily(score_date)

    try:
        start_simple("ai.daily_auto", f"每日 AI 打分 {score_date}",
                     work, step=f"AI 自动打分 {score_date}…")
    except Exception:  # noqa: BLE001
        logger.warning("[AI打分] 自动打分入队失败 %s", score_date, exc_info=True)


def _is_market_closed_now() -> bool:
    """当前是否已过收盘确认时间（交易日 + 15:05）。"""
    from datetime import datetime

    from app.market.calendar import is_market_closed

    return is_market_closed(datetime.now())


def _guard_today(score_date: str) -> None:
    """收盘守卫：仅当打分目标是「今天」且未收盘时拒绝；历史日期数据已定格，不受限。"""
    if score_date == _now()[:10] and not _is_market_closed_now():
        raise ValueError("交易时段不允许对今日打分，请收盘后（15:05）再试")


def catchup_pending_daily() -> None:
    """启动/全量刷新：今天已收盘 + 有 buy/sell 交易 + 尚无 AI 报告 → 入队 AI 车道补打一次。

    盘中/未收盘不触发；无模型或无交易不触发；已有报告不重复打。失败只记日志。
    """
    from datetime import datetime

    if not _is_market_closed_now():
        return
    today = datetime.now().date().isoformat()
    if not _trade_rows(today):
        return
    if get_daily_report(today) is not None:
        return
    _enqueue_daily_score(today)
    logger.info("[AI打分] 收盘后补打今日（%s）AI 评分", today)


def maybe_auto_score_daily(score_date: str) -> None:
    """交易变动后：先同步失效该日旧报告（保证后续失败时页面为"未评分"而非陈旧结果）；
    有激活模型且当日有交易才入队 AI 车道重打分（无模型/无交易不入队，保证测试确定、不触发网络）。
    收盘守卫：当日未收盘（盘中录入交易）只失效、不打分，保持「未评分」，收盘后由 catchup 补打。"""
    invalidate_daily(score_date)
    if ai.get_active_model() is None:
        return
    if not _trade_rows(score_date):
        return
    try:
        if score_date == _now()[:10] and not _is_market_closed_now():
            return  # 盘中：今日数据未定格，不打
        _enqueue_daily_score(score_date)
    except Exception:  # noqa: BLE001
        logger.warning("[AI打分] 启动后台打分线程失败", exc_info=True)

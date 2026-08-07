"""指数服务：注册表读改、ETF→指数映射、名称自动匹配、全量预热。

ETF 无可靠跟踪指数接口，采用「ETF 名称子串匹配指数名」半自动（能匹配自动、匹配不到留空），
前端可手动填。估值 = 跟踪指数估值（乐咕），指数本身经 index_defs 注册表。
"""
import logging
from concurrent.futures import ThreadPoolExecutor, as_completed
from datetime import datetime

from app.models.db import get_conn

logger = logging.getLogger("indices")

_EDITABLE = {"name", "symbol", "legu_code", "pe_source", "pb_source"}


def get_index_defs() -> list[dict]:
    """全量指数注册表行（含估值源，按 sort_order）。"""
    with get_conn() as c:
        rows = c.execute(
            "SELECT code,name,symbol,legu_code,pe_source,pb_source,sort_order "
            "FROM index_defs ORDER BY sort_order"
        ).fetchall()
    return [dict(r) for r in rows]


def get_index_def(code: str) -> dict | None:
    with get_conn() as c:
        row = c.execute(
            "SELECT code,name,symbol,legu_code,pe_source,pb_source,sort_order "
            "FROM index_defs WHERE code=?",
            (code,),
        ).fetchone()
    return dict(row) if row else None


def update_index_def(code: str, fields: dict) -> None:
    """更新指数注册表单条（name/symbol/legu_code/pe_source/pb_source，白名单）。"""
    sets = {k: v for k, v in fields.items() if k in _EDITABLE}
    if not sets:
        return
    cols = ", ".join(f"{k}=?" for k in sets)
    with get_conn() as c:
        c.execute(f"UPDATE index_defs SET {cols} WHERE code=?", (*sets.values(), code))
    from app.data.base import load_index_registry
    from app.instruments import invalidate

    load_index_registry()  # 注册表随修改刷新内存
    invalidate(code)  # 已缓存的 IndexInstrument 失效，下次读取新注册表值


def get_etf_index_mapping(etf_code: str) -> dict | None:
    with get_conn() as c:
        row = c.execute(
            """SELECT m.etf_code, m.index_code, m.source, i.name AS index_name
               FROM etf_index_map m LEFT JOIN index_defs i ON i.code = m.index_code
               WHERE m.etf_code = ?""",
            (etf_code,),
        ).fetchone()
    return dict(row) if row else None


def set_etf_index_mapping(etf_code: str, index_code: str | None, source: str = "manual") -> None:
    """设置/清除 ETF→指数映射（upsert）。index_code=None 清空映射。校验 index_code 存在。"""
    now = datetime.now().strftime("%Y-%m-%d %H:%M:%S")
    with get_conn() as c:
        if index_code is None:
            c.execute("DELETE FROM etf_index_map WHERE etf_code=?", (etf_code,))
            return
        if not c.execute("SELECT 1 FROM index_defs WHERE code=?", (index_code,)).fetchone():
            raise ValueError(f"指数不存在: {index_code}")
        c.execute(
            """INSERT INTO etf_index_map(etf_code, index_code, source, created_at, updated_at)
               VALUES (?,?,?,?,?)
               ON CONFLICT(etf_code) DO UPDATE SET index_code=excluded.index_code,
                 source=excluded.source, updated_at=excluded.updated_at""",
            (etf_code, index_code, source, now, now),
        )


def auto_map_etf_index(etf_name: str) -> str | None:
    """ETF 名称子串匹配指数名，多命中取最长名（如「沪深300ETF华泰柏瑞」→ 沪深300）。返回指数 code 或 None。"""
    if not etf_name:
        return None
    best, best_len = None, 0
    for d in get_index_defs():
        name = d["name"]
        if name and name in etf_name and len(name) > best_len:
            best, best_len = d["code"], len(name)
    return best


def auto_map_holdings_etfs() -> int:
    """遍历 active 持仓中未映射的 ETF，按名称自动映射写 source='auto'（幂等）。返回新映射数。"""
    from app.data.base import is_etf_code

    with get_conn() as c:
        holdings = c.execute("SELECT code FROM holdings WHERE status='active'").fetchall()
        mapped = {r["etf_code"] for r in c.execute("SELECT etf_code FROM etf_index_map").fetchall()}
    n = 0
    for h in holdings:
        code = h["code"]
        if code in mapped or not is_etf_code(code):
            continue
        with get_conn() as c:
            row = c.execute("SELECT name FROM stocks WHERE code=?", (code,)).fetchone()
        name = row["name"] if row else ""
        idx = auto_map_etf_index(name)
        if idx:
            set_etf_index_mapping(code, idx, source="auto")
            n += 1
            mapped.add(code)
    if n:
        logger.info("[指数] ETF 自动映射 %d 只", n)
    return n


def refresh_all_indices() -> dict:
    """并发预热全部指数（限流 6，同 refresh._run_parallel 思路），单指数失败不中断。返回汇总。"""
    from app.services.refresh import get_index_codes, refresh_index

    codes = get_index_codes()
    if not codes:
        return {"codes": [], "ok": 0, "fail": []}
    if len(codes) == 1:
        results = [refresh_index(codes[0])]
    else:
        max_workers = min(6, len(codes))
        results = []
        with ThreadPoolExecutor(max_workers=max_workers) as ex:
            futs = {ex.submit(refresh_index, c): c for c in codes}
            for fut in as_completed(futs):
                results.append(fut.result())
    fail = [r["code"] for r in results if r.get("error")]
    if fail:
        logger.warning("[指数] 预热失败 %d 个: %s", len(fail), fail)
    return {"codes": codes, "ok": len(codes) - len(fail), "fail": fail}


def _a_share_session_progress(as_of: str) -> float:
    """A 股交易时段进度 0~1（09:30-11:30 + 13:00-15:00，共 240 分钟）。as_of='HH:MM'。"""
    try:
        hh, mm = (as_of or "00:00").split(":")[:2]
        mins = int(hh) * 60 + int(mm)
    except (TypeError, ValueError):
        return 0.0
    # 09:30=570, 11:30=690, 13:00=780, 15:00=900
    if mins <= 570:
        elapsed = 0
    elif mins <= 690:
        elapsed = mins - 570
    elif mins < 780:
        elapsed = 120
    elif mins <= 900:
        elapsed = 120 + (mins - 780)
    else:
        elapsed = 240
    return min(1.0, max(0.0, elapsed / 240.0))


def index_turnover_compare(code: str) -> dict:
    """指数成交额 + 较上一交易日同时段成交额（纯缓存零网络）。

    口径（额，非量）：
    - amount：行情当日成交额（腾讯三元组，权威）
    - 分时只有量无额 → 用「额/量」比例折成金额（与 combo_index_volume 同构）：
        scale_today = 今日额 / 今日分时累计量
        scale_prev  = 昨全日额/昨全日量（昨额为 0 时退回 scale_today）
    - 同比优先：今日累计额 vs 昨同时钟分时量×scale_prev
    - 昨无分时：昨全日量 × 交易进度 × scale_prev（basis=scaled）
    - 都无分时：今/昨全日额；昨额缺失则用量×scale 估（basis=daily）
    - state：expand 放量 / shrink 缩量 / flat 持平（阈值 ±3%）
    """
    from datetime import date, timedelta

    from app.data.cache import get_daily_price, get_daily_prices, get_index_intraday
    from app.services.quote import get_quote

    out = {
        "amount": None, "prev_amount": None, "chg_pct": None,
        "state": None, "as_of": None, "basis": None,
    }
    try:
        q = get_quote(code) or {}
    except Exception:  # noqa: BLE001
        q = {}
    today_amt = q.get("amount")
    out["amount"] = float(today_amt) if today_amt is not None else None

    today = date.today().isoformat()
    start = (date.today() - timedelta(days=21)).isoformat()
    bars = get_daily_prices(code, start, today)
    prev_dates = [r["trade_date"] for r in bars if r["trade_date"] < today]
    if not prev_dates:
        return out
    prev_date = prev_dates[-1]
    prev_bar = get_daily_price(code, prev_date)
    prev_vol_full = float(prev_bar["volume"] or 0.0) if prev_bar else 0.0
    prev_amt_full = float(prev_bar["amount"] or 0.0) if prev_bar else 0.0

    def _set_amt_chg(today_a: float, prev_a: float) -> None:
        if prev_a <= 0 or today_a < 0:
            return
        chg = (today_a / prev_a - 1.0) * 100.0
        out["chg_pct"] = round(chg, 1)
        out["prev_amount"] = round(prev_a, 0)
        out["state"] = "expand" if chg > 3 else ("shrink" if chg < -3 else "flat")

    today_intra = get_index_intraday(code, today)
    prev_intra = get_index_intraday(code, prev_date)

    if today_intra and out["amount"] is not None:
        as_of = today_intra[-1].get("ts") or ""
        today_vol = sum(float(r.get("volume") or 0.0) for r in today_intra)
        out["as_of"] = as_of or None
        if today_vol <= 0:
            return out
        scale_today = out["amount"] / today_vol
        scale_prev = (prev_amt_full / prev_vol_full) if (prev_amt_full > 0 and prev_vol_full > 0) else scale_today

        if prev_intra:
            prev_vol = sum(
                float(r.get("volume") or 0.0)
                for r in prev_intra
                if (r.get("ts") or "") <= as_of
            )
            out["basis"] = "intraday"
            _set_amt_chg(out["amount"], prev_vol * scale_prev)
        elif prev_vol_full > 0:
            progress = _a_share_session_progress(as_of)
            out["basis"] = "scaled"
            _set_amt_chg(out["amount"], prev_vol_full * progress * scale_prev)
        return out

    # 无今日分时：全日额对比
    today_a = out["amount"]
    if today_a is None:
        today_vol = float(q.get("volume") or 0.0)
        if today_vol > 0 and prev_vol_full > 0 and prev_amt_full > 0:
            # 仅有量：用昨比例估今额再比（弱）
            today_a = today_vol * (prev_amt_full / prev_vol_full)
            out["amount"] = today_a
    if today_a is not None and today_a > 0:
        if prev_amt_full > 0:
            out["basis"] = "daily"
            _set_amt_chg(today_a, prev_amt_full)
        elif prev_vol_full > 0:
            # 昨额缺失：用今额/今量比例估昨全日额
            today_vol = float(q.get("volume") or 0.0)
            if today_vol <= 0 and bars and bars[-1]["trade_date"] == today:
                today_vol = float(bars[-1]["volume"] or 0.0)
            if today_vol > 0 and today_a > 0:
                out["basis"] = "daily"
                _set_amt_chg(today_a, prev_vol_full * (today_a / today_vol))
    return out

"""持仓与交易：移动加权成本、撤销回滚（重放法）。

持仓是交易记录的物化视图。任何插入/删除交易后，对受影响股票按时间顺序重放全部交易，
重算持仓数量与移动加权成本——保证绝对一致，撤销只需删交易再重放。
"""
from datetime import datetime

from app.models.db import get_conn

# 交易必须字段
_REQUIRED = ("code", "side", "price", "quantity")


def rebuild(code: str, conn) -> dict:
    """按时间顺序重放 code 的全部交易，重建持仓。返回持仓 dict。"""
    rows = conn.execute(
        "SELECT * FROM trades WHERE code=? ORDER BY trade_time, id", (code,)
    ).fetchall()
    qty = 0.0
    avg_cost = 0.0
    total_buy = 0.0
    for t in rows:
        if t["side"] == "buy":
            total_buy += t["amount"] + t["fee"]
            new_qty = qty + t["quantity"]
            avg_cost = (qty * avg_cost + t["amount"] + t["fee"]) / new_qty if new_qty > 0 else 0.0
            qty = new_qty
        else:  # sell
            qty -= t["quantity"]
            if qty < -1e-9:
                raise ValueError(f"卖出数量超过持仓（{code}）")
    status = "active" if qty > 1e-9 else "closed"
    holding = {
        "code": code,
        "quantity": round(qty, 6),
        "avg_cost": round(avg_cost, 4),
        "total_buy": round(total_buy, 2),
        "status": status,
    }
    conn.execute(
        """INSERT INTO holdings(code, quantity, avg_cost, total_buy, status)
           VALUES (:code,:quantity,:avg_cost,:total_buy,:status)
           ON CONFLICT(code) DO UPDATE SET
             quantity=excluded.quantity, avg_cost=excluded.avg_cost,
             total_buy=excluded.total_buy, status=excluded.status""",
        holding,
    )
    # 清仓（数量归零转 closed）：删除该股全部数据缓存，组合缓存由整体重算处理
    if status == "closed":
        _purge_stock_cache(code, conn)
    return holding


def _ensure_stock(conn, code: str, name: str | None) -> None:
    if name:
        mkt = "sh" if code.startswith(("60", "68", "90")) else "sz"
        conn.execute(
            """INSERT INTO stocks(code, name, market) VALUES(?,?,?)
               ON CONFLICT(code) DO UPDATE SET name=excluded.name""",
            (code, name, mkt),
        )


def record_trade(code, side, price, quantity, fee=0.0, trade_time=None, note=None, name=None) -> dict:
    """录入交易并重放持仓。返回 {trade_id, holding}。"""
    for f in _REQUIRED:
        if locals().get(f) is None:
            raise ValueError(f"缺少必填字段: {f}")
    if side not in ("buy", "sell"):
        raise ValueError("side 必须为 buy/sell")
    if price <= 0 or quantity <= 0:
        raise ValueError("价格与数量必须为正")
    trade_time = trade_time or datetime.now().strftime("%Y-%m-%d %H:%M:%S")
    amount = round(price * quantity, 4)
    with get_conn() as conn:
        _ensure_stock(conn, code, name)
        cur = conn.execute(
            """INSERT INTO trades(code, side, price, quantity, amount, fee, trade_time, note)
               VALUES (?,?,?,?,?,?,?,?)""",
            (code, side, price, quantity, amount, fee, trade_time, note),
        )
        trade_id = cur.lastrowid
        try:
            holding = rebuild(code, conn)
        except ValueError:
            raise
    # 事务外重算当日综合评分（失败不影响交易录入）
    daily = _rebuild_daily(trade_time[:10])
    # 新股：缓存无该股行情 → 自动同步全部数据（失败不阻断录入）
    if _is_new_stock(code):
        _sync_stock_data(code)
    # 开仓/清仓引入新持仓权重 → 重算组合综合 PE/PB 序列缓存
    _rebuild_portfolio()
    return {"trade_id": trade_id, "holding": holding, "daily_score": daily}


def delete_trade(trade_id: int) -> dict:
    """撤销交易（删记录 + 重放）。若删除后持仓非法则拒绝并回滚。"""
    with get_conn() as conn:
        row = conn.execute("SELECT * FROM trades WHERE id=?", (trade_id,)).fetchone()
        if not row:
            raise ValueError(f"交易不存在: {trade_id}")
        conn.execute("DELETE FROM trades WHERE id=?", (trade_id,))
        try:
            holding = rebuild(row["code"], conn)
        except ValueError:
            raise
    daily = _rebuild_daily(row["trade_time"][:10])
    _rebuild_portfolio()
    return {"deleted_trade_id": trade_id, "holding": holding, "daily_score": daily}


def update_trade(trade_id: int, **fields) -> dict:
    """修改单笔交易：字段为 None 则保持原值。改后重放持仓并重算涉及日的综合分。

    若改动 code/日期，涉及新旧两日的综合分都重算。
    """
    with get_conn() as conn:
        row = conn.execute("SELECT * FROM trades WHERE id=?", (trade_id,)).fetchone()
        if not row:
            raise ValueError(f"交易不存在: {trade_id}")

        new = {
            "code": fields.get("code") or row["code"],
            "side": fields.get("side") or row["side"],
            "price": fields.get("price") if fields.get("price") is not None else row["price"],
            "quantity": fields.get("quantity") if fields.get("quantity") is not None else row["quantity"],
            "fee": fields.get("fee") if fields.get("fee") is not None else row["fee"],
            "trade_time": fields.get("trade_time") or row["trade_time"],
            "note": fields["note"] if "note" in fields else row["note"],
        }
        if new["side"] not in ("buy", "sell"):
            raise ValueError("side 必须为 buy/sell")
        if new["price"] <= 0 or new["quantity"] <= 0:
            raise ValueError("价格与数量必须为正")
        amount = round(new["price"] * new["quantity"], 4)
        try:
            conn.execute(
                """UPDATE trades SET code=?, side=?, price=?, quantity=?, amount=?, fee=?, trade_time=?, note=?
                   WHERE id=?""",
                (new["code"], new["side"], new["price"], new["quantity"], amount,
                 new["fee"], new["trade_time"], new["note"], trade_id),
            )
            holding_new = rebuild(new["code"], conn)
            holding_old = rebuild(row["code"], conn) if row["code"] != new["code"] else None
        except ValueError:
            raise
        except Exception as e:  # noqa: BLE001  UNIQUE 冲突等 → 转业务错误
            raise ValueError(f"修改失败: {e}")

    dates = {new["trade_time"][:10]}
    if row["trade_time"][:10] != new["trade_time"][:10]:
        dates.add(row["trade_time"][:10])
    daily = {}
    for d in dates:
        daily[d] = _rebuild_daily(d)
    _rebuild_portfolio()
    return {"trade_id": trade_id, "holding": holding_new, "holding_old": holding_old, "daily_scores": daily}


def _rebuild_daily(score_date: str):
    """重算当日综合评分（失败返回 None，不影响交易操作）。"""
    try:
        from app.analysis.scoring import rebuild_daily

        return rebuild_daily(score_date)
    except Exception:
        return None


def _rebuild_portfolio():
    """开仓/清仓/改仓后重算组合综合 PE/PB 序列缓存（权重已变，历史序列需重建）。

    失败不影响交易操作（返回 None），日志里记录原因。
    """
    import logging

    try:
        from app.analysis.portfolio import rebuild_portfolio_series

        return rebuild_portfolio_series()
    except Exception as e:  # noqa: BLE001 组合序列数据不全不中断交易
        logging.getLogger("holdings").warning("组合序列重算失败：%s", e)
        return None


def _is_new_stock(code: str) -> bool:
    """该股是否首笔交易（此前无任何交易记录 → 需同步数据）。

    用交易记录而非缓存判定：清仓会删除该股缓存，若按缓存判"新股"会把旧股误判并重复同步。
    record_trade 事务已提交，故 trades 中该股仅当前这一笔即视为首笔。
    """
    from app.models.db import get_conn

    try:
        with get_conn() as c:
            row = c.execute("SELECT COUNT(*) AS n FROM trades WHERE code=?", (code,)).fetchone()
        return row["n"] <= 1
    except Exception:  # noqa: BLE001
        return False


def _purge_stock_cache(code: str, conn) -> None:
    """清仓后删除该股全部数据缓存（日K/估值/分位/财务/序列/资金流）。

    在传入的事务连接内执行（与持仓重建同一事务，清仓成功提交则缓存一并删除，
    若随后重放失败回滚则缓存也不删）。失败不阻断持仓操作。
    """
    import logging

    try:
        from app.data.cache import purge_stock_cache

        n = purge_stock_cache(code, conn=conn)
        logging.getLogger("holdings").info("清仓 %s：删除数据缓存 %d 行", code, n)
    except Exception as e:  # noqa: BLE001 缓存删除失败不阻断
        logging.getLogger("holdings").warning("清仓清理缓存失败 %s：%s", code, e)


def _sync_stock_data(code: str):
    """开仓新股后同步其全部数据（日K/财务/百度序列/分位）。失败不阻断交易录入。"""
    import logging

    try:
        from app.services.refresh import sync_stock_full

        sync_stock_full(code)
    except Exception as e:  # noqa: BLE001 数据同步失败不阻断录入
        logging.getLogger("holdings").warning("新股数据同步失败 %s：%s", code, e)


def list_trades(code: str | None = None) -> list[dict]:
    with get_conn() as c:
        if code:
            rows = c.execute("SELECT * FROM trades WHERE code=? ORDER BY trade_time, id", (code,)).fetchall()
        else:
            rows = c.execute("SELECT * FROM trades ORDER BY trade_time, id").fetchall()
        return [dict(r) for r in rows]


def get_holdings(active_only: bool = True) -> list[dict]:
    """持仓列表（含股票名称）。"""
    with get_conn() as c:
        sql = (
            "SELECT h.*, s.name FROM holdings h LEFT JOIN stocks s ON h.code=s.code"
            + (" WHERE h.status='active'" if active_only else "")
            + " ORDER BY h.quantity DESC"
        )
        return [dict(r) for r in c.execute(sql).fetchall()]


def init_holdings(items: list[dict]) -> list[dict]:
    """批量初始化持仓：每项 {code, name, price, quantity, fee?}。"""
    results = []
    for it in items:
        r = record_trade(
            code=it["code"], side="buy", price=it["price"], quantity=it["quantity"],
            fee=it.get("fee", 0.0), name=it.get("name"),
        )
        results.append(r)
    return results

"""新浪源实现（全部经 akshare，单股接口）。

- 实时行情：ak.stock_zh_a_minute(period='1') 当日分时末根
- 日K：     ak.stock_zh_a_daily
- 财务：    ak.stock_financial_abstract
"""
import math

import akshare as ak

from app.data.base import Bar, DataSource, Financials, Quote, to_symbol


class SinaSource(DataSource):
    def name(self) -> str:
        return "sina"

    def quote(self, code: str) -> Quote | None:
        """当日分时末根作为最新价；昨收用最近日K倒数第二根。"""
        symbol = to_symbol(code)
        minute = ak.stock_zh_a_minute(symbol=symbol, period="1", adjust="")
        if minute is None or minute.empty:
            return None
        last = minute.iloc[-1]
        price = float(last["close"])
        ts = str(last["day"])
        # 昨收：从最近日K取上一交易日收盘
        prev_close = price
        try:
            daily = ak.stock_zh_a_daily(symbol=symbol)
            if len(daily) >= 2:
                prev_close = float(daily.iloc[-2]["close"])
        except Exception:
            pass
        pct_chg = (price / prev_close - 1) * 100 if prev_close else 0.0
        return Quote(
            code=code,
            name=str(last.get("name", "")),
            price=price,
            pct_chg=round(pct_chg, 2),
            prev_close=round(prev_close, 4),
            open=float(last["open"]),
            high=float(last["high"]),
            low=float(last["low"]),
            volume=float(last["volume"]),
            amount=float(last["amount"]),
            ts=ts,
        )

    def daily_bars(self, code: str, start: str, end: str) -> list[Bar]:
        symbol = to_symbol(code)
        df = ak.stock_zh_a_daily(symbol=symbol, start_date=start, end_date=end)
        if df is None or df.empty:
            return []
        bars = []
        for _, r in df.iterrows():
            bars.append(
                Bar(
                    date=str(r["date"]),
                    open=float(r["open"]),
                    high=float(r["high"]),
                    low=float(r["low"]),
                    close=float(r["close"]),
                    volume=float(r["volume"]),
                    amount=float(r["amount"]),
                )
            )
        return bars

    def financials(self, code: str) -> Financials | None:
        """最新一期财务 + 估值实时计算所需静态数据（去年年报净利/EPS、总股本、最新归母净资产、支付率、多期净利）。

        报告期为累计口径；支付率 = 去年每股股息 / 去年每股收益（每股口径，不需总股本）。
        归母净资产 = 每股净资产 × 总股本；总股本来自雪球 reg_asset。
        """
        df = ak.stock_financial_abstract(symbol=code)
        if df is None or df.empty:
            return None
        periods = [c for c in df.columns if c not in ("选项", "指标")]

        def cell(indicator: str, period: str | None = None):
            rows = df[df["指标"] == indicator]
            if rows.empty:
                return None
            v = rows.iloc[0][period if period else periods[0]]
            try:
                v = float(v)
                return None if math.isnan(v) else v
            except (TypeError, ValueError):
                return None

        latest = periods[0] if periods else ""
        annuals = [p for p in periods if str(p).endswith("1231")]
        last_annual = annuals[0] if annuals else None

        # 近8期净利序列（含累计同比）
        profit_series = []
        for p in periods[:8]:
            np_ = cell("归母净利润", p)
            yoy_ = cell("归属母公司净利润增长率", p)
            profit_series.append(
                {"report_date": str(p), "net_profit": round(np_, 2) if np_ is not None else None,
                 "profit_yoy": round(yoy_, 2) if yoy_ is not None else None}
            )

        dv_per_share, dv_report = self.dividend_info(code)
        net_profit = cell("归母净利润", last_annual) if last_annual else None
        eps = cell("基本每股收益", last_annual) if last_annual else None
        payout_ratio = round(dv_per_share / eps * 100, 2) if (dv_per_share and eps and eps > 0) else None
        total_shares = self.total_shares(code)
        bps = cell("每股净资产_最新股数")
        if bps is None:
            bps = cell("每股净资产")
        net_assets = round(bps * total_shares, 2) if (bps is not None and total_shares) else cell("股东权益合计(净资产)")

        return Financials(
            report_date=str(latest),
            roe=cell("净资产收益率(ROE)"),
            roa=cell("总资产报酬率(ROA)"),
            revenue_yoy=cell("营业总收入增长率"),
            profit_yoy=cell("归属母公司净利润增长率"),
            net_profit=net_profit,
            net_assets=net_assets,
            eps=eps,
            dv_per_share=dv_per_share,
            payout_ratio=payout_ratio,
            dv_report=dv_report,
            profit_series=profit_series,
            total_shares=total_shares,
        )

    def total_shares(self, code: str) -> float | None:
        """总股本(股)：雪球 reg_asset（注册资本，A股面值1元=股数）。"""
        try:
            df = ak.stock_individual_basic_info_xq(symbol=to_symbol(code).upper())
            row = df[df["item"] == "reg_asset"]
            if row.empty:
                return None
            return float(row.iloc[0]["value"])
        except Exception:  # noqa: BLE001 单源失败不阻塞财务同步
            return None

    def daily_fundflow(self, code: str) -> list:
        """新浪源未在 akshare 提供单股资金流封装，返回空（资金流本期放弃）。"""
        return []

    def dividend_info(self, code: str) -> tuple[float | None, str | None]:
        """最近财年每股总股息（元，含中期+末期全部派息）+ 报告期，来自巨潮分红数据。

        一个财年（如 2025）的分红会分散在「2025年报 / 2025三季报(中期)」等多条记录，
        按报告时间年份前缀取同财年所有派息比例求和，避免只算末期漏掉中期。
        """
        df = ak.stock_dividend_cninfo(symbol=code)
        if df is None or df.empty:
            return None, None
        annual = df[df["报告时间"].astype(str).str.contains("年报")]
        if annual.empty:
            return None, None
        last = annual.iloc[-1]
        year = str(last["报告时间"])[:4]  # 如 '2025年报' → '2025'
        total_per_10 = 0.0
        for _, r in df[df["报告时间"].astype(str).str.startswith(year)].iterrows():
            try:
                per_10 = float(r["派息比例"])  # 每10股派X元（含税）
            except (TypeError, ValueError):
                continue
            if per_10 and per_10 > 0:
                total_per_10 += per_10
        if total_per_10 <= 0:
            return None, None
        return round(total_per_10 / 10, 4), str(last["报告时间"])

    def dividend_per_share(self, code: str) -> float | None:
        """最近年报每股股息（元），兼容旧接口。"""
        dv, _ = self.dividend_info(code)
        return dv

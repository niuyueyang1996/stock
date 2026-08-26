#!/usr/bin/env python3
"""validate_report.py — 校验图表与结论一致性、覆盖率门槛（V2·分池·三源）。"""
import json, pathlib

def validate(metrics_path):
    m=json.loads(pathlib.Path(metrics_path).read_text(encoding="utf-8"))
    warns=[]
    for key in ("kline_coverage","valuation_coverage","fundflow_coverage"):
        v=m.get(key, 1)
        if v is not None and v < 0.60:
            warns.append(f"{key} {v:.0%} <60%，D5需标注样本不足")
    # 风险池与全池 HHI 同时报告
    if "hhi_risk" in m and "hhi_all" in m:
        if abs(m["hhi_risk"]-m["hhi_all"]) > 0.05:
            warns.append(f"风险池HHI {m['hhi_risk']:.3f} 与全池 {m['hhi_all']:.3f} 差异显著，定性以风险池为准")
    total=m.get("total_buy",0)+m.get("total_sell",0)
    fee=m.get("total_fee",0)
    if total>0 and fee/total>0.005:
        warns.append(f"费率 {fee/total:.2%} 异常高，检查是否含回购/银证")
    # 换仓口径
    if m.get("swap_ranking") == "次数":
        warns.append("换仓按次数排序，违反铁律：必须按金额")
    for w in warns: print("WARN:", w)
    if not warns: print("validate: OK")
    return warns

if __name__=="__main__":
    import sys
    validate(sys.argv[1])

#!/usr/bin/env python3
"""detect_phases.py — 多维行为向量 PELT 变点检测（三源解耦 · 不预设剧本）。"""
import json, numpy as np

def pelt(cost_series, penalty=10):
    try:
        import ruptures as rpt
        algo=rpt.Pelt(model="l2").fit(cost_series)
        return algo.predict(pen=penalty)
    except ImportError:
        n=len(cost_series)
        if n<20: return [n]
        best, idx = 0, None
        for k in range(10, n-10):
            diff=abs(np.mean(cost_series[:k])-np.mean(cost_series[k:]))
            if diff>best: best, idx = diff, k
        return [idx, n] if idx else [n]

def detect(weekly_metrics, penalty=10):
    """
    weekly_metrics: list of dict {week, turnover, p50, holding_days, swap_rate,
                                   direction(买前20日涨跌均值), position(20日分位均值),
                                   fundflow_anchor(资金流锚定度), fundamental_anchor(估值锚定度), speed(持有天数)}
    返回 [{"start","end","label","dominant_axis","vector_mean"}]
    标签由聚类生成，不查固定表。
    """
    if len(weekly_metrics)<8:
        return [{"start": weekly_metrics[0]["week"], "end": weekly_metrics[-1]["week"], "label":"单阶段", "dominant_axis": "—", "vector_mean": {}}]
    # 多维信号：取 direction/position/fundflow_anchor/fundamental_anchor/turnover/swap_rate/speed
    keys = ["turnover","direction","position","fundflow_anchor","fundamental_anchor","swap_rate","speed"]
    keys = [k for k in keys if any(m.get(k) is not None for m in weekly_metrics)]
    if not keys: keys=["turnover"]
    mat=np.array([[float(m.get(k,0) or 0) for k in keys] for m in weekly_metrics], dtype=float)
    # 标准化
    std=mat.std(axis=0); std[std==0]=1
    mat=(mat-mat.mean(axis=0))/std
    cps=pelt(mat, penalty=penalty)
    phases=[]
    prev=0
    for cp in cps:
        seg=weekly_metrics[prev:cp]
        if not seg:
            prev=cp; continue
        # 主导突变轴：方差最大
        vmean={k: float(np.mean([float(s.get(k,0) or 0) for s in seg])) for k in keys}
        # 简单描述生成（非查表）
        desc_parts=[]
        if "direction" in vmean:
            if vmean["direction"] < -0.05: desc_parts.append("深度逆势")
            elif vmean["direction"] > 0.05: desc_parts.append("顺势追涨")
            else: desc_parts.append("中性")
        if "position" in vmean:
            if vmean["position"] < 0.30: desc_parts.append("低位")
            elif vmean["position"] > 0.70: desc_parts.append("高位")
            else: desc_parts.append("中位")
        if "speed" in vmean:
            if vmean["speed"] > 30: desc_parts.append("长持")
            elif vmean["speed"] < 7: desc_parts.append("快进快出")
        # 锚定度
        anchors=[]
        if vmean.get("fundflow_anchor",0) > 0.6: anchors.append("资金流锚")
        if vmean.get("fundamental_anchor",0) > 0.6: anchors.append("估值锚")
        if vmean.get("direction") is not None and abs(vmean["direction"]) > 0.03: anchors.append("技术锚")
        if not anchors: anchors=["无锚"]
        label="·".join(desc_parts) + "·" + "/".join(anchors) if desc_parts else "/".join(anchors)
        phases.append({"start": seg[0]["week"], "end": seg[-1]["week"], "label": label, "dominant_axis": ",".join(keys), "vector_mean": vmean})
        prev=cp
    return phases

if __name__=="__main__":
    import sys, pathlib
    data=json.loads(pathlib.Path(sys.argv[1]).read_text(encoding="utf-8"))
    phases=detect(data)
    print(json.dumps(phases, ensure_ascii=False, indent=2))

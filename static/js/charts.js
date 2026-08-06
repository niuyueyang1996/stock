/** ECharts 封装。 */
function initChart(el) {
  if (!window.echarts) return null;
  const chart = echarts.getInstanceByDom(el) || echarts.init(el);
  window.addEventListener('resize', () => chart.resize());
  return chart;
}

// 图例「一键独显」：点某条线一次 → 只显示它（关掉其余）；独显状态再点同一条 → 恢复全部。
// params.selected 反映点击后的勾选状态：被点项为选中 → 独显（关掉其余）；若点了最后一条导致全关 → 恢复。
const _soloWired = new WeakSet();   // 同一 echarts 实例只绑一次（initChart 复用实例，避免重复监听）
function applyLegendSolo(chart, names) {
  if (!chart || _soloWired.has(chart)) return;
  _soloWired.add(chart);
  chart.on('legendselectchanged', (params) => {
    const selected = params.selected || {};
    if (selected[params.name]) {
      for (const n of names) if (n !== params.name) chart.dispatchAction({ type: 'legendUnSelect', name: n });
    } else if (!names.some((n) => selected[n])) {
      chart.dispatchAction({ type: 'legendAllSelect' });
    }
  });
}

// 当前值在窗口序列中的百分位（count(<=cur)/n×100），负值允许参与；样本为空返回 null
function percentileOf(values, target) {
  if (target === null || target === undefined || !values.length) return null;
  let c = 0;
  for (const v of values) if (v <= target) c++;
  return +(c / values.length * 100).toFixed(1);
}

// 当前 dataZoom 可见窗口的索引区间 {start, end}
function dataZoomWindow(chart, axisLen) {
  if (!chart || typeof chart.getModel !== 'function') return { start: 0, end: axisLen - 1 };
  const modelObj = chart.getModel();
  const model = modelObj && typeof modelObj.getComponent === 'function' ? modelObj.getComponent('dataZoom') : null;
  if (!model) return { start: 0, end: axisLen - 1 };
  const sv = model.option.startValue;
  const ev = model.option.endValue;
  if (sv !== undefined && sv !== null && ev !== undefined && ev !== null) {
    return { start: Math.min(sv, ev), end: Math.max(sv, ev) };
  }
  const s = model.option.start;
  const e = model.option.end;
  return {
    start: Math.max(0, Math.floor((s || 0) / 100 * (axisLen - 1))),
    end: Math.min(axisLen - 1, Math.ceil((e === 0 ? 0 : e || 100) / 100 * (axisLen - 1))),
  };
}

// 周期切换 tab（1y/3y/5y）。opts: {periods, active, onChange}
function periodTabs(opts) {
  const wrap = document.createElement('div');
  wrap.className = 'period-tabs';
  (opts.periods || ['1y', '3y', '5y']).forEach((p) => {
    const b = document.createElement('button');
    b.type = 'button';
    b.className = 'ptab' + (p === opts.active ? ' active' : '');
    b.textContent = p;
    b.onclick = () => {
      wrap.querySelectorAll('.ptab').forEach((x) => x.classList.remove('active'));
      b.classList.add('active');
      opts.onChange(p);
    };
    wrap.appendChild(b);
  });
  return wrap;
}

// 权重环形图
function weightPie(el, data, title) {
  const chart = initChart(el);
  if (!chart) return null;
  chart.setOption({
    title: { text: title || '', left: 'center', textStyle: { fontSize: 14 } },
    tooltip: { trigger: 'item', formatter: (p) => `${p.name}<br>市值 ${fmtNum(p.data.value)} 元<br>权重 ${p.percent}%` },
    series: [{
      type: 'pie', radius: ['40%', '68%'], center: ['50%', '55%'],
      itemStyle: { borderRadius: 6, borderColor: '#fff', borderWidth: 2 },
      label: { formatter: '{b}\n{d}%' },
      data: data.map((d) => ({ name: d.name, value: d.value })),
    }],
  });
  return chart;
}

// 分位横向条形
function quantileBars(el, items, title) {
  const chart = initChart(el);
  if (!chart) return null;
  chart.setOption({
    title: { text: title || '', left: 'center', textStyle: { fontSize: 14 } },
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' }, formatter: (ps) => {
      const p = ps[0];
      return `${p.name}<br>${p.seriesName}：${p.value}%`;
    } },
    grid: { left: 90, right: 50, top: 40, bottom: 30 },
    xAxis: { type: 'value', max: 100, axisLabel: { formatter: '{value}%' } },
    yAxis: { type: 'category', data: items.map((i) => i.name), inverse: true },
    series: [{
      type: 'bar', data: items.map((i) => ({
        value: i.value, name: i.name,
        itemStyle: { color: i.value <= 25 ? '#2f9e44' : i.value >= 75 ? '#e03131' : '#2563eb' },
      })),
      label: { show: true, position: 'right', formatter: '{c}%' },
      barWidth: 16,
    }],
  });
  return chart;
}

// 评分雷达图
function scoreRadar(el, factors, title) {
  const chart = initChart(el);
  if (!chart) return null;
  const dims = factors.filter((f) => f.used).map((f) => ({ name: f.name, max: 100 }));
  chart.setOption({
    title: { text: title || '', left: 'center', textStyle: { fontSize: 14 } },
    tooltip: {},
    legend: { bottom: 0 },
    radar: {
      indicator: dims,
      radius: '60%',
      axisName: { color: '#666', fontSize: 11 },
      splitArea: { areaStyle: { color: ['#fff', '#f7f8fa'] } },
    },
    series: [{
      type: 'radar',
      data: [{
        value: factors.filter((f) => f.used).map((f) => f.score),
        name: '因子得分',
        areaStyle: { opacity: 0.15 },
        lineStyle: { color: '#2563eb', width: 2 },
        itemStyle: { color: '#2563eb' },
      }],
    }],
  });
  return chart;
}

// 资金流条形（正红负绿）
function fundflowBars(el, latest) {
  const chart = initChart(el);
  if (!chart) return null;
  const items = [
    ['特大单', latest.super_large_net],
    ['大单', latest.large_net],
    ['中单', latest.medium_net],
    ['小单', latest.small_net],
    ['特小单', latest.xs_net],
  ];
  chart.setOption({
    title: { text: '全天五档资金净流入', left: 'center', textStyle: { fontSize: 14 } },
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' }, formatter: (ps) => {
      const p = ps[0];
      return `${p.name}<br>净流入 ${fmtFlow(p.value)}`;
    } },
    grid: { left: 70, right: 50, top: 40, bottom: 30 },
    xAxis: { type: 'value', axisLabel: { formatter: (v) => fmtFlow(v) } },
    yAxis: { type: 'category', data: items.map((i) => i[0]), inverse: true },
    series: [{
      type: 'bar', barWidth: 16,
      data: items.map(([name, v]) => ({
        value: v,
        itemStyle: { color: v >= 0 ? '#e03131' : '#2f9e44' },
      })),
      label: { show: true, position: 'right', formatter: (p) => fmtFlow(p.value) },
    }],
  });
  return chart;
}

// ============================================================
// 估值历史折线（个股 / 组合通用控件）
// series: [{date,value}] 或 {dates, values}
// opts: {
//   title, label, color,
//   marks: [{name, value, color, symbol, line}]
// }
//   - marks 为目标标记（如 实时/综合PE/前瞻）：只画图形符号（默认 pin，可 triangle），不画文字；
//     数值统一显示在标题 subtext（含可见窗口分位 + 高/低点），避免互相覆盖/压住折线
//   - line:true 时额外画一条该值的虚线
//   - 图上方有分位线控制条（P10/30/50/70/90），数值显示在按钮上
// ============================================================
function valuationChart(el, series, opts) {
  const chart = initChart(el);
  if (!chart) return null;
  const o = opts || {};
  const title = o.title || '';
  const label = o.label || '估值';
  const color = o.color || '#2563eb';
  const isArr = Array.isArray(series);
  const dates = isArr ? series.map((d) => d.date) : (series.dates || []);
  const values = isArr ? series.map((d) => d.value) : (series.values || []);
  const lastX = dates.length ? dates.length - 1 : 0;
  // 目标标记（过滤无效值；只画图形，文字交给 subtext）
  const targets = (o.marks || []).filter((m) => m.value !== null && m.value !== undefined);
  const points = targets.map((m) => ({
    coord: [lastX, m.value],
    value: m.name,
    symbol: m.symbol || 'pin',
    symbolSize: m.symbolSize || (m.symbol === 'triangle' ? 8 : 11),
    itemStyle: { color: m.color || '#2563eb' },
    label: { show: false }, // 图形旁不写「实时/前瞻」文字，只靠颜色图案区分
  }));
  const QUANTILES = [10, 30, 50, 70, 90];
  let currentQs = [];
  const activeLines = new Set();

  // 图例说明（实时/前瞻 的颜色+图案对应关系）放在图表上方，与分位控制条一起，重绘时清理旧控件
  const parent = el.parentNode;
  const ctrlKey = el.id || ('vc-' + Math.random().toString(36).slice(2));
  if (parent) parent.querySelectorAll('.valuation-quantiles[data-for="' + ctrlKey + '"]').forEach((x) => x.remove());
  if (parent) parent.querySelectorAll('.valuation-legend[data-for="' + ctrlKey + '"]').forEach((x) => x.remove());
  const legend = document.createElement('div');
  legend.className = 'valuation-legend';
  legend.dataset.for = ctrlKey;
  legend.innerHTML = targets.map((m) => {
    const c = m.color || '#e03131';
    return m.symbol === 'triangle'
      ? `<span class="vg"><i class="vg-tri" style="border-bottom-color:${c}"></i>${m.name}</span>`
      : `<span class="vg"><i class="vg-pin" style="background:${c}"></i>${m.name}</span>`;
  }).join('');
  if (parent) parent.insertBefore(legend, el);
  const ctrl = document.createElement('div');
  ctrl.className = 'valuation-quantiles';
  ctrl.dataset.for = ctrlKey;
  if (parent) parent.insertBefore(ctrl, el);

  function windowValues() {
    const win = dataZoomWindow(chart, values.length);
    const vis = [];
    for (let i = win.start; i <= win.end; i++) {
      const v = values[i];
      if (v !== null && v !== undefined) vis.push(v);
    }
    return vis;
  }

  function calcQuantiles(vis) {
    if (!vis.length) return [];
    const sorted = vis.slice().sort((a, b) => a - b);
    return QUANTILES.map((p) => ({
      p,
      v: sorted[Math.min(sorted.length - 1, Math.floor(p / 100 * sorted.length))],
    }));
  }

  function windowMinMax() {
    const win = dataZoomWindow(chart, values.length);
    let min = null;
    let max = null;
    for (let i = win.start; i <= win.end; i++) {
      const v = values[i];
      if (v === null || v === undefined) continue;
      if (!min || v < min.v) min = { i, v };
      if (!max || v > max.v) max = { i, v };
    }
    return { min, max };
  }

  function buildSeries() {
    const { min, max } = windowMinMax();
    const pointData = points.slice();
    // 可见窗口高/低点：小圆点 + 白底小字（向内自适应，避免压住折线或画到图外）
    const mid = min && max ? (min.v + max.v) / 2 : null;
    const inward = (v) => (mid != null && v > mid ? 'bottom' : 'top');
    const tipLabel = { backgroundColor: '#fff', padding: [2, 4], borderRadius: 3 };
    if (min) {
      pointData.push({
        coord: [min.i, min.v], value: '低 ' + fmtNum(min.v),
        symbol: 'circle', symbolSize: 8, itemStyle: { color: '#2f9e44' },
        label: { ...tipLabel, position: inward(min.v), fontSize: 10 },
      });
    }
    if (max) {
      pointData.push({
        coord: [max.i, max.v], value: '高 ' + fmtNum(max.v),
        symbol: 'circle', symbolSize: 8, itemStyle: { color: '#e03131' },
        label: { ...tipLabel, position: inward(max.v), fontSize: 10 },
      });
    }
    // 虚线只标位置，文字数值显示在标题 subtext；分位线数值在控制条按钮上
    const lineData = [];
    targets.filter((m) => m.line).forEach((m) => {
      lineData.push({
        yAxis: m.value, name: m.name,
        lineStyle: { color: m.color || '#e03131', type: 'dashed' },
      });
    });
    currentQs.filter((q) => activeLines.has(q.p)).forEach((q) => {
      lineData.push({
        yAxis: q.v, pct: q.p,
        lineStyle: { color: '#2f9e44', type: 'dashed' },
      });
    });
    return {
      name: label, type: 'line', data: values, symbol: 'none', smooth: true,
      lineStyle: { width: 2, color },
      areaStyle: { opacity: 0.1 },
      markPoint: { data: pointData },
      markLine: lineData.length ? {
        silent: true, symbol: 'none',
        label: { show: false },
        lineStyle: { type: 'dashed' },
        data: lineData,
      } : undefined,
    };
  }

  // 图表上方 subtext：彩色 chip 标签(实时/前瞻) + 数值 + 绿色分位；第二行低/高（语义色呼应高低点圆点）
  function updateSub() {
    const vis = windowValues();
    const pct = (v) => {
      const p = percentileOf(vis, v);
      return p == null ? '' : 'P' + p;
    };
    const rich = { q: { color: '#2f9e44', fontSize: 11, fontWeight: 'bold' } };
    const parts = targets.map((m, i) => {
      const k = 'c' + i;
      rich[k] = {
        color: '#fff', backgroundColor: m.color || '#2563eb',
        borderRadius: 3, padding: [2, 5], fontSize: 11, fontWeight: 'bold',
      };
      const p = pct(m.value);
      return `{${k}|${m.name}} ${fmtNum(m.value)}` + (p ? ` {q|${p}}` : '');
    });
    const { min, max } = windowMinMax();
    const mm = [];
    if (min) mm.push('{lo|低 ' + fmtNum(min.v) + '}');
    if (max) mm.push('{hi|高 ' + fmtNum(max.v) + '}');
    rich.lo = { color: '#2f9e44', fontSize: 12 };
    rich.hi = { color: '#e03131', fontSize: 12 };
    const sub = [parts.join('　　'), mm.join('　　')].filter((s) => s).join('\n');
    chart.setOption({
      title: { subtext: sub, subtextStyle: { fontSize: 12, color: '#495057', lineHeight: 20, rich } },
    });
  }

  function renderControls() {
    currentQs = calcQuantiles(windowValues());
    ctrl.innerHTML = '';
    const hint = document.createElement('span');
    hint.className = 'muted';
    hint.textContent = '分位线：';
    ctrl.appendChild(hint);
    currentQs.forEach((q) => {
      const b = document.createElement('button');
      b.type = 'button';
      b.className = 'qbtn' + (activeLines.has(q.p) ? ' active' : '');
      b.textContent = `P${q.p} ${fmtNum(q.v)}`;
      b.onclick = () => {
        if (activeLines.has(q.p)) activeLines.delete(q.p);
        else activeLines.add(q.p);
        renderControls();
        applySeries();
      };
      ctrl.appendChild(b);
    });
  }

  function applySeries() {
    chart.setOption({ series: [buildSeries()] }, { replaceMerge: ['series'] });
  }

  function onZoom() {
    renderControls();
    applySeries();
    updateSub();
  }

  chart.off('datazoom');
  chart.on('datazoom', onZoom);

  chart.setOption({
    title: { text: title, left: 'center', textStyle: { fontSize: 14 } },
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'cross' },
      formatter: (ps) => {
        const p = ps[0];
        if (!p || p.componentType !== 'series') return '';
        const v = p.value;
        const q = percentileOf(windowValues(), v);
        let qTxt = '';
        if (q != null) {
          const c = q <= 25 ? '#2f9e44' : q >= 75 ? '#e03131' : '#2563eb';
          qTxt = `<br>窗口分位 <b style="color:${c}">${q}%</b>`;
        }
        return `${p.axisValue}<br>${label}：${v}${qTxt}`;
      },
    },
    grid: { left: 60, right: 50, top: 64, bottom: 46 },
    xAxis: {
      type: 'category', data: dates, boundaryGap: false,
      axisLabel: { fontSize: 10, hideOverlap: true },
      axisLine: { lineStyle: { color: '#ced4da' } },
      axisTick: { alignWithLabel: true },
    },
    yAxis: {
      type: 'value', scale: true, axisLabel: { fontSize: 10 },
      splitLine: { lineStyle: { color: '#f1f3f5', type: 'dashed' } },
    },
    dataZoom: [
      { type: 'inside', start: 0, end: 100 },
      { type: 'slider', height: 16, bottom: 8, start: 0, end: 100 },
    ],
    series: [buildSeries()],
  });
  renderControls();
  updateSub();
  return chart;
}

// 前端 1 分钟基础重采样到目标窗口（与后端 app/data/fundflow.resample_points 同口径）
function resampleFlow(points, windowMin) {
  if (windowMin <= 1) return points;
  const buckets = new Map();
  for (const p of points) {
    const [h, m] = p.ts.split(':').map(Number);
    const start = Math.floor((h * 60 + m) / windowMin) * windowMin;
    const key = String(Math.floor(start / 60)).padStart(2, '0') + ':' + String(start % 60).padStart(2, '0');
    if (!buckets.has(key)) buckets.set(key, { ts: key, small_net: 0, medium_net: 0, large_net: 0, super_large_net: 0, xs_net: 0, buy_amount: 0, sell_amount: 0 });
    const b = buckets.get(key);
    b.small_net += p.small_net;
    b.medium_net += p.medium_net;
    b.large_net += p.large_net;
    b.super_large_net += p.super_large_net;
    b.xs_net += p.xs_net;
    b.buy_amount += p.buy_amount;
    b.sell_amount += p.sell_amount;
  }
  return [...buckets.values()].sort((a, b) => (a.ts < b.ts ? -1 : 1));
}

// 分时五档资金流【累积】折线：每分钟净流入逐段累加，看清整体趋势，不再波来波去。
// 五档各自一条线，特大单加粗并标 0 轴，金额智能转万/亿，tooltip 显示到该时刻的累积净流入。
function fundflowIntraday(el, points, window) {
  const chart = initChart(el);
  if (!chart) return null;
  const ts = points.map((p) => p.ts);
  const KEYS = ['super_large_net', 'large_net', 'medium_net', 'small_net', 'xs_net'];
  const cum = {};
  for (const k of KEYS) {
    let s = 0;
    cum[k] = points.map((p) => (s += Math.round(p[k])));
  }
  const series = [
    { name: '特大单', type: 'line', smooth: true, symbol: 'none', data: cum.super_large_net, lineStyle: { width: 2.5, color: '#e03131' },
      markLine: { data: [{ yAxis: 0 }], lineStyle: { color: '#999', type: 'dashed' } } },
    { name: '大单', type: 'line', smooth: true, symbol: 'none', data: cum.large_net, lineStyle: { width: 1.5, color: '#f76707' } },
    { name: '中单', type: 'line', smooth: true, symbol: 'none', data: cum.medium_net, lineStyle: { width: 1.5, color: '#339af0' } },
    { name: '小单', type: 'line', smooth: true, symbol: 'none', data: cum.small_net, lineStyle: { width: 1.5, color: '#12b886' } },
    { name: '特小单', type: 'line', smooth: true, symbol: 'none', data: cum.xs_net, lineStyle: { width: 1.5, color: '#7048e8' } },
  ];
  chart.setOption({
    title: { text: '分时资金流（' + window + '分钟 · 累积净流入）', left: 'center', textStyle: { fontSize: 14 } },
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'cross' },
      formatter: (ps) => {
        const i = ps[0].dataIndex;
        return [
          ts[i],
          '特大单 ' + fmtFlow(cum.super_large_net[i]),
          '大单   ' + fmtFlow(cum.large_net[i]),
          '中单   ' + fmtFlow(cum.medium_net[i]),
          '小单   ' + fmtFlow(cum.small_net[i]),
          '特小单 ' + fmtFlow(cum.xs_net[i]),
        ].join('<br>');
      },
    },
    legend: { data: ['特大单', '大单', '中单', '小单', '特小单'], top: 28 },
    grid: { left: 70, right: 30, top: 60, bottom: 40 },
    xAxis: { type: 'category', data: ts, axisLabel: { interval: 'auto' } },
    yAxis: { type: 'value', axisLabel: { formatter: (v) => fmtFlow(v) } },
    dataZoom: [{ type: 'inside', start: 0, end: 100 }],
    series,
  }, { notMerge: true });
  applyLegendSolo(chart, ['特大单', '大单', '中单', '小单', '特小单']);
  return chart;
}

// 买盘/卖盘【累积】金额折线：买盘红、卖盘绿，另加一条「累积净流入」（买盘−卖盘）看清买卖净流向。
// 再叠一组「窗口净流入」柱（每根柱=该窗口的净流入，非累加，右轴独立刻度），一眼看出哪分钟异动。
function fundflowBuySell(el, points, window) {
  const chart = initChart(el);
  if (!chart) return null;
  const ts = points.map((p) => p.ts);
  const cum = {};
  const windowNet = [];
  let buy = 0, sell = 0;
  cum.buy_amount = [];
  cum.sell_amount = [];
  cum.net = [];
  for (const p of points) {
    buy += Math.round(p.buy_amount);
    sell += Math.round(p.sell_amount);
    cum.buy_amount.push(buy);
    cum.sell_amount.push(sell);
    cum.net.push(buy - sell);
    windowNet.push(Math.round(p.buy_amount - p.sell_amount));
  }
  chart.setOption({
    title: { text: '买盘 / 卖盘 / 净流入（' + window + '分钟 · 累积）· 柱=窗口净流入', left: 'center', textStyle: { fontSize: 14 } },
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'cross' },
      formatter: (ps) => {
        const i = ps[0].dataIndex;
        return [
          ts[i],
          '窗口净流入 ' + fmtFlow(windowNet[i]),
          '买盘      ' + fmtFlow(cum.buy_amount[i]),
          '卖盘      ' + fmtFlow(cum.sell_amount[i]),
          '累积净流入 ' + fmtFlow(cum.net[i]),
        ].join('<br>');
      },
    },
    legend: { data: ['买盘', '卖盘', '净流入', '窗口净流入'], top: 28 },
    grid: { left: 70, right: 70, top: 60, bottom: 40 },
    xAxis: { type: 'category', data: ts, axisLabel: { interval: 'auto' } },
    yAxis: [
      { type: 'value', axisLabel: { formatter: (v) => fmtFlow(v) } },
      { type: 'value', splitLine: { show: false }, axisLabel: { formatter: (v) => fmtFlow(v) } },
    ],
    dataZoom: [{ type: 'inside', start: 0, end: 100 }],
    series: [
      { name: '买盘', type: 'line', smooth: true, symbol: 'none', data: cum.buy_amount, lineStyle: { width: 2, color: '#e03131' } },
      { name: '卖盘', type: 'line', smooth: true, symbol: 'none', data: cum.sell_amount, lineStyle: { width: 2, color: '#2f9e44' } },
      { name: '净流入', type: 'line', smooth: true, symbol: 'none', data: cum.net,
        lineStyle: { width: 2, color: '#2563eb', type: 'dashed' },
        markLine: { data: [{ yAxis: 0 }], lineStyle: { color: '#999', type: 'dashed' } } },
      { name: '窗口净流入', type: 'bar', yAxisIndex: 1, data: windowNet, barWidth: '60%',
        itemStyle: { color: (params) => params.value >= 0 ? 'rgba(225,29,72,.6)' : 'rgba(16,185,129,.6)' } },
    ],
  }, { notMerge: true });
  applyLegendSolo(chart, ['买盘', '卖盘', '净流入', '窗口净流入']);
  return chart;
}

// 历史资金净流入折线
function fundflowLine(el, history) {
  const chart = initChart(el);
  if (!chart) return null;
  chart.setOption({
    title: { text: '近30日净流入', left: 'center', textStyle: { fontSize: 14 } },
    tooltip: { trigger: 'axis', formatter: (ps) => {
      const p = ps[0];
      return `${p.axisValue}<br>净流入 ${fmtFlow(p.value)}`;
    } },
    grid: { left: 70, right: 30, top: 40, bottom: 30 },
    xAxis: { type: 'category', data: history.map((h) => h.trade_date), boundaryGap: false },
    yAxis: { type: 'value', axisLabel: { formatter: (v) => fmtFlow(v) } },
    series: [{
      type: 'line', smooth: true, symbol: 'none', data: history.map((h) => h.netamount),
      lineStyle: { width: 2, color: '#2563eb' },
      areaStyle: { opacity: 0.08 },
      markLine: { data: [{ yAxis: 0 }], lineStyle: { color: '#999', type: 'dashed' } },
    }],
  }, { notMerge: true });
  return chart;
}

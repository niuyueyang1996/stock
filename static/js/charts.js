/** ECharts 封装。 */
/** 与 style.css 令牌对齐的图表色板（ECharts 无法读 CSS 变量） */
const CHART_COLORS = {
  primary: '#2f6fed',
  primaryDeep: '#1f56cf',
  up: '#e03131',
  down: '#2f9e44',
  warn: '#e8590c',
  orange: '#f76707',
  mid: '#339af0',
  purple: '#7048e8',
  grid: '#f1f3f5',
  axis: '#ced4da',
  muted: '#999',
};
const CHART_PALETTE = [
  CHART_COLORS.primary, CHART_COLORS.up, CHART_COLORS.down, CHART_COLORS.orange,
  CHART_COLORS.purple, '#0ca678', '#f59f00', CHART_COLORS.mid,
  '#c2255c', '#495057', '#1c7ed6', '#12b886',
];
const _chartResizeWired = new WeakSet();  // 同一 echarts 实例只绑一次 resize，避免 loadPage 累加监听

function initChart(el) {
  if (!el || !window.echarts) return null;
  // 容器曾被 display:none / 清空过：先恢复可见，否则 init 宽高为 0，切回有数据也画不出来
  if (el.style.display === 'none') el.style.display = '';
  const chart = echarts.getInstanceByDom(el) || echarts.init(el);
  if (!_chartResizeWired.has(chart)) {
    _chartResizeWired.add(chart);
    window.addEventListener('resize', () => chart.resize());
  }
  // 切日期后容器刚恢复显示：下一帧补一次 resize
  requestAnimationFrame(() => { try { chart.resize(); } catch (e) { /* ignore */ } });
  return chart;
}

/** 安全清空图容器：先 dispose 再清 DOM，并恢复可见（避免切日期后图永久空白）。 */
function clearChart(el) {
  if (!el) return;
  if (window.echarts) {
    const chart = echarts.getInstanceByDom(el);
    if (chart) {
      try { chart.dispose(); } catch (e) { /* ignore */ }
    }
  }
  el.innerHTML = '';
  el.style.display = '';
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
        itemStyle: { color: i.value <= 25 ? CHART_COLORS.down : i.value >= 75 ? CHART_COLORS.up : CHART_COLORS.primary },
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
        lineStyle: { color: CHART_COLORS.primary, width: 2 },
        itemStyle: { color: CHART_COLORS.primary },
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
        itemStyle: { color: v >= 0 ? CHART_COLORS.up : CHART_COLORS.down },
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
  if (!el) return null;
  // 每次重画先 dispose：切 as_of / 空态写过 .empty / 折叠态 0 宽高后，复用实例容易白板挂死
  clearChart(el);
  const chart = initChart(el);
  if (!chart) return null;
  const o = opts || {};
  const title = o.title || '';
  const label = o.label || '估值';
  const color = o.color || CHART_COLORS.primary;
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
    itemStyle: { color: m.color || CHART_COLORS.primary },
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
    const c = m.color || CHART_COLORS.up;
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
        symbol: 'circle', symbolSize: 8, itemStyle: { color: CHART_COLORS.down },
        label: { ...tipLabel, position: inward(min.v), fontSize: 10 },
      });
    }
    if (max) {
      pointData.push({
        coord: [max.i, max.v], value: '高 ' + fmtNum(max.v),
        symbol: 'circle', symbolSize: 8, itemStyle: { color: CHART_COLORS.up },
        label: { ...tipLabel, position: inward(max.v), fontSize: 10 },
      });
    }
    // 虚线只标位置，文字数值显示在标题 subtext；分位线数值在控制条按钮上
    const lineData = [];
    targets.filter((m) => m.line).forEach((m) => {
      lineData.push({
        yAxis: m.value, name: m.name,
        lineStyle: { color: m.color || CHART_COLORS.up, type: 'dashed' },
      });
    });
    currentQs.filter((q) => activeLines.has(q.p)).forEach((q) => {
      lineData.push({
        yAxis: q.v, pct: q.p,
        lineStyle: { color: CHART_COLORS.down, type: 'dashed' },
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
    const rich = { q: { color: CHART_COLORS.down, fontSize: 11, fontWeight: 'bold' } };
    const parts = targets.map((m, i) => {
      const k = 'c' + i;
      rich[k] = {
        color: '#fff', backgroundColor: m.color || CHART_COLORS.primary,
        borderRadius: 3, padding: [2, 5], fontSize: 11, fontWeight: 'bold',
      };
      const p = pct(m.value);
      return `{${k}|${m.name}} ${fmtNum(m.value)}` + (p ? ` {q|${p}}` : '');
    });
    const { min, max } = windowMinMax();
    const mm = [];
    if (min) mm.push('{lo|低 ' + fmtNum(min.v) + '}');
    if (max) mm.push('{hi|高 ' + fmtNum(max.v) + '}');
    rich.lo = { color: CHART_COLORS.down, fontSize: 12 };
    rich.hi = { color: CHART_COLORS.up, fontSize: 12 };
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
          const c = q <= 25 ? CHART_COLORS.down : q >= 75 ? CHART_COLORS.up : CHART_COLORS.primary;
          qTxt = `<br>窗口分位 <b style="color:${c}">${q}%</b>`;
        }
        return `${p.axisValue}<br>${label}：${v}${qTxt}`;
      },
    },
    grid: { left: 60, right: 50, top: 64, bottom: 46 },
    xAxis: {
      type: 'category', data: dates, boundaryGap: false,
      axisLabel: { fontSize: 10, hideOverlap: true },
      axisLine: { lineStyle: { color: CHART_COLORS.axis } },
      axisTick: { alignWithLabel: true },
    },
    yAxis: {
      type: 'value', scale: true, axisLabel: { fontSize: 10 },
      splitLine: { lineStyle: { color: CHART_COLORS.grid, type: 'dashed' } },
    },
    dataZoom: [
      { type: 'inside', start: 0, end: 100 },
      { type: 'slider', height: 16, bottom: 8, start: 0, end: 100 },
    ],
    series: [buildSeries()],
  }, { notMerge: true });
  renderControls();
  updateSub();
  requestAnimationFrame(() => { try { chart.resize(); } catch (e) { /* ignore */ } });
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
    if (!buckets.has(key)) buckets.set(key, { ts: key, small_net: 0, medium_net: 0, large_net: 0, super_large_net: 0, xs_net: 0, buy_amount: 0, sell_amount: 0, price: null });
    const b = buckets.get(key);
    b.small_net += p.small_net;
    b.medium_net += p.medium_net;
    b.large_net += p.large_net;
    b.super_large_net += p.super_large_net;
    b.xs_net += p.xs_net;
    b.buy_amount += p.buy_amount;
    b.sell_amount += p.sell_amount;
    if (p.price != null) b.price = p.price;   // 桶末笔价（股价折线）
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
    { name: '特大单', type: 'line', smooth: true, symbol: 'none', data: cum.super_large_net,
      itemStyle: { color: CHART_COLORS.up }, lineStyle: { width: 2.5, color: CHART_COLORS.up },
      emphasis: { focus: 'series' },
      markLine: { data: [{ yAxis: 0 }], lineStyle: { color: '#999', type: 'dashed' } } },
    { name: '大单', type: 'line', smooth: true, symbol: 'none', data: cum.large_net,
      itemStyle: { color: CHART_COLORS.orange }, lineStyle: { width: 1.5, color: CHART_COLORS.orange },
      emphasis: { focus: 'series' } },
    { name: '中单', type: 'line', smooth: true, symbol: 'none', data: cum.medium_net,
      itemStyle: { color: CHART_COLORS.mid }, lineStyle: { width: 1.5, color: CHART_COLORS.mid },
      emphasis: { focus: 'series' } },
    { name: '小单', type: 'line', smooth: true, symbol: 'none', data: cum.small_net,
      itemStyle: { color: '#12b886' }, lineStyle: { width: 1.5, color: '#12b886' },
      emphasis: { focus: 'series' } },
    { name: '特小单', type: 'line', smooth: true, symbol: 'none', data: cum.xs_net,
      itemStyle: { color: CHART_COLORS.purple }, lineStyle: { width: 1.5, color: CHART_COLORS.purple },
      emphasis: { focus: 'series' } },
  ];
  chart.setOption({
    title: { text: window + '分钟 · 累积净流入', left: 'center', textStyle: { fontSize: 13 } },
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
    legend: { data: ['特大单', '大单', '中单', '小单', '特小单'], top: 26, type: 'scroll',
              icon: 'roundRect', itemWidth: 22, itemHeight: 10, itemGap: 10 },
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
    legend: { data: ['买盘', '卖盘', '净流入', '窗口净流入'], top: 26, type: 'scroll',
              icon: 'roundRect', itemWidth: 22, itemHeight: 10, itemGap: 10 },
    grid: { left: 70, right: 70, top: 60, bottom: 40 },
    xAxis: { type: 'category', data: ts, axisLabel: { interval: 'auto' } },
    yAxis: [
      { type: 'value', axisLabel: { formatter: (v) => fmtFlow(v) } },
      { type: 'value', splitLine: { show: false }, axisLabel: { formatter: (v) => fmtFlow(v) } },
    ],
    dataZoom: [{ type: 'inside', start: 0, end: 100 }],
    series: [
      { name: '买盘', type: 'line', smooth: true, symbol: 'none', data: cum.buy_amount,
        itemStyle: { color: CHART_COLORS.up }, lineStyle: { width: 2, color: CHART_COLORS.up },
        emphasis: { focus: 'series' } },
      { name: '卖盘', type: 'line', smooth: true, symbol: 'none', data: cum.sell_amount,
        itemStyle: { color: CHART_COLORS.down }, lineStyle: { width: 2, color: CHART_COLORS.down },
        emphasis: { focus: 'series' } },
      { name: '净流入', type: 'line', smooth: true, symbol: 'none', data: cum.net,
        itemStyle: { color: CHART_COLORS.primary }, lineStyle: { width: 2, color: CHART_COLORS.primary, type: 'dashed' },
        emphasis: { focus: 'series' },
        markLine: { data: [{ yAxis: 0 }], lineStyle: { color: '#999', type: 'dashed' } } },
      { name: '窗口净流入', type: 'bar', yAxisIndex: 1, data: windowNet, barWidth: '60%',
        itemStyle: { color: (params) => params.value >= 0 ? 'rgba(225,29,72,.6)' : 'rgba(16,185,129,.6)' } },
    ],
  }, { notMerge: true });
  applyLegendSolo(chart, ['买盘', '卖盘', '净流入', '窗口净流入']);
  return chart;
}

// ============================================================
// 多日资金流（统一时间窗的天窗口）：五档堆叠柱 + 每日买卖盘
// ============================================================

// 把逐日五档行按 N 个交易日聚合成桶（聚合桶语义：7天 = 每 7 个交易日一个点）
function bucketFlowDays(hist, bucket) {
  if (!hist.length || bucket <= 1) return hist;
  const FIELDS = ['super_large_net', 'large_net', 'medium_net', 'small_net', 'xs_net',
                  'netamount', 'main_net', 'buy_amount', 'sell_amount'];
  const out = [];
  for (let i = 0; i < hist.length; i += bucket) {
    const g = hist.slice(i, i + bucket);
    const acc = { trade_date: g[0].trade_date + (g.length > 1 ? '~' + g[g.length - 1].trade_date.slice(5) : '') };
    for (const k of FIELDS) acc[k] = 0;
    for (const r of g) for (const k of FIELDS) acc[k] += r[k] || 0;
    const last = g[g.length - 1];
    acc.price = last.price != null ? last.price : null;   // 桶末交易日收盘价（股价折线）
    out.push(acc);
  }
  return out;
}

// 五档堆叠柱：每日（或聚合桶）五档净流入，正值向上堆、负值向下堆
function fundflowStacked(el, days, windowLabel) {
  const chart = initChart(el);
  if (!chart) return null;
  const labels = days.map((d) => d.trade_date);
  const BANDS = [['xs_net', '特小单', CHART_COLORS.purple], ['small_net', '小单', '#12b886'],
                 ['medium_net', '中单', CHART_COLORS.mid], ['large_net', '大单', CHART_COLORS.orange],
                 ['super_large_net', '特大单', CHART_COLORS.up]];
  const series = BANDS.map(([key, name, color]) => ({
    name, type: 'bar', stack: 'flow', data: days.map((d) => Math.round(d[key] || 0)),
    itemStyle: { color }, emphasis: { focus: 'series' },
  }));
  chart.setOption({
    title: { text: '五档资金净流入（' + windowLabel + '）', left: 'center', textStyle: { fontSize: 14 } },
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'shadow' },
      formatter: (ps) => {
        const i = ps[0].dataIndex;
        const d = days[i];
        const lines = [labels[i], '净流入 ' + fmtFlow(d.netamount || 0), '主力 ' + fmtFlow(d.main_net || 0)];
        for (const [key, name] of BANDS) lines.push(name + ' ' + fmtFlow(d[key] || 0));
        return lines.join('<br>');
      },
    },
    legend: { data: ['特大单', '大单', '中单', '小单', '特小单'], top: 26, type: 'scroll',
              icon: 'roundRect', itemWidth: 22, itemHeight: 10, itemGap: 10 },
    grid: { left: 75, right: 30, top: 60, bottom: 40 },
    xAxis: { type: 'category', data: labels, axisLabel: { interval: 'auto', rotate: labels.length > 12 ? 35 : 0 } },
    yAxis: { type: 'value', axisLabel: { formatter: (v) => fmtFlow(v) } },
    dataZoom: [{ type: 'inside', start: 0, end: 100 }],
    series,
  }, { notMerge: true });
  applyLegendSolo(chart, ['特大单', '大单', '中单', '小单', '特小单']);
  return chart;
}

// 每日（或聚合桶）买盘/卖盘累积 + 每桶净流入柱（与分时 fundflowBuySell 同构，聚合粒度换成天）
function fundflowDayBuySell(el, days, windowLabel) {
  const chart = initChart(el);
  if (!chart) return null;
  const labels = days.map((d) => d.trade_date);
  const cumBuy = [], cumSell = [], cumNet = [], bucketNet = [];
  let buy = 0, sell = 0;
  for (const d of days) {
    buy += Math.round(d.buy_amount || 0);
    sell += Math.round(d.sell_amount || 0);
    cumBuy.push(buy); cumSell.push(sell);
    cumNet.push(buy - sell);
    bucketNet.push(Math.round((d.buy_amount || 0) - (d.sell_amount || 0)));
  }
  chart.setOption({
    title: { text: '买盘 / 卖盘 / 净流入（' + windowLabel + ' · 累积）· 柱=' + windowLabel + '净流入',
             left: 'center', textStyle: { fontSize: 14 } },
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'cross' },
      formatter: (ps) => {
        const i = ps[0].dataIndex;
        return [
          labels[i],
          windowLabel + '净流入 ' + fmtFlow(bucketNet[i]),
          '买盘      ' + fmtFlow(cumBuy[i]),
          '卖盘      ' + fmtFlow(cumSell[i]),
          '累积净流入 ' + fmtFlow(cumNet[i]),
        ].join('<br>');
      },
    },
    legend: { data: ['买盘', '卖盘', '净流入', windowLabel + '净流入'], top: 26, type: 'scroll',
              icon: 'roundRect', itemWidth: 22, itemHeight: 10, itemGap: 10 },
    grid: { left: 75, right: 70, top: 60, bottom: 40 },
    xAxis: { type: 'category', data: labels, axisLabel: { interval: 'auto', rotate: labels.length > 12 ? 35 : 0 } },
    yAxis: [
      { type: 'value', axisLabel: { formatter: (v) => fmtFlow(v) } },
      { type: 'value', splitLine: { show: false }, axisLabel: { formatter: (v) => fmtFlow(v) } },
    ],
    dataZoom: [{ type: 'inside', start: 0, end: 100 }],
    series: [
      { name: '买盘', type: 'line', smooth: true, symbol: 'none', data: cumBuy,
        itemStyle: { color: CHART_COLORS.up }, lineStyle: { width: 2, color: CHART_COLORS.up },
        emphasis: { focus: 'series' } },
      { name: '卖盘', type: 'line', smooth: true, symbol: 'none', data: cumSell,
        itemStyle: { color: CHART_COLORS.down }, lineStyle: { width: 2, color: CHART_COLORS.down },
        emphasis: { focus: 'series' } },
      { name: '净流入', type: 'line', smooth: true, symbol: 'none', data: cumNet,
        itemStyle: { color: CHART_COLORS.primary }, lineStyle: { width: 2, color: CHART_COLORS.primary, type: 'dashed' },
        emphasis: { focus: 'series' },
        markLine: { data: [{ yAxis: 0 }], lineStyle: { color: '#999', type: 'dashed' } } },
      { name: windowLabel + '净流入', type: 'bar', yAxisIndex: 1, data: bucketNet, barWidth: '60%',
        itemStyle: { color: (params) => params.value >= 0 ? 'rgba(225,29,72,.6)' : 'rgba(16,185,129,.6)' } },
    ],
  }, { notMerge: true });
  applyLegendSolo(chart, ['买盘', '卖盘', '净流入', windowLabel + '净流入']);
  return chart;
}

// ============================================================
// 指数量价图（东财五档弃用后指数资金面全用腾讯量价）
// ============================================================

// 股价/净值独立折线图（资金流趋势面板最上方）：只看价格/净值自身走势，独立自适应刻度。
// points 为分时（{ts,price}）或日级（{trade_date,price}）；无 price 返回空。
function fundflowPrice(el, points, windowLabel) {
  const labels = (points || []).map((p) => p.ts || p.trade_date || '');
  const prices = (points || []).map((p) => (p.price != null ? p.price : null));
  if (!prices.some((v) => v != null)) { clearChart(el); el.style.display = 'none'; return null; }
  el.style.display = '';
  const chart = initChart(el);
  if (!chart) return null;
  chart.setOption({
    title: { text: '股价 / 组合净值（' + windowLabel + '）', left: 'center', textStyle: { fontSize: 13 } },
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'cross' },
      formatter: (ps) => {
        const i = ps[0].dataIndex;
        return [labels[i], '价格 ' + prices[i]].join('<br>');
      },
    },
    grid: { left: 62, right: 20, top: 36, bottom: 30 },
    xAxis: { type: 'category', data: labels, axisLabel: { fontSize: 10, hideOverlap: true },
             axisPointer: { label: { show: false } } },
    yAxis: { type: 'value', scale: true, axisLabel: { fontSize: 10 } },
    dataZoom: [{ type: 'inside', start: 0, end: 100 }],
    series: [{
      name: '股价', type: 'line', smooth: true, symbol: 'none', data: prices,
      itemStyle: { color: CHART_COLORS.primaryDeep }, lineStyle: { width: 2, color: CHART_COLORS.primaryDeep },
      areaStyle: { color: 'rgba(47,111,237,.08)' },
    }],
  }, { notMerge: true });
  // 容器曾隐藏过：下一帧强制 resize，否则宽高为 0
  requestAnimationFrame(() => { try { chart.resize(); } catch (e) { /* ignore */ } });
  return chart;
}

// 量价图：Σ累计成交额折线（右轴粗线）+ 各期成交额柱（右轴）+ 各指数价格折线（左轴）。
// points 为 [{ts|date, amount(该期成交额), prices|closes:{code:price}}]，names={code:指数名}。
// 成交额从起始逐期累积（单调递增），避免当期波动造成「越看越少」的错觉；柱显示各期独立量。
function indexVolumePrice(el, points, windowLabel, names) {
  if (!points || !points.length) { clearChart(el); return null; }
  const chart = initChart(el);
  if (!chart) return null;
  const PALETTE = CHART_PALETTE;
  const labels = points.map((p) => p.ts || p.date);
  const amounts = points.map((p) => p.amount || 0);
  const cum = []; let acc = 0;
  for (const a of amounts) { acc += a; cum.push(acc); }
  const codes = [...new Set(points.flatMap((p) => Object.keys(p.prices || p.closes || {})))];
  const named = (c) => (names && names[c]) || c;
  const series = [
    ...codes.map((c, i) => ({
      name: named(c), type: 'line', smooth: true, symbol: 'none',
      data: points.map((p) => { const m = p.prices || p.closes || {}; return m[c] != null ? m[c] : null; }),
      itemStyle: { color: PALETTE[i % PALETTE.length] },      // 图例色块颜色 = 折线颜色
      lineStyle: { width: 1.6, color: PALETTE[i % PALETTE.length] },
      emphasis: { focus: 'series' },
    })),
    { name: '各期成交额', type: 'bar', yAxisIndex: 1, data: amounts, barWidth: '50%',
      itemStyle: { color: 'rgba(59,130,246,.32)' } },
    { name: '累计成交额', type: 'line', smooth: true, symbol: 'none', yAxisIndex: 1, data: cum,
      itemStyle: { color: '#495057' },                      // 图例色块颜色 = 折线颜色
      lineStyle: { width: 3, color: '#495057' },
      emphasis: { focus: 'series' } },
  ];
  chart.setOption({
    title: { text: windowLabel + ' · 成交额变化（柱=各期 / 线=累计）+ 价格', left: 'center', textStyle: { fontSize: 13 } },
    tooltip: {
      trigger: 'axis', axisPointer: { type: 'cross' },
      formatter: (ps) => {
        const i = ps[0].dataIndex;
        const cur = amounts[i] || 0;
        const prev = i > 0 ? (amounts[i - 1] || 0) : null;
        let deltaLine = '';
        if (prev != null && prev > 0) {
          const d = cur - prev;
          const pct = (d / prev) * 100;
          const tag = pct > 3 ? '放量' : (pct < -3 ? '缩量' : '持平');
          deltaLine = `较上期 ${tag} ${(pct >= 0 ? '+' : '') + pct.toFixed(1)}%（${d >= 0 ? '+' : ''}${fmtAmt(d)}）`;
        } else if (prev != null) {
          deltaLine = '较上期 —（上期无额）';
        }
        const lines = [
          '<b>' + labels[i] + '</b>',
          '各期成交额  ' + fmtAmt(cur),
          deltaLine,
          '累计成交额  ' + fmtAmt(cum[i]),
        ].filter(Boolean);
        for (const p of ps) {
          if (!['累计成交额', '各期成交额'].includes(p.seriesName) && p.value != null) {
            lines.push(p.seriesName + '  ' + fmtNum(p.value));
          }
        }
        return lines.join('<br>');
      },
    },
    legend: { data: series.map((s) => s.name), top: 26, type: 'scroll', icon: 'roundRect',
              itemWidth: 22, itemHeight: 10, itemGap: 10 },
    grid: { left: 60, right: 62, top: 60, bottom: 46 },
    xAxis: { type: 'category', data: labels, boundaryGap: true,
             axisLabel: { fontSize: 10, hideOverlap: true, rotate: labels.length > 20 ? 35 : 0 } },
    yAxis: [
      { type: 'value', scale: true, name: '点位', nameTextStyle: { fontSize: 10, color: '#8b95a5' },
        axisLabel: { fontSize: 10 } },
      { type: 'value', scale: true, name: '成交额', nameTextStyle: { fontSize: 10, color: '#8b95a5' },
        splitLine: { show: false }, axisLabel: { fontSize: 10, formatter: (v) => fmtAmt(v) } },
    ],
    dataZoom: [
      { type: 'inside', start: 0, end: 100 },
      { type: 'slider', height: 16, bottom: 8, start: 0, end: 100 },
    ],
    series,
  }, { notMerge: true });
  applyLegendSolo(chart, series.map((s) => s.name));
  return chart;
}

// 分时 1 分钟量价 → 目标窗口聚合（Σ成交额、价格取窗口末分钟），与后端 index_intraday_window_series 同构
function resampleIndexVolume(points, windowMin) {
  if (windowMin <= 1) return points;
  const buckets = new Map();
  for (const p of points) {
    const [h, m] = p.ts.split(':').map(Number);
    const start = Math.floor((h * 60 + m) / windowMin) * windowMin;
    const key = String(Math.floor(start / 60)).padStart(2, '0') + ':' + String(start % 60).padStart(2, '0');
    if (!buckets.has(key)) buckets.set(key, { ts: key, amount: 0, prices: {} });
    const b = buckets.get(key);
    b.amount += p.amount || 0;
    b.prices = p.prices || b.prices;   // 窗口末分钟价格覆盖
  }
  return [...buckets.values()].sort((a, b) => (a.ts < b.ts ? -1 : 1));
}

// 逐日量价 → N 个交易日聚合桶（Σ成交额、价格取桶末交易日），与后端 _bucket_day_prices 同构
function bucketIndexVolume(daily, bucket) {
  if (!daily.length || bucket <= 1) return daily;
  const out = [];
  for (let i = 0; i < daily.length; i += bucket) {
    const g = daily.slice(i, i + bucket);
    const last = g[g.length - 1];
    const amt = g.reduce((s, r) => s + (r.amount || 0), 0);
    out.push({ date: g[0].date + (g.length > 1 ? '~' + last.date.slice(5) : ''), amount: amt, closes: last.closes });
  }
  return out;
}

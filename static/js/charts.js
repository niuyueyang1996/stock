/** ECharts 封装。 */
function initChart(el) {
  if (!window.echarts) return null;
  const chart = echarts.getInstanceByDom(el) || echarts.init(el);
  window.addEventListener('resize', () => chart.resize());
  return chart;
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
    ['主力', latest.main_net],
    ['超大单', latest.super_large_net],
    ['大单', latest.large_net],
    ['中单', latest.medium_net],
    ['小单', latest.small_net],
  ];
  chart.setOption({
    title: { text: '全天五档资金净流入（元）', left: 'center', textStyle: { fontSize: 14 } },
    tooltip: { trigger: 'axis', axisPointer: { type: 'shadow' }, formatter: (ps) => {
      const p = ps[0];
      return `${p.name}<br>净流入 ${fmtNum(p.value)} 元`;
    } },
    grid: { left: 70, right: 40, top: 40, bottom: 30 },
    xAxis: { type: 'value', axisLabel: { formatter: (v) => (v / 1e8).toFixed(2) + '亿' } },
    yAxis: { type: 'category', data: items.map((i) => i[0]), inverse: true },
    series: [{
      type: 'bar', barWidth: 16,
      data: items.map(([name, v]) => ({
        value: v,
        itemStyle: { color: v >= 0 ? '#e03131' : '#2f9e44' },
      })),
      label: { show: true, position: 'right', formatter: (p) => fmtNum(p.value) },
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

// 历史主力资金净流入折线
function fundflowLine(el, history) {
  const chart = initChart(el);
  if (!chart) return null;
  chart.setOption({
    title: { text: '近30日主力净流入（亿元）', left: 'center', textStyle: { fontSize: 14 } },
    tooltip: { trigger: 'axis' },
    grid: { left: 70, right: 30, top: 40, bottom: 30 },
    xAxis: { type: 'category', data: history.map((h) => h.trade_date), boundaryGap: false },
    yAxis: { type: 'value', axisLabel: { formatter: (v) => (v / 1e8).toFixed(1) + '亿' } },
    series: [{
      type: 'line', smooth: true, symbol: 'none', data: history.map((h) => h.main_net),
      lineStyle: { width: 2, color: '#2563eb' },
      areaStyle: { opacity: 0.08 },
      markLine: { data: [{ yAxis: 0 }], lineStyle: { color: '#999', type: 'dashed' } },
    }],
  });
  return chart;
}

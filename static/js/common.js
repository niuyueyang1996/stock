/** 公共：导航、双刷新按钮、格式化工具。 */

// 数字千分位
function fmtNum(n) {
  if (n === null || n === undefined || Number.isNaN(n)) return '—';
  return Number(n).toLocaleString('zh-CN', { maximumFractionDigits: 2 });
}
// 百分比
function fmtPct(n) {
  if (n === null || n === undefined || Number.isNaN(n)) return '—';
  return Number(n).toFixed(2) + '%';
}
// 金额智能转万/亿（资金流等大额展示用）
function fmtFlow(v) {
  if (v === null || v === undefined || Number.isNaN(v)) return '—';
  const a = Math.abs(v);
  if (a >= 1e8) return (v / 1e8).toFixed(2) + '亿';
  if (a >= 1e4) return (v / 1e4).toFixed(2) + '万';
  return Number(v).toLocaleString('zh-CN', { maximumFractionDigits: 0 });
}
// 成交额等超大金额：万 / 百万 / 千万 / 亿（避免一长串数字）
function fmtAmt(v) {
  if (v === null || v === undefined || Number.isNaN(v)) return '—';
  const a = Math.abs(v);
  if (a >= 1e8) return (v / 1e8).toFixed(2) + '亿';
  if (a >= 1e7) return (v / 1e7).toFixed(2) + '千万';
  if (a >= 1e6) return (v / 1e6).toFixed(2) + '百万';
  if (a >= 1e4) return (v / 1e4).toFixed(2) + '万';
  return Number(v).toLocaleString('zh-CN', { maximumFractionDigits: 0 });
}
// A股颜色：涨红跌绿
function cls(n) {
  if (n === null || n === undefined || Number.isNaN(n)) return 'muted';
  return n > 0 ? 'up' : n < 0 ? 'down' : 'muted';
}
function signed(n) {
  if (n === null || n === undefined || Number.isNaN(n)) return '—';
  return (n > 0 ? '+' : '') + Number(n).toFixed(2);
}

function toast(msg, ms = 2500) {
  let el = document.getElementById('toast');
  if (!el) {
    el = document.createElement('div');
    el.id = 'toast';
    document.body.appendChild(el);
  }
  el.textContent = msg;
  el.classList.add('show');
  clearTimeout(toast._t);
  toast._t = setTimeout(() => el.classList.remove('show'), ms);
}

// 持仓页/组合页：点击标签直接修改（事件委托，重渲染后依然生效）
document.addEventListener('click', (e) => {
  const b = e.target.closest('[data-edittag]');
  if (!b) return;
  const code = b.dataset.edittag;
  const cur = b.textContent.trim();
  const t = prompt('修改标签（如：红利/科技/银行，留空报错）', cur || '');
  if (t === null) return; // 取消
  const tag = t.trim();
  if (!tag) { toast('标签不能为空'); return; }
  api('/stocks/' + code + '/tag', { method: 'PUT', body: { tag } })
    .then(() => { toast('已修改标签'); loadPage(); })
    .catch((e2) => toast('修改失败：' + e2.message, 4000));
});

function initNav(active) {
  const nav = document.querySelector('nav');
  if (!nav) return;
  const links = [
    ['/static/index.html', 'index', '持仓'],
    ['/static/indices.html', 'indices', '指数'],
    ['/static/portfolio.html', 'portfolio', '组合分析'],
    ['/static/stock.html', 'stock', '个股'],
    ['/static/trade.html', 'trade', '交易评分'],
  ];
  nav.innerHTML = '';
  links.forEach(([href, page, label]) => {
    const a = document.createElement('a');
    a.href = href;
    a.textContent = label;
    if (page === active) a.classList.add('active');
    nav.appendChild(a);
  });
  const spacer = document.createElement('span');
  spacer.className = 'spacer';
  nav.appendChild(spacer);

  const aiBtn = document.createElement('button');
  aiBtn.textContent = '🤖 AI';
  aiBtn.title = '配置 / 切换 AI 诊股大模型';
  aiBtn.onclick = openAiSettings;
  nav.appendChild(aiBtn);

  const dynamic = document.createElement('button');
  dynamic.textContent = '⚡ 刷新动态数据';
  dynamic.title = '只刷新实时价格 / 当前PE/PB / 股息率（增量、快）';
  dynamic.onclick = () => doRefresh(dynamic, false);
  nav.appendChild(dynamic);

  const full = document.createElement('button');
  full.textContent = '🔄 全量刷新';
  full.className = 'primary';
  full.title = '日K + 估值分位重算 + 财务/股息，触发全部重计算';
  full.onclick = () => doRefresh(full, true);
  nav.appendChild(full);

  const status = document.createElement('span');
  status.id = 'statusText';
  nav.appendChild(status);

  setupPrewarmBar(nav);
}

// 启动后台预热提示条（全页通用）：轮询 /status/prewarm，预热中显示当前步骤，刚完成时短暂展示结果。
// 预热是启动后台线程执行的（拉汇率/查除权/缓存市场列表），不提示会让用户误以为程序没反应。
let _prewarmSeen = false;
function setupPrewarmBar(nav) {
  if (!nav || document.getElementById('prewarmBar')) return;
  const bar = document.createElement('div');
  bar.id = 'prewarmBar';
  bar.className = 'prewarm-bar';
  bar.style.display = 'none';
  const txt = document.createElement('span');
  txt.id = 'prewarmText';
  bar.appendChild(txt);
  nav.insertAdjacentElement('afterend', bar);

  async function checkPrewarm() {
    try {
      const p = await api('/status/prewarm', { silent: true });
      if (p.running) {
        _prewarmSeen = true;
        bar.classList.remove('done');
        bar.style.display = 'block';
        txt.textContent = p.step ? '后台预热中：' + p.step + '…' : '后台预热中…';
        setTimeout(checkPrewarm, 3000);
      } else if (_prewarmSeen && p.done.length) {
        // 页面加载时预热进行中，现已完成 → 展示一次结果后隐藏
        bar.classList.add('done');
        txt.textContent = '后台预热完成：' + p.done.join(' / ') + '，数据已就绪';
        setTimeout(() => { bar.style.display = 'none'; }, 5000);
      }
    } catch (e) { /* 预热接口查询失败静默 */ }
  }
  checkPrewarm();
}

// 刷新内容项（与后端 DYNAMIC_ITEMS / FULL_ITEMS / STOCK_*_ITEMS 对应）
// AI 打分不在刷新项里：由组合页/交易页手动触发（POST /api/ai-scoring/*）
const REFRESH_OPTIONS = {
  dynamic: [
    ['price', '实时价格（分钟级）'],
    ['valuation', '当前估值（PE/PB/股息率/市值）'],
    ['flow', '当日资金流（分笔拉取）'],
  ],
  full: [
    ['bars', '日K历史（全量重拉覆盖）'],
    ['financials', '财务数据（净利/净资产/EPS/支付率）'],
    ['valuation', '估值分位（百度序列+1y/3y/5y分位+实时估值）'],
    ['fx', '港股汇率（HKD/CNY）'],
    ['flow', '当日资金流（分笔拉取）'],
    ['portfolio', '组合综合序列重算'],
  ],
  stock_dynamic: [
    ['price', '实时价格（分钟级）'],
    ['valuation', '当前估值（PE/PB/股息率/市值）'],
    ['flow', '当日资金流（分笔拉取）'],
  ],
  stock_full: [
    ['bars', '日K历史（全量重拉覆盖）'],
    ['financials', '财务数据（净利/净资产/EPS/支付率）'],
    ['valuation', '估值分位（百度序列+1y/3y/5y分位+实时估值）'],
    ['flow', '当日资金流（分笔拉取）'],
  ],
};

// 弹窗多选本次要刷新的内容；scope='global'（首页/组合页）或 'stock'（个股页）；
// 返回 Promise<items[] | null>（null = 用户取消）
function chooseRefreshItems(full, scope = 'global') {
  return new Promise((resolve) => {
    const key = scope === 'stock' ? (full ? 'stock_full' : 'stock_dynamic') : (full ? 'full' : 'dynamic');
    const opts = REFRESH_OPTIONS[key];
    const suffix = scope === 'stock' ? '（个股）' : '';
    const title = (full ? '🔄 全量刷新' : '⚡ 刷新动态数据') + suffix;
    const mask = document.createElement('div');
    mask.className = 'modal-mask';
    mask.innerHTML = `
      <div class="modal">
        <h3>${title} — 选择本次刷新内容</h3>
        <div class="modal-items">
          ${opts.map(([key, label]) => `
            <label class="mi"><input type="checkbox" value="${key}" checked> ${label}</label>`).join('')}
        </div>
        <div class="modal-actions">
          <span class="link muted" id="toggleAll">全选 / 取消全选</span>
          <button class="btn" id="cancelBtn">取消</button>
          <button class="btn primary" id="okBtn">开始刷新</button>
        </div>
      </div>`;
    document.body.appendChild(mask);
    const boxes = [...mask.querySelectorAll('input[type=checkbox]')];
    const close = (items) => { mask.remove(); resolve(items); };
    mask.querySelector('#toggleAll').onclick = () => {
      const all = boxes.every((b) => b.checked);
      boxes.forEach((b) => { b.checked = !all; });
    };
    mask.querySelector('#cancelBtn').onclick = () => close(null);
    mask.querySelector('#okBtn').onclick = () => close(boxes.filter((b) => b.checked).map((b) => b.value));
    mask.addEventListener('click', (e) => { if (e.target === mask) close(null); });
  });
}

async function doRefresh(btn, full, scope = 'global', code) {
  const items = await chooseRefreshItems(full, scope);
  if (!items || !items.length) return; // 用户取消或未勾选任何内容
  btn.disabled = true;
  document.getElementById('statusText').textContent = '刷新中…';
  try {
    const path = scope === 'stock'
      ? `/stocks/${code}/refresh` + (full ? '/full' : '')
      : full ? '/refresh/full' : '/refresh';
    const r = await api(path, { blocking: true, method: 'POST', body: JSON.stringify({ items }) });
    const n = r.total_fetched ?? 0;
    const label = scope === 'stock' ? '个股' : (full ? '全量' : '动态');
    document.getElementById('statusText').textContent =
      `${label}刷新完成：${scope === 'stock' ? r.code : (r.stocks?.length ?? 0) + ' 只持仓'}，拉取 ${n} 条 ${r.time ?? ''}`;
    toast(`${label}刷新完成`);
    // 刷新后重新加载当前页数据
    if (typeof loadPage === 'function') loadPage();
  } catch (e) {
    document.getElementById('statusText').textContent = '刷新失败';
    toast('刷新失败：' + e.message, 4000);
  } finally {
    btn.disabled = false;
  }
}

// 股票搜索控件：返回 {code, name}
function stockSearchInput() {
  const wrap = document.createElement('span');
  wrap.className = 'stock-autocomplete';
  wrap.innerHTML = `
    <input type="text" placeholder="输入代码或名称搜索" autocomplete="off" style="width:200px">
    <div class="stock-sug" style="display:none"></div>`;
  const input = wrap.querySelector('input');
  const sug = wrap.querySelector('.stock-sug');
  let timer = null;
  input.addEventListener('input', () => {
    clearTimeout(timer);
    const q = input.value.trim();
    if (!q) { sug.style.display = 'none'; return; }
    timer = setTimeout(async () => {
      try {
        const rows = await api('/stocks/search?q=' + encodeURIComponent(q) + '&limit=10', { silent: true });
        sug.innerHTML = '';
        rows.forEach((r) => {
          const o = document.createElement('div');
          o.className = 'stock-sug-item';
          o.textContent = `${r.code} ${r.name}` + (r.market === 'hk' ? '（港股）' : r.market === 'etf' ? '（ETF）' : '');
          o.onclick = () => {
            input.value = `${r.code} ${r.name}`;
            sug.style.display = 'none';
            input.dispatchEvent(new Event('change'));
          };
          sug.appendChild(o);
        });
        sug.style.display = rows.length ? 'block' : 'none';
      } catch (e) { /* 忽略搜索失败 */ }
    }, 250);
  });
  input.addEventListener('blur', () => setTimeout(() => { sug.style.display = 'none'; }, 150));
  input.addEventListener('focus', () => { if (input.value.trim()) input.dispatchEvent(new Event('input')); });
  return wrap;
}

// 从 "600519 贵州茅台" 解析 {code,name}
function parseStockChoice(text) {
  const m = String(text || '').trim().match(/^(\d{5,6})\s*(.*)$/);
  return m ? { code: m[1], name: m[2] || null } : null;
}


// ============ AI 模型配置弹窗 ============
let _aiEditingId = null;

async function openAiSettings() {
  const mask = document.createElement('div');
  mask.className = 'modal-mask';
  mask.innerHTML = `
    <div class="modal" style="width:620px;max-width:94vw">
      <h3>🤖 AI 诊股模型</h3>
      <div id="aiActive" class="muted" style="margin-bottom:10px;font-size:12px"></div>
      <div class="row" style="align-items:center;gap:8px;margin-bottom:12px">
        <span class="muted" style="font-size:12px">思考级别（打分/诊股用，越高越深入但更慢更贵）</span>
        <select id="aiReasoning" style="width:140px" title="OpenAI 兼容 reasoning_effort（low/high/max），provider 不支持时自动忽略">
          <option value="low">低</option>
          <option value="medium">中</option>
          <option value="high">高</option>
          <option value="max">最高（max）</option>
        </select>
      </div>
      <div id="aiModelList" style="max-height:220px;overflow-y:auto;margin-bottom:12px"></div>
      <div style="border-top:1px solid var(--border);padding-top:12px">
        <div style="font-size:13px;font-weight:700;margin-bottom:8px" id="aiFormTitle">新增模型</div>
        <div class="row" style="gap:8px;flex-wrap:wrap;margin-bottom:8px">
          <div style="flex:1;min-width:140px">
            <label class="muted" style="font-size:11px;display:block;margin-bottom:2px">名称</label>
            <input id="aiName" placeholder="如 DeepSeek" style="width:100%;box-sizing:border-box">
          </div>
          <div style="flex:2;min-width:220px">
            <label class="muted" style="font-size:11px;display:block;margin-bottom:2px">Base URL（OpenAI 兼容）</label>
            <input id="aiBaseUrl" placeholder="https://api.deepseek.com/v1" style="width:100%;box-sizing:border-box">
          </div>
        </div>
        <div class="row" style="gap:8px;flex-wrap:wrap;margin-bottom:10px">
          <div style="flex:2;min-width:200px">
            <label class="muted" style="font-size:11px;display:block;margin-bottom:2px">API Key</label>
            <input id="aiApiKey" type="password" placeholder="sk-..." style="width:100%;box-sizing:border-box">
          </div>
          <div style="flex:1;min-width:200px">
            <label class="muted" style="font-size:11px;display:block;margin-bottom:2px">模型（可自动获取）</label>
            <div class="row" style="gap:6px">
              <select id="aiModel" style="flex:1"><option value="">— 获取模型列表 —</option></select>
              <button class="btn" id="aiFetch" title="用上方 Base URL + API Key 拉取可用模型">获取</button>
            </div>
          </div>
        </div>
        <div class="modal-actions">
          <span class="link muted" id="aiCancel">关闭</span>
          <span class="grow" style="flex:1"></span>
          <button class="btn" id="aiNew">新增</button>
          <button class="btn primary" id="aiSave">保存模型</button>
        </div>
      </div>
    </div>`;
  document.body.appendChild(mask);
  const close = () => mask.remove();
  mask.querySelector('#aiCancel').onclick = close;
  mask.addEventListener('click', (e) => { if (e.target === mask) close(); });

  // 思考级别：读当前值并保存（默认 high，用户可在弹窗自行选到 max）
  const reasoningSel = mask.querySelector('#aiReasoning');
  api('/ai/reasoning', { silent: true })
    .then((d) => { reasoningSel.value = d.effort || 'high'; })
    .catch(() => { reasoningSel.value = 'high'; });
  reasoningSel.onchange = async () => {
    try {
      await api('/ai/reasoning', { method: 'PUT', body: { effort: reasoningSel.value } });
      toast('思考级别已设为 ' + (reasoningSel.selectedOptions[0] ? reasoningSel.selectedOptions[0].textContent : reasoningSel.value));
    } catch (e) {
      toast('保存失败：' + e.message, 4000);
    }
  };

  mask.querySelector('#aiNew').onclick = () => {
    _aiEditingId = null;
    ['aiName', 'aiBaseUrl', 'aiApiKey'].forEach((id) => (mask.querySelector('#' + id).value = ''));
    mask.querySelector('#aiModel').innerHTML = '<option value="">— 获取模型列表 —</option>';
    mask.querySelector('#aiFormTitle').textContent = '新增模型';
  };

  mask.querySelector('#aiFetch').onclick = async () => {
    const baseUrl = mask.querySelector('#aiBaseUrl').value.trim();
    const apiKey = mask.querySelector('#aiApiKey').value.trim();
    if (!baseUrl || !apiKey) return toast('请先填 Base URL 和 API Key');
    const sel = mask.querySelector('#aiModel');
    sel.innerHTML = '<option value="">加载中…</option>';
    try {
      const data = await api('/ai/models/available', { method: 'POST', body: { base_url: baseUrl, api_key: apiKey } });
      const models = data.models || [];
      if (!models.length) { sel.innerHTML = '<option value="">无可用模型</option>'; return toast('未获取到模型'); }
      sel.innerHTML = '<option value="">选择模型…</option>' + models.map((m) => `<option value="${m}">${m}</option>`).join('');
      toast(`获取到 ${models.length} 个模型`);
    } catch (e) {
      sel.innerHTML = '<option value="">— 获取模型列表 —</option>';
      toast('获取失败：' + e.message, 4000);
    }
  };

  mask.querySelector('#aiSave').onclick = async () => {
    const body = {
      name: mask.querySelector('#aiName').value.trim(),
      base_url: mask.querySelector('#aiBaseUrl').value.trim(),
      api_key: mask.querySelector('#aiApiKey').value.trim(),
      model: mask.querySelector('#aiModel').value.trim(),
    };
    if (_aiEditingId) body.id = _aiEditingId;
    if (!body.name || !body.base_url || !body.api_key || !body.model) return toast('名称 / Base URL / API Key / 模型 均必填');
    try {
      await api('/ai/models', { method: 'POST', body });
      toast('已保存模型');
      await loadAiModels(mask);
    } catch (e) { toast('保存失败：' + e.message, 4000); }
  };
  await loadAiModels(mask);
}

async function loadAiModels(mask) {
  const wrap = mask.querySelector('#aiModelList');
  const activeEl = mask.querySelector('#aiActive');
  try {
    const data = await api('/ai/models', { silent: true });
    const active = data.active;
    activeEl.innerHTML = active
      ? `当前使用：<strong>${active.name}</strong>（${active.model}）`
      : '<span style="color:#e03131">尚未启用任何模型，请新增并「启用」。</span>';
    if (!data.models.length) {
      wrap.innerHTML = '<div class="muted" style="font-size:12px;padding:8px 4px">尚未配置模型，填下方表单并保存。</div>';
      return;
    }
    wrap.innerHTML = '';
    data.models.forEach((m) => {
      const card = document.createElement('div');
      card.style.cssText = 'border:1px solid var(--border);border-radius:8px;padding:9px 12px;margin-bottom:8px;display:flex;align-items:center;gap:10px;font-size:13px';
      card.innerHTML = `
        <div style="flex:1;min-width:0">
          <div><strong>${m.name}</strong> ${m.is_active ? '<span class="badge A">当前</span>' : ''}
            <span class="muted" style="font-size:11px;margin-left:6px">${m.model}</span></div>
          <div class="muted" style="font-size:11px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap">${m.base_url}</div>
        </div>
        <button class="btn" ${m.is_active ? 'disabled' : ''} data-act="${m.id}">启用</button>
        <button class="btn" data-edit="${m.id}">编辑</button>
        <button class="btn danger" data-del="${m.id}">删除</button>`;
      card.querySelector('[data-act]').onclick = async () => {
        try { await api('/ai/models/' + m.id + '/activate', { method: 'POST' }); toast('已切换到 ' + m.name); await loadAiModels(mask); }
        catch (e) { toast('切换失败：' + e.message, 4000); }
      };
      card.querySelector('[data-edit]').onclick = () => {
        _aiEditingId = m.id;
        mask.querySelector('#aiFormTitle').textContent = '编辑模型：' + m.name;
        mask.querySelector('#aiName').value = m.name;
        mask.querySelector('#aiBaseUrl').value = m.base_url;
        mask.querySelector('#aiApiKey').value = m.api_key;
        const sel = mask.querySelector('#aiModel');
        sel.innerHTML = `<option value="${m.model}">${m.model}</option>`;
      };
      card.querySelector('[data-del]').onclick = async () => {
        if (!confirm('删除模型 ' + m.name + '？')) return;
        try { await api('/ai/models/' + m.id, { method: 'DELETE' }); await loadAiModels(mask); }
        catch (e) { toast('删除失败：' + e.message, 4000); }
      };
      wrap.appendChild(card);
    });
  } catch (e) {
    wrap.innerHTML = '<div class="muted" style="font-size:12px">加载失败：' + e.message + '</div>';
  }
}


// ============ AI 评分共享渲染助手 + 标签偏好弹窗 ============

// HTML 转义（AI 文本/标签名进入 innerHTML 前必须转义，防破坏布局）
function esc(s) {
  return String(s ?? '').replace(/[&<>"']/g, (c) => (
    { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]
  ));
}

function aiNotConfiguredHtml() {
  return '<div class="empty" style="padding:16px;margin:0">未配置 AI（点右上角「🤖 AI」配置模型）</div>';
}

// AI 报告头部：大分 + 评级徽章 + 一句话结论
function aiScoreHeader(r) {
  return `
    <div style="display:flex;align-items:center;gap:12px;margin-bottom:10px;flex-wrap:wrap">
      <span style="font-size:42px;font-weight:700;line-height:1">${r.score == null ? '—' : fmtNum(r.score)}</span>
      <span class="badge ${r.rating || 'N'}">${r.rating || 'N/A'} ${esc(r.rating_name || '')}</span>
      ${r.summary ? `<div class="muted" style="font-size:13px;flex:1;min-width:200px">${esc(r.summary)}</div>` : ''}
    </div>`;
}

// AI 报告正文：HTML 报告入口 + 建议/风险/理由（详细分析在 HTML 报告里，不再内联展示）
function renderAiReportBlock(r) {
  const list = (title, arr) => (arr && arr.length) ? `
    <div style="margin:6px 0">
      <div style="font-size:12px;font-weight:700;color:var(--muted,#868e96)">${title}</div>
      <ul style="margin:4px 0 0 18px;font-size:13px">${arr.map((x) => `<li>${esc(x)}</li>`).join('')}</ul>
    </div>` : '';
  const htmlBtn = r.html ? aiHtmlButton() : '';
  return `
    ${htmlBtn}
    ${list('💡 建议', r.advice)}
    ${list('⚠️ 风险', r.risks)}
    ${list('核心理由', r.reasons)}`;
}

// AI 打分按钮（innerHTML 注入后由调用方 querySelector('[data-aiscore-btn]') 绑定 onclick）
function aiScoreButtonHtml(label) {
  return `<button class="btn primary" data-aiscore-btn>${label}</button>`;
}

// AI 生成的 HTML 详细报告：按钮 + 在新窗口打开（innerHTML 注入后由 wireAiHtmlButton 绑定）
function aiHtmlButton() {
  return `<button class="btn" data-ai-html title="AI 生成的完整 HTML 详细报告">📄 查看详细报告</button>`;
}

function openAiHtmlReport(html) {
  if (!html) return toast('该报告没有 HTML 版');
  const w = window.open('', '_blank');
  if (!w) return toast('浏览器拦截了弹窗，请允许本站弹窗', 3500);
  w.document.open();
  w.document.write(html);
  w.document.close();
}

function wireAiHtmlButton(container, report) {
  const b = container && container.querySelector('[data-ai-html]');
  if (b && report && report.html) b.onclick = () => openAiHtmlReport(report.html);
}

// AI 资金流分析：按钮 HTML（innerHTML 注入后由调用方用 runFundflowAi 绑定）
function fundflowAiButtonHtml(label = 'AI 分析资金流') {
  return `<button class="btn primary" data-fundflow-ai style="margin-bottom:8px">🤖 ${label}</button>`;
}

// 资金流资金×股价相关性 → 徽章/配色（红=坏 绿=好 灰=中性；divergence 兼容旧数据未知方向）
const FUNDFLOW_CORR = {
  positive: ['同涨', '#2f9e44'], negative: ['同跌', '#e03131'],
  bottom_divergence: ['底背离', '#2b8a3e'], top_divergence: ['顶背离', '#c92a2a'],
  divergence: ['背离', '#f08c00'], neutral: ['中性', '#8792a4'],
};

// 渲染资金流 AI 分析结果面板（简明快报，无 HTML）
function renderFundflowAiPanel(el, d) {
  const a = d.analysis || {};
  const corrMap = FUNDFLOW_CORR;
  const corr = corrMap[a.correlation] || corrMap.neutral;
  const winLabel = /d$/.test(d.window || '')
    ? parseInt(d.window, 10) + ' 天窗口' : parseInt(d.window, 10) + ' 分钟窗口';
  const divs = (a.divergence || []).filter((x) => x && typeof x === 'object')
    .map((x) => `<li><b>${esc(x.ts || '')}</b>：${esc(x.detail || '')}</li>`).join('');
  const alerts = (a.alerts || []).map((x) => `<li>${esc(x)}</li>`).join('');
  const rows = [
    ['💪 主力行为', a.main_force],
    ['🕐 全天节奏', a.rhythm],
  ].filter(([, v]) => v).map(([k, v]) =>
    `<div style="margin:7px 0"><b style="font-size:12.5px">${k}</b><div style="font-size:13px;margin-top:2px">${esc(v)}</div></div>`).join('');
  el.innerHTML = `
    <div style="border:1px solid var(--border);border-radius:var(--radius-sm);padding:10px 12px;background:#fff">
      <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
        <span class="badge" style="color:#fff;background:${corr[1]}">资金×股价：${corr[0]}</span>
        <span class="muted" style="font-size:12px">${winLabel} · ${d.points_count} 个点 · ${esc(d.date || '')}</span>
        ${a.html ? aiHtmlButton() : ''}
      </div>
      <div style="font-size:14px;font-weight:700;margin:8px 0 4px">${esc(a.summary || '')}</div>
      ${rows}
      ${divs ? `<div style="margin:7px 0"><b style="font-size:12.5px;color:#f08c00">⚠️ 背离</b><ul style="margin:4px 0 0 18px;font-size:13px">${divs}</ul></div>` : ''}
      ${alerts ? `<div style="margin:7px 0"><b style="font-size:12.5px;color:var(--red)">🔔 注意</b><ul style="margin:4px 0 0 18px;font-size:13px">${alerts}</ul></div>` : ''}
      ${a.conclusion ? `<div style="margin:8px 0 0;padding:8px 10px;border-radius:var(--radius-xs);background:var(--primary-weak);font-size:13px"><b>结论</b>：${esc(a.conclusion)}</div>` : ''}
    </div>`;
  wireAiHtmlButton(el, a);   // 📄 按钮（深入模式 html 非空时绑定）
}

// 渲染个股页最近落库资金流 AI 结果（标注来源 组合批量/个股分析 + 日期 + 窗口）
function renderFundflowPersistedPanel(el, r) {
  const a = r || {};
  const src = a.source === 'batch' ? '组合批量分析' : a.source === 'single' ? '个股分析' : '';
  const winLabel = /d$/.test(a.window || '')
    ? parseInt(a.window, 10) + ' 天' : parseInt(a.window, 10) + ' 分钟';
  const divs = (a.divergence || []).filter((x) => x && typeof x === 'object')
    .map((x) => `<li><b>${esc(x.ts || '')}</b>：${esc(x.detail || '')}</li>`).join('');
  const alerts = (a.alerts || []).map((x) => `<li>${esc(x)}</li>`).join('');
  const rows = [
    ['💪 主力行为', a.main_force],
    ['🕐 全天节奏', a.rhythm],
  ].filter(([, v]) => v).map(([k, v]) =>
    `<div style="margin:7px 0"><b style="font-size:12.5px">${k}</b><div style="font-size:13px;margin-top:2px">${esc(v)}</div></div>`).join('');
  el.innerHTML = `
    <div style="border:1px solid var(--border);border-radius:var(--radius-sm);padding:10px 12px;background:#fff">
      <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
        ${fundflowCorrBadge(a)}
        <span class="muted" style="font-size:12px">${src} · ${a.trade_date || ''} · ${winLabel}</span>
        ${a.html ? aiHtmlButton() : ''}
      </div>
      <div style="font-size:14px;font-weight:700;margin:8px 0 4px">${esc(a.summary || '')}</div>
      ${rows}
      ${divs ? `<div style="margin:7px 0"><b style="font-size:12.5px;color:#f08c00">⚠️ 背离</b><ul style="margin:4px 0 0 18px;font-size:13px">${divs}</ul></div>` : ''}
      ${alerts ? `<div style="margin:7px 0"><b style="font-size:12.5px;color:var(--red)">🔔 注意</b><ul style="margin:4px 0 0 18px;font-size:13px">${alerts}</ul></div>` : ''}
      ${a.conclusion ? `<div style="margin:8px 0 0;padding:8px 10px;border-radius:var(--radius-xs);background:var(--primary-weak);font-size:13px"><b>结论</b>：${esc(a.conclusion)}</div>` : ''}
    </div>`;
  wireAiHtmlButton(el, a);   // 📄 按钮（深入模式 html 非空时绑定）
}

// 分析前先动态刷新资金流/价格（个股刷该股 flow+price；组合刷全部持仓 flow+price），
// 保证 AI 分析基于新鲜缓存而非旧数据。失败不阻断分析（退化为缓存数据）。
async function refreshFlowBeforeAnalysis(code) {
  try {
    const body = { items: ['flow', 'price'] };
    if (code) await api('/stocks/' + code + '/refresh', { method: 'POST', body, silent: true });
    else await api('/refresh', { method: 'POST', body, silent: true });
  } catch (e) { /* 刷新失败不阻断，分析用缓存数据 */ }
}

// ============ AI 指令预览/编辑弹窗（5 个分析入口共用） ============
let _promptsCache = null;
// 各 AI 分析入口默认 system prompt（后端单一来源，前端只读缓存）
async function getDefaultPrompts() {
  if (_promptsCache) return _promptsCache;
  try { const d = await api('/ai/prompts', { silent: true }); _promptsCache = d || {}; }
  catch (e) { _promptsCache = {}; }
  return _promptsCache;
}

// 弹窗展示某入口的默认指令，用户可编辑/恢复默认/取消 + 选分析强度（快速/普通/深入）。
// 确定后 onConfirm(customPrompt|null, intensity)：内容与默认一致 → null（后端用默认）；
// 修改 → 自定义文本。数据点不展示。
async function openPromptEditor(kind, onConfirm) {
  const prompts = await getDefaultPrompts();
  const def = prompts[kind] || '';
  const mask = document.createElement('div');
  mask.className = 'modal-mask';
  mask.innerHTML = `
    <div class="modal" style="width:680px;max-width:94vw">
      <h3>🤖 AI 要求</h3>
      <div class="muted" style="font-size:12px;margin-bottom:8px">以下是要发给 AI 的重点要求，可修改后确认；系统完整指令与分析数据会自动附加，不在弹窗展示。</div>
      <textarea rows="6" spellcheck="false" style="width:100%;box-sizing:border-box;font-family:inherit;font-size:13px;line-height:1.7;padding:8px">${esc(def)}</textarea>
      <div style="display:flex;align-items:center;gap:4px;margin:8px 0 4px;font-size:13px">
        <span class="muted" style="font-size:12px;margin-right:6px">分析强度</span>
        <label><input type="radio" name="peIntensity" value="fast"> 快速</label>
        <label style="margin-left:12px"><input type="radio" name="peIntensity" value="normal" checked> 普通</label>
        <label style="margin-left:12px"><input type="radio" name="peIntensity" value="deep"> 深入</label>
      </div>
      <div class="modal-actions">
        <span class="link muted" id="peCancel">取消</span>
        <span class="grow" style="flex:1"></span>
        <button class="btn" id="peReset" title="恢复为系统默认指令">恢复默认</button>
        <button class="btn primary" id="peOk">确定并发送</button>
      </div>
    </div>`;
  document.body.appendChild(mask);
  const ta = mask.querySelector('textarea');
  const close = () => mask.remove();
  mask.querySelector('#peCancel').onclick = close;
  mask.addEventListener('click', (e) => { if (e.target === mask) close(); });
  mask.querySelector('#peReset').onclick = () => { ta.value = def; };
  mask.querySelector('#peOk').onclick = () => {
    const v = ta.value.trim();
    const inten = mask.querySelector('input[name="peIntensity"]:checked').value;
    close();
    onConfirm(v && v !== def ? v : null, inten);
  };
}

// 触发个股资金流 AI 分析并把结果渲染进 panel（先弹窗确认/编辑指令）
async function runFundflowAi({ code, window, btn, panel }) {
  openPromptEditor('fundflow', async (systemPrompt, intensity) => {
    btn.disabled = true;
    panel.style.display = 'block';
    panel.innerHTML = '<div class="empty" style="padding:10px;margin:0">🔄 刷新资金流数据…</div>';
    try {
      await refreshFlowBeforeAnalysis(code);
      panel.innerHTML = '<div class="empty" style="padding:10px;margin:0">🤖 AI 分析中…</div>';
      const body = { code: code || '', window: window || '15m', intensity };
      if (systemPrompt) body.system_prompt = systemPrompt;
      const d = await api('/ai/fundflow-analysis', { method: 'POST', body });
      renderFundflowAiPanel(panel, d);
    } catch (e) {
      panel.innerHTML = `<div class="empty" style="padding:10px;margin:0">分析失败：${esc(e.message)}</div>`;
    } finally {
      btn.disabled = false;
    }
  });
}

// 资金面徽章：r = {correlation,summary,trade_date,window,source}；无结果 → '—'
function fundflowCorrBadge(r) {
  if (!r || !r.correlation) return '<span class="muted">—</span>';
  const m = FUNDFLOW_CORR;
  const [label, color] = m[r.correlation] || m.neutral;
  const src = r.source === 'batch' ? '组合批量' : r.source === 'single' ? '个股分析' : '';
  const t = `${r.summary || ''} · ${r.trade_date || ''} · ${src} ${r.window || ''}`;
  return `<span class="badge" style="color:#fff;background:${color};cursor:default" title="${esc(t)}">${label}</span>`;
}

// 一次拉取多只最近落库资金面报告 map：{code:{correlation,summary,trade_date,window,source}}
async function loadFlowReports(codes) {
  if (!codes || !codes.length) return {};
  try {
    return (await api('/ai/fundflow-reports?codes=' + encodeURIComponent(codes.join(',')), { silent: true })) || {};
  } catch (e) {
    return {};
  }
}

// 读取最近一次组合级资金相关性报告（F5 后批量面板顶部「组合相关性」块重建用）。
// scope='indices'|'portfolio'；scopeKey 精确匹配（可空→按 scope 取最近）；window 精确匹配（可空→跨窗最近）
async function loadCoherence(scope, scopeKey, window) {
  try {
    const params = new URLSearchParams({ scope });
    if (scopeKey) params.set('scope_key', scopeKey);
    if (window) params.set('window', window);
    const r = await api('/ai/fundflow-coherence?' + params.toString(), { silent: true });
    return r || null;
  } catch (e) {
    return null;
  }
}

// 渲染组合批量分析结果面板：顶部「组合相关性」块 + 每只一行 代码 名称 [相关性徽章] 结论
// d.coherence = {correlation,summary,points[],conclusion}；d.mode = 'indices'|'portfolio'
function renderFundflowBatchPanel(el, d) {
  const winLabel = /d$/.test(d.window || '')
    ? parseInt(d.window, 10) + ' 天' : parseInt(d.window, 10) + ' 分钟';
  const title = d.mode === 'indices' ? '批量分析指数' : '批量分析持仓';
  const coh = d.coherence;
  const cohHtml = coh && coh.correlation
    ? (() => {
        const [label, color] = FUNDFLOW_CORR[coh.correlation] || FUNDFLOW_CORR.neutral;
        const points = (coh.points || []).map((x) => `<li style="margin:2px 0">${esc(x)}</li>`).join('');
        return `
        <div style="background:rgba(37,99,235,.04);border:1px solid #b9cdff;border-radius:8px;padding:8px 10px;margin-bottom:8px">
          <div style="display:flex;align-items:center;gap:8px;font-size:13px;font-weight:700;flex-wrap:wrap">
            <span>组合相关性</span>
            <span class="badge" style="color:#fff;background:${color}">${label}</span>
            ${coh.summary ? `<span style="flex:1;font-size:12px;font-weight:400;color:var(--muted,#868e96);min-width:160px">${esc(coh.summary)}</span>` : ''}
          </div>
          ${points ? `<ul style="margin:6px 0 0 18px;font-size:12px">${points}</ul>` : ''}
          ${coh.conclusion ? `<div style="font-size:12px;margin-top:6px">${esc(coh.conclusion)}</div>` : ''}
        </div>`;
      })()
    : '';
  const rows = (d.reports || []).map((r) => `
    <div style="display:flex;align-items:center;gap:10px;padding:4px 0;border-bottom:1px dashed var(--border)">
      <a href="/static/stock.html?code=${r.code}${d.mode === 'indices' ? '&index=1' : ''}" style="white-space:nowrap;min-width:120px">${r.code} ${esc(r.name || '')}</a>
      ${fundflowCorrBadge({ correlation: r.correlation, summary: r.summary, trade_date: d.date, window: d.window, source: r.source })}
      <span style="flex:1;font-size:13px">${esc(r.summary || '')}</span>
    </div>`).join('');
  el.innerHTML = `
    <div style="border:1px solid var(--border);border-radius:var(--radius-sm);padding:10px 12px;background:#fff">
      ${cohHtml}
      <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
        <div style="font-size:13px;font-weight:700;flex:1;min-width:200px">${title}（${d.stocks_count || 0} 只 · ${winLabel}窗口）</div>
        ${d.html ? aiHtmlButton() : ''}
      </div>
      ${rows || '<div class="muted">无分析结果</div>'}
    </div>`;
  wireAiHtmlButton(el, d);   // 📄 按钮（深入模式批量 html 非空时绑定）
}

// 批量分析资金面：POST 批量端点 → 渲染面板 → onDone 刷新列表列（先弹窗确认/编辑指令）
// 持仓模式（tags）：先刷新持仓资金流；指数模式（codes/weights）：直接分析（资金流由全量刷新拉取）
async function runFundflowBatch({ tags, window, btn, panel, onDone, codes, weights }) {
  openPromptEditor('batch', async (systemPrompt, intensity) => {
    btn.disabled = true;
    panel.style.display = 'block';
    panel.innerHTML = '<div class="empty" style="padding:10px;margin:0">🔄 准备资金流数据…</div>';
    try {
      const body = { code: '', window: window || '15m', intensity };
      if (systemPrompt) body.system_prompt = systemPrompt;
      if (codes && codes.length) {
        body.codes = codes.join(',');
        if (weights && weights.length) body.weights = weights.join(',');
      } else {
        body.tags = tags || null;
        await refreshFlowBeforeAnalysis('');
      }
      panel.innerHTML = '<div class="empty" style="padding:10px;margin:0">🤖 批量 AI 分析中…</div>';
      const d = await api('/ai/fundflow-batch', { method: 'POST', body });
      renderFundflowBatchPanel(panel, d);
      if (typeof onDone === 'function') onDone();
    } catch (e) {
      panel.innerHTML = `<div class="empty" style="padding:10px;margin:0">批量分析失败：${esc(e.message)}</div>`;
    } finally {
      btn.disabled = false;
    }
  });
}

// 标签偏好弹窗：输入简短偏好 → 保存自动 AI 补全（draft）→ 确认后生效
async function openTagPrefModal(tag, onSaved) {
  const mask = document.createElement('div');
  mask.className = 'modal-mask';
  mask.innerHTML = `
    <div class="modal" style="width:580px;max-width:94vw">
      <h3>标签偏好：${esc(tag)}</h3>
      <div class="muted" style="font-size:12px;margin-bottom:10px">输入你的偏好（如「喜欢低估值高股息」）。保存时自动请求 AI 补全成完整评分指引并展示，<strong>确认后才用于该标签的打分</strong>。</div>
      <label class="muted" style="font-size:11px;display:block;margin-bottom:4px">你的偏好（简短描述）</label>
      <textarea id="prefRaw" rows="2" style="width:100%;box-sizing:border-box" placeholder="如：喜欢低估值、高股息、现金流稳定的公司"></textarea>
      <label class="muted" style="font-size:11px;display:block;margin:10px 0 4px">AI 补全的评分指引（可编辑）</label>
      <textarea id="prefPrompt" rows="6" style="width:100%;box-sizing:border-box" placeholder="点「🤖 AI 补全」生成，或手动填写"></textarea>
      <div id="prefHint" class="muted" style="font-size:12px;margin-top:6px"></div>
      <div class="modal-actions">
        <button class="btn" id="prefCancel">取消</button>
        <button class="btn" id="prefExpand">🤖 AI 补全</button>
        <span class="grow" style="flex:1"></span>
        <button class="btn primary" id="prefSave">保存并生效</button>
      </div>
    </div>`;
  document.body.appendChild(mask);
  const close = () => mask.remove();
  mask.querySelector('#prefCancel').onclick = close;
  mask.addEventListener('click', (e) => { if (e.target === mask) close(); });
  const enc = encodeURIComponent(tag);

  // 预填已有偏好
  try {
    const p = await api('/ai-scoring/prefs/' + enc, { silent: true });
    if (p) {
      mask.querySelector('#prefRaw').value = p.raw_pref || '';
      mask.querySelector('#prefPrompt').value = p.prompt || '';
      mask.querySelector('#prefHint').textContent = p.status === 'confirmed'
        ? '当前为【已确认】状态，已用于打分。修改后需重新确认。'
        : '当前为【待确认】状态，确认后才用于打分。';
    }
  } catch (e) { /* 无则空 */ }

  mask.querySelector('#prefExpand').onclick = async () => {
    const raw = mask.querySelector('#prefRaw').value.trim();
    if (!raw) return toast('请先输入偏好描述');
    const btn = mask.querySelector('#prefExpand');
    btn.disabled = true;
    try {
      const d = await api('/ai-scoring/prefs/' + enc + '/expand', { method: 'POST', body: { raw_pref: raw } });
      mask.querySelector('#prefPrompt').value = d.prompt || '';
      mask.querySelector('#prefHint').textContent = '已由 AI 补全（待确认），点击「保存并生效」后用于打分。';
      toast('AI 已补全评分指引');
    } catch (e) {
      toast('补全失败：' + e.message, 4000);
    } finally {
      btn.disabled = false;
    }
  };

  mask.querySelector('#prefSave').onclick = async () => {
    const raw = mask.querySelector('#prefRaw').value.trim();
    if (!raw) return toast('请填写偏好描述');
    const prompt = mask.querySelector('#prefPrompt').value.trim();
    const btn = mask.querySelector('#prefSave');
    btn.disabled = true;
    try {
      if (btn.dataset.confirm === '1') {
        await api('/ai-scoring/prefs/' + enc + '/confirm', { method: 'POST' });
        toast('已确认生效');
        close();
        if (typeof onSaved === 'function') onSaved();
        return;
      }
      const d = await api('/ai-scoring/prefs/' + enc, {
        method: 'PUT',
        body: prompt ? { raw_pref: raw, prompt } : { raw_pref: raw, auto_expand: true },
      });
      if (d.status === 'draft' && d.prompt) {
        // 自动补全成草稿：展示并引导确认
        mask.querySelector('#prefPrompt').value = d.prompt;
        mask.querySelector('#prefHint').textContent = '已保存为【待确认】。AI 已补全指引，点击「确认生效」后用于打分。';
        btn.textContent = '确认生效';
        btn.dataset.confirm = '1';
        return;
      }
      if (d.status === 'draft') {
        mask.querySelector('#prefHint').textContent = '已保存为【待确认】，请 AI 补全或手动填写评分指引。';
        return;
      }
      toast('已保存并生效');
      close();
      if (typeof onSaved === 'function') onSaved();
    } catch (e) {
      toast('保存失败：' + e.message, 4000);
    } finally {
      btn.disabled = false;
    }
  };
}

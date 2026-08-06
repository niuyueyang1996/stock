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
// 综合评分重建不在刷新项里：由评分页独立触发（/scoring/rebuild）
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
          o.textContent = `${r.code} ${r.name}` + (r.market === 'hk' ? '（港股）' : '');
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

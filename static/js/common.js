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
}

// 刷新内容项（与后端 DYNAMIC_ITEMS / FULL_ITEMS / STOCK_*_ITEMS 对应）
// 综合评分重建不在刷新项里：由评分页独立触发（/scoring/rebuild）
const REFRESH_OPTIONS = {
  dynamic: [
    ['price', '实时价格（分钟级）'],
    ['valuation', '当前估值（PE/PB/股息率/市值）'],
  ],
  full: [
    ['bars', '日K历史（全量重拉覆盖）'],
    ['financials', '财务数据（净利/净资产/EPS/支付率）'],
    ['valuation', '估值分位（百度序列+1y/3y/5y分位+实时估值）'],
    ['portfolio', '组合综合序列重算'],
  ],
  stock_dynamic: [
    ['price', '实时价格（分钟级）'],
    ['valuation', '当前估值（PE/PB/股息率/市值）'],
  ],
  stock_full: [
    ['bars', '日K历史（全量重拉覆盖）'],
    ['financials', '财务数据（净利/净资产/EPS/支付率）'],
    ['valuation', '估值分位（百度序列+1y/3y/5y分位+实时估值）'],
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
    const r = await api(path, { method: 'POST', body: JSON.stringify({ items }) });
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
        const rows = await api('/stocks/search?q=' + encodeURIComponent(q) + '&limit=10');
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

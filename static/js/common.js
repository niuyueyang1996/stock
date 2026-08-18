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

function toast(msg, ms = 2500, type = '') {
  let el = document.getElementById('toast');
  if (!el) {
    el = document.createElement('div');
    el.id = 'toast';
    document.body.appendChild(el);
  }
  el.textContent = msg;
  el.classList.remove('ok', 'err');
  if (type) el.classList.add(type);
  el.classList.add('show');
  clearTimeout(toast._t);
  toast._t = setTimeout(() => el.classList.remove('show'), ms);
}

// 页面数据重载防抖：ws 推送/任务完成/设置变化可能一瞬触发多次，合并成一次重载，避免接口刷屏
function schedulePageReload(delay = 400) {
  clearTimeout(schedulePageReload._t);
  schedulePageReload._t = setTimeout(() => {
    schedulePageReload._t = null;
    if (typeof loadPage === 'function') loadPage();
  }, delay);
}

/* ---- ws 假死检测（顶层：setupJobBar 内定义曾被 waitForJob 引用而不可见，导致
 * "wsStale is not defined"——提到模块作用域共享）---- */
let _wsLastMsgAt = 0;
function wsStale() {
  return window._ws && window._ws.readyState === 1 && _wsLastMsgAt > 0 &&
    Date.now() - _wsLastMsgAt > 30000;
}

/** 可折叠 panel：标题常显，body 可收起；摘要写在标题旁。不删功能，只改展示密度。 */
function _foldKey(id) {
  return 'fold:' + (location.pathname || '') + ':' + id;
}

function makeFoldable(panel, opts = {}) {
  if (!panel || panel.dataset.foldable === '1') return panel;
  const id = opts.id || panel.id || ('p' + Math.random().toString(36).slice(2, 8));
  panel.dataset.foldable = '1';
  panel.dataset.foldId = id;
  panel.classList.add('foldable');

  const h2 = panel.querySelector(':scope > h2');
  const headRow = panel.querySelector(':scope > .row');
  const head = document.createElement('div');
  head.className = 'panel-head';
  const summary = document.createElement('span');
  summary.className = 'panel-summary';
  const chev = document.createElement('span');
  chev.className = 'fold-chevron';
  chev.textContent = '▼';

  const body = document.createElement('div');
  body.className = 'panel-body';

  if (h2 && (!headRow || !headRow.contains(h2))) {
    // 典型结构：panel > h2 + 内容
    const kids = [...panel.childNodes];
    kids.forEach((n) => {
      if (n === h2) head.appendChild(n);
      else body.appendChild(n);
    });
  } else if (headRow && headRow.querySelector('h2')) {
    // panel > .row(h2 + 控件) + 内容：标题进 head，控件留 body
    const h = headRow.querySelector('h2');
    head.appendChild(h);
    const kids = [...panel.childNodes];
    kids.forEach((n) => body.appendChild(n));
  } else {
    const title = document.createElement('h2');
    title.textContent = opts.title || '详情';
    head.appendChild(title);
    while (panel.firstChild) body.appendChild(panel.firstChild);
  }

  // 长副标题下沉到 body（保留原 id，避免后续脚本 getElementById 丢引用）
  const sub = head.querySelector('h2 .sub');
  if (sub) {
    const note = document.createElement('div');
    note.className = 'sub-note';
    if (sub.id) note.id = sub.id;
    note.textContent = sub.textContent;
    sub.remove();
    body.insertBefore(note, body.firstChild);
  }

  head.appendChild(summary);
  head.appendChild(chev);
  panel.appendChild(head);
  panel.appendChild(body);

  let open = opts.defaultOpen === true;
  try {
    const saved = localStorage.getItem(_foldKey(id));
    if (saved === '1') open = true;
    if (saved === '0') open = false;
  } catch (e) { /* ignore */ }
  panel.classList.toggle('folded', !open);

  function resizeCharts() {
    if (typeof echarts === 'undefined') return;
    panel.querySelectorAll('.chart').forEach((el) => {
      try {
        const inst = echarts.getInstanceByDom(el);
        if (inst) inst.resize();
      } catch (e) { /* ignore */ }
    });
  }

  head.addEventListener('click', (e) => {
    if (e.target.closest('button, a, input, select, label')) return;
    const willOpen = panel.classList.contains('folded');
    panel.classList.toggle('folded', !willOpen);
    try { localStorage.setItem(_foldKey(id), willOpen ? '1' : '0'); } catch (err) { /* ignore */ }
    if (willOpen) {
      requestAnimationFrame(() => {
        resizeCharts();
        window.dispatchEvent(new CustomEvent('panel-unfold', { detail: { id, panel } }));
      });
    }
    if (typeof opts.onToggle === 'function') opts.onToggle(willOpen);
  });
  return panel;
}

/** 按 URL hash 展开对应 foldable 面板并滚入视野（组合批量列表跳 #newsAiPanel/#techAiPanel 用）。 */
function openFoldableFromHash() {
  const id = (location.hash || '').replace(/^#/, '');
  if (!id) return;
  const panel = document.getElementById(id);
  if (!panel || !panel.classList.contains('foldable')) return;
  panel.classList.remove('folded');
  try { localStorage.setItem(_foldKey(panel.dataset.foldId || id), '1'); } catch (e) { /* ignore */ }
  requestAnimationFrame(() => {
    try { panel.scrollIntoView({ behavior: 'smooth', block: 'start' }); } catch (e) { /* ignore */ }
  });
}

function setPanelSummary(panelOrId, html) {
  const panel = typeof panelOrId === 'string'
    ? document.getElementById(panelOrId)
    : panelOrId;
  if (!panel) return;
  const el = panel.querySelector('.panel-summary');
  if (el) el.innerHTML = html == null ? '' : html;
}

function isPanelOpen(panelOrId) {
  const panel = typeof panelOrId === 'string'
    ? document.getElementById(panelOrId)
    : panelOrId;
  return !!(panel && !panel.classList.contains('folded'));
}

// 折叠摘要色标：标签灰 / 数字墨色 / 涨跌红绿 / 分位高低 / 评级色块（与 badge 同相）
function sumL(label) { return `<span class="muted">${esc(String(label))}</span>`; }
function sumV(text, tone) { return `<span class="${tone || 'hi'}">${text}</span>`; }
function sumSep() { return `<span class="sep"> · </span>`; }
function sumChip(text) { return `<span class="chip">${esc(String(text))}</span>`; }
function sumKV(label, text, tone) { return `${sumL(label)} ${sumV(text, tone)}`; }
function sumSigned(n, text) {
  const tone = (n > 0 ? 'up' : (n < 0 ? 'down' : 'hi'));
  return sumV(text != null ? text : signed(n), tone);
}
/** 估值分位色：≤25 低估(绿) · ≥75 高估(红) · 中间墨色 */
function sumQ(pct) {
  if (pct == null || pct === '' || Number.isNaN(+pct)) return sumV('—', 'muted');
  const v = +pct;
  const tone = v <= 25 ? 'down' : (v >= 75 ? 'up' : 'hi');
  return sumV((Math.round(v * 10) / 10) + '%', tone);
}
/** AI 评级色块 A/B/C/D（折叠摘要专用，展开态仍用 .badge）；接受 grade|rating */
function sumGrade(rating) {
  const raw = (rating == null ? '' : String(rating)).trim().toUpperCase();
  const g = 'ABCD'.includes(raw.charAt(0)) ? raw.charAt(0) : 'N';
  return `<span class="grade ${g}">${g}</span>`;
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
    ['/static/help.html', 'help', '帮助'],
  ];
  nav.innerHTML = '';
  // 页面主链接：桌面直接排进导航条；手机端（≤700px）变成底部 Tab 栏（见 style.css）
  const linksWrap = document.createElement('div');
  linksWrap.className = 'nav-links';
  // antd-mobile TabBar 结构：图标（.nav-ic）+ 文字（.nav-tx）；桌面端隐藏图标
  const NAV_ICONS = { index: '📊', indices: '📈', portfolio: '💼', stock: '🔍', trade: '📝', help: '❓' };
  links.forEach(([href, page, label]) => {
    const a = document.createElement('a');
    a.href = href;
    a.innerHTML = `<span class="nav-ic">${NAV_ICONS[page] || ''}</span><span class="nav-tx">${label}</span>`;
    if (page === active) a.classList.add('active');
    // 帮助链接：手机端从底部 Tab 栏挪到顶部「设置」旁（.nav-help 手机端隐藏，见 style.css）
    if (page === 'help') a.classList.add('nav-help');
    linksWrap.appendChild(a);
  });
  nav.appendChild(linksWrap);

  const spacer = document.createElement('span');
  spacer.className = 'spacer';
  nav.appendChild(spacer);

  // 全局操作（AI/设置/刷新/自定义/状态）：桌面跟在导航中间；手机端收进顶部工具栏
  const actions = document.createElement('div');
  actions.className = 'nav-actions';

  const aiBtn = document.createElement('button');
  aiBtn.textContent = '🤖 AI';
  aiBtn.title = '配置 AI（推荐一键填入 DeepSeek）';
  aiBtn.onclick = openAiSettings;
  actions.appendChild(aiBtn);

  const settingsBtn = document.createElement('button');
  settingsBtn.textContent = '⚙ 设置';
  settingsBtn.title = '界面模式 / 刷新设置（简单·高级）';
  settingsBtn.onclick = openSettings;
  actions.appendChild(settingsBtn);

  // 帮助按钮：手机端显示在「设置」旁（桌面端隐藏，桌面帮助仍在导航链接里）
  const helpBtn = document.createElement('button');
  helpBtn.className = 'nav-help-btn';
  helpBtn.textContent = '❓ 帮助';
  helpBtn.title = '使用帮助';
  helpBtn.onclick = () => { location.href = '/static/help.html'; };
  actions.appendChild(helpBtn);

  const dynamic = document.createElement('button');
  dynamic.className = 'nav-refresh-btn';
  dynamic.textContent = '⚡ 更新行情';
  dynamic.title = '快速更新：现价、估值、今日资金流（大约几十秒）';
  dynamic.onclick = () => doRefresh(dynamic, false);
  actions.appendChild(dynamic);

  const full = document.createElement('button');
  full.className = 'primary nav-refresh-btn';
  full.textContent = '🔄 完整更新';
  full.title = '完整更新：历史行情、财务、估值分位、汇率等（首次或隔很久建议点一次）';
  full.onclick = () => doRefresh(full, true);
  actions.appendChild(full);

  const custom = document.createElement('span');
  custom.id = 'navCustomRefresh';
  custom.className = 'link muted nav-refresh-btn';
  custom.textContent = '自定义';
  custom.title = '选择要更新的内容';
  custom.style.cssText = 'font-size:12px;cursor:pointer;margin-left:6px;user-select:none';
  custom.onclick = () => openCustomRefresh('global');
  actions.appendChild(custom);

  const status = document.createElement('span');
  status.id = 'statusText';
  actions.appendChild(status);

  nav.appendChild(actions);

  setupPrewarmBar(nav);
  applyNavMode();
  refreshAppSettings().then(applyNavMode);
}

// 全局界面设置（简单/高级模式、静态节流、动态间隔）；页面渲染据此分流
window.APP_SETTINGS = { mode: 'simple', static_ttl_minutes: 60, dynamic_interval_seconds: 180 };

async function refreshAppSettings() {
  try {
    const d = await api('/settings/refresh', { silent: true });
    if (d) window.APP_SETTINGS = Object.assign({}, window.APP_SETTINGS, d);
  } catch (e) { /* 读设置失败保持默认 */ }
  window.dispatchEvent(new CustomEvent('app-settings', { detail: window.APP_SETTINGS }));
  return window.APP_SETTINGS;
}

// 简单模式：隐藏刷新按钮（一切自动）；高级模式：显示
function applyNavMode() {
  const mode = (window.APP_SETTINGS && window.APP_SETTINGS.mode) || 'simple';
  const show = mode === 'advanced';
  document.querySelectorAll('.nav-refresh-btn').forEach((b) => { b.style.display = show ? '' : 'none'; });
}

// ⚙ 设置弹窗：界面模式 + 静态节流 + 动态间隔
function openSettings() {
  const mask = document.createElement('div');
  mask.className = 'modal-mask';
  mask.innerHTML = `
    <div class="modal" style="width:540px;max-width:94vw">
      <h3>⚙ 设置</h3>
      <div style="margin-bottom:16px">
        <div style="font-size:13px;font-weight:700;margin-bottom:6px">界面模式</div>
        <label style="display:flex;gap:8px;align-items:center;margin-bottom:6px;cursor:pointer">
          <input type="radio" name="uiMode" value="simple"> 简单（一切自动，不显示刷新按钮，适合只想看的人）
        </label>
        <label style="display:flex;gap:8px;align-items:center;cursor:pointer">
          <input type="radio" name="uiMode" value="advanced"> 高级（显示刷新按钮和更多指标）
        </label>
      </div>
      <div style="margin-bottom:10px">
        <div style="font-size:13px;font-weight:700;margin-bottom:6px">刷新</div>
        <div class="row" style="align-items:center;gap:8px;flex-wrap:wrap">
          <span class="muted" style="font-size:12px">静态数据</span>
          <input type="number" id="setTtl" min="10" max="1440" step="10" style="width:80px" title="打开个股时，静态数据（日K/财务/估值分位）在此时间内不重复下载">
          <span class="muted" style="font-size:12px">分钟不重拉</span>
          <span class="muted" style="font-size:12px;margin-left:8px">动态自动刷新</span>
          <input type="number" id="setInterval" min="30" max="3600" step="10" style="width:80px" title="现价/资金流在交易时段按此间隔后台自动更新">
          <span class="muted" style="font-size:12px">秒</span>
        </div>
        <div class="muted" style="font-size:12px;margin-top:8px;line-height:1.6">
          现价/资金流会在交易时段自动更新，无需手动点刷新；静态数据在打开个股、启动程序、每天收盘后自动同步。
        </div>
      </div>
      <div class="modal-actions">
        <span class="link muted" id="setCancel">关闭</span>
        <span class="grow" style="flex:1"></span>
        <button class="btn primary" id="setSave">保存</button>
      </div>
    </div>`;
  document.body.appendChild(mask);
  const close = () => mask.remove();
  mask.querySelector('#setCancel').onclick = close;
  mask.addEventListener('click', (e) => { if (e.target === mask) close(); });

  const st = window.APP_SETTINGS || {};
  const mode = st.mode === 'advanced' ? 'advanced' : 'simple';
  mask.querySelector('input[name="uiMode"][value="' + mode + '"]').checked = true;
  mask.querySelector('#setTtl').value = st.static_ttl_minutes || 60;
  mask.querySelector('#setInterval').value = st.dynamic_interval_seconds || 300;

  mask.querySelector('#setSave').onclick = async () => {
    const modeV = mask.querySelector('input[name="uiMode"]:checked').value;
    const ttl = parseInt(mask.querySelector('#setTtl').value, 10);
    const interval = parseInt(mask.querySelector('#setInterval').value, 10);
    if (!ttl || !interval) return toast('请填写有效数值');
    const btn = mask.querySelector('#setSave');
    btn.disabled = true;
    try {
      await api('/settings/refresh', {
        method: 'PUT',
        body: { mode: modeV, static_ttl_minutes: ttl, dynamic_interval_seconds: interval },
      });
      await refreshAppSettings();
      applyNavMode();
      close();
      toast('设置已保存');
    } catch (e) {
      toast('保存失败：' + e.message, 4000);
    } finally {
      btn.disabled = false;
    }
  };
}

// 统一后台任务条：双车道队列 + 取消；轮询 /status/jobs。
let _jobBarActive = false;
let _jobBarSeenIds = new Set();
let _jobBarHideTimer = null;

function setupPrewarmBar(nav) {
  setupJobBar(nav);
}

function setupJobBar(nav) {
  if (!nav || document.getElementById('prewarmBar')) return;
  const bar = document.createElement('div');
  bar.id = 'prewarmBar';
  bar.className = 'prewarm-bar';
  bar.style.display = 'none';
  bar.innerHTML = `
    <div class="prewarm-row">
      <span id="prewarmText"></span>
      <span class="progress-track prewarm-track"><span class="progress-fill" id="prewarmFill"></span></span>
      <span class="prewarm-actions">
        <span id="prewarmPct" class="prewarm-pct"></span>
        <button type="button" class="job-toggle-btn" id="jobQueueToggle" style="display:none">任务</button>
        <button type="button" class="job-cancel-btn" id="jobCancelPrimary" title="取消" style="display:none">取消</button>
      </span>
    </div>
    <div id="jobQueueList" class="job-queue-list" style="display:none"></div>`;
  // 进度条融进导航中间（spacer 之后），占导航弹性空间，不挤动左右元素
  const spacer = nav.querySelector('.spacer');
  (spacer || nav).insertAdjacentElement('afterend', bar);
  const txt = bar.querySelector('#prewarmText');
  const pctEl = bar.querySelector('#prewarmPct');
  const fill = bar.querySelector('#prewarmFill');
  const qList = bar.querySelector('#jobQueueList');
  const cancelBtn = bar.querySelector('#jobCancelPrimary');
  const toggleBtn = bar.querySelector('#jobQueueToggle');
  let queueOpen = false;  // 列表默认收起，点「任务」才展开

  async function cancelJob(id, isBatch) {
    if (!id) return;
    try {
      await api(isBatch ? '/jobs/batch/' + id : '/jobs/' + id, {
        method: 'DELETE', silent: true,
      });
      toast('已取消');
      tick();
    } catch (e) {
      toast(e.message || '取消失败', 3000);
    }
  }
  cancelBtn.onclick = () => {
    const id = cancelBtn.dataset.id;
    const batch = cancelBtn.dataset.batch === '1';
    cancelJob(id, batch);
  };
  toggleBtn.onclick = () => {
    queueOpen = !queueOpen;
    if (window._lastJobsData) {
      // 直接用最近一份快照重绘列表（ws 推送已就绪；避免空白）
      renderQueueList(window._lastJobsData.jobs || []);
    } else if (queueOpen) {
      qList.style.display = 'block';
      tick();
    } else {
      qList.style.display = 'none';
    }
  };

  function fireDone(detail) {
    const id = detail.job_id;
    if (!id || _jobBarSeenIds.has(id + ':' + (detail.status || detail.ok))) return;
    _jobBarSeenIds.add(id + ':' + (detail.status || detail.ok));
    window.dispatchEvent(new CustomEvent('app-job-done', { detail }));
    const kind = detail.kind || '';
    const ok = detail.ok !== false && detail.status !== 'cancelled' && !detail.error;
    // 只在整个任务/批次收尾时重载页面（批次子任务逐个完成不触发，否则刷新过程中接口刷屏）
    const isChild = !!(detail.batch_id && detail.job_id !== detail.batch_id);
    if (ok && !isChild && (kind.startsWith('refresh') || kind.startsWith('index.refresh'))) {
      schedulePageReload();
    }
  }

  function renderQueueList(jobs) {
    const listItems = jobs
      .filter(j => j.status === 'running' || j.status === 'queued')
      .slice(0, 24);
    const runN = listItems.filter(j => j.status === 'running').length;
    const qN = listItems.length - runN;
    if (!listItems.length) {
      toggleBtn.style.display = 'none';
      qList.innerHTML = '';
      qList.style.display = 'none';
      return;
    }
    toggleBtn.style.display = '';
    toggleBtn.textContent = queueOpen
      ? `收起 (${listItems.length})`
      : `任务 ${listItems.length}` + (qN ? ` · 排队${qN}` : '');
    toggleBtn.classList.toggle('on', queueOpen);
    if (!queueOpen) {
      qList.style.display = 'none';
      return;
    }
    qList.innerHTML =
      `<div class="job-queue-title">进行中 ${runN} · 排队 ${qN}</div>` +
      listItems.map(j => {
        const st = j.status === 'running' ? '进行中' : '排队';
        const can = j.cancellable !== false && j.kind !== 'system.prewarm';
        const x = can
          ? `<button type="button" class="job-cancel-btn" data-id="${j.job_id}" title="取消">×</button>`
          : `<span class="job-queue-locked" title="启动预热不可取消">—</span>`;
        return `<div class="job-queue-item">
          <span class="job-queue-label"><span class="job-queue-st">${st}</span>${(j.meta && j.meta.name) || j.label || j.kind}</span>
          ${x}
        </div>`;
      }).join('');
    qList.querySelectorAll('button[data-id]').forEach(btn => {
      btn.onclick = () => cancelJob(btn.dataset.id, false);
    });
    qList.style.display = 'block';
  }

  // 渲染一份 job 快照（ws 推送或轮询兜底共用）
  function renderJobSnapshot(p) {
    if (!p) return;
    window._lastJobsData = p;   // 存最近快照：点「任务」展开/ waitForJob 立即命中用
    const jobs = p.jobs || [];
    const batches = p.batches || [];
    const active = jobs.length > 0 || batches.length > 0;
    (p.recent || []).slice(0, 8).forEach(r => {
      if (r && (r.status === 'done' || r.status === 'error' || r.status === 'cancelled')) {
        fireDone(r);
      }
    });

    if (active) {
      _jobBarActive = true;
      if (_jobBarHideTimer) { clearTimeout(_jobBarHideTimer); _jobBarHideTimer = null; }
      bar.classList.remove('done', 'error');
      bar.style.display = 'block';

      const batch = batches[0];
      const runJobs = jobs.filter(j => j.status === 'running');
      const queued = jobs.filter(j => j.status === 'queued');
      let label, cur, tot, step, pct, cancelId, cancelIsBatch, cancellable = true;

      if (batch) {
        label = batch.label || '刷新';
        cur = batch.done_count || 0;
        tot = batch.total || 1;
        const runNames = (batch.running || []).slice(0, 3).join('、') || batch.current_label || '进行中';
        step = runNames;
        pct = batch.pct != null ? batch.pct : 0;
        cancelId = batch.batch_id;
        cancelIsBatch = true;
        cancellable = true;
      } else if (runJobs[0]) {
        const j = runJobs[0];
        label = j.label || j.kind || '后台任务';
        cur = j.current || (j.done_count || 0) + (j.step ? 1 : 0);
        tot = j.total || 1;
        step = j.step || '进行中';
        pct = j.pct != null ? j.pct : 0;
        cancelId = j.job_id;
        cancelIsBatch = false;
        cancellable = j.cancellable !== false && j.kind !== 'system.prewarm';
      } else if (queued[0]) {
        label = '排队中';
        cur = 0;
        tot = queued.length;
        step = queued.map(j => j.label).slice(0, 4).join('、');
        pct = 0;
        cancelId = queued[0].job_id;
        cancelIsBatch = false;
        cancellable = queued[0].cancellable !== false && queued[0].kind !== 'system.prewarm';
      }

      txt.textContent = label + (step ? ' · ' + step : '');   // 任务类型 + 当前阶段（分步进度）
      pctEl.textContent = (pct != null ? pct : 0) + '%';
      fill.style.width = Math.max(0, Math.min(100, pct || 0)) + '%';
      if (cancelId && cancellable) {
        cancelBtn.style.display = '';
        cancelBtn.dataset.id = cancelId;
        cancelBtn.dataset.batch = cancelIsBatch ? '1' : '0';
        cancelBtn.textContent = cancelIsBatch ? '取消整批' : '取消';
      } else {
        cancelBtn.style.display = 'none';
      }

      renderQueueList(jobs);

      const st = document.getElementById('statusText');
      if (st) { st.textContent = ''; st.style.display = 'none'; }  // 进度只在任务条，避免双写
    } else if (_jobBarActive) {
      _jobBarActive = false;
      queueOpen = false;
      cancelBtn.style.display = 'none';
      toggleBtn.style.display = 'none';
      qList.style.display = 'none';
      qList.innerHTML = '';
      const last = (p.recent || [])[0];
      const label = (last && last.label) || p.label || '任务';
      const ok = last ? (last.ok !== false && last.status !== 'cancelled') : (p.ok !== false);
      const detail = (last && last.done_count != null && last.total)
        ? `（${last.done_count}/${last.total}）` : '';
      bar.classList.toggle('done', ok);
      bar.classList.toggle('error', !ok);
      bar.style.display = 'block';
      txt.textContent = ok ? (label + '完成' + detail) : (label + (last && last.status === 'cancelled' ? '已取消' : '失败'));
      pctEl.textContent = ok ? '100%' : '';
      fill.style.width = ok ? '100%' : '0%';
      if (last && last.status === 'cancelled') toast('已取消：' + label, 4000, 'err');
      else if (ok) toast('✓ ' + label + '完成' + detail, 5000, 'ok');
      else toast('✗ ' + ((last && last.error) || label + '失败'), 6000, 'err');
      const st = document.getElementById('statusText');
      if (st) { st.style.display = ''; st.textContent = ''; }
      _jobBarHideTimer = setTimeout(() => { bar.style.display = 'none'; }, 6000);
    }
  }

  // 进度来源：ws 在线时零轮询（靠推送，省请求）；**仅两种场景轮询兜底**——
  //  1) ws 断线/未连上（__wsDown）：任务中 1s / 空闲 2.5s
  //  2) ws 假死（连接在但 30s 无任何消息，如 Android WebView 半死连接）：
  //     主动关闭重连并转轮询，避免任务完成事件永久丢失 → 页面一直「正在加载中」
  // 另：waitForJob 在任务等待期间无条件轮询（见下），保证任务终态必达。
  function tick() {
    if (wsStale()) {
      // 假死：强制断开触发 onclose → 重连 + 转轮询
      try { window._ws.close(); } catch (e) {}
    }
    if (window.__wsDown !== false || wsStale()) {
      api('/status/jobs', { silent: true })
        .then(renderJobSnapshot)
        .catch(() => {})
        .finally(() => { setTimeout(tick, _jobBarActive ? 1000 : 2500); });
    } else {
      // ws 在线：零轮询；每 10s 检查一次假死
      setTimeout(tick, 10000);
    }
  }
  // 任务入队后调用：确保进度条立即显示新任务。
  // 注意：新任务入队的 ws 推送可能被后端 0.3s 节流吞掉
  // （如紧接上个任务完成推送之后入队），导致进度条停留在旧状态、几秒后消失。
  // 因此这里主动拉一次快照，不依赖推送时序。
  window.kickJobBar = () => {
    _jobBarActive = true;
    api('/status/jobs', { silent: true })
      .then(renderJobSnapshot)
      .catch(() => {});
    tick();
  };
  tick();

  // ---- WebSocket：任务进度推送 + 数据更新推送 ----
  function wsOnMessage(ev) {
    _wsLastMsgAt = Date.now();
    let msg;
    try { msg = JSON.parse(ev.data); } catch (e) { return; }
    if (!msg || !msg.type) return;
    if (msg.type === 'jobs') {
      renderJobSnapshot(msg.data);
    } else if (msg.type === 'data_updated') {
      window.dispatchEvent(new CustomEvent('app-data-updated', { detail: msg }));
    }
  }
  window.connectWs = () => {
    if (window._ws && (window._ws.readyState === 0 || window._ws.readyState === 1)) return;
    let ws;
    try {
      const proto = location.protocol === 'https:' ? 'wss://' : 'ws://';
      ws = new WebSocket(proto + location.host + '/ws');
    } catch (e) { window.__wsDown = true; return; }
    window._ws = ws;
    window.__wsDown = true;
    ws.onopen = () => { window.__wsDown = false; _wsLastMsgAt = Date.now(); tick(); };
    ws.onmessage = wsOnMessage;
    ws.onclose = () => {
      window.__wsDown = true;
      tick();
      setTimeout(window.connectWs, 3000);
    };
    ws.onerror = () => { try { ws.close(); } catch (e) {} };
  };
  window.connectWs();
}

/** 启动后台任务（POST 秒回 job_id / batch_id），顶部条跟进度。 */
async function startBackgroundJob(path, body, opts = {}) {
  const data = await api(path, {
    method: 'POST',
    body: body || {},
    silent: !!opts.silent,
  });
  if (!data || !data.job_id) throw new Error('未返回任务 ID');
  if (!opts.silent) toast(opts.toast || '任务已加入队列，进度见顶部');
  if (typeof window.kickJobBar === 'function') window.kickJobBar();
  return data;
}

/** 等待指定 job/batch 出现在 recent（队列连跑时不能等全局 idle）。 */
function waitForJob(jobId, timeoutMs = 600000) {
  return new Promise((resolve, reject) => {
    let settled = false;
    let timer = null;
    let poll = null;
    const finish = (ok, detail, err) => {
      if (settled) return;
      settled = true;
      if (timer) clearTimeout(timer);
      if (poll) clearInterval(poll);
      window.removeEventListener('app-job-done', onDone);
      if (ok) resolve(detail || {});
      else reject(new Error(err || '任务失败'));
    };
    function match(d) {
      if (!d) return false;
      if (!jobId) return true;
      // 只认 job_id：batch 收尾写入 recent 时 job_id===batch_id。
      // 勿用 batch_id 匹配，否则会提前命中扇出子任务（kind=refresh.stock.*）。
      return d.job_id === jobId;
    }
    function onDone(e) {
      const d = e.detail || {};
      if (!match(d)) return;
      if (d.status === 'cancelled' || d.ok === false || d.error) finish(false, d, d.error || '已取消');
      else finish(true, d);
    }
    window.addEventListener('app-job-done', onDone);
    // ws 在线且已有快照：任务可能先完成、监听后注册 → 查快照立即命中，避免干等超时
    if (window.__wsDown === false && window._lastJobsData && !settled) {
      const hit = (window._lastJobsData.recent || []).find(r => match(r));
      if (hit) {
        if (hit.status === 'cancelled' || hit.ok === false || hit.error) finish(false, hit, hit.error || '已取消');
        else finish(true, hit);
        return;
      }
    }
    timer = setTimeout(() => finish(false, null, '任务超时'), timeoutMs);
    // 仅在 ws 断线/未连上/假死（30s 无消息）时轮询兜底；ws 在线靠推送（app-job-done）+ 快照命中，
    // 避免任务期间每 500ms 刷 /status/jobs（本地接口也会在请求日志里刷屏）
    if (window.__wsDown !== false || wsStale()) {
    poll = setInterval(async () => {
      try {
        const p = await api('/status/jobs', { silent: true });
        const hit = (p.recent || []).find(r => match(r));
        if (hit) {
          if (hit.status === 'cancelled' || hit.ok === false || hit.error)
            finish(false, hit, hit.error || '已取消');
          else finish(true, hit);
        }
      } catch (e) { /* ignore */ }
    }, 500);
    }
  });
}

// 刷新内容项（与后端 DYNAMIC_ITEMS / FULL_ITEMS / STOCK_*_ITEMS 对应）
// AI 打分不在刷新项里：由组合页/交易页手动触发（POST /api/ai-scoring/*）
const REFRESH_OPTIONS = {
  dynamic: [
    ['price', '现价'],
    ['valuation', '当前估值（市盈率 / 市净率 / 股息率等）'],
    ['flow', '今日资金流'],
  ],
  full: [
    ['bars', '历史日K行情'],
    ['financials', '财务数据（利润、净资产等）'],
    ['valuation', '估值分位（近1/3/5年）'],
    ['fx', '港股汇率'],
    ['flow', '今日资金流'],
    ['portfolio', '组合历史估值重算'],
    ['news', '个股新闻（AI 消息面用）'],
  ],
  stock_dynamic: [
    ['price', '现价'],
    ['valuation', '当前估值（市盈率 / 市净率 / 股息率等）'],
    ['flow', '今日资金流'],
  ],
  stock_full: [
    ['bars', '历史日K行情'],
    ['financials', '财务数据（利润、净资产等）'],
    ['valuation', '估值分位（近1/3/5年）'],
    ['flow', '今日资金流'],
  ],
};

// 弹窗多选本次要刷新的内容；scope='global'（首页/组合页）或 'stock'（个股页）；
// 返回 Promise<items[] | null>（null = 用户取消）
function chooseRefreshItems(full, scope = 'global') {
  return new Promise((resolve) => {
    const key = scope === 'stock' ? (full ? 'stock_full' : 'stock_dynamic') : (full ? 'full' : 'dynamic');
    const opts = REFRESH_OPTIONS[key];
    const suffix = scope === 'stock' ? '（个股）' : '';
    const title = (full ? '🔄 完整更新' : '⚡ 更新行情') + suffix;
    const mask = document.createElement('div');
    mask.className = 'modal-mask';
    mask.innerHTML = `
      <div class="modal">
        <h3>${title}</h3>
        <p class="muted" style="font-size:12px;margin:-6px 0 12px">勾选要更新的内容，一般保持全选即可。</p>
        <div class="modal-items">
          ${opts.map(([key, label]) => `
            <label class="mi"><input type="checkbox" value="${key}" checked> ${label}</label>`).join('')}
        </div>
        <div class="modal-actions">
          <span class="link muted" id="toggleAll">全选 / 取消全选</span>
          <button class="btn" id="cancelBtn">取消</button>
          <button class="btn primary" id="okBtn">开始更新</button>
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

// 默认刷新项：该 scope/mode 下的全部内容（直接开跑用，不弹勾选框）
function defaultRefreshItems(full, scope = 'global') {
  const key = scope === 'stock' ? (full ? 'stock_full' : 'stock_dynamic') : (full ? 'full' : 'dynamic');
  return (REFRESH_OPTIONS[key] || []).map(([k]) => k);
}

// 启动刷新任务（直跑或「自定义」勾选后共用）。btn 可为空（提示条等非按钮入口）。
async function startRefresh(btn, full, scope, code, items) {
  if (btn) btn.disabled = true;
  const st = document.getElementById('statusText');
  if (st) st.textContent = '已提交刷新…';
  const label = scope === 'stock' ? '个股' : (full ? '完整' : '行情');
  try {
    const path = scope === 'stock'
      ? `/stocks/${code}/refresh` + (full ? '/full' : '')
      : full ? '/refresh/full' : '/refresh';
    await startBackgroundJob(path, { items }, { toast: `${label}更新已开始，进度见顶部` });
  } catch (e) {
    if (st) st.textContent = '更新失败';
    toast('更新失败：' + e.message, 4000);
  } finally {
    if (btn) btn.disabled = false;
  }
}

async function doRefresh(btn, full, scope = 'global', code) {
  // 默认直接开跑（全项），不再弹勾选框；想自定义走 openCustomRefresh
  return startRefresh(btn, full, scope, code, defaultRefreshItems(full, scope));
}

// 「自定义」：弹勾选框（完整更新内容）选择后开始；保留按需勾选能力
async function openCustomRefresh(scope = 'global', code) {
  const items = await chooseRefreshItems(true, scope);
  if (!items || !items.length) return; // 用户取消或未勾选任何内容
  return startRefresh(null, true, scope, code, items);
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
  let reqId = 0;
  input.addEventListener('input', () => {
    clearTimeout(timer);
    const q = input.value.trim();
    if (!q) { sug.style.display = 'none'; return; }
    timer = setTimeout(async () => {
      const myId = ++reqId;
      try {
        // 读完整 JSON（含 lists_ready）；api() 只返回 data，看不到预热失败提示
        const res = await fetch('/api/stocks/search?q=' + encodeURIComponent(q) + '&limit=10');
        const json = await res.json().catch(() => ({}));
        if (myId !== reqId) return;  // 过期响应丢弃，防旧结果覆盖新输入
        if (!res.ok) throw new Error(json.detail || json.msg || `HTTP ${res.status}`);
        const rows = json.data || [];
        sug.innerHTML = '';
        if (!rows.length) {
          // hint: ok=正常无匹配不提示；loading=预热中；error=预热结束但列表仍缺失（需重启）
          const h = json.hint || (json.lists_ready === false ? 'error' : 'ok');
          if (h !== 'ok') {
            const tip = document.createElement('div');
            tip.className = 'stock-sug-item';
            tip.style.cssText = 'color:#888;cursor:default';
            tip.textContent = h === 'loading'
              ? '市场列表正在加载，请稍后重试…'
              : '市场列表加载异常，请重新打开应用';
            sug.appendChild(tip);
            sug.style.display = 'block';
          } else {
            sug.style.display = 'none';
          }
          return;
        }
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
        sug.style.display = 'block';
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

// DeepSeek / 小米 MiMo 一键模板：用户只需填 API Key；模型名从接口列表取第一个（不写死）
const DEEPSEEK_PRESET = {
  name: 'DeepSeek',
  base_url: 'https://api.deepseek.com',
  docs_keys: 'https://platform.deepseek.com/api_keys',
  docs_guide: 'https://api-docs.deepseek.com/zh-cn/',
};
const XIAOMI_PRESET = {
  name: '小米 MiMo',
  base_url: 'https://api.xiaomimimo.com/v1',
  docs_keys: 'https://platform.xiaomimimo.com/#/console/api-keys',
  docs_guide: 'https://mimo.mi.com/docs/zh-CN/quick-start/summary/first-api-call',
};

async function openAiSettings() {
  const mask = document.createElement('div');
  mask.className = 'modal-mask';
  mask.innerHTML = `
    <div class="modal ai-settings" style="width:620px;max-width:94vw">
      <h3>🤖 AI 设置</h3>
      <div id="aiActive" class="muted" style="margin-bottom:10px;font-size:12px"></div>
      <div class="row" style="align-items:center;gap:8px;margin-bottom:12px">
        <span class="muted" style="font-size:12px">分析深度（越高越细，也更慢、更费额度）</span>
        <select id="aiReasoning" style="width:140px" title="一般选「高」即可；太慢可改成「中」">
          <option value="low">低（更快）</option>
          <option value="medium">中</option>
          <option value="high">高（推荐）</option>
          <option value="max">最高</option>
        </select>
      </div>
      <div class="row" style="align-items:center;gap:8px;margin-bottom:12px;flex-wrap:wrap">
        <span class="muted" style="font-size:12px">输出预算 max_tokens</span>
        <input type="number" id="aiMaxTokens" min="2048" step="1024" style="width:110px"
               title="AI 单次回复最大 token 数。持仓多/组合深入分析时调大（缺省 81920）；提供商上限内越大越稳、也越费">
        <span class="muted" style="font-size:12px;margin-left:8px">请求超时（秒）</span>
        <input type="number" id="aiTimeout" min="30" step="10" style="width:90px"
               title="AI 请求超时。大任务（几十只/深入 HTML）生成更久，超时会掐断；缺省 300">
      </div>
      <div id="aiModelList" style="max-height:180px;overflow-y:auto;margin-bottom:12px"></div>

      <div class="ai-preset">
        <div class="ai-preset-title">一键启用 DeepSeek</div>
        <ol class="ai-preset-steps">
          <li>打开
            <a href="${DEEPSEEK_PRESET.docs_keys}" target="_blank" rel="noopener">DeepSeek 控制台</a>
            注册/登录，创建「API Key」
          </li>
          <li>把 Key 粘贴到下方，点「一键启用」（地址自动填好，模型取接口列表第一个）</li>
        </ol>
        <p class="muted" style="font-size:11px;margin:0 0 8px">
          说明文档：<a href="${DEEPSEEK_PRESET.docs_guide}" target="_blank" rel="noopener">api-docs.deepseek.com/zh-cn</a>
          · 按量计费，用多少花多少
        </p>
        <div class="row" style="gap:8px;flex-wrap:wrap;align-items:flex-end">
          <div style="flex:1;min-width:220px">
            <label class="muted" style="font-size:11px;display:block;margin-bottom:2px">API Key（只需填这一项）</label>
            <input id="aiDsKey" type="password" placeholder="粘贴 sk- 开头的密钥" style="width:100%;box-sizing:border-box">
          </div>
          <button class="btn primary" id="aiDsEnable">一键启用 DeepSeek</button>
        </div>
      </div>

      <div class="ai-preset" style="margin-top:10px">
        <div class="ai-preset-title">一键启用 小米 MiMo</div>
        <ol class="ai-preset-steps">
          <li>打开
            <a href="${XIAOMI_PRESET.docs_keys}" target="_blank" rel="noopener">小米 MiMo 控制台</a>
            用小米账号登录，创建「API Key」
          </li>
          <li>把 Key 粘贴到下方，点「一键启用」（地址自动填好，模型取接口列表第一个）</li>
        </ol>
        <p class="muted" style="font-size:11px;margin:0 0 8px">
          说明文档：<a href="${XIAOMI_PRESET.docs_guide}" target="_blank" rel="noopener">mimo.mi.com 首次调用 API</a>
          · 按量计费；地址 ${XIAOMI_PRESET.base_url}
        </p>
        <div class="row" style="gap:8px;flex-wrap:wrap;align-items:flex-end">
          <div style="flex:1;min-width:220px">
            <label class="muted" style="font-size:11px;display:block;margin-bottom:2px">API Key（只需填这一项）</label>
            <input id="aiXmKey" type="password" placeholder="粘贴 sk- 开头的密钥" style="width:100%;box-sizing:border-box">
          </div>
          <button class="btn primary" id="aiXmEnable">一键启用 小米 MiMo</button>
        </div>
      </div>

      <details id="aiAdvanced" style="margin-top:14px;border-top:1px solid var(--border);padding-top:10px">
        <summary class="muted" style="cursor:pointer;font-size:12px;user-select:none">高级：其他模型（OpenAI 兼容接口）</summary>
        <div style="padding-top:10px">
          <div style="font-size:13px;font-weight:700;margin-bottom:8px" id="aiFormTitle">新增模型</div>
          <div class="row" style="gap:8px;flex-wrap:wrap;margin-bottom:8px">
            <div style="flex:1;min-width:140px">
              <label class="muted" style="font-size:11px;display:block;margin-bottom:2px">名称</label>
              <input id="aiName" placeholder="自定义名称" style="width:100%;box-sizing:border-box">
            </div>
            <div style="flex:2;min-width:220px">
              <label class="muted" style="font-size:11px;display:block;margin-bottom:2px">接口地址</label>
              <input id="aiBaseUrl" placeholder="https://api.openai.com/v1" style="width:100%;box-sizing:border-box">
            </div>
          </div>
          <div class="row" style="gap:8px;flex-wrap:wrap;margin-bottom:10px">
            <div style="flex:2;min-width:200px">
              <label class="muted" style="font-size:11px;display:block;margin-bottom:2px">API Key</label>
              <input id="aiApiKey" type="password" placeholder="sk-..." style="width:100%;box-sizing:border-box">
            </div>
            <div style="flex:1;min-width:200px">
              <label class="muted" style="font-size:11px;display:block;margin-bottom:2px">模型</label>
              <div class="row" style="gap:6px">
                <select id="aiModel" style="flex:1"><option value="">— 点获取 —</option></select>
                <button class="btn" id="aiFetch" title="用上方地址和密钥拉取可用模型">获取</button>
              </div>
            </div>
          </div>
          <div class="row" style="gap:8px;justify-content:flex-end">
            <button class="btn" id="aiNew">清空重填</button>
            <button class="btn primary" id="aiSave">保存模型</button>
          </div>
        </div>
      </details>

      <div class="modal-actions" style="margin-top:14px">
        <span class="link muted" id="aiCancel">关闭</span>
      </div>
    </div>`;
  document.body.appendChild(mask);
  const close = () => mask.remove();
  mask.querySelector('#aiCancel').onclick = close;
  mask.addEventListener('click', (e) => { if (e.target === mask) close(); });

  const reasoningSel = mask.querySelector('#aiReasoning');
  api('/ai/reasoning', { silent: true })
    .then((d) => { reasoningSel.value = d.effort || 'high'; })
    .catch(() => { reasoningSel.value = 'high'; });
  reasoningSel.onchange = async () => {
    try {
      await api('/ai/reasoning', { method: 'PUT', body: { effort: reasoningSel.value } });
      toast('分析深度已设为「' + (reasoningSel.selectedOptions[0] ? reasoningSel.selectedOptions[0].textContent : reasoningSel.value) + '」');
    } catch (e) {
      toast('保存失败：' + e.message, 4000);
    }
  };

  // AI 输出预算 / 请求超时（config 表 ai_max_tokens / ai_request_timeout，改动即时生效）
  const mtEl = mask.querySelector('#aiMaxTokens');
  const toEl = mask.querySelector('#aiTimeout');
  api('/ai/runtime', { silent: true })
    .then((d) => {
      if (mtEl) mtEl.value = d.max_tokens != null ? d.max_tokens : '';
      if (toEl) toEl.value = d.request_timeout != null ? d.request_timeout : '';
    })
    .catch(() => {});
  const saveRuntime = () => {
    const body = {};
    if (mtEl && mtEl.value) body.max_tokens = parseInt(mtEl.value, 10);
    if (toEl && toEl.value) body.request_timeout = parseInt(toEl.value, 10);
    if (!Object.keys(body).length) return Promise.resolve();
    return api('/ai/runtime', { method: 'PUT', body });
  };
  if (mtEl) mtEl.onchange = async () => {
    try { await saveRuntime(); toast('已保存输出预算 max_tokens'); }
    catch (e) { toast('保存失败：' + e.message, 4000); }
  };
  if (toEl) toEl.onchange = async () => {
    try { await saveRuntime(); toast('已保存请求超时'); }
    catch (e) { toast('保存失败：' + e.message, 4000); }
  };

  const enablePreset = async (preset, keyInputId, btn) => {
    const apiKey = mask.querySelector(keyInputId).value.trim();
    if (!apiKey) return toast('请先粘贴 ' + preset.name + ' 的 API Key');
    btn.disabled = true;
    try {
      const avail = await api('/ai/models/available', {
        method: 'POST',
        body: { base_url: preset.base_url, api_key: apiKey },
      });
      const models = avail.models || [];
      if (!models.length) {
        toast('未获取到可用模型，请检查 Key 或稍后重试', 4000);
        return;
      }
      const model = models[0];
      const row = await api('/ai/models', {
        method: 'POST',
        body: {
          name: preset.name,
          base_url: preset.base_url,
          api_key: apiKey,
          model,
        },
      });
      await api('/ai/models/' + row.id + '/activate', { method: 'POST' });
      mask.querySelector(keyInputId).value = '';
      toast('已启用 ' + preset.name + '（' + model + '），可以开始 AI 分析了');
      await loadAiModels(mask);
    } catch (e) {
      toast('启用失败：' + e.message, 4000);
    } finally {
      btn.disabled = false;
    }
  };
  mask.querySelector('#aiDsEnable').onclick = () => enablePreset(DEEPSEEK_PRESET, '#aiDsKey', mask.querySelector('#aiDsEnable'));
  mask.querySelector('#aiXmEnable').onclick = () => enablePreset(XIAOMI_PRESET, '#aiXmKey', mask.querySelector('#aiXmEnable'));

  mask.querySelector('#aiNew').onclick = () => {
    _aiEditingId = null;
    ['aiName', 'aiBaseUrl', 'aiApiKey'].forEach((id) => (mask.querySelector('#' + id).value = ''));
    mask.querySelector('#aiModel').innerHTML = '<option value="">— 点获取 —</option>';
    mask.querySelector('#aiFormTitle').textContent = '新增模型';
  };

  mask.querySelector('#aiFetch').onclick = async () => {
    const baseUrl = mask.querySelector('#aiBaseUrl').value.trim();
    const apiKey = mask.querySelector('#aiApiKey').value.trim();
    if (!baseUrl || !apiKey) return toast('请先填接口地址和 API Key');
    const sel = mask.querySelector('#aiModel');
    sel.innerHTML = '<option value="">加载中…</option>';
    try {
      const data = await api('/ai/models/available', { method: 'POST', body: { base_url: baseUrl, api_key: apiKey } });
      const models = data.models || [];
      if (!models.length) { sel.innerHTML = '<option value="">无可用模型</option>'; return toast('未获取到模型'); }
      sel.innerHTML = '<option value="">选择模型…</option>' + models.map((m) => `<option value="${m}">${m}</option>`).join('');
      toast(`获取到 ${models.length} 个模型`);
    } catch (e) {
      sel.innerHTML = '<option value="">— 点获取 —</option>';
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
    if (!body.name || !body.base_url || !body.api_key || !body.model) return toast('名称、接口地址、API Key、模型都要填');
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
      : '<span style="color:#e03131">还没启用 AI。上方 DeepSeek 或小米 MiMo 一键启用，只需填密钥。</span>';
    if (!data.models.length) {
      wrap.innerHTML = '<div class="muted" style="font-size:12px;padding:4px 0 8px">暂无已保存的模型。</div>';
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
        <div class="card-actions">
          <button class="btn btn-sm" ${m.is_active ? 'disabled' : ''} data-act="${m.id}">启用</button>
          <button class="btn btn-sm" data-edit="${m.id}">编辑</button>
          <button class="btn btn-sm danger" data-del="${m.id}">删除</button>
        </div>`;
      card.querySelector('[data-act]').onclick = async () => {
        try { await api('/ai/models/' + m.id + '/activate', { method: 'POST' }); toast('已切换到 ' + m.name); await loadAiModels(mask); }
        catch (e) { toast('切换失败：' + e.message, 4000); }
      };
      card.querySelector('[data-edit]').onclick = () => {
        _aiEditingId = m.id;
        const adv = mask.querySelector('#aiAdvanced');
        if (adv) adv.open = true;
        mask.querySelector('#aiFormTitle').textContent = '编辑模型：' + m.name;
        mask.querySelector('#aiName').value = m.name;
        mask.querySelector('#aiBaseUrl').value = m.base_url;
        mask.querySelector('#aiApiKey').value = m.api_key;
        const sel = mask.querySelector('#aiModel');
        sel.innerHTML = `<option value="${m.model}">${m.model}</option>`;
      };
      card.querySelector('[data-del]').onclick = async () => {
        if (!confirm('删除模型「' + m.name + '」？')) return;
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
  return '<div class="empty" style="padding:16px;margin:0">还没配置 AI。点右上角「🤖 AI」，用 DeepSeek 或小米 MiMo 一键启用即可（只需填密钥）。</div>';
}

// ScoreCard 质量/动作/维度中文名（三处打分共用；grade=质量，action=操作建议）
const GRADE_NAMES = { A: '优秀', B: '良好', C: '一般', D: '较差' };
const ACTION_NAMES = {
  add: '加仓', hold: '持有', watch: '观望', reduce: '减仓', exit: '清仓',
  repeat: '可复制', cautious: '谨慎复制', avoid: '避免重复',
};
const SCORE_DIM_CN = {
  cyclicality: '周期性', moat: '护城河', fundamentals: '基本面', growth: '增长',
  dividend: '股息', valuation: '估值', competition: '同业竞争', fundflow: '资金面',
  news: '消息面', technical: '技术面',
  structure: '结构集中度', tag_fit: '标签契合',
  timing: '时机', execution: '价格执行', sizing: '仓位管理', discipline: '纪律',
};
const _RISK_LEVEL_CN = { low: '低', medium: '中', high: '高' };
const _CONF_CN = { high: '高', medium: '中', low: '低' };
const _RISK_COLORS = { low: '#2f9e44', medium: '#e8590c', high: '#e03131' };

function cardGrade(r) { return (r && (r.grade || r.rating)) || ''; }
function cardGradeName(r) {
  if (!r) return '';
  return r.grade_name || r.rating_name || GRADE_NAMES[cardGrade(r)] || '';
}
function cardRisk(r) {
  if (!r) return null;
  if (r.risk != null && r.risk !== '') return Number(r.risk);
  if (r.risk_score != null && r.risk_score !== '') return Number(r.risk_score);
  return null;
}
function cardRiskLevel(r) {
  if (r && r.risk_level) return r.risk_level;
  const v = cardRisk(r);
  if (v == null || Number.isNaN(v)) return '';
  return v < 35 ? 'low' : v < 65 ? 'medium' : 'high';
}
function cardActionName(r) {
  if (!r || !r.action) return '';
  return r.action_name || ACTION_NAMES[r.action] || r.action;
}

// AI 报告头部：大分 + 质量评级徽章 + 一句话结论（兼容 grade|rating）
function aiScoreHeader(r) {
  const g = cardGrade(r) || 'N';
  const gName = cardGradeName(r);
  return `
    <div style="display:flex;align-items:center;gap:12px;margin-bottom:10px;flex-wrap:wrap">
      <span style="font-size:42px;font-weight:700;line-height:1">${r.score == null ? '—' : fmtNum(r.score)}</span>
      <span class="badge ${g}">${g} ${esc(gName)}</span>
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

/** Unified ScoreCard renderer. opts: { stale, rescoreLabel, extraHtml, dimOrder, showDims, drillHtml(dimKey) }
 *  Returns HTML string; caller wires [data-aiscore-btn] / [data-ai-html] after inject. */
function renderScoreCard(r, opts = {}) {
  if (!r) return '';
  const g = cardGrade(r) || 'N';
  const gName = cardGradeName(r);
  const scoreTxt = r.score == null ? '—' : fmtNum(r.score);
  const actionName = cardActionName(r);
  const risk = cardRisk(r);
  const riskLv = cardRiskLevel(r);
  const riskColor = _RISK_COLORS[riskLv] || '#aeb6c2';
  const riskPct = (risk != null && !Number.isNaN(risk)) ? Math.max(0, Math.min(100, risk)) : null;
  const conf = r.confidence ? (_CONF_CN[r.confidence] || r.confidence) : '';

  const actionPill = actionName
    ? `<span style="display:inline-block;font-size:12px;font-weight:600;padding:3px 10px;border-radius:999px;border:1px solid var(--border,#dee2e6);background:#f8f9fa;color:#495057">${esc(actionName)}</span>`
    : '';
  const riskBar = riskPct != null ? `
    <div style="flex:1;min-width:140px;max-width:260px">
      <div style="display:flex;justify-content:space-between;font-size:12px;color:#555">
        <span>风险</span>
        <span style="color:${riskColor};font-weight:700">${fmtNum(risk)}${riskLv ? `（${_RISK_LEVEL_CN[riskLv] || riskLv}）` : ''}</span>
      </div>
      <div style="height:8px;border-radius:4px;background:#eee;margin-top:4px;overflow:hidden">
        <div style="height:100%;width:${riskPct}%;background:${riskColor}"></div>
      </div>
    </div>` : '';
  const confHtml = conf
    ? `<span class="muted" style="font-size:12px">置信 ${esc(conf)}</span>` : '';
  const staleChip = opts.stale
    ? '<span class="status-chip">已变化</span>' : '';

  const header = `
    <div style="display:flex;align-items:center;gap:12px;margin-bottom:10px;flex-wrap:wrap">
      <span style="font-size:42px;font-weight:700;line-height:1">${scoreTxt}</span>
      <span class="badge ${g}">${g} ${esc(gName)}</span>
      ${actionPill}
      ${riskBar}
      ${confHtml}
      ${staleChip}
      ${r.summary ? `<div class="muted" style="font-size:13px;flex:1;min-width:200px">${esc(r.summary)}</div>` : ''}
    </div>`;

  let dimsHtml = '';
  const showDims = opts.showDims !== false;
  const dims = r.dimensions && typeof r.dimensions === 'object' ? r.dimensions : null;
  if (showDims && dims) {
    const known = Object.keys(SCORE_DIM_CN);
    const order = (opts.dimOrder && opts.dimOrder.length) ? opts.dimOrder
      : known.filter((k) => dims[k]).concat(Object.keys(dims).filter((k) => !known.includes(k)));
    const items = order.map((k) => {
      const dim = dims[k];
      if (!dim) return '';
      const label = SCORE_DIM_CN[k] || k;
      const ds = dim.score;
      const scoreColor = ds == null ? '#868e96' : ds >= 70 ? '#2f9e44' : ds >= 40 ? '#e8590c' : '#e03131';
      const barW = ds == null ? 0 : Math.max(0, Math.min(100, Number(ds)));
      const dg = dim.grade || '';
      const riskCls = dim.risk === 'low' ? '#2f9e44' : dim.risk === 'high' ? '#e03131' : dim.risk === 'medium' ? '#e8590c' : '';
      const supp = dim.data_source === 'supplemented'
        ? '<span style="font-size:10px;color:#7048e8;border:1px solid #7048e8;border-radius:4px;padding:1px 5px;margin-left:6px">AI补充</span>' : '';
      const gradeBadge = dg ? `<span class="badge ${dg}" style="font-size:10px;padding:1px 6px">${dg}</span>` : '';
      const riskBadge = (dim.risk && riskCls)
        ? `<span style="font-size:11px;color:${riskCls};border:1px solid ${riskCls};border-radius:4px;padding:1px 5px">${esc(dim.risk)}</span>` : '';
      const drill = (typeof opts.drillHtml === 'function') ? (opts.drillHtml(k) || '') : '';
      return `<details style="border:1px solid var(--border);border-radius:8px;padding:8px 12px;margin-bottom:6px">
        <summary style="cursor:pointer;display:flex;align-items:center;gap:10px;font-size:13px;user-select:none;list-style:none">
          <span style="flex:1;min-width:72px"><strong>${esc(label)}</strong>${supp}</span>
          <span style="flex:2;min-width:80px;height:6px;border-radius:3px;background:#eee;overflow:hidden">
            <span style="display:block;height:100%;width:${barW}%;background:${scoreColor}"></span>
          </span>
          <span style="font-size:15px;font-weight:700;color:${scoreColor};min-width:28px;text-align:right">${ds == null ? '—' : ds}</span>
          ${gradeBadge}${riskBadge}
        </summary>
        <div style="margin-top:8px;font-size:12px;color:#444;line-height:1.8">${esc(dim.analysis || '—')}</div>
        ${drill}
      </details>`;
    }).filter(Boolean).join('');
    if (items) {
      dimsHtml = `<div style="margin:10px 0 8px">${items}</div>`;
    }
  }

  const extra = opts.extraHtml || '';
  const lists = renderAiReportBlock(r);
  const btn = opts.rescoreLabel ? `<div style="margin:10px 0 4px">${aiScoreButtonHtml(opts.rescoreLabel)}</div>` : '';

  return header + extra + dimsHtml + lists + btn;
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
  w.document.write(wrapAiReportForPdf(html));
  w.document.close();
}

// 给 AI 报告套「导出 PDF」外壳：sticky 工具条 + 打印样式。
// 工具条/样式属于包装层自身代码（AI HTML 禁 script 约束不受影响）；
// 点击后调起浏览器打印，用户选「另存为 PDF」即可。
function wrapAiReportForPdf(html) {
  const style = `<style>
    #pdf-toolbar{position:sticky;top:0;z-index:99999;display:flex;align-items:center;gap:10px;
      padding:10px 16px;background:#1f2937;color:#fff;
      font:13px/1.5 system-ui,-apple-system,"Segoe UI","Microsoft YaHei",sans-serif;
      box-shadow:0 2px 6px rgba(0,0,0,.3)}
    #pdf-toolbar button{margin:0;padding:7px 14px;border:0;border-radius:6px;background:#2f6fed;color:#fff;font-size:13px;cursor:pointer}
    #pdf-toolbar button:hover{background:#1f56cf}
    #pdf-toolbar .hint{margin:0;font-size:12px;color:#c7d0db}
    @media print{#pdf-toolbar{display:none!important}}
  </style>`;
  const toolbar = `<div id="pdf-toolbar">
    <button type="button" onclick="window.print()">🖨️ 导出 PDF</button>
    <span class="hint">点击后选择「另存为 PDF」即可下载（也可直接按 Ctrl/Cmd+P）</span>
  </div>`;
  const hasHead = /<\/head>/i.test(html);
  const hasBody = /<body[^>]*>/i.test(html);
  let out = html;
  if (hasHead) out = out.replace(/<\/head>/i, style + '</head>');
  if (hasBody) out = out.replace(/<body[^>]*>/i, (m) => m + toolbar);
  if (!hasHead) out = style + out;
  if (!hasBody) out = out + toolbar;
  return out;
}

function wireAiHtmlButton(container, report) {
  const b = container && container.querySelector('[data-ai-html]');
  if (b && report && report.html) b.onclick = () => openAiHtmlReport(report.html);
}

// 资金流资金×股价相关性 → 徽章/配色（红=坏 绿=好 灰=中性；divergence 兼容旧数据未知方向）
const FUNDFLOW_CORR = {
  positive: ['同涨', '#2f9e44'], negative: ['同跌', '#e03131'],
  bottom_divergence: ['底背离', '#2b8a3e'], top_divergence: ['顶背离', '#c92a2a'],
  divergence: ['背离', '#f08c00'], neutral: ['中性', '#8792a4'],
};

// 资金流窗口名 → 中文标签（分钟/天窗口；'day'/'week'/'month' 为后端归一标准名，不能用 parseInt 解析）
const FLOW_WIN_LABEL = { '1m': '1 分钟', '5m': '5 分钟', '15m': '15 分钟', '30m': '30 分钟', 'day': '日', 'week': '周', 'month': '月' };

// 渲染个股页最近落库资金流 AI 结果（标注来源 组合批量/个股分析 + 日期 + 窗口）
function renderFundflowPersistedPanel(el, r) {
  const a = r || {};
  const src = a.source === 'batch' ? '组合批量分析' : a.source === 'single' ? '个股分析' : '';
  const winLabel = (FLOW_WIN_LABEL[a.window] || a.window) + '窗口';
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
    const path = code ? '/stocks/' + code + '/refresh' : '/refresh';
    const job = await startBackgroundJob(path, body, { silent: true });
    await waitForJob(job.job_id);
  } catch (e) { /* 刷新失败不阻断，分析用缓存数据 */ }
}

// ============ AI 指令预览/编辑弹窗（9 个分析入口共用） ============
let _promptsCache = null;
// 各 AI 分析入口默认 system prompt + 用户已保存的自定义覆盖（后端单一来源，前端只读缓存）
async function getDefaultPrompts() {
  if (_promptsCache) return _promptsCache;
  try {
    const d = await api('/ai/prompts', { silent: true });
    _promptsCache = {
      defaults: (d && d.defaults) || {},
      saved: (d && d.saved) || {},
    };
  } catch (e) {
    _promptsCache = { defaults: {}, saved: {} };
  }
  return _promptsCache;
}

// 弹窗展示某入口的提示词（有已保存的自定义版本则默认显示它，否则显示默认），
// 用户可编辑/恢复默认/取消 + 选分析强度（快速/普通/深入）。
// 确定后 onConfirm(customPrompt|null, intensity)：内容与默认一致 → null（后端用默认）；
// 修改 → 自定义文本，并自动持久化（下次打开默认沿用，不用每次重改）。
async function openPromptEditor(kind, onConfirm) {
  const prompts = await getDefaultPrompts();
  const def = prompts.defaults[kind] || '';
  const savedVal = prompts.saved[kind] || '';
  const init = savedVal || def;
  const mask = document.createElement('div');
  mask.className = 'modal-mask';
  mask.innerHTML = `
    <div class="modal" style="width:680px;max-width:94vw">
      <h3>🤖 AI 要求</h3>
      <div class="muted" style="font-size:12px;margin-bottom:8px">以下是要发给 AI 的重点要求，可修改后确认；系统完整指令与分析数据会自动附加，不在弹窗展示。修改后的内容会自动保存，下次打开沿用。</div>
      <textarea rows="6" spellcheck="false" style="width:100%;box-sizing:border-box;font-family:inherit;font-size:13px;line-height:1.7;padding:8px">${esc(init)}</textarea>
      <div style="display:flex;align-items:center;gap:4px;margin:8px 0 4px;font-size:13px">
        <span class="muted" style="font-size:12px;margin-right:6px">分析强度</span>
        <label><input type="radio" name="peIntensity" value="fast"> 快速</label>
        <label style="margin-left:12px"><input type="radio" name="peIntensity" value="normal" checked> 普通</label>
        <label style="margin-left:12px"><input type="radio" name="peIntensity" value="deep"> 深入</label>
      </div>
      <div class="modal-actions">
        <span class="link muted" id="peCancel">取消</span>
        <span class="grow" style="flex:1"></span>
        <button class="btn" id="peReset" title="恢复为系统默认指令（同时清除已保存的自定义版本）">恢复默认</button>
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
    // 与默认一致（或清空）→ 用默认并清除已保存；不同 → 保存自定义版本（静默，失败不阻断分析）
    const custom = (v && v !== def) ? v : null;
    const ov = {};
    if (custom) { if (v !== savedVal) ov[kind] = v; }
    else if (savedVal) { ov[kind] = null; }
    if (Object.keys(ov).length) {
      api('/ai/prompts', { method: 'PUT', body: { overrides: ov } }).then(() => {
        prompts.saved[kind] = custom ? v : undefined;
      }).catch(() => {});
    }
    onConfirm(custom, inten);
  };
}

// 触发个股资金流 AI 分析并把结果渲染进 panel（先弹窗确认/编辑指令；skipEditor=true 一键跳过编辑直接用默认要求）
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
    const params = new URLSearchParams();
    if (scope === 'indices') params.set('codes', scopeKey || '');
    else params.set('tags', scopeKey || '');
    if (window) params.set('window', window);
    const r = await api('/ai/analyze-portfolio?' + params.toString(), { silent: true });
    return (r && r.flow) || null;
  } catch (e) {
    return null;
  }
}

// 渲染组合批量分析结果面板：顶部「组合相关性」块 + 每只一行 代码 名称 [相关性徽章] 结论
// d.coherence = {correlation,summary,points[],conclusion}；d.mode = 'indices'|'portfolio'
function renderFundflowBatchPanel(el, d) {
  const winLabel = FLOW_WIN_LABEL[d.window] || d.window;   // 后面拼「窗口」，此处不带后缀
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
async function runFundflowBatch({ tags, window, btn, panel, onDone, codes, weights, skipEditor, intensity, systemPrompt }) {
  const doRun = async (systemPrompt, intensity) => {
    btn.disabled = true;
    panel.style.display = 'block';
    panel.innerHTML = '<div class="empty" style="padding:10px;margin:0">🔄 准备资金流数据…</div>';
    try {
      const body = { types: ['flow'], window: window || '15m', intensity };
      if (systemPrompt) body.system_prompts = { flow: systemPrompt };
      if (codes && codes.length) {
        body.codes = codes.join(',');
      } else {
        body.tags = tags || '';
        await refreshFlowBeforeAnalysis('');
      }
      panel.innerHTML = '<div class="empty" style="padding:10px;margin:0">🤖 批量 AI 已提交，进度见顶部…</div>';
      const job = await startBackgroundJob('/ai/analyze-portfolio', body, { toast: '批量资金流 AI 已开始' });
      await waitForJob(job.job_id);
      panel.innerHTML = '<div class="empty" style="padding:10px;margin:0">✅ 批量分析完成，正在刷新…</div>';
      if (typeof onDone === 'function') onDone();
    } catch (e) {
      panel.innerHTML = `<div class="empty" style="padding:10px;margin:0">批量分析失败：${esc(e.message)}</div>`;
    } finally {
      btn.disabled = false;
    }
  };
  if (skipEditor) { await doRun(systemPrompt || null, intensity || 'normal'); return; }
  openPromptEditor('batch', doRun);
}

// ============ 消息面 / 技术面 AI 共享渲染（个股面板 + 组合批量列表） ============

// 消息面立场 / 技术面趋势徽章配色（A股口径：红=多/涨，绿=空/跌）
const NEWS_STANCE = {
  bullish: ['利多', '#e03131'], neutral: ['中性', '#8792a4'], bearish: ['利空', '#2f9e44'],
};
const TECH_TREND = {
  up: ['上行', '#e03131'], down: ['下行', '#2f9e44'], range: ['震荡', '#8792a4'],
};

function newsStanceBadge(r) {
  if (!r || !r.stance) return '<span class="muted">—</span>';
  const [label, color] = NEWS_STANCE[r.stance] || NEWS_STANCE.neutral;
  const src = r.source === 'batch' ? '组合批量' : r.source === 'single' ? '个股分析' : '';
  const t = `${r.summary || ''} · ${r.as_of || ''} · ${src}`;
  return `<span class="badge" style="color:#fff;background:${color};cursor:default" title="${esc(t)}">${label}</span>`;
}

function techTrendBadge(r) {
  if (!r || !r.trend_short) return '<span class="muted">—</span>';
  const [shortLabel, color] = TECH_TREND[r.trend_short] || TECH_TREND.range;
  const midLabel = r.trend_mid ? ((TECH_TREND[r.trend_mid] || TECH_TREND.range)[0]) : '';
  // 紧凑展示短期/中期，持仓表与批量列表共用
  const label = midLabel ? `短${shortLabel}/中${midLabel}` : shortLabel;
  const src = r.source === 'batch' ? '组合批量' : r.source === 'single' ? '个股分析' : '';
  const t = `短期${shortLabel}${midLabel ? ` · 中期${midLabel}` : ''} · ${r.summary || ''} · ${r.as_of || ''} · ${src}`;
  return `<span class="badge" style="color:#fff;background:${color};cursor:default" title="${esc(t)}">${label}</span>`;
}

// 渲染个股页消息面结果：d=null → 未分析空态；d.omit_reason → AI 判定无足够新信息（非错误样式）；否则完整报告
function renderNewsPanel(el, d) {
  if (!el) return;
  if (!d) {
    el.innerHTML = '<div class="empty" style="padding:14px;margin:0">尚未分析消息面。点上方「🤖 分析消息面」。</div>';
    return;
  }
  const src = d.source === 'batch' ? '组合批量分析' : '个股分析';
  const items = (d.items || []).map((it) => `
    <li style="margin:6px 0">
      <div style="font-size:13px"><b>${esc(it.headline || '')}</b>
        ${it.event_date ? `<span class="muted" style="font-size:11px;margin-left:6px">${esc(it.event_date)}</span>` : ''}
        ${it.impact ? `<span class="chip" style="margin-left:6px;font-size:11px">${esc(it.impact)}</span>` : ''}
      </div>
      ${it.summary ? `<div style="font-size:12px;color:#555;margin-top:2px;line-height:1.6">${esc(it.summary)}</div>` : ''}
    </li>`).join('');
  const risks = (d.risks || []).map((x) => `<li>${esc(x)}</li>`).join('');
  const hasContent = !!(d.summary || items || risks);
  // AI 因时效放弃且无任何实质内容 → 空态说明（非错误样式）
  if (d.omit_reason && !hasContent) {
    el.innerHTML = `
      <div style="border:1px solid var(--border);border-radius:var(--radius-sm);padding:10px 12px;background:#fff">
        <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
          ${newsStanceBadge(d)}
          <span class="muted" style="font-size:12px">${src} · ${d.as_of || ''}</span>
        </div>
        <div style="font-size:13px;margin-top:8px">AI 判定当前无近期可确认的新闻事件（基于模型公开知识，非实时新闻源，时效由 AI 自行判定）。</div>
        <div style="font-size:12px;color:#868e96;margin-top:4px">${esc(d.omit_reason)}</div>
      </div>`;
    return;
  }
  // 完整报告：立场徽章 + 摘要 + 事件/风险；omit_reason 作为「时效说明」附注，不与立场冲突
  el.innerHTML = `
    <div style="border:1px solid var(--border);border-radius:var(--radius-sm);padding:10px 12px;background:#fff">
      <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
        ${newsStanceBadge(d)}
        <span class="muted" style="font-size:12px">${src} · ${d.as_of || ''}</span>
        ${d.html ? aiHtmlButton() : ''}
      </div>
      ${d.summary ? `<div style="font-size:14px;font-weight:700;margin:8px 0 4px">${esc(d.summary)}</div>` : ''}
      ${d.omit_reason ? `<div style="font-size:12px;color:#e8590c;margin:6px 0"><b>📌 时效说明</b>：${esc(d.omit_reason)}</div>` : ''}
      ${items ? `<div style="margin:7px 0"><b style="font-size:12.5px">📅 近期事件</b><ul style="margin:4px 0 0 18px;font-size:13px">${items}</ul></div>` : ''}
      ${risks ? `<div style="margin:7px 0"><b style="font-size:12.5px;color:var(--red)">⚠️ 风险</b><ul style="margin:4px 0 0 18px;font-size:13px">${risks}</ul></div>` : ''}
    </div>`;
  wireAiHtmlButton(el, d);
}

// 渲染个股页技术面结果：d=null → 未分析空态；否则完整报告（短/中趋势 + 关键位 + 白话信号 + 证伪条件）
function renderTechPanel(el, d) {
  if (!el) return;
  if (!d) {
    el.innerHTML = '<div class="empty" style="padding:14px;margin:0">尚未分析技术面。点上方「🤖 分析技术面」。</div>';
    return;
  }
  const src = d.source === 'batch' ? '组合批量分析' : '个股分析';
  const [shortLabel, shortColor] = TECH_TREND[d.trend_short] || TECH_TREND.range;
  const [midLabel, midColor] = TECH_TREND[d.trend_mid] || TECH_TREND.range;
  const lv = d.key_levels || {};
  const support = (lv.support || []).map((x) => `<span class="chip">支撑 ${esc(x)}</span>`).join(' ');
  const resistance = (lv.resistance || []).map((x) => `<span class="chip">压力 ${esc(x)}</span>`).join(' ');
  const signals = (d.signals || []).map((x) => `<li>${esc(x)}</li>`).join('');
  el.innerHTML = `
    <div style="border:1px solid var(--border);border-radius:var(--radius-sm);padding:10px 12px;background:#fff">
      <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
        <span class="badge" style="color:#fff;background:${shortColor}">短期 ${shortLabel}</span>
        <span class="badge" style="color:#fff;background:${midColor}">中期 ${midLabel}</span>
        <span class="muted" style="font-size:12px">${src} · ${d.as_of || ''}</span>
        ${d.html ? aiHtmlButton() : ''}
      </div>
      ${d.summary ? `<div style="font-size:14px;font-weight:700;margin:8px 0 4px">${esc(d.summary)}</div>` : ''}
      ${(support || resistance) ? `<div style="margin:7px 0"><b style="font-size:12.5px">🔑 关键价位</b><div style="margin-top:4px;display:flex;gap:8px;flex-wrap:wrap">${resistance}${support}</div></div>` : ''}
      ${signals ? `<div style="margin:7px 0"><b style="font-size:12.5px">💬 白话信号</b><ul style="margin:4px 0 0 18px;font-size:13px">${signals}</ul></div>` : ''}
      ${d.invalidation ? `<div style="margin:7px 0;font-size:12px;color:#e8590c"><b>证伪条件：</b>${esc(d.invalidation)}</div>` : ''}
    </div>`;
  wireAiHtmlButton(el, d);
}

// 组合批量列表通用渲染：rows 每行含 code/name/summary；badge(r) 输出徽章；
// fragment 为个股页对应面板锚点（如 '#newsAiPanel' / '#techAiPanel'），点击跳转对应面板；
// coherence={summary,html} 为整组合整体输出（深入模式），非空时顶部展示 + 📄 按钮
function renderBatchListPanel(el, title, rows, badge, fragment, coherence) {
  if (!el) return;
  const frag = fragment || '';
  const coh = coherence || {};
  const cohHtml = (coh.summary || coh.html) ? `
    <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap;background:rgba(37,99,235,.04);border:1px solid #b9cdff;border-radius:8px;padding:8px 10px;margin-bottom:8px">
      <span style="font-size:13px;font-weight:700">整组合${title.replace('批量分析', '')}</span>
      ${coh.summary ? `<span style="flex:1;font-size:12px;color:var(--muted,#868e96);min-width:160px">${esc(coh.summary)}</span>` : ''}
      ${coh.html ? aiHtmlButton() : ''}
    </div>` : '';
  el.innerHTML = `
    <div style="border:1px solid var(--border);border-radius:var(--radius-sm);padding:10px 12px;background:#fff">
      ${cohHtml}
      <div style="font-size:13px;font-weight:700;margin-bottom:6px">${title}（${rows.length} 只）</div>
      ${rows.map((r) => `
        <div style="display:flex;align-items:center;gap:10px;padding:4px 0;border-bottom:1px dashed var(--border)">
          <a href="/static/stock.html?code=${r.code}${frag}" style="white-space:nowrap;min-width:120px">${r.code} ${esc(r.name || '')}</a>
          ${badge(r)}
          <span style="flex:1;font-size:13px">${esc(r.summary || '')}</span>
        </div>`).join('') || '<div class="muted">无分析结果</div>'}
    </div>`;
  wireAiHtmlButton(el, coh);   // 📄 按钮（整组合深入 html 非空时绑定）
}

// 一次拉取多只最近落库消息面/技术面报告 map：{code:{stance|trend_short,summary,as_of,source}}
async function loadNewsReports(codes) {
  if (!codes || !codes.length) return {};
  try {
    return (await api('/ai/news-reports?codes=' + encodeURIComponent(codes.join(',')), { silent: true })) || {};
  } catch (e) { return {}; }
}

async function loadTechReports(codes) {
  if (!codes || !codes.length) return {};
  try {
    return (await api('/ai/tech-reports?codes=' + encodeURIComponent(codes.join(',')), { silent: true })) || {};
  } catch (e) { return {}; }
}

// 批量分析消息面：POST 批量端点 → onDone 重载持久化面板（先弹窗确认/编辑指令）
// ============ AI 扩展分析一键勾选弹窗（个股/组合共用） ============
// items: [{key, label, checked}]；onRun(keys) 逐个执行选中项。
function openAiExtPicker({ items, onRun, note, withIntensity, title }) {
  const mask = document.createElement('div');
  mask.className = 'modal-mask';
  const overrides = {};   // key → 用户改过的提示词（null=恢复默认）
  const editPrompt = (it) => {
    if (!it.promptKind) return;
    openPromptEditor(it.promptKind, (custom) => { overrides[it.key] = custom || null; });
  };
  mask.innerHTML = `
    <div class="modal" style="width:520px;max-width:94vw">
      <h3>${title || '🤖 AI 分析'}</h3>
      <div class="muted" style="font-size:12px;margin-bottom:10px">${note || '勾选要分析的内容（默认不勾），将逐个进行分析，进度见顶部；每项可单独「改提示词」。'}</div>
      <div>
        ${(items || []).map((it) => `
          <label style="display:flex;align-items:center;gap:8px;padding:9px 12px;margin:5px 0;border:1px solid var(--border);border-radius:var(--radius-sm);cursor:pointer;font-size:13px">
            <input type="checkbox" data-key="${it.key}" ${it.checked === false ? '' : 'checked'}>
            <b>${esc(it.label)}</b>${it.desc ? `<span class="muted" style="font-size:12px">${esc(it.desc)}</span>` : ''}
            <span class="grow" style="flex:1"></span>
            ${it.promptKind ? `<button type="button" class="btn" data-prompt-edit="${it.key}" style="padding:2px 10px;font-size:12px">✏️ 改提示词</button>` : ''}
          </label>`).join('')}
      </div>
      ${withIntensity ? `
      <div style="display:flex;align-items:center;gap:8px;margin-top:12px;font-size:13px">
        <b>分析强度</b>
        <select id="extPickerIntensity" style="padding:5px 8px;border:1px solid var(--border);border-radius:var(--radius-sm);background:var(--bg)">
          <option value="fast">快速</option>
          <option value="normal" selected>普通</option>
          <option value="deep">深入（HTML 详细报告）</option>
        </select>
      </div>` : ''}
      <div class="modal-actions" style="margin-top:14px">
        <button class="btn" id="extPickerCancel">取消</button>
        <span class="grow" style="flex:1"></span>
        <button class="btn primary" id="extPickerStart">开始分析</button>
      </div>
    </div>`;
  document.body.appendChild(mask);
  (items || []).forEach((it) => {
    const b = mask.querySelector(`[data-prompt-edit="${it.key}"]`);
    if (b) b.onclick = (e) => { e.preventDefault(); e.stopPropagation(); editPrompt(it); };
  });
  const close = () => mask.remove();
  mask.querySelector('#extPickerCancel').onclick = close;
  mask.querySelector('#extPickerStart').onclick = async () => {
    const keys = [...mask.querySelectorAll('input[data-key]:checked')].map((i) => i.dataset.key);
    const intensity = withIntensity ? (mask.querySelector('#extPickerIntensity') || {}).value : null;
    close();
    if (!keys.length) return toast('请至少勾选一项');
    if (typeof onRun === 'function') await onRun(keys, intensity, overrides);
  };
  mask.addEventListener('click', (e) => { if (e.target === mask) close(); });
}


// 批量分析消息面：POST 批量端点 → onDone 重载持久化面板（先弹窗确认/编辑指令；skipEditor=true 一键跳过编辑直接用默认要求）
async function runNewsBatch({ tags, btn, panel, onDone, codes, skipEditor, intensity, systemPrompt }) {
  const doRun = async (systemPrompt, intensity) => {
    btn.disabled = true;
    panel.style.display = 'block';
    panel.innerHTML = '<div class="empty" style="padding:10px;margin:0">🤖 批量消息面 AI 已提交，进度见顶部…</div>';
    try {
      const body = { types: ['news'], intensity };
      if (systemPrompt) body.system_prompts = { news: systemPrompt };
      if (codes && codes.length) body.codes = codes.join(',');
      else body.tags = tags || '';
      const job = await startBackgroundJob('/ai/analyze-portfolio', body, { toast: '批量消息面 AI 已开始' });
      await waitForJob(job.job_id);
      panel.innerHTML = '<div class="empty" style="padding:10px;margin:0">✅ 批量分析完成，正在刷新…</div>';
      if (typeof onDone === 'function') onDone();
    } catch (e) {
      panel.innerHTML = /AI 模型/.test(e.message)
        ? aiNotConfiguredHtml()
        : `<div class="empty" style="padding:10px;margin:0">批量分析失败：${esc(e.message)}</div>`;
    } finally {
      btn.disabled = false;
    }
  };
  if (skipEditor) { await doRun(systemPrompt || null, intensity || 'normal'); return; }
  openPromptEditor('news_batch', doRun);
}

// 批量分析技术面：POST 批量端点 → onDone 重载持久化面板（先弹窗确认/编辑指令；skipEditor=true 一键跳过编辑直接用默认要求）
async function runTechBatch({ tags, btn, panel, onDone, codes, skipEditor, intensity, systemPrompt }) {
  const doRun = async (systemPrompt, intensity) => {
    btn.disabled = true;
    panel.style.display = 'block';
    panel.innerHTML = '<div class="empty" style="padding:10px;margin:0">🤖 批量技术面 AI 已提交，进度见顶部…</div>';
    try {
      const body = { types: ['tech'], intensity };
      if (systemPrompt) body.system_prompts = { tech: systemPrompt };
      if (codes && codes.length) body.codes = codes.join(',');
      else body.tags = tags || '';
      const job = await startBackgroundJob('/ai/analyze-portfolio', body, { toast: '批量技术面 AI 已开始' });
      await waitForJob(job.job_id);
      panel.innerHTML = '<div class="empty" style="padding:10px;margin:0">✅ 批量分析完成，正在刷新…</div>';
      if (typeof onDone === 'function') onDone();
    } catch (e) {
      panel.innerHTML = /AI 模型/.test(e.message)
        ? aiNotConfiguredHtml()
        : `<div class="empty" style="padding:10px;margin:0">批量分析失败：${esc(e.message)}</div>`;
    } finally {
      btn.disabled = false;
    }
  };
  if (skipEditor) { await doRun(systemPrompt || null, intensity || 'normal'); return; }
  openPromptEditor('tech_batch', doRun);
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

// ---------- 按日期查看（估值 + 资金流分时） ----------

function todayISO() {
  const d = new Date();
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, '0');
  const day = String(d.getDate()).padStart(2, '0');
  return `${y}-${m}-${day}`;
}

function shiftISO(iso, days) {
  const d = new Date(iso + 'T12:00:00');
  d.setDate(d.getDate() + days);
  return d.toISOString().slice(0, 10);
}

/** 周一→周五吸附；周末退到上周五（与后端 resolve_trade_day 工作日近似一致）。 */
function lastWeekdayISO(iso) {
  const d = new Date((iso || todayISO()) + 'T12:00:00');
  while (d.getDay() === 0 || d.getDay() === 6) d.setDate(d.getDate() - 1);
  return d.toISOString().slice(0, 10);
}

function asOfQuery(asOf) {
  return asOf ? '&as_of=' + encodeURIComponent(asOf) : '';
}

function asOfEmptyHint(meta, kind) {
  /** kind: 'flow' | 'valuation' */
  if (!meta) return '';
  if (kind === 'flow') {
    if (meta.market_status === 'pre_open') {
      return `今日尚未开盘，已自动按上一交易日 ${meta.as_of}。该日没有分时数据（当时未刷新采集），可切到「1天」看近45日走势。`;
    }
    if (meta.as_of_adjusted) {
      return `所选日期非交易日，已自动切到 ${meta.as_of}。该日没有分时数据（当时未刷新采集），可切到「1天」看近45日走势。`;
    }
    return `${meta.as_of || '该日'}没有分时数据（当时未刷新采集），可切到「1天」看近45日走势。`;
  }
  if (meta.market_status === 'pre_open') {
    return `今日尚未开盘，估值按上一交易日 ${meta.as_of}。这天还没有估值数据，试试其它日期或做一次完整更新。`;
  }
  if (meta.as_of_adjusted) {
    return `所选日期非交易日，已自动切到 ${meta.as_of}。这天还没有估值数据，试试其它日期或做一次完整更新。`;
  }
  return `${meta.as_of || '该日'}还没有估值数据，试试其它日期或做一次完整更新。`;
}

/**
 * 挂载日期选择条。value=null 表示「最新」（不传 as_of）；选日期后 onChange(iso)。
 * container: DOM 节点；会清空并写入控件。
 */
function mountAsOfPicker(container, { value, onChange, label } = {}) {
  if (!container) return;
  const cur = value || '';
  const isLive = !cur;
  container.innerHTML = `
    <div class="row" style="gap:8px;align-items:center;flex-wrap:wrap;margin:0;max-width:100%">
      <span class="muted asof-label" style="font-size:13px;min-width:0">${esc(label || '按日期查看')}</span>
      <div style="display:flex;align-items:center;gap:6px;flex-wrap:nowrap;min-width:0;flex:1 1 auto;max-width:100%">
        <button type="button" class="btn${isLive ? ' primary' : ''}" data-asof="live" style="padding:2px 8px;font-size:12px;border-radius:999px;flex:0 0 auto;white-space:nowrap">最新</button>
        <button type="button" class="btn" data-asof="yesterday" style="padding:2px 8px;font-size:12px;border-radius:999px;flex:0 0 auto;white-space:nowrap">昨天</button>
        <input type="date" class="asof-input" data-asof-input value="${esc(cur)}" max="${todayISO()}"
               style="padding:2px 8px;font-size:12px;border:1px solid var(--border,#e5e7eb);border-radius:999px;min-width:0;width:130px;max-width:100%">
      </div>
      <span class="muted asof-hint" data-asof-hint style="font-size:12px;min-width:0;word-break:break-word;overflow-wrap:anywhere"></span>
    </div>`;
  const emit = (iso) => {
    if (typeof onChange === 'function') onChange(iso);
  };
  container.querySelector('[data-asof="live"]').onclick = () => emit(null);
  container.querySelector('[data-asof="yesterday"]').onclick = () => {
    emit(lastWeekdayISO(shiftISO(todayISO(), -1)));
  };
  const inp = container.querySelector('[data-asof-input]');
  inp.onchange = () => {
    if (!inp.value) { emit(null); return; }
    emit(lastWeekdayISO(inp.value));
  };
}

function setAsOfHint(container, meta) {
  const hint = container && container.querySelector('[data-asof-hint]');
  if (!hint) return;
  if (!meta || !meta.as_of) { hint.textContent = ''; return; }
  if (meta.hist_view) {
    hint.textContent = meta.as_of_adjusted
      ? `截至 ${meta.as_of}（已从非交易日自动调整）`
      : `截至 ${meta.as_of}`;
  } else if (meta.market_status === 'pre_open') {
    hint.textContent = `今日未开盘，分时/估值按最近交易日 ${meta.as_of}`;
  } else if (meta.as_of_adjusted) {
    hint.textContent = `分时按最近交易日 ${meta.as_of}`;
  } else {
    hint.textContent = '';
  }
}

// ---- 后端日志入口（App 内排查）：右下角悬浮按钮 → logs.html ----
(function () {
  if (window.__logsBtnInjected) return;
  window.__logsBtnInjected = true;
  const inject = () => {
    if (document.getElementById('logsFloatBtn')) return;
    const b = document.createElement('button');
    b.id = 'logsFloatBtn';
    b.textContent = '🛠 日志';
    b.title = '查看后端运行日志（排查问题用）';
    b.style.cssText =
      'position:fixed;right:14px;bottom:70px;z-index:9999;padding:6px 10px;font-size:12px;' +
      'border-radius:16px;border:1px solid #33415f;background:rgba(22,32,58,.85);color:#c8d3e0;' +
      'cursor:pointer;opacity:.5;box-shadow:0 2px 6px rgba(0,0,0,.35);';
    b.onmouseenter = () => { b.style.opacity = '1'; };
    b.onmouseleave = () => { b.style.opacity = '.5'; };
    b.onclick = () => window.open('/static/logs.html', '_blank');
    document.body.appendChild(b);
  };
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', inject);
  } else {
    inject();
  }
})();

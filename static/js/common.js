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
/** AI 评级色块 A/B/C/D（折叠摘要专用，展开态仍用 .badge） */
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
  aiBtn.title = '配置 AI（推荐一键填入 DeepSeek）';
  aiBtn.onclick = openAiSettings;
  nav.appendChild(aiBtn);

  const dynamic = document.createElement('button');
  dynamic.textContent = '⚡ 更新行情';
  dynamic.title = '快速更新：现价、估值、今日资金流（大约几十秒）';
  dynamic.onclick = () => doRefresh(dynamic, false);
  nav.appendChild(dynamic);

  const full = document.createElement('button');
  full.textContent = '🔄 完整更新';
  full.className = 'primary';
  full.title = '完整更新：历史行情、财务、估值分位、汇率等（首次或隔很久建议点一次）';
  full.onclick = () => doRefresh(full, true);
  nav.appendChild(full);

  const status = document.createElement('span');
  status.id = 'statusText';
  nav.appendChild(status);

  setupPrewarmBar(nav);
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
      <span class="prewarm-actions">
        <span id="prewarmPct" class="prewarm-pct"></span>
        <button type="button" class="job-toggle-btn" id="jobQueueToggle" style="display:none">任务</button>
        <button type="button" class="job-cancel-btn" id="jobCancelPrimary" title="取消" style="display:none">取消</button>
      </span>
    </div>
    <div class="progress-track prewarm-track">
      <div class="progress-fill" id="prewarmFill"></div>
    </div>
    <div id="jobQueueList" class="job-queue-list" style="display:none"></div>`;
  nav.insertAdjacentElement('afterend', bar);
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
    qList.style.display = queueOpen ? 'block' : 'none';
    toggleBtn.classList.toggle('on', queueOpen);
    tick();
  };

  function fireDone(detail) {
    const id = detail.job_id;
    if (!id || _jobBarSeenIds.has(id + ':' + (detail.status || detail.ok))) return;
    _jobBarSeenIds.add(id + ':' + (detail.status || detail.ok));
    window.dispatchEvent(new CustomEvent('app-job-done', { detail }));
    const kind = detail.kind || '';
    const ok = detail.ok !== false && detail.status !== 'cancelled' && !detail.error;
    if (ok && (kind.startsWith('refresh') || kind.startsWith('index.refresh'))) {
      if (typeof loadPage === 'function') loadPage();
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
          <span class="job-queue-label"><span class="job-queue-st">${st}</span>${j.label || j.kind}</span>
          ${x}
        </div>`;
      }).join('');
    qList.querySelectorAll('button[data-id]').forEach(btn => {
      btn.onclick = () => cancelJob(btn.dataset.id, false);
    });
    qList.style.display = 'block';
  }

  async function tick() {
    try {
      const p = await api('/status/jobs', { silent: true });
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

        txt.textContent = `${label} ${cur}/${tot}：${step}`;
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
        setTimeout(tick, 1000);
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
        bar.classList.toggle('done', ok);
        bar.classList.toggle('error', !ok);
        bar.style.display = 'block';
        txt.textContent = ok ? (label + '完成') : (label + (last && last.status === 'cancelled' ? '已取消' : '失败'));
        pctEl.textContent = ok ? '100%' : '';
        fill.style.width = ok ? '100%' : '0%';
        if (last && last.status === 'cancelled') toast(label + '已取消');
        else if (ok) toast(label + '完成');
        else toast((last && last.error) || label + '失败', 4000);
        const st = document.getElementById('statusText');
        if (st) { st.style.display = ''; st.textContent = ''; }
        _jobBarHideTimer = setTimeout(() => { bar.style.display = 'none'; }, 4000);
        setTimeout(tick, 2000);
      } else {
        setTimeout(tick, 2500);
      }
    } catch (e) {
      setTimeout(tick, 3000);
    }
  }
  window.kickJobBar = () => { _jobBarActive = true; tick(); };
  tick();
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
    const finish = (ok, detail, err) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      clearInterval(poll);
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
    const timer = setTimeout(() => finish(false, null, '任务超时'), timeoutMs);
    const poll = setInterval(async () => {
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

async function doRefresh(btn, full, scope = 'global', code) {
  const items = await chooseRefreshItems(full, scope);
  if (!items || !items.length) return; // 用户取消或未勾选任何内容
  btn.disabled = true;
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
        // 读完整 JSON（含 lists_ready）；api() 只返回 data，看不到预热失败提示
        const res = await fetch('/api/stocks/search?q=' + encodeURIComponent(q) + '&limit=10');
        const json = await res.json().catch(() => ({}));
        if (!res.ok) throw new Error(json.detail || json.msg || `HTTP ${res.status}`);
        const rows = json.data || [];
        sug.innerHTML = '';
        if (!rows.length) {
          if (json.lists_ready === false) {
            const tip = document.createElement('div');
            tip.className = 'stock-sug-item';
            tip.style.cssText = 'color:#888;cursor:default';
            tip.textContent = '市场列表未就绪：请退出后重新打开应用完成预热';
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

// DeepSeek 一键模板：用户只需填 API Key，其余写死推荐值
const DEEPSEEK_PRESET = {
  name: 'DeepSeek',
  base_url: 'https://api.deepseek.com',
  model: 'deepseek-chat',
  docs_keys: 'https://platform.deepseek.com/api_keys',
  docs_guide: 'https://api-docs.deepseek.com/zh-cn/',
};

async function openAiSettings() {
  const mask = document.createElement('div');
  mask.className = 'modal-mask';
  mask.innerHTML = `
    <div class="modal" style="width:620px;max-width:94vw">
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
      <div id="aiModelList" style="max-height:180px;overflow-y:auto;margin-bottom:12px"></div>

      <div class="ai-preset">
        <div class="ai-preset-title">推荐：一键启用 DeepSeek</div>
        <ol class="ai-preset-steps">
          <li>打开
            <a href="${DEEPSEEK_PRESET.docs_keys}" target="_blank" rel="noopener">DeepSeek 控制台</a>
            注册/登录，创建「API Key」
          </li>
          <li>把 Key 粘贴到下方，点「一键启用」（地址和模型已帮你填好）</li>
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

  mask.querySelector('#aiDsEnable').onclick = async () => {
    const apiKey = mask.querySelector('#aiDsKey').value.trim();
    if (!apiKey) return toast('请先粘贴 DeepSeek 的 API Key');
    const btn = mask.querySelector('#aiDsEnable');
    btn.disabled = true;
    try {
      const row = await api('/ai/models', {
        method: 'POST',
        body: {
          name: DEEPSEEK_PRESET.name,
          base_url: DEEPSEEK_PRESET.base_url,
          api_key: apiKey,
          model: DEEPSEEK_PRESET.model,
        },
      });
      await api('/ai/models/' + row.id + '/activate', { method: 'POST' });
      mask.querySelector('#aiDsKey').value = '';
      toast('DeepSeek 已启用，可以开始 AI 分析了');
      await loadAiModels(mask);
    } catch (e) {
      toast('启用失败：' + e.message, 4000);
    } finally {
      btn.disabled = false;
    }
  };

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
      : '<span style="color:#e03131">还没启用 AI。推荐用上方 DeepSeek 一键启用，只需填密钥。</span>';
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
        <button class="btn" ${m.is_active ? 'disabled' : ''} data-act="${m.id}">启用</button>
        <button class="btn" data-edit="${m.id}">编辑</button>
        <button class="btn danger" data-del="${m.id}">删除</button>`;
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
  return '<div class="empty" style="padding:16px;margin:0">还没配置 AI。点右上角「🤖 AI」，用 DeepSeek 一键启用即可（只需填密钥）。</div>';
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
    const path = code ? '/stocks/' + code + '/refresh' : '/refresh';
    const job = await startBackgroundJob(path, body, { silent: true });
    await waitForJob(job.job_id);
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
      panel.innerHTML = '<div class="empty" style="padding:10px;margin:0">🤖 AI 已提交，进度见顶部…</div>';
      const body = { code: code || '', window: window || '15m', intensity };
      if (systemPrompt) body.system_prompt = systemPrompt;
      const job = await startBackgroundJob('/ai/fundflow-analysis', body, { toast: '资金流 AI 已开始' });
      await waitForJob(job.job_id);
      const d = await api('/ai/fundflow-report/' + encodeURIComponent(code) + '?window=' + encodeURIComponent(window || '15m'), { silent: true });
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
      panel.innerHTML = '<div class="empty" style="padding:10px;margin:0">🤖 批量 AI 已提交，进度见顶部…</div>';
      const job = await startBackgroundJob('/ai/fundflow-batch', body, { toast: '批量资金流 AI 已开始' });
      await waitForJob(job.job_id);
      // 完成后由页面 onDone 重载持久化面板
      panel.innerHTML = '<div class="empty" style="padding:10px;margin:0">✅ 批量分析完成，正在刷新…</div>';
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

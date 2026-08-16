/** fetch 封装：统一 /api 前缀、JSON 解包、错误抛出（错误带 status 与结构化 body）。
 *  全屏 loading 屏障只用于「耗时操作」：opts.blocking = true 的请求（刷新/导入/下载）
 *  显示全屏 spinner，全部完成后隐藏；快速操作（保存/交易/弹窗查询）默认不弹屏障。
 *  刷新进度用 showProgressLoading / updateProgressLoading / hideProgressLoading。 */
let _pending = 0;
let _loadingEl = null;

function _showLoading() {
  _pending += 1;
  if (_loadingEl) return;
  _loadingEl = document.createElement('div');
  _loadingEl.className = 'loading-mask';
  _loadingEl.innerHTML = '<div class="loading-spinner"></div>';
  document.body.appendChild(_loadingEl);
}

function _hideLoading() {
  _pending = Math.max(0, _pending - 1);
  if (_pending === 0 && _loadingEl) {
    _loadingEl.remove();
    _loadingEl = null;
  }
}

/** 带文案 + 进度条的刷新遮罩（与 Excel 导入同款进度条样式）。 */
function showProgressLoading(text, pct) {
  if (!_loadingEl) {
    _loadingEl = document.createElement('div');
    _loadingEl.className = 'loading-mask';
    _loadingEl.innerHTML = `
      <div class="loading-progress-box">
        <div class="loading-spinner"></div>
        <div class="loading-progress-text" id="loadingProgressText"></div>
        <div class="progress-track loading-progress-track">
          <div class="progress-fill" id="loadingProgressFill"></div>
        </div>
      </div>`;
    document.body.appendChild(_loadingEl);
    _pending += 1;
  }
  updateProgressLoading(text, pct);
}

function updateProgressLoading(text, pct) {
  if (!_loadingEl) return;
  const t = _loadingEl.querySelector('#loadingProgressText');
  const f = _loadingEl.querySelector('#loadingProgressFill');
  if (t && text != null) t.textContent = text;
  if (f && pct != null) f.style.width = Math.max(0, Math.min(100, pct)) + '%';
}

function hideProgressLoading() {
  if (_loadingEl) {
    _loadingEl.remove();
    _loadingEl = null;
  }
  _pending = 0;
}

/** 消费刷新 NDJSON 流，返回 done 行；onProgress(msg) 可选。 */
async function consumeRefreshStream(path, body, onProgress) {
  const res = await fetch('/api' + path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body || {}),
  });
  if (!res.ok) {
    const j = await res.json().catch(() => ({}));
    const err = new Error(j.detail || j.msg || `HTTP ${res.status}`);
    err.status = res.status;
    err.body = j;
    throw err;
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  let buf = '';
  let doneMsg = null;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += decoder.decode(value, { stream: true });
    let idx;
    while ((idx = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, idx).trim();
      buf = buf.slice(idx + 1);
      if (!line) continue;
      let m;
      try { m = JSON.parse(line); } catch (e) { continue; }
      if (typeof onProgress === 'function') onProgress(m);
      if (m.status === 'done') doneMsg = m;
    }
  }
  if (!doneMsg) throw new Error('刷新未返回完成事件');
  return doneMsg;
}

async function api(path, opts = {}) {
  const showMask = !!opts.blocking;
  if (showMask) _showLoading();
  try {
    const isForm = opts.body instanceof FormData;
    const cfg = { ...opts };
    delete cfg.silent;
    delete cfg.blocking;
    if (isForm) {
      // FormData：不手动设 Content-Type（浏览器自动带 multipart boundary）
      delete cfg.headers;
    } else {
      cfg.headers = { 'Content-Type': 'application/json', ...(opts.headers || {}) };
      if (cfg.body && typeof cfg.body !== 'string') cfg.body = JSON.stringify(cfg.body);
    }
    const res = await fetch('/api' + path, cfg);
    const json = await res.json().catch(() => ({}));
    if (!res.ok) {
      const err = new Error(json.detail || json.msg || `HTTP ${res.status}`);
      err.status = res.status;
      err.body = json;
      throw err;
    }
    return json.data;
  } finally {
    if (showMask) _hideLoading();
  }
}

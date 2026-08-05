/** fetch 封装：统一 /api 前缀、JSON 解包、错误抛出（错误带 status 与结构化 body）。
 *  全屏 loading 屏障只用于「耗时操作」：opts.blocking = true 的请求（页面加载/刷新/导入/下载）
 *  显示全屏 spinner，全部完成后隐藏；快速操作（保存/交易/弹窗查询）默认不弹屏障。 */
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

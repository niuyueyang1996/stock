/** fetch 封装：统一 /api 前缀、JSON 解包、错误抛出。 */
async function api(path, opts = {}) {
  const cfg = { headers: { 'Content-Type': 'application/json' }, ...opts };
  if (cfg.body && typeof cfg.body !== 'string') cfg.body = JSON.stringify(cfg.body);
  const res = await fetch('/api' + path, cfg);
  const json = await res.json().catch(() => ({}));
  if (!res.ok) throw new Error(json.detail || json.msg || `HTTP ${res.status}`);
  return json.data;
}

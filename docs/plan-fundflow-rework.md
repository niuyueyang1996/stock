# 分时资金流改造：修 1/5 切换、去主力、五档改 5 档、新增买盘/卖盘图

> 实施计划（尚未实施）。用户已确认：主力三处全去掉、五档分位 P15/P40/P75/P95、买盘/卖盘用累积金额线、评分 fund_flow 因子一并移除。

## Context

个股页分时资金流（腾讯分笔，最近新增）存在 4 个问题/需求：

1. **1/5 分钟切换经常不能用**：根因是 `stock.html::loadPage()`（line 491）请求 `/stocks/{code}` **不带 `?window=1`**，后端默认 `window=15`，于是 `fundflow_15m` 返回 15 分钟粒度数据。前端把这份数据当"1 分钟基础"，`resampleFlow(base, 1)` 对 `window<=1` 原样返回 → "1分钟"标签显示的就是 15 分钟的点（看起来没反应）；`resampleFlow(base, 5)` 把 15 分钟点映射到自己 → 也是同样 16 个点。刷新/存标签/录交易/应用预期后都走 `loadPage`，所以"经常"。首次打开 `openStock`（line 196）带了 `?window=1` 所以正常。15/30 因为恰好在 15 分钟基础上能继续合并，显得"能用"。
2. **去掉主力资金（三处全去掉，用户确认）**：分时累积图的「主力」线、日级五档柱的「主力」行、近30日主力历史图（后者改成「近30日净流入」，用总净流入 netamount）。
3. **五档改 特大/大/中/小/特小**，仍按自适应分位，**用户指定切分 P15/P40/P75/P95**（特小<P15、小15~40、中40~75、大75~95、特大>P95）。
4. **新增「买盘/卖盘」折线图**：两根线（买盘累积金额、卖盘累积金额），与现有分时图共用 1/5/15/30 切换。
5. **评分因子一起去掉（用户确认）**：`fund_flow`（权重 0.15）从 BUY/SELL 权重与 `_score_factors` 中移除，剩余权重按比例重新归一化到 1.0。

约束：评分/AI 直接读 `daily_fundflow_cache`（非 API），日级 `main_net/main_net_pct` 列保留（`app/services/ai.py` 上下文仍用 main_net；评分因子移除后不再读取）。

## 后端改动

### app/data/fundflow.py（核心算法）
- `FundflowPoint` 末尾追加默认字段：`xs_net=0.0`、`buy_amount=0.0`、`sell_amount=0.0`（保持现有位置参数构造兼容）。
- `compute_quantiles` → 返回 `(p15, p40, p75, p95)`；空样本 `(0,0,0,0)`。
- `classify_tick(amount, p15, p40, p75, p95)`：`>p95`→super、`>p75`→large、`>p40`→medium、`>p15`→small、否则 xs（边界归下档，与现 P95→large 一致）。
- `aggregate_ticks`：桶增加 `xs` 档；逐笔分类用 5 档；另按桶累加 `buy_amount`（sign>0 的 amount 求和）与 `sell_amount`（sign<0 求和），中性盘 sign=0 两者都不计；输出 `xs_net/buy_amount/sell_amount`。`main_net` 仍 = super+large（供评分/AI 派生，API 不再下发）。
- `resample_points`：桶累加从 4 项扩到 7 项（sp/lg/md/sm/xs/buy/sell）。
- `tick_bands` → `{"p15","p40","p75","p95"}`。
- `ticks_to_day`：`tot` 加 `xs`，`net = sp+lg+md+sm+xs`；`main/main_net_pct` 不变；`FundflowDay` 加 `xs_net`。

### app/data/base.py
- `FundflowDay` 末尾加 `xs_net: float = 0.0`。

### app/models/db.py（迁移 v4）
- `_CURRENT_VERSION = 4`。
- `_SCHEMA` 新表定义同步加列：
  - `daily_fundflow_cache`：`xs_net REAL`、`p15 REAL`、`p40 REAL`、`p75 REAL`（`p95` 已存在；旧 `p50/p80` 列保留不用）。
  - `fundflow_15m_cache`：`xs_net REAL`、`buy_amount REAL`、`sell_amount REAL`。
- `_MIGRATE_COLUMNS[4]`：同上 7 条 `ALTER TABLE ... ADD COLUMN`（`migrate_db` 每列独立捕获 OperationalError，新库跳过）。

### app/data/cache.py
- `upsert_daily_fundflow`：INSERT/UPDATE 加 `xs_net`（`getattr(flow, "xs_net", 0.0)`）与 `bands.get("p15"/"p40"/"p75")`。
- `upsert_fundflow_min`：executemany 加 `xs_net`、`buy_amount`、`sell_amount`。
- `get_fundflow_min` 是 `SELECT *`，新列自动带回。

### app/api/stocks.py::stock_detail（1/5 bug 修复点）
- **始终返回 1 分钟基础**：删掉响应构建里的 `resample_points(points, fundflow_window)`，直接用 `get_fundflow_min` 的 1 分钟点构造 `fundflow_15m`。`window` 参数仅作信息保留（前端本地重采样）。移除 `resample_points` 的 import（函数保留在 fundflow.py，测试仍用）。
- 分时点字典：`{ts, super_large_net, large_net, medium_net, small_net, xs_net, buy_amount, sell_amount}`，**不含 `main_net`**。
- `fundflow_bands` 从 `("p15","p40","p75","p95")` 取值。
- `fundflow_history` → `{trade_date, netamount}`（去 main_net/main_net_pct）；`fundflow_latest` 去掉 `main_net/main_net_pct` 后返回（前端 5 档柱只需 super/large/medium/small/xs/netamount）。

### app/config.py + app/analysis/scoring.py（评分去主力因子）
- `BUY_WEIGHTS` / `SELL_WEIGHTS` 删 `fund_flow`，剩余权重按比例归一化到 1.0：
  - BUY：pe_pct 0.2941、pb_pct 0.2353、dv_ratio 0.1765、pct_chg 0.1176、roe 0.1765。
  - SELL：pe_pct 0.2941、pb_pct 0.2353、dv_ratio 0.1176、pct_chg 0.1176、concentration 0.2354。
- `scoring._score_factors`：buy/sell 的 `defs` 各删 `("fund_flow", ...)` 一行（剩余权重由 `wsum` 归一化，行为不变）。
- `app/services/ai.py` 保持不动（AI 上下文仍用 main_net，属另一功能，本次不改）。

### app/services/refresh.py（文案，可选）
- `sync_fundflow` 日志与「当日资金流」label 措辞去掉「主力」字样（如 `主力净流入=%s` → 总净流入）。

## 前端改动

### static/stock.html
- `loadPage()`（line 491）：请求改 `/stocks/{code}?window=1`（双保险；后端已保证返回 1 分钟基础）。
- 分时面板（line 106-112）内、`#flowIntraday` 下方加新容器：`<div class="chart lg" id="flowBuySell" style="height:300px"></div>`，与 `#flowTabs` 共用切换。
- 「近30日主力净流入」面板标题改「近30日净流入」。
- `clearStockView`（line 180-186）：清理 `#flowBuySell`。
- `renderFlow`（line 670-717）：
  - 分档说明文案按 5 档：`特小单 <P15 · 小单 P15~P40 · 中单 P40~P75 · 大单 P75~P95 · 特大单 >P95`（读 `bands.p15/p40/p75/p95`）。
  - `draw(w)` 同时渲染两个图：`fundflowIntraday(intraEl, pts, w)` + `fundflowBuySell(bsEl, pts, w)`。
  - `base` 为空时两个容器都清空、显示空提示。
  - 30 日历史改 `netamount`（近30日净流入）。

### static/js/charts.js
- `resampleFlow`（line 400-416）：桶初始值加 `xs_net/buy_amount/sell_amount`，删 `main_net`。
- `fundflowBars`（line 127-156）：5 行 `[['特大单',super_large_net],['大单',large_net],['中单',medium_net],['小单',small_net],['特小单',xs_net]]`，去「主力」行。
- `fundflowIntraday`（line 420-463）：KEYS 改 `['super_large_net','large_net','medium_net','small_net','xs_net']`，5 条线（特大红/大橙/中蓝/小灰/特小浅灰），去「主力」线/图例/tooltip；`yAxis:0` 虚线 markLine 挪到特大单；**`setOption(option, {notMerge:true})`**（切换全量重渲染，修 1/5 渲染残留）。
- `fundflowLine`（line 466-486）：改画 `history.map(h => h.netamount)`，标题「近30日净流入」。
- 新增 `fundflowBuySell(el, points, window)`：买盘、卖盘两根**累积**线（`买盘 #e03131`、`卖盘 #2f9e44`），`smooth:true`、`symbol:'none'`，共享 category x 轴与 `dataZoom`，tooltip `买盘 X / 卖盘 Y`（fmtFlow），标题 `买盘 / 卖盘（X分钟 · 累积金额）`，`setOption(..., {notMerge:true})`。

## 测试改动

- **test_fundflow.py**（全部重算 P15/P40/P75/P95 口径）：
  - `test_compute_quantiles` / empty → 4 元组。
  - `test_classify_tick` → 5 参数 + `xs` 分支（P15 边界归特小）。
  - `test_aggregate_ticks_window1` / `test_aggregate_ticks_neutral_ignored` → 档位归属重算（部分小单变特小），补 `buy_amount/sell_amount` 断言（中性不计入）。
  - `test_resample_points` → xs/buy/sell 累加断言。
  - `test_ticks_to_day` → xs_net 加入、small/net 重算。
  - `test_sync_fundflow_persists` → 断言 p15/p40/p75/p95 均非空。
  - `test_stock_fundflow_api` → bands 4 键；window=1 与 window=15 返回**相同**的 1 分钟基础点（后端不再重采样）；每点含 xs_net/buy_amount/sell_amount 且**不含 main_net**。
- **test_scoring.py**（评分因子移除）：
  - 删 `scores["fund_flow"]` 断言（buy/sell 两处）。
  - `test_buy_score_full_factors` 期望总分按新权重重算（≈64.1）。
  - `test_missing_factor_shrinks_toward_50` / `test_low_confidence_between_60_80`：fund_flow 不再是因子，改用 ROE/pct_chg 等缺失组合制造 60~80% 覆盖率。
  - `test_single_factor_insufficient`：最小权重变为 pct_chg 0.1176，覆盖率断言从 0.10 → 0.1176。
- **test_migration.py**：版本断言 3→4；`daily_fundflow_cache` 检查 p15/p40/p75/xs_net；`fundflow_15m_cache` 检查 xs_net/buy_amount/sell_amount。
- **test_api.py**：仅断言 `fundflow_15m in data`，无需改。

## 验证

1. `pytest tests/ -q -p no:cacheprovider --basetemp=.tmp_test` 全绿。
2. 手动：开个股页 → 1/5/15/30 全部能切换且形态正确；**刷新此股后**再切 1/5（复现原 bug 的场景）确认正常；五档显示 特大/大/中/小/特小 5 行与 5 条累积线；买盘/卖盘两线随窗口切换；全页无「主力」字样（除 AI 报告区域，本次不改）。

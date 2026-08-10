# 资金流 日/周/月 窗口 + 并发 AI 队列 + 历史放宽（验证记录）

> 本次改动已 commit（`0720191`）。文档记录改动的验证结论与修复的回归，供后续维护参考。

## 已完成的改动（代码核对属实）

### 1. 资金流窗口改名并对齐 K 线周期
- 分钟窗口保留：`1m/5m/15m/30m`
- 天窗口：`1d/7d/30d` → **`day/week/month`**
  - `day` = 逐日
  - `week` = **自然周**（ISO 周，周一为一周起始）
  - `month` = **自然月**
- 聚合逻辑与 K 线周线/月线对齐（后端 `_bucket_day_flows`/`_bucket_day_prices` + 前端 `bucketFlowDays`/`bucketIndexVolume`，均新增 `naturalGroupKey`）
- 旧窗口名兼容：`_norm_flow_window` 将 `1d→day`、`7d→week`、`30d→month`；`get_stock_fundflow_report` 读取时新名匹配旧落库报告（`ai_fundflow_reports.window`）
- 涉及文件：`app/services/ai.py`、`static/js/charts.js`、`static/{stock,portfolio,indices}.html`

### 2. 资金流 AI 发送量与 K 线对齐
- `day` 最多发 120 桶 / `week` 60 / `month` 36（`_DAY_AI_LIMITS`），数据源不足有多少发多少
- 自然周/月按每桶约 5/20 交易日估算截断行数（`_natural_group_key` + `est_days_per_bucket`）

### 3. 资金流历史放宽（45 天 → 覆盖新浪 500 条 ≈ 2 年）
- 新增常量 `FUNDFLOW_HISTORY_DAYS = 760`（`app/data/fundflow.py`）
- 放宽处：`instruments/detail.py`（个股页 fundflow_history）、`analysis/instrument_fundflow.py`（组合页）、`analysis/portfolio.py`（净值线）、`services/ai.py`/`ai_scoring.py`（AI context）
- 新浪回填 `count` 300 → 500（`raw_sina.py` / `ashares.py`）
- 日K 全量刷新窗口 400 → 760（`refresh.py`，保证净值图 price 覆盖资金流历史）

### 4. 前端：进度条 + 默认窗口对齐 K 线
- 资金流图（`fundflowStacked`/`fundflowDayBuySell`/`fundflowPrice`/`indexVolumePrice`）新增 **dataZoom slider 进度条**（不只鼠标滚轮）
- 默认展示最近 60 桶（`defaultZoomRange`，与 K 线 `defaultShow:60` 一致）
- 净值图修复头部空白：`fundflowPrice` 剔除头部 price 为 null 的点（日K缓存短于资金流历史时）

### 5. AI 并发 worker queue（承接「AI 请求排队」问题）
- `app/jobs.py`：`AI_WORKERS 3 → 8`
- 自动打分（`maybe_auto_score_daily`/`catchup_pending_daily`）从裸线程改为 **入队 LANE_AI**（`_enqueue_daily_score`，`app/services/ai_scoring.py`），统一并发上限 + `/status/jobs` 可见可取消
- 测试同步改为 mock `start_simple` 同步执行

## 验证结果（2026-08-10）

### 完整测试套件 ✅ 全量 399 passed（~80s）
首次全量子集跑出 20 失败（大量 job timeout，19 分钟），定位为**测试隔离回归**，非功能回归。单独重跑失败项全部通过（单测 1.3s）。修复 3 处后全量全绿。修复的回归：

1. **`tests/test_ai_scoring.py::test_catchup_pending_daily_spawns_after_close`**
   - 仍 mock `threading.Thread`（改入队 LANE_AI 后过时）。其 `_SyncThread.__init__` 不认 `name=` 参数，`_ensure_workers` 创建 worker 时抛 TypeError——但 `_workers_started` 已在抛错前置位 → **全局 AI/refresh worker 永久缺失**，之后 `test_ai.py`/`test_api.py` 所有依赖真实 worker 的 job 全部卡 queued → 连锁 timeout。
   - 修法：改为 mock `job_runners.start_simple` 同步替身（与 `test_maybe_auto_score_daily_with_model_spawns` 一致），不再 mock `threading.Thread`。
   - ⚠️ 后续若再改 `_ensure_workers`，注意：`_workers_started=True` 后启动线程抛错会留下半启动状态（本次测试暴露的健壮性隐患，未在产品代码修复）。

2. **`tests/test_kline.py::test_build_technical_bars_multi_limits`**
   - 种子日期 `anchor-1/-2` 在 2026-08-10（周一）落到周六/周日，被 `df692bb` 的非交易日过滤剔除，只剩 1 根。
   - 修法：改用 `conftest.last_n_trade_days(3)` 取最近 3 个真实交易日。

3. **`tests/test_ai_news_tech.py::test_build_technical_bars_limit_tail`**
   - 同上，周末种子日期问题；改用 `last_n_trade_days(3)`。

### 自然周/月聚合边界 ✅
- 后端 `_natural_group_key`（Python `isocalendar`）与前端 `naturalGroupKey`（JS ISO 周算法）边界**完全一致**（node 实测比对）：
  - `2024-12-30`→`2025-W01`、`2025-01-01`→`2025-W01`（跨年周正确归到次年）
  - `2025-12-29`→`2026-W01`、`2026-01-04`→`2026-W01`
  - `2025-06-30`（周一）→ 新周 `2025-W27`；month 按 `YYYY-MM`
- 批量资金流 AI（tags/codes 模式）天窗口复用 `build_fundflow_analysis_context` → 同一 `_bucket_day_flows` 聚合，输出与个股一致（代码核对）。

### 旧报告兼容 ✅
- `get_stock_fundflow_report` 用 `alias = {day:1d, week:7d, month:30d}` 匹配旧落库报告，SQL `CASE` 排序优先命中新名（无该窗结果返回 None，不跨窗兜底）。

### 新浪 count=500 实测 ✅
- `fundflow_daily_history('sz000001', 500)` 实测返回 **500 条**（0.17s），覆盖 `2024-07-17 ~ 2026-08-07`（≈2 年交易日），接口稳定。

### 日K 全量刷新变慢 ✅ 影响可控
- 实测单只 760 自然天窗口拉 **504 根**日K，耗时 **0.22s**（腾讯单次请求返回全部，非逐日请求）。
- 数据量约旧 400 天窗口（~280 根）的 1.8 倍，主要是解析/落库/涨跌幅计算增加；几十只持仓的全量刷新增加秒级，可接受。

### 前端联调 ⏳（代码路径已核对，浏览器实测待人工）
- 三页 `FLOW_WINDOWS` 均已统一为 `['1m','5m','15m','30m','day','week','month']`（`stock.html:168`、`portfolio.html:108`、`indices.html:66`），窗口切换传值经 `_norm_flow_window` 后端归一化。
- `fundflowStacked`/`fundflowDayBuySell`/`fundflowPrice`/`indexVolumePrice` 均配 dataZoom **slider** + `defaultZoomRange(labels.length, 60)`。
- `fundflowPrice` 头部 price null 剔除逻辑在位。
- 待人工确认：日/周/月切换是否正常出图、slider 拖动与默认 60 桶、净值图头部不再空白（需先全量刷新让日K覆盖 760 天）。

## 备注
- 运行测试：`.venv/Scripts/python.exe -m pytest tests/ -q -p no:cacheprovider --basetemp=.tmp_test`（macOS 用 `.venv/bin/python`）
- 清理：`git clean -fd` 可移除 `.tmp_test_*`、`_bd_*`、`_sina_hk_page.html` 等残留

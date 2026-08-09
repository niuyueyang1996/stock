# 资金流 日/周/月 窗口 + 并发 AI 队列 + 历史放宽（未验证项留存）

> 本次改动已 commit。以下为**尚未完整验证**的项，后续测试/联调时需逐一确认。

## 已完成的改动

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

## 未验证项（待做）

- [ ] **完整跑测试套件**（本次只跑了 `test_ai_fundflow.py` 35 通过 + `test_fundflow.py` 19 通过）：
  - `tests/test_ai_scoring.py`（改了 maybe_auto_score_daily → 队列）
  - `tests/test_instruments.py`（detail fundflow_history 45→760）
  - `tests/test_as_of.py`、`tests/test_kline.py`、`tests/test_ai.py`、`tests/test_api_*.py`
- [ ] **前端联调**：
  - 个股页 / 组合页 / 指数页资金流面板 日/周/月 切换是否正常出图
  - slider 进度条拖动、默认最近 60 桶是否符合预期
  - 净值图头部不再空白（需先全量刷新让日K覆盖 760 天）
- [ ] **自然周/月聚合边界**：
  - 跨周日/月末的日期分组是否正确（ISO 周起止、月末合并）
  - 批量资金流 AI（tags/codes 模式）天窗口输出是否正常
- [ ] **旧报告兼容**：切到新窗口名后，之前 `1d/7d/30d` 落库的资金流 AI 报告能否通过 `get_stock_fundflow_report('day')` 读回
- [ ] **新浪 count=500 实测**：`fundflow_daily_history(symbol, count=500)` 实际返回是否足 500 条、接口是否稳定
- [ ] **日K 全量刷新变慢**：760 天窗口会显著增加全量刷新耗时，评估是否可接受

## 备注
- 运行测试：`.venv/Scripts/python.exe -m pytest tests/ -q -p no:cacheprovider --basetemp=.tmp_test`
- 清理：`git clean -fd` 可移除 `.tmp_test_*`、`_bd_*`、`_sina_hk_page.html` 等残留

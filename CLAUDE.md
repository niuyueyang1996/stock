# 项目上下文（Claude Code 记忆）

个人自建 ETF 持仓分析系统：把自己的一篮子持仓（A 股 + 港股 + 场内 ETF）当自建 ETF 分析。
FastAPI + SQLite + 原生 JS/ECharts，无前端构建。数据源 akshare（新浪/百度/雪球/东财/中行）。

## 数据分层原则（最高优先级，所有数据相关代码必须分层）

数据接入统一三层结构，新增/修改任何数据相关代码必须遵守（用户明确要求，后续所有代码分层设计）：

```
app/data/
  base.py          对外门面：DataSource 抽象 + SourceManager 降级链 + Financials 等标准模型
  raw/             接口层：真实访问底层接口（akshare/原始 HTTP），只做请求与最小解析，返回原始 dict/DataFrame，无业务口径
  providers.py     接口转换层：平台选择 + 降级链（哪平台先试、失败换下一个），调 raw 接口 + normalizer，返回标准模型
  normalizers.py   数据转换层：raw → Financials/Quote/Bar/…，统一字段名、单位、货币、报告期口径
  cache.py fx.py   （缓存/汇率，不变）
```

- **对外屏蔽**：唯一入口 `build_manager()` 返回的 SourceManager，外部（refresh/valuation/portfolio/api）只调 `manager.financials(code)`/`quote()` 等，**不直接依赖具体源或层**。
- **货币统一在数据转换层**：各平台原始货币一律折算人民币（Financials 全是人民币口径）。港股财务接口返回的是**公司记账本位币（功能货币）**——青岛港=人民币、腾讯=港元；市值永远是港元。任何「市值÷净利」「每股股息÷股价」等比率**必须先同货币**，绝不允许混币计算（青岛港 PE 混币出 8.65 实为 7.7 的教训）。
- **口径下沉**：TTM 计算、同比、货币折算、单位统一只允许在数据转换层做；上层只消费统一模型，不重复折算、不重复算口径。
- **判定功能货币**：东财港股主指标 MAX 接口自带 EM 计算的 PE_TTM/PB_TTM（币种自洽），作锚点重算比对判定报表货币（见 normalizers.py `_detect_reporting_currency`）。`IS_CNY_CODE` 是交易计价币种非报表货币，不可用。

## 快速上手

```bash
.venv/Scripts/python.exe -m uvicorn app.main:app --host 127.0.0.1 --port 8000   # 启动
.venv/Scripts/python.exe -m pytest tests/ -q -p no:cacheprovider --basetemp=.tmp_test  # 测试（Windows 需 basetemp）
```

## 架构

```
app/
  main.py          FastAPI 入口：init_db、启动后台自动除权线程、路由挂载
  config.py        DB_PATH/评分权重/评级阈值
  models/db.py     SQLite 建表 + 版本化迁移（migrate_db，迁移前备份）
  data/
    base.py        数据源抽象（Quote/Bar/Financials）+ SourceManager 降级链（对外门面）
    raw/           接口层：真实访问底层接口，返回原始数据（raw_em/raw_sina/raw_tencent/raw_baidu/raw_mock）
    providers.py   接口转换层：平台选择 + 降级链（EmProvider/SinaProvider/BaiduProvider/MockProvider）
    normalizers.py 数据转换层：raw → 标准模型，统一字段/单位/货币（人民币）/口径
    cache.py       缓存 DAO（价格/估值/分位/财务/资金流/汇率）
    fx.py          汇率拉取（新浪实时 HKD/CNY）
  services/
    holdings.py    持仓/交易（重放法）、成本/股数调整、分红除权触发
    fx.py          汇率服务（折算/刷新/回填）
    dividend.py    分红除权自动调整（幂等）
    quote.py       纯缓存行情读取（零网络）
    refresh.py     刷新编排（动态/全量/单股）
  analysis/
    valuation.py   实时估值 + 前瞻 PB（修正口径）+ 分位（分段排序）
    scoring.py     交易评分冻结快照 + 日聚合
    portfolio.py   组合穿透式指标 + 打包序列
    volatility.py  人民币波动率（前向填充 3 日 + 95% 覆盖门槛）
  api/             system/holdings/trades/stocks/portfolio/scoring
static/            前端 4 页 + js/（api.js common.js charts.js）
packaging/windows/ Windows 托盘程序 + PyInstaller/Inno Setup 构建链
tests/             pytest（含 Windows 打包支持测试，全程离线，conftest 打桩网络）
```

## 核心口径（用户确认，改前必读）

### 数据层：缓存优先 + 手动刷新
- **GET 只读缓存，零网络、零数据库写入**。唯一数据入口是右上角刷新按钮。
- 缓存分三类：原始缓存（行情/财务/估值/汇率，长期保留）、不可变快照（评分）、派生缓存（组合序列+分位，带 `portfolio_hash` 可失效重建）。
- 清仓只改持仓状态，**不删原始缓存**（再次开仓复用）。
- GET 缓存缺失返回 HTTP 409 CACHE_MISS（个股详情），前端弹窗询问下载。

### 评分快照（scoring.py）
- 每笔交易生成**冻结快照**（trade_score_snapshots）：因子原值/数据日期/权重/模型版本/覆盖率/状态。
- 状态：`frozen`（交易时正式）/ `estimated`（旧交易回填）/ `insufficient`（覆盖率<60% 不评级）。
- 评分 = `50 + (已知因子得分 − 50) × 覆盖率`；覆盖率 60%~80% 低置信度，>80% 正常。
- 历史回填只用交易日及以前数据（消除时间穿越）；`/api/scoring/rebuild` 只重建日聚合，不重算冻结快照。
- 日评分聚合快照、按人民币金额加权；可评分金额 <80% 当日不评级。
- 权重修改生成新模型版本，只作用于之后交易。

### 港股折算
- 五位代码（06198/00700）→ `currency='HKD'`；港股**不再复用 ETF 分类**（is_etf 仅按场内基金代码/ETF 标签）。
- **汇率新浪实时** `hq.sinajs.cn/list=HKDCNY`（1 HKD = 买卖价中点 CNY）。中行 `ak.currency_boc_sina` 在本环境仅返回 2023 历史不可用。缓存 fx_rate_cache；交易存 `fx_rate/amount_cny`，持仓双币种成本。
- 缺汇率**绝不按 1:1**：返回 `missing_fx`，从人民币汇总剔除。
- **汇率时机**：启动进程强制拉一次 + 仅「全量刷新」重拉；动态刷新不拉。汇率可用后自动回填缺失的港股 `amount_cny`（`backfill_trade_cny`）。
- **港股显示口径（券商方式，用户确认）**：单价（现价/成本）显示港币，金额（市值/盈亏/成交金额）显示人民币；个股页现价/市值卡 + 买卖弹窗显示 `≈¥` 人民币换算。交易流水/评分明细金额用 `amount_cny`。

### 组合穿透式指标（portfolio.py）
- 全部人民币口径；股票/ETF/港股都参与资产与风险指标。
- `归属利润 = 持股数÷总股本×公司TTM利润`；综合 PE = Σ市值/Σ归属利润；PB/ROE 同口径，**ROE% = PB/PE×100**。
- 每指标返回 `coverage_weight`，<70% 前端显示"覆盖不足"且不进组合打分。
- **亏损股负值全程参与**（负 PE/PB/ROE/利润/预测利润）；仅分母为零才"不适用"。
- 首页 pe_pct/pb_pct 来自打包组合历史序列分位（`portfolio_valuation_cache`），非个股加权。
- 分段排序分位：正小→大 → 0 → 负（绝对值大→小），如 `8,15,30,0,-100,-20,-5`。
- 序列样本：只取当前日期前、单日市值覆盖率≥90%、≥60 天。历史数据升序、真实交易日。
- `portfolio_hash` **只基于持仓**（code/quantity/currency）——不要把估值序列 `updated_at` 纳入，否则缓存频繁失效、分位消失（踩过坑）。
- 股息率穿透式：`Σ分红 ÷ 组合总市值`（含不派息股，加仓不派息股会稀释）。
- `_combo_current` 亏损股用 `市值÷负TTM` 得负 PE 参与（与历史样本口径一致）。

### 前瞻 PB / PE（valuation.py）
- 预测年末净资产 = 上年年末净资产 + 预测净利 − 预测分红；前瞻 PB = 市值/预测年末净资产。
- 降级链：增速(用户→TTM→财报同比→0%)、支付率(用户→上年→0%)、上年末净资产(年报→每股净资产×股本→次级推演)。
- 置信度：完整年报 high / 次级推演 medium / 零假设 low；预测净资产为负 → 负 PB + `invalid`。
- 组合前瞻 PE/PB 用**穿透汇总**（Σ市值/Σ归属预测净利/净资产），不做个股加权。

### 成本调整与分红除权（holdings.py / dividend.py）
- `POST /holdings/{code}/cost-adjust`：amount（成本，正=加 负=减）+ delta_qty（股数，拆股/送股）；插入 `adjust` 交易，重放只改成本/股数，不生成评分快照。
- 分红除权：启动进程/全量刷新时扫描持仓，今天除权的自动按 `每股分红×持仓` 摊薄成本；`dividend_adjustments` 表幂等。
- 手动「📅 分红除权」按钮查最近除权（东财）填入；`is_dividend=1` 标记计入**累计分红**。
- 累计分红 = trades 中 `side='adjust' AND is_dividend=1` 的 `SUM(-amount)`。

### 今日盈亏（券商口径）
- `_day_pnl`：前日持仓用 `(现价−昨收)`；当日买入用 `(现价−买入均价)`；当日卖出 FIFO（先前日昨收、后当日买入均价）；减当日费用。
- 按现价买入当日盈亏 = 0（不是按昨收算亏）。

### 前端约定（static/）
- **api.js**：所有请求经 `api()`；全屏 loading 屏障**仅** `opts.blocking:true` 的耗时请求（页面加载/刷新/导入/下载）显示，快速操作默认不弹；搜索联想用 `silent:true`。支持 FormData（不 JSON.stringify、不手动 Content-Type）。
- **交易评分页（trade.html）**：左目录 + 右详情布局。`.row` 高度 `70vh` + `flex-wrap:nowrap`；`day-dir`/`day-detail` 需 `min-height:0` 才让 `overflow-y:auto` 生效（flex 子项默认 min-height:auto 会阻止滚动，踩过坑）。
- 评分快照状态中文：frozen→冻结 / estimated→回填 / insufficient→数据不足；日状态 rated→已评级 / low_coverage→覆盖不足 / no_scoreable→无可评分。
- **start.cmd 必须纯 ASCII**（cmd 按 GBK 解析 UTF-8 中文会崩）；`if` 括号块内不能有 `(3,10)` 这类括号（被当块结束），版本检查移到独立标签。
- Windows 打包态的数据库/日志/运行状态位于 `%LOCALAPPDATA%\StockAnalyzer`；静态资源从 PyInstaller `_MEIPASS` 只读加载。`STOCK_APP_HOME` 仅用于测试/便携覆盖。
- Windows 启动器必须保持单实例、只监听 `127.0.0.1`，用 `/api/health` 判断服务身份；安装包严禁包含仓库 `data/` 中的个人文件。

## 关键坑（踩过，改前注意）
- SQLite 写事务内不要再开连接写（如汇率落库）会 "database is locked"——汇率计算移到事务外。
- `_MIGRATE_COLUMNS` 加新列后，旧库需手动 `ALTER TABLE`（migrate 只在版本落后时跑；已到目标版本不会重跑）。
- daily_scores.total_score 可空（覆盖不足无分）；旧表 NOT NULL 需 `_recreate_daily_scores_nullable`。
- 全市场列表只在**启动后台预热**时联网下载（`preload_market_lists`，幂等：缓存新鲜读文件不发网络）；`/stocks/search` 与名称回填只读本地缓存（GET 零网络、绝不阻塞）；测试在 conftest mock 为空防联网。
- Windows 跑 pytest 需 `--basetemp`（默认临时目录权限失败）。
- `_ensure_stock` 写 name；`stock_detail` 名称缺失时从列表回填到 stocks 表。

## 测试
- 145 用例。重点：scoring（冻结快照/覆盖率/日聚合）、portfolio（穿透式/分段分位/覆盖门槛/今日盈亏）、fx（港股折算/汇率）、dividend（除权幂等/累计分红/东财降级）、migration（旧库升级）、api（409 CACHE_MISS/GET 零写入/名称回填）、Windows 打包路径/健康检查/单实例复用/本地 ECharts。
- conftest：每测试独立临时 DB，mock build_manager 为 MockProvider、quote 固定、列表为空。

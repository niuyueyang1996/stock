# 项目上下文（Claude Code 记忆）

股票持仓分析系统：把自己的一篮子持仓（A 股 + 港股 + 场内 ETF）当组合分析。
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
  config.py        DB_PATH/网络/数据源开关
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
    ai_scoring.py  AI 评分：标签偏好(补全/确认) + 组合/每日 AI 打分（复用 services/ai.py）
  analysis/
    valuation.py   实时估值 + 前瞻 PB（修正口径）+ 分位（分段排序）
    portfolio.py   组合穿透式指标 + 打包序列
    volatility.py  人民币波动率（前向填充 3 日 + 95% 覆盖门槛）
  api/             system/holdings/trades/stocks/portfolio/ai/ai_scoring
static/            前端 4 页 + js/（api.js common.js charts.js）
packaging/windows/ Windows 托盘程序 + PyInstaller/Inno Setup 构建链
tests/             pytest（含 Windows 打包支持测试，全程离线，conftest 打桩网络）
```

## 核心口径（用户确认，改前必读）

### 数据层：缓存优先 + 手动刷新
- **GET 只读缓存，零网络、零数据库写入**。唯一数据入口是右上角刷新按钮。
- 缓存分三类：原始缓存（行情/财务/估值/汇率，长期保留）、AI 评分报告（`ai_*_reports`，手动 POST 触发落库）、派生缓存（组合序列+分位，带 `portfolio_hash` 可失效重建）。
- 清仓只改持仓状态，**不删原始缓存**（再次开仓复用）。
- GET 缓存缺失返回 HTTP 409 CACHE_MISS（个股详情），前端弹窗询问下载。

### AI 评分（ScoreCard 统一：ai.py 公共定义 + ai_scoring.py；公式评分已彻底移除）
- 组合打分 + 每笔交易/每日打分 + **个股诊股**全部由 AI 产出统一 **ScoreCard**（`score`/`grade`/`action`/`risk`/`confidence`/`dimensions`…）；**公式评分已移除**（`trade_score_snapshots`/`daily_scores` 表 v5 迁移 DROP，`scoring.py`/`/api/scoring/*` 删除）。
- **质量与动作拆开**：`grade`=质量（A优秀/B良好/C一般/D较差，三处统一）；`action`=操作建议（个股/组合 `add|hold|watch|reduce|exit`；交易 `repeat|cautious|avoid`）。**禁止**再用 A=强烈推荐 等动作语义。后端只做枚举校验（非法 grade→C、action→hold/cautious），**不做 score→grade 转换**；明显冲突只记 warning。
- **共享 5 维** `fundamentals/valuation/fundflow/news/technical`；个股追加 5（共 10）、组合追加 `structure/tag_fit`、交易追加 `timing/execution/sizing/discipline`。消息/技术进打分：context 塞 `as_of_datetime` + `bars`（`build_technical_bars`），有专项报告则附摘要优先采信。
- **新鲜度**：组合 `profile_hash`→`stale`；个股诊股快照哈希变化→`stale`；读取层 `upgrade_legacy_card` 内存映射老键（`rating`→`grade`、`risk_score`→`risk`）不回写。前端三页共用 `renderScoreCard`（common.js）。
- **标签偏好（tag_prefs）**：每标签用户输入简短偏好 → 保存时自动请求 AI 补全成完整「评分指引」（draft），确认后（confirmed）才用于打分；提供手动「AI 补全」。
- **组合打分跟随标签筛选 + 按组合分开存储**：`POST /api/ai-scoring/portfolio?tags=红利,科技` 只打该子组合（只带这些持仓汇总 + 这些标签指引）；「全部」= 全持仓 + 全部已确认偏好。**每个标签组合各存一份**（`ai_portfolio_reports.tags_json` 记录组合），打个股不覆盖 个股+港股，也不覆盖 全部；同一组合画像变化标 `stale` 保留旧分，另一组合不受影响。持仓页（index.html）不打分。
- **每日打分偏好去重**：一天 50 笔时 `tag_prefs` 只放当天涉及的标签各一份，每笔交易只带 `tag` 名 + 该股因子（`compute_live` 现取）；AI 按 tag 匹配指引逐笔打分再汇总。
- **触发**：组合 = 手动按钮（GET 读最新报告，`stale`=画像哈希变化提示重新打分）；新交易 = 录入时 `maybe_auto_score_daily` 失效并后台线程重打分（无激活模型/当日无交易不起线程）。
- **HTML 详细报告**：个股诊股/组合打分/每日打分/个股资金流/批量资金流五种 AI 分析，仅分析强度「深入」要求生成**自包含 HTML 报告**（`_HTML_REQUIREMENT`/`_PORTFOLIO_HTML_REQUIREMENT`/`_DAILY_HTML_REQUIREMENT`/`_FUNDFLOW_HTML_REQUIREMENT`/`_BATCH_FUNDFLOW_HTML_REQUIREMENT`，≥1000字、内联 CSS、无外部依赖、禁止 `<script>`；基础 prompt 不含 HTML 块）；快速/普通不要求（schema 去 `html` 字段），无「📄」按钮。资金流 HTML 落库：个股存 `ai_fundflow_reports.html`（v8 迁移），批量存 `ai_fundflow_coherence_reports.html`，F5 后 📄 按钮恢复。前端「📄 查看详细报告」按钮用 `openAiHtmlReport` 在新窗口打开。**HTML 聚焦深入分析，不罗列用户已可见的原始数据表**；内联 summary/advice/risks/reasons 保留。规整函数缺省把 html 置空串（AI 未生成时按钮不显示）。
- **思考级别（用用户配置，不做强度覆盖）**：`chat_json` 附带 `reasoning_effort`，**一律取用户全局配置**（config 表 `ai_reasoning_effort`，默认 `high`；「🤖 AI」弹窗可选 low/medium/high/max，端点 `GET/PUT /api/ai/reasoning`）——**不因分析强度压级/拔高**（用户确认：配的啥就用啥，不怕烧）。provider 不支持时 `chat_json` 降级重试会移除该参数。
- **输出预算与超时（可在「🤖 AI」弹窗配置）**：`_AI_MAX_TOKENS=81920`（实测 deepseek-v4-flash 接受；组合/批量几十只持仓 + 整组合 HTML 需要大预算；8192 时高思考级常被 reasoning 烧光 → `finish=length` 只出盘算不出 JSON）；`AI_REQUEST_TIMEOUT=300s`。用户可在「🤖 AI」弹窗改 config 表 `ai_max_tokens`（2048~262144）/ `ai_request_timeout`（30~1800），端点 `GET/PUT /api/ai/runtime`，`chat_json`/`_post_chat_completion` 每次读取即时生效；`get_max_tokens()`/`get_request_timeout()` 读配置，缺省回落常量。提供商拒绝大值则 `chat_json` 降级 `_AI_MAX_TOKENS_SAFE=16384` 重试。
- **输出格式双端强化**：所有带 schema 的 AI 调用（诊股/资金流/批量资金流/消息面/技术面/批量消息面/批量技术面/组合打分/每日打分）统一用 `ai._schema_user(label, ctx, schema)` 组装 user 消息——完整 schema **写在开头与结尾双端**（首因+近因效应，减少模型跑偏/乱答），中间夹输入数据；两端都重申「只输出严格 JSON、不要额外文字、不要 markdown 围栏」。
- `profile_hash` 只基于持仓(筛选内 code/qty/currency) + 已确认偏好(筛选内) + 标签筛选 + 模型——**绝不含时间戳/价格**。

### 消息面 / 技术面 AI（专项深入层，阶段一）
- 两张新表 `ai_news_reports` / `ai_tech_reports`（PK `code+as_of+source`，`source='single'/'batch'`，进 `_SCHEMA` 幂等补建，**无需 bump 版本**）。个股 GET 取该 code 最近一条（`ORDER BY as_of DESC, updated_at DESC LIMIT 1`）。
- **时效机制（核心）**：所有消息面/技术面 AI 调用的 user JSON 注入 `as_of_datetime`（`now_as_of_datetime()` 本地时区带时区 ISO）；prompt 强制 `_ASOF_RULES`——AI 相对该时刻判定时效，过时/不确信不输出，宁可 `items: []` + `omit_reason`。后端只轻量规整：剥离无 `event_date` 或 AI 自标过时的 item、枚举兜底（stance→neutral、trend→range）、**空结果视为成功**；不设 N 天死阈值。
- 技术面原始数 = `daily_price_cache` 日K，`build_technical_bars(code, as_of, limit=120)`（`resolve_trade_day(as_of)` 截断到最近交易日，无数据返回 `[]`）+ `build_technical_bars_many`（`get_daily_prices_many` 一次多只）。阶段二打分 context 复用同一函数。
- 端点 10 个：`POST /ai/news-analysis`、`GET /ai/news-report/{code}`、`GET /ai/news-reports?codes=`、`POST /ai/news-batch`、`GET /ai/news-coherence?scope&scope_key`，技术面同形 `/ai/tech-analysis|tech-report|tech-reports|tech-batch|tech-coherence`。批量 tags/codes 互斥 400、无模型 400；逐只落库 `source='batch'`，单只失败记日志继续。
- **HTML 门控（用户确认：每个深入类请求都要该次请求的整体 HTML）**：个股/批量**深入**强度均要求 `html`（`_NEWS_HTML_REQUIREMENT`/`_TECH_HTML_REQUIREMENT`/`_BATCH_NEWS_HTML_REQUIREMENT`/`_BATCH_TECH_HTML_REQUIREMENT`）。批量 HTML 是**整组合整体输出**（不是逐只拼接），落 `ai_news_coherence_reports` / `ai_tech_coherence_reports`（scope='portfolio'/'indices' + scope_key='全部'/排序 tags/codes，`_coherence_key` 归一同 key；**每次批量都写最新一条**，GET 按 `as_of DESC` 取最近——普通/深入均覆盖旧结果，要看最新不保留旧深入 HTML）。GET 端点 `/ai/news-coherence|tech-coherence` 供 F5 重建，前端批量面板顶部展示整组合 summary + 📄 按钮。
- 前端：个股页两块 panel（`newsAiPanel`/`techAiPanel`，诊股面板后、资金流前；**指数模式隐藏消息面、保留技术面**）；组合页「AI 扩展分析」区**三个 tab**（消息面/技术面/资金流，`.ptab` 样式，批量跟随标签筛选，F5 从 `source='batch'` 重建；资金流批量已从组合资金流面板收敛进来，资金流面板只留图表）；持仓页（index.html）**不展示**资金面/消息面/技术面列（只在个股/组合页看）。`_EDITABLE_PROMPTS` 增 `news/technical/news_batch/tech_batch` 4 key。共享渲染在 common.js（`renderNewsPanel`/`renderTechPanel`/`renderBatchListPanel`/`newsStanceBadge`/`techTrendBadge`/`runNewsBatch`/`runTechBatch`/`loadNewsCoherence`/`loadTechCoherence` 等）。

### 港股折算
- 五位代码（06198/00700）→ `currency='HKD'`；港股**不再复用 ETF 分类**（is_etf 仅按场内基金代码/ETF 标签）。
- **汇率新浪实时** `hq.sinajs.cn/list=HKDCNY`（1 HKD = 买卖价中点 CNY）。中行 `ak.currency_boc_sina` 在本环境仅返回 2023 历史不可用。缓存 fx_rate_cache；交易存 `fx_rate/amount_cny`，持仓双币种成本。
- 缺汇率**绝不按 1:1**：返回 `missing_fx`，从人民币汇总剔除。
- **汇率时机**：启动进程强制拉一次 + 仅「全量刷新」重拉；动态刷新不拉。汇率可用后自动回填缺失的港股 `amount_cny`（`backfill_trade_cny`）。
- **港股显示口径（券商方式，用户确认）**：单价（现价/成本）显示港币，金额（市值/盈亏/成交金额）显示人民币；个股页现价/市值卡 + 买卖弹窗显示 `≈¥` 人民币换算。交易流水/AI 评分明细金额用 `amount_cny`。

### 组合穿透式指标（portfolio.py）
- 全部人民币口径；股票/ETF/港股都参与资产与风险指标。
- `归属利润 = 持股数÷总股本×公司TTM利润`；综合 PE = Σ市值/Σ归属利润；PB/ROE 同口径，**ROE% = PB/PE×100**。
- 每指标返回 `coverage_weight`，<70% 前端显示"覆盖不足"（仅提示，不作打分硬约束）。
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
- **指数/ETF 估值列（normalize_index_valuation_http）**：乐咕 `index-basic-pe|pb` 返回多套口径，`ttmPe/lyrPe/pb` 是**等权算术平均**（对银行为主的指数虚高，沪深300 PE≈35.7/PB≈4.47）；标准口径用 `add*` 列（**整体法：总市值÷归母净利/净资产总和**）：`addTtmPe`（缺/全 0 回退 `addLyrPe`，恒指场景）与 `addPb`。已修正：沪深300 PE 13.71/PB 1.44、上证50 11.48/1.23、中证红利 8.32/0.8。
- **指数估值刷新（refresh_index）**：`pe_source/pb_source=legu` 的指数（沪深300/上证50/科创50/中证红利/恒指）刷新时调 `sync_valuation`（带实时点位价）拉序列+分位；`none` 源跳过。ETF 估值 = 跟踪指数估值（`etf_index_map` → `index_defs` 乐咕代码）。

### 成本调整与分红除权（holdings.py / dividend.py）
- `POST /holdings/{code}/cost-adjust`：amount（成本，正=加 负=减）+ delta_qty（股数，拆股/送股）；插入 `adjust` 交易，重放只改成本/股数（adjust 不参与 AI 打分）。
- 分红除权：启动进程/全量刷新时扫描持仓，今天除权的自动按 `每股分红×持仓` 摊薄成本；`dividend_adjustments` 表幂等。
- 手动「📅 分红除权」按钮查最近除权（东财）填入；`is_dividend=1` 标记计入**累计分红**。
- 累计分红 = trades 中 `side='adjust' AND is_dividend=1` 的 `SUM(-amount)`。

### 今日盈亏（券商口径）
- `_day_pnl`：前日持仓用 `(现价−昨收)`；当日买入用 `(现价−买入均价)`；当日卖出 FIFO（先前日昨收、后当日买入均价）；减当日费用。
- 按现价买入当日盈亏 = 0（不是按昨收算亏）。

### 前端约定（static/）
- **api.js**：所有请求经 `api()`；全屏 loading 屏障**仅** `opts.blocking:true` 的耗时请求（页面加载/刷新/导入/下载）显示，快速操作默认不弹；搜索联想用 `silent:true`。支持 FormData（不 JSON.stringify、不手动 Content-Type）。
- **交易评分页（trade.html）**：左目录 + 右详情布局。`.row` 高度 `70vh` + `flex-wrap:nowrap`；`day-dir`/`day-detail` 需 `min-height:0` 才让 `overflow-y:auto` 生效（flex 子项默认 min-height:auto 会阻止滚动，踩过坑）。
- **个股页指数模式（stock.html）**：`render` 按 `d.is_index`（或 URL `?index=1`）隐藏买卖/五档/财务/诊股面板，资金流趋势改「指数资金面（全量价）」量价图（`renderIndexFlow` 调 `/indices/fundflow?codes=X` + `indexVolumePrice`，镜像 indices.html）；**估值历史/历史分位按是否有乐咕数据展示**（有序列才显示估值线，有分位才显示分位图，拿不到隐藏——`render` 指数分支用 `d.valuation_history.periods`/`d.quantiles` 判断）。`/api/stocks/{code}` 对指数代码优雅返回 `is_index=True`（无 409），搜索打开指数也走此模式；前端 `lastIsIndex` 守卫买卖/刷新/预期增速（URL 无 `index=1` 时不可交易）。ETF 页面估值 = 跟踪指数估值，与指数页同源一致。
- **资金流图股价/净值折线**：腾讯分笔原始行第 3 段是价格，`_parse_tick_page` 返回 4 元组 `(time,amount,sign,price)`，`aggregate_ticks`/`resample_points` 取窗口末笔价存 `fundflow_15m_cache.price`（v9 迁移）。`build_detail` 分时点带 `price`、日级点带 `price`（`daily_price_cache` 收盘价）；`portfolio_fundflow` 额外算**组合净值线** `price`（Σ价×股数，仅参与资金流的 A股/ETF，避免与港股混币），**分时价前向沿用**（持仓缺价 → 沿用最近价，净值不砍半/不 null）。前端股价/净值**独立成图 `fundflowPrice`**：个股页 `#flowPrice` 放资金流趋势面板最上方，只看价格自身走势、独立自适应刻度；**组合页 2×2 布局**（第一排 净值+净流入，第二排 买盘/卖盘+全天五档）。指数模式用 `indexVolumePrice` 已带价故隐藏独立图。`resampleFlow`/`bucketFlowDays` 带桶末价。AI 资金流上下文本已带价格（分钟/日级），无需改。
- AI ScoreCard：`grade` 徽章 A→优秀…（质量）；`action` 单独药丸（加仓/持有/观望…）；风险条用 `risk`/`risk_level`。未配置 AI 时评分区显示「未配置 AI」。
- **start.cmd 必须纯 ASCII**（cmd 按 GBK 解析 UTF-8 中文会崩）；`if` 括号块内不能有 `(3,10)` 这类括号（被当块结束），版本检查移到独立标签。
- Windows 打包态的数据库/日志/运行状态位于 `%LOCALAPPDATA%\StockAnalyzer`；静态资源从 PyInstaller `_MEIPASS` 只读加载。`STOCK_APP_HOME` 仅用于测试/便携覆盖。
- Windows 启动器必须保持单实例、只监听 `127.0.0.1`，用 `/api/health` 判断服务身份；安装包严禁包含仓库 `data/` 中的个人文件。

## 关键坑（踩过，改前注意）
- SQLite 写事务内不要再开连接写（如汇率落库）会 "database is locked"——汇率计算移到事务外。
- `_MIGRATE_COLUMNS` 加新列后，旧库需手动 `ALTER TABLE`（migrate 只在版本落后时跑；已到目标版本不会重跑）。
- **v5 迁移彻底移除公式评分**：`_drop_formula_scoring` DROP `trade_score_snapshots`/`daily_scores`；历史 v2 迁移里的 `_recreate_daily_scores_nullable` 保留只为旧库升级（新库该表未建，`PRAGMA table_info` 空表早退）。
- AI 打分是慢操作：`chat_json` 最长 180s；后台自动打分失败只记日志，当日保持"未评分"（`maybe_auto_score_daily` 先同步 invalidate）。`_trigger_ai_daily`/`_invalidate_portfolio_ai` 必须放在写事务外调用。
- 全市场列表（A股 `stock_info_a_code_name` + 场内 ETF `fund_etf_spot_em` 存 `etf_list.json` + 港股）只在**启动后台预热**时联网下载（`preload_market_lists`，幂等：缓存新鲜读文件不发网络）；`/stocks/search` 与名称回填只读本地缓存（GET 零网络、绝不阻塞）；测试在 conftest mock 为空防联网。
- Windows 跑 pytest 需 `--basetemp`（默认临时目录权限失败）。
- `_ensure_stock` 写 name；`stock_detail` 名称缺失时从列表回填到 stocks 表。
- **送转/拆股后总股本会变（公司玩送转），季报 EPS 与雪球 reg_asset 都是旧股本基准**：`net_profit/EPS` 兜底只会还原旧股数 → 市值/PE/PB 全错（伯特利 603596 6.06亿→8.97亿，市值 161亿 vs 真实 239亿）。A 股总股本一律优先取腾讯实时「总市值÷现价」（`AshareInstrument.total_shares`，当天股数），雪球降级；净利/每股净资产折算时须用「每股净资产_最新股数」（当前股本口径）×当前股本，禁止报告期口径「每股净资产」×当前股本（虚高）。改完需全量刷新才更新 `financial_cache.total_shares`。

## 测试
- 367 用例。重点：ai_scoring（标签偏好/组合画像哈希与 stale/每日偏好去重与 trade_id 对齐/自动触发/ScoreCard dimensions·action·risk，mock chat_json 离线）、ai（诊股 10 维含消息/技术 + score/grade/action/stale/upgrade_legacy、分析强度 HTML 门控含资金流）、ai_news_tech（消息面/技术面：as_of 注入、时效空态、剥离规则、技术面 bars as-of 锚点截断、批量落库/单只失败/tags-codes 400、迁移建表、HTML 门控）、portfolio（穿透式/分段分位/覆盖门槛/今日盈亏）、fx（港股折算/汇率）、dividend（除权幂等/累计分红/东财降级）、migration（旧库升级 + v8 资金流 html 列 + v9 分时 price 列）、api（409 CACHE_MISS/GET 零写入/AI 评分端点/指数代码返回 is_index）、instruments（判型/ETF 名称搜索/指数刷新补估值）、indices（估值列 add* 整体法）、fundflow（分时点带 price）、Windows 打包路径/健康检查/单实例复用/本地 ECharts。
- conftest：每测试独立临时 DB，mock build_manager 为 MockProvider、quote 固定、列表为空。测试灌数据一律用 as-of 锚点（`resolve_trade_day(None)[0]`），不用 `date.today()`，否则周末必红。
- conftest：每测试独立临时 DB，mock build_manager 为 MockProvider、quote 固定、列表为空。

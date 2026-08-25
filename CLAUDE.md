# 项目上下文（Go 版记忆）

股票持仓分析系统：把自己的一篮子持仓（A 股 + 港股 + 场内 ETF）当组合分析。
**Go 单后端**（gin + gorm + glebarez/sqlite，零 CGO）+ 原生 JS/ECharts 前端（无构建）+ Android APK 壳。
数据源 HTTP 直连（腾讯/新浪/东财/百度/乐咕/中行）。Python 后端与 Windows 打包链已删除（git 历史可找回）。

## 分层红线（最高优先级，所有数据相关代码必须遵守）

```
route（internal/route）    第1层：gin 路由 + handler（参数校验 → 调 service → 组响应）
  ↓ 只依赖 service 接口（Services 结构不暴露 DB/DAO/缓存句柄，零 db/dao 触点）
service（internal/service）第2层：业务逻辑（多态子包：market/finance/valuation/portfolio/ai…）
  ↓
db/dao（internal/db/dao）  第3层：数据访问（gorm 查询）
raw（internal/raw）        第4层：真实接口访问（腾讯/新浪/东财/百度/乐咕 HTTP）
```

- **route 禁止持有 `*gorm.DB`/DAO 句柄**，所有数据访问必须经 service 方法；service 依赖由 cmd/server/main.go 装配注入（构造注入 + 函数回调）。
- **货币统一在 service 层**：各平台原始货币一律折算人民币（港股财务为记账本位币、市值恒为港元）；任何「市值÷净利」「每股股息÷股价」必须先同货币（青岛港混币出 8.65 实为 7.7 的教训）。
- **判定功能货币**：东财港股主指标带 EM 计算的 PE_TTM/PB_TTM（币种自洽），作锚点重算比对判定报表货币。`IS_CNY_CODE` 是交易计价币种非报表货币，不可用。
- **口径下沉**：TTM/同比/货币折算/单位统一只在 service 层算，上层只消费统一模型。

## 工程原则（新增代码必须遵守）

- **零重复**：同一份逻辑只写一次，**禁止复制粘贴式复用**。发现重复立即抽取为公共函数/类型/接口，调用方只消费不重写。
- **单一实现**：同一个功能（汇率折算、分位计算、PE/PB 口径、资金流聚合、fullCode 判定等）**全仓唯一实现**，所有链路复用该实现。新增路径先找既有实现，不存在再新增，**禁止另起一套**。
- **口径一致**：计算口径是全局契约，任何修改必须同步到所有消费方并通过既有单测/口径校验。**改一处口径必须改全仓**，不允许新旧口径并存。
- **多态与抽象优先**：同类变体（A 股/港股/ETF、腾讯/新浪/东财数据源、实时/前瞻估值）用**接口多态 + 降级链/策略模式**抽象，不用 `if/else` 分支堆砌。新增变体只加实现，不改调用方。
- **层级清晰**：抽象分层与目录结构一一对应，依赖单向（route→service→dao→raw），**禁止跨层直连与循环依赖**。公共能力下沉到最底层可复用处，上层只做编排。

## 快速上手

```bash
cd backend
STOCK_APP_HOME=../data go run ./cmd/server    # 默认 8000；STOCK_PORT 或 --listen host:port 可改
GOCACHE=/tmp/gocache go test ./...            # 测试（GOCACHE 绕开被拦截的默认缓存目录）
```

## 代码结构（backend/internal）

- `route/` 85 端点；`service/`：ai（诊股/消息面/技术面/资金流/组合与每日打分）、calendar、datamanage、detail（409/as_of/分位重算）、dividend（除权幂等）、finance（多态降级链 A股/港股）、fx、holdings（重放法）、indices、jobs（双车道 worker+batch+ws 广播）、market（行情/资金流多态降级链）、model（Bar/Quote/Financials）、portfolio（穿透/序列/标签分组/资金流）、quote（零网络缓存读+K线+搜索）、refresh（动态/全量/单股/周月K/港股分时/指数）、settings、stockmeta、valuation（实时+前瞻+分位）、volatility、ws（hub）
- 配置：`config.Load`（`STOCK_APP_HOME` 优先；`STOCK_PROJECT_ROOT` 决定 static 目录；`STOCK_PORT` 端口；`--listen` 覆盖）

## 核心口径（用户确认，改前必读）

### 数据层：缓存优先 + 手动刷新
- **GET 只读缓存，零网络、零数据库写入**。唯一数据入口是右上角刷新按钮。
- 缓存三类：原始缓存（行情/财务/估值/汇率，长期保留）、AI 评分报告（手动 POST 触发落库）、派生缓存（组合序列+分位，带 `portfolio_hash` 失效重建）。
- 清仓只改持仓状态，**不删原始缓存**（再次开仓复用）。
- GET 缓存缺失返回 HTTP 409 CACHE_MISS（个股详情），前端弹窗询问下载。

### AI 评分（ScoreCard 统一；公式评分已彻底移除）
- 组合打分 + 每笔交易/每日打分 + **个股诊股**全部由 AI 产出统一 **ScoreCard**（`score`/`grade`/`action`/`risk`/`confidence`/`dimensions`…）；公式评分已移除（`trade_score_snapshots`/`daily_scores` 表 v5 迁移 DROP）。
- **质量与动作拆开**：`grade`=质量（A优秀/B良好/C一般/D较差，三处统一）；`action`=操作建议（个股/组合 `add|hold|watch|reduce|exit`；交易 `repeat|cautious|avoid`）。**禁止**用 A=强烈推荐等动作语义。后端只做枚举校验（非法 grade→C、action→hold/cautious），**不做 score→grade 转换**；明显冲突只记 warning。
- **共享 5 维** `fundamentals/valuation/fundflow/news/technical`；个股追加 5（共 10）、组合追加 `structure/tag_fit`、交易追加 `timing/execution/sizing/discipline`。消息/技术进打分：context 塞 `as_of_datetime` + `bars`（`build_technical_bars`），有专项报告则附摘要优先采信。
- **新鲜度**：组合 `profile_hash`→`stale`；个股诊股快照哈希变化→`stale`；读取层内存映射老键（`rating`→`grade`、`risk_score`→`risk`）不回写。前端三页共用 `renderScoreCard`（common.js）。
- **标签偏好（tag_prefs）**：每标签短句 → 保存时自动请求 AI 补全成完整「评分指引」（draft），确认后（confirmed）才用于打分；提供手动「AI 补全」。
- **组合打分跟随标签筛选 + 按组合分开存储**：`POST /api/ai-scoring/portfolio?tags=红利,科技` 只打该子组合；「全部」= 全持仓 + 全部已确认偏好。**每个标签组合各存一份**（`ai_portfolio_reports.tags_json`）；打个股不覆盖 个股+港股，也不覆盖 全部；同一组合画像变化标 `stale` 保留旧分。
- **每日打分偏好去重**：一天 50 笔时 `tag_prefs` 只放当天涉及的标签各一份，每笔交易只带 `tag` 名 + 该股因子。
- **触发**：组合 = 手动按钮（GET 读最新报告，`stale`=画像哈希变化提示重新打分）；新交易 = 录入时 `MaybeAutoScoreDaily` 失效并后台重打分（无激活模型/当日无交易不起线程）。
- **HTML 详细报告**：五种 AI 分析仅「深入」强度要求**自包含 HTML**（≥1000字、内联 CSS、无外部依赖、禁止 `<script>`）；快速/普通不要求（schema 去 `html` 字段）。组合深入 HTML 必须含「组合调仓指引」表（逐标的建议操作/关键价格/目标仓位/一句话理由，表尾声明「仅供参考，不构成投资建议」）。
- **可编辑提示词覆盖持久化**：9 个 AI 入口共用弹窗（`openPromptEditor`）默认显示已保存的自定义版本，存 config 表 `ai_prompt_overrides`（JSON `{kind: 文本}`）；端点 `GET/PUT /api/ai/prompts`，null/空串=清除恢复默认。
- **思考级别（用用户配置，不做强度覆盖）**：`chat_json` 附 `reasoning_effort` 一律取用户全局配置（config 表 `ai_reasoning_effort`，默认 high），**不因分析强度压级/拔高**。provider 不支持时降级重试移除该参数。
- **输出预算与超时**：`_AI_MAX_TOKENS=81920`（deepseek-v4-flash 接受；8192 时高思考级常被 reasoning 烧光）；`AI_REQUEST_TIMEOUT=300s`。可配 `ai_max_tokens`（2048~262144）/`ai_request_timeout`（30~1800），`GET/PUT /api/ai/runtime` 即时生效。提供商拒绝大值降级 `16384` 重试。
- **输出格式双端强化**：带 schema 的 AI 调用统一用「schema 写在开头与结尾双端 + 中间夹输入数据」，两端重申「只输出严格 JSON、不要额外文字、不要 markdown 围栏」。
- `profile_hash` 只基于持仓(code/qty/currency) + 已确认偏好 + 标签筛选 + 模型——**绝不含时间戳/价格**。

### 消息面 / 技术面 AI（专项深入层）
- 两张表 `ai_news_reports` / `ai_tech_reports`（PK `code+as_of+source`，`source='single'/'batch'`，幂等补建）。个股 GET 取最近一条（`ORDER BY as_of DESC, updated_at DESC LIMIT 1`）。
- **时效机制**：user JSON 注入 `as_of_datetime`（本地时区带时区 ISO）；prompt 强制 `_ASOF_RULES`——AI 相对该时刻判定时效，过时不输出，宁可 `items: []` + `omit_reason`。后端轻量规整：剥离无 `event_date` 或自标过时 item、枚举兜底（stance→neutral、trend→range）、**空结果视为成功**；不设 N 天死阈值。
- 技术面原始数 = `daily_price_cache` 日K，`build_technical_bars(code, as_of, limit=120)`（`resolve_trade_day` 截断最近交易日，无数据返回 `[]`）。阶段二打分 context 复用。
- 端点：`/api/ai/news-analysis|news-report|news-reports|news-batch|news-coherence`，技术面同形。批量 tags/codes 互斥 400、无模型 400；逐只落库 `source='batch'`，单只失败记日志继续。
- **HTML 门控**：个股/批量**深入**均要求 `html`。批量 HTML 是整组合整体输出，落 `ai_news_coherence_reports`/`ai_tech_coherence_reports`（scope='portfolio'/'indices' + scope_key）；每次批量写最新一条，GET 按 as_of DESC 取最近。
- 前端：个股页 `newsAiPanel`/`techAiPanel`（指数模式隐藏消息面、保留技术面）；组合页 AI 面板四 tab（打分/消息面/技术面/资金流）；持仓页不展示这些列。共享渲染在 common.js（`renderNewsPanel`/`renderTechPanel`/`renderBatchListPanel`/`newsStanceBadge`/`techTrendBadge`/`runNewsBatch`/`runTechBatch`/`loadNewsCoherence`/`loadTechCoherence`）。`_EDITABLE_PROMPTS` 增 `news/technical/news_batch/tech_batch`。

### 港股折算
- 五位代码（06198/00700）→ `currency='HKD'`；港股**不再复用 ETF 分类**（is_etf 仅按场内基金代码/ETF 标签）。
- **汇率新浪实时** `hq.sinajs.cn/list=HKDCNY`（1 HKD = 买卖价中点 CNY）。缓存 fx_rate_cache；交易存 `fx_rate/amount_cny`，持仓双币种成本。
- 缺汇率**绝不按 1:1**：返回 `missing_fx`，从人民币汇总剔除。
- **汇率时机**：启动进程强制拉一次 + 仅「全量刷新」重拉；动态刷新不拉。汇率可用后自动回填缺失的港股 `amount_cny`。
- **港股显示口径（券商方式）**：单价（现价/成本）显示港币，金额（市值/盈亏/成交金额）显示人民币；个股页现价/市值卡 + 买卖弹窗显示 `≈¥` 人民币换算。交易流水/AI 评分明细金额用 `amount_cny`。

### 组合穿透式指标（portfolio）
- 全部人民币口径；股票/ETF/港股都参与资产与风险指标。
- `归属利润 = 持股数÷总股本×公司TTM利润`；综合 PE = Σ市值/Σ归属利润；PB/ROE 同口径，**ROE% = PB/PE×100**。
- 每指标返回 `coverage_weight`，<70% 前端显示"覆盖不足"（仅提示，不作打分硬约束）。
- **亏损股负值全程参与**（负 PE/PB/ROE/利润/预测利润）；仅分母为零才"不适用"。
- 首页 pe_pct/pb_pct 来自打包组合历史序列分位（`portfolio_valuation_cache`），非个股加权。
- 分段排序分位：正小→大 → 0 → 负（绝对值大→小），如 `8,15,30,0,-100,-20,-5`。
- 序列样本：只取当前日期前、单日市值覆盖率≥90%、≥60 天。历史数据升序、真实交易日。
- `portfolio_hash` **只基于持仓**（code/quantity/currency）——不要把估值序列 `updated_at` 纳入，否则缓存频繁失效（踩过坑）。
- 股息率穿透式：`Σ分红 ÷ 组合总市值`（含不派息股）。
- 子集（tags 筛选）组合序列实时打包（`ComputePortfolioSeries` 按权重逐日重算，单日覆盖≥0.90 门槛）；全量组合读缓存（hash 校验）。

### 前瞻 PB / PE（valuation）
- 预测年末净资产 = 上年年末净资产 + 预测净利 − 预测分红；前瞻 PB = 市值/预测年末净资产。
- 降级链：增速(用户→TTM→财报同比→0%)、支付率(用户→上年→0%)、上年末净资产(年报→每股净资产×股本→次级推演)。
- 置信度：完整年报 high / 次级推演 medium / 零假设 low；预测净资产为负 → 负 PB + `invalid`。
- 组合前瞻 PE/PB 用**穿透汇总**（Σ市值/Σ归属预测净利/净资产），不做个股加权。

### 指数/ETF
- 指数估值（`index_basic_pe/pb` 多套口径）：标准口径用 `add*` 列（整体法：总市值÷归母净利/净资产总和）`addTtmPe`（缺/全0 回退 `addLyrPe`）/`addPb`；`ttmPe/lyrPe/pb` 是等权算术平均（虚高，禁用）。
- 指数估值刷新：`pe_source/pb_source=legu` 的指数刷新时同步估值序列+分位；`none` 跳过。ETF 估值 = 跟踪指数估值（`etf_index_map` → `index_defs` 乐咕代码）。
- 指数分时资金面：`syncIndexIntraday` 腾讯 mkline 量价按日落库（`index_intraday_cache`），价格 round3；指数资金面只在盘中刷新同步，非交易日为空。

### 资金流
- A股/ETF：腾讯分笔 tick 五档聚合（`fundflow_15m_cache`），band P15/P40/P75/P95。
- 港股：腾讯分时分钟量价按价向派生 tick（tick rule），近 5 日窗口，逐日落库。
- **盘前污染双保险**：腾讯分笔接口不带日期，盘前刷新会拉到昨日数据 → 落库前按 `p.ts <= now` 过滤超前点 + 删今日残留；`fetch_ticks` 检测游标末笔超前 → 自愈重置快照全量重拉。
- 资金流图带价格：分时点 `price`（分笔第3段），日级点收盘价；组合资金流额外算**组合净值线**（Σ价×股数，**仅参与资金流的 A股/ETF**，避免与港股混币——**港股参与组合资金流**（用户明确，与 Python 参考不同），但不参与净值线；分时价前向沿用不砍半）。
- 指数/ETF 资金面量价图（`indexVolumePrice`）。

### 成本调整与分红除权（holdings / dividend）
- `POST /api/holdings/{code}/cost-adjust`：amount（正=加 负=减）+ delta_qty（拆股/送股）；插入 `adjust` 交易，重放只改成本/股数（adjust 不参与 AI 打分）。
- 分红除权：启动进程/全量刷新扫描持仓，今天除权的按 `每股分红×持仓` 摊薄成本；`dividend_adjustments` 表幂等。
- 手动「📅 分红除权」按钮查最近除权（东财）填入；`is_dividend=1` 标记计入**累计分红**。
- 累计分红 = trades 中 `side='adjust' AND is_dividend=1` 的 `SUM(-amount)`。

### 今日盈亏（券商口径）
- `_day_pnl`：前日持仓用 `(现价−昨收)`；当日买入用 `(现价−买入均价)`；当日卖出 FIFO（先前日昨收、后当日买入均价）；减当日费用。
- 按现价买入当日盈亏 = 0（不是按昨收算亏）。

### 周末行为（用户确认的改版）
- 刷新目标日 = 最近交易日（`resolve_trade_day`），**不按周几跳过**；「今日是否开盘」由当日日 K 是否存在判定（`hasTodayKline`）；targetDate==today 时才过滤超前分笔。

## 前端约定（static/）
- **api.js**：所有请求经 `api()`；全屏 loading 屏障**仅** `opts.blocking:true` 的耗时请求显示，快速操作默认不弹；搜索联想用 `silent:true`。支持 FormData。
- **个股页指数模式**：`render` 按 `d.is_index` 隐藏买卖/五档/财务/诊股面板，资金流趋势改「指数资金面」量价图；`/api/stocks/{code}` 对指数代码优雅返回 `is_index=True`（无 409）；前端 `lastIsIndex` 守卫买卖/刷新/预期增速。
- AI 入口收敛：个股只留「🤖 AI诊股」（`openAiExtPicker` items diagnose/news/tech/flow，每项可改提示词 promptKind）；组合「🤖 AI诊组合」+ 4 tab 面板（打分/消息面/技术面/资金流），批量项 promptKind portfolio/news_batch/tech_batch/batch。
- REFRESH_OPTIONS 全量含 `news`（个股新闻，AI 消息面用），新闻进全局刷新链路（刷新时预拉）。
- AI ScoreCard：`grade` 徽章 A→优秀…（质量）；`action` 单独药丸（加仓/持有/观望…）；风险条用 `risk`/`risk_level`。未配置 AI 时显示「未配置 AI」。
- 资金流图股价/净值独立成图 `fundflowPrice`：个股页放资金流趋势面板最上方；组合页 2×2 布局。

## 关键坑（踩过，改前注意）
- SQLite 写事务内不要再开连接写（如汇率落库）会 "database is locked"——写外置。
- 迁移加列后旧库需手动 ALTER（migrate 只在版本落后时跑）。
- Go `int64()` 截断 vs Python `round(,0)`——所有截断点用 `math.Round`。
- 分位计数用 `<=` 不是 `<`；分段 key 排序：pos(0→大) → 0 → neg(|值|大→小)；样本 ≥60 天。
- 腾讯分笔 `toSymbol` 规则（沪 60/68/90/50/51/56/58；深 00/30/39/15/16/20；北 43/82/83/87/92；港股 5 位）——ETF 前缀缺失会拉空。
- 送转/拆股后总股本变化：A 股总股本优先取腾讯实时「总市值÷现价」，雪球降级；净利折算须用最新股数口径。
- Go 构建缓存默认目录被拦 → `GOCACHE=/tmp/gocache`。
- 服务重启：`lsof -ti :8081 | xargs kill`，确认 0 后再起，否则 "bind: address already in use" 且旧进程掩盖新行为。
- Android 壳：`Process.pid()` 不可用；assets 不可直接 exec/读文件，须解压到 filesDir；Go 静态目录经 `STOCK_PROJECT_ROOT` 指向 filesDir；**AppCompatActivity 必须配 AppCompat 主题否则必崩**。

## 测试
- Go 15 个包全部离线；重点：detail（409/as_of/分位重算）、holdings（重放/交易/标签）、portfolio（穿透/lite/标签分组/分位）、refresh（items 过滤/周月K/batch/港股分时/指数）、ai（ScoreCard/每日自动打分/消息面技术面/HTML 门控）、jobs（任务/批/ws 广播）、settings、datamanage、ws hub。
- 测试灌数据一律用 as-of 锚点（`resolve_trade_day`），不用 `date.today()`，否则周末必红。

## 打包（APK）
- `.github/workflows/android-apk.yml`：workflow_dispatch / push(main, refactor/golang-backend, v*) / 标签自动发 Release。
- 本地：`packaging/android/build.sh`；APK 产物 `dist/android/`（gitignore）。
- Android 壳：GoServer.kt 把 assets/bin/stockanalyzer-server 解压执行（`--listen 127.0.0.1:8080`），前端同步 filesDir/static，`STOCK_APP_HOME`=filesDir/data。
- 版本：`versionName` 在 packaging/android/app/build.gradle.kts；标签须 v{versionName} 一致。

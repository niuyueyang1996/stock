# 阶段二：打分统一化（组合 / 个股 / 交易共用一张评分卡）

> 实施计划（**已实施**）。**本文件自成一体**，包含两件事：把三处打分统一成同一张评分卡，并把消息面 / 技术面正式接入打分。下钻（组合维→持仓）为体验加分项，未做（组合报告无逐只同名维分）。
>
> 前置：[阶段一 消息面/技术面详细分析](plan-news-tech-ai.md) 提供 `now_as_of_datetime()`、`_ASOF_RULES`、`build_technical_bars` 与两张专项报告表。**若先做本阶段**，需要把这三个公共件一并实现（见「对阶段一的依赖」一节），专项报告摘要则按缺失降级处理。

## Context：现在三处打分不一致（改前基线，已核对代码）

- **总分缺一处**：组合 `_PORTFOLIO_OUTPUT_SCHEMA` 有 `score` 0-100，每日 `_DAILY_OUTPUT_SCHEMA` 有 `score`，但**个股诊股 `_OUTPUT_SCHEMA` 没有总分**，只有 `rating` + `risk_score`。用户没法回答「这只股几分」。
- **评级语义打架（最伤体验）**：个股前端 `AI_RATING` 把 A/B/C/D 解释为「强烈推荐 / 推荐 / 中性 / 回避」（动作），而 `ai_scoring._RATING_NAMES` 把 A/B/C/D 解释为「优秀 / 良好 / 一般 / 较差」（质量）。同一个「A」在两个页面不是一回事。
- **分项覆盖不均**：个股有 8 维 `{score, analysis, risk, data_source}`；组合**完全没有分项**（资金面只是被要求「纳入分析」，界面看不到独立评价）；每日只有逐笔 `score/rating/comment`，没有维度。
- **风险表达不统一**：个股有 `risk_score`（0-100）+ 每维 `risk` 三档；组合与每日只有 `risks[]` 文本。
- **新鲜度只有组合有**：组合靠 `portfolio_profile_hash` 给 `stale` 提示；个股诊股只有 `updated_at`，数据早变了也不提醒；每日打分是录入交易后自动重打。

另外，消息面 / 技术面此时只有专项报告（阶段一产物），**尚未进入任何打分**。

结论：不是「再加两个维度」能解决的，需要统一顶层模型，并在同一次改动里把消息/技术接进去，避免同一段 prompt 与 schema 改两遍。

## 统一模型：ScoreCard

三种对象输出同一顶层结构（各自 `report_json` 内的形状统一）：

```json
{
  "subject": {"kind": "portfolio|stock|trade_day", "key": "标识", "name": "显示名"},
  "score": 78,
  "grade": "B",
  "grade_name": "良好",
  "action": "hold",
  "action_name": "持有",
  "risk": 45,
  "risk_level": "medium",
  "confidence": "high",
  "summary": "一句话结论",
  "dimensions": {
    "valuation": {"score": 82, "grade": "A", "analysis": "…"},
    "fundflow":  {"score": 55, "grade": "C", "analysis": "…"},
    "news":      {"score": 60, "grade": "B", "analysis": "…"},
    "technical": {"score": 48, "grade": "C", "analysis": "…"}
  },
  "advice": [], "risks": [], "reasons": [],
  "as_of": "2026-08-09T15:21:00+08:00",
  "html": ""
}
```

### 1. 拆开「质量」与「动作」

- `grade`：**质量分级**，三处统一为 A 优秀 / B 良好 / C 一般 / D 较差。
- `action`：**操作建议**，独立字段。
  - 个股与组合：`add`(加仓) / `hold`(持有) / `watch`(观望) / `reduce`(减仓) / `exit`(清仓)
  - 交易复盘：`repeat`(可复制) / `cautious`(谨慎复制) / `avoid`(避免重复)

好处：「好公司但现在贵」可表达为 `grade=A` + `action=watch`，不再让一个字母兼职两种含义。个股原先「A=强烈推荐」的动作含义迁移到 `action`。

### 2. 四要素三处都有

- `score` 0-100：**个股补上**总分。
- `grade`：语义统一。
- `risk` 0-100 + `risk_level`(low/medium/high)：**组合与交易补上**风险分（个股已有 `risk_score`，改名并保留兼容读取）。
- `confidence`(high/medium/low)：数据可信度。组合直接用现成的 `coverage_weight`（`analysis/portfolio.py` 每指标已返回），个股用现有 `*_confidence` / `*_source` 归纳，交易用 `asof_fallback` 是否命中。

### 3. 维度体系：共享 + 专属

共享 5 维（三处同名，可横向比较、可下钻）：

- `fundamentals` 基本面
- `valuation` 估值
- `fundflow` 资金面
- `news` 消息面
- `technical` 技术面

对象专属维度：

- 个股追加：`moat` 护城河、`cyclicality` 周期性、`growth` 成长、`dividend` 股息、`competition` 同业竞争（即现有 8 维中剩下的 5 个，键名不变）
- 组合追加：`structure` 结构集中度、`tag_fit` 标签契合度（对 `tag_prefs` 已确认指引的遵守程度）
- 交易追加：`timing` 时机、`execution` 价格执行、`sizing` 仓位管理、`discipline` 纪律

每维统一 `{score, grade, analysis}`（个股沿用的 `risk` / `data_source` 作为可选附加字段保留，不删）。组合由此自然获得资金面 / 消息面 / 技术面的分项评级。

### 4. 消息面 / 技术面如何进打分（对齐资金面）

资金面现在的做法是：把缓存原始数直接塞进打分 context，**不要求**先跑专项分析。消息/技术照抄：

- **技术面**：直接塞 `build_technical_bars` 的日 K 当原始数。
- **消息面**：无正文库 → 塞 `as_of_datetime` + code/name，由模型按 `_ASOF_RULES` 补，过时不写。
- **若已有阶段一的专项报告**：额外附摘要（`stance`/`trend`/`summary`/`omit_reason`），prompt 声明「有报告优先采信」；没有也不挡打分。

### 5. 新鲜度统一

- 组合：沿用 `portfolio_profile_hash` → `stale`（**组成规则不变**，不把专项报告时间戳写进哈希，避免历史报告集体 stale）。
- 个股：新增诊股快照哈希（价格档位 + 财报报告期 + 资金流日期 + 模型名），变化则 `stale=true`，展示「数据已更新，建议重诊」。
- 交易：沿用录入后自动重打；当日报告存在但当天又有新交易 → `stale`。

三处共用同一个 chip 样式与「重新打分」按钮。

### 6. 口径护栏（公共 prompt 片段 `_SCORE_RULES`）

- 分数锚点：80+ 优秀 / 60-79 良好 / 40-59 一般 / 40 以下较差；`grade` 必须与 `score` 区间自洽。
- 保持现有原则：**评级由 AI 直接给出，后端不做 score→grade 换算**；后端只在明显冲突（如 85 分给 D）时记 warning，不改数据。
- `risk` 与 `grade` 解耦：允许「高质量 + 高风险」。
- `action` 必须能被 `advice` 支撑，且给出触发条件。
- 缺数据时降 `confidence` 并写明缺什么，禁止臆造。

## 对阶段一的依赖

本阶段直接复用阶段一的三个公共件：

- `now_as_of_datetime()`：所有打分请求注入当前时间。
- `_ASOF_RULES`：消息面时效约束（打分里的 `news` 维同样适用）。
- `build_technical_bars(code, as_of, limit)`：`technical` 维与组合 context 的日 K 来源。

若跳过阶段一直接做本阶段：把这三个件一并实现（约半天量），并把「专项报告摘要」相关分支按「无报告」降级即可，其余不受影响。

## 后端改动

### 公共定义（app/services/ai.py）

`GRADE_NAMES` / `ACTION_NAMES` / `RISK_LEVELS` / `SHARED_DIMENSIONS` / `_SCORE_RULES` 抽到 `ai.py`，`ai_scoring.py` 复用。现在 `ai_scoring._RATING_NAMES` 与前端 `AI_RATING` 两套并存的问题在此收口。

### 个股诊股（app/services/ai.py）

- `_OUTPUT_SCHEMA`：加 `score`、`action`；`rating` → `grade`（读取层兼容老键）。
- `DIMENSIONS`：现有 8 维中的 `fundamentals`/`valuation`/`fundflow` 归入共享维，新增 `news`、`technical`，其余 5 维保留为个股专属，共 10 维；`DIMENSION_CN` 同步。
- `build_stock_context`：加 `as_of_datetime` 与 `bars`；若有专项报告则附摘要。
- `_SYSTEM_PROMPT`：并入 `_SCORE_RULES` + `_ASOF_RULES`，说明 `grade` 与 `action` 的分工。
- 新增诊股快照哈希与 `stale` 计算。

### 组合打分（app/services/ai_scoring.py）

- `build_portfolio_context`：与 `fundflow` 同级新增 `as_of_datetime`、`technical`（筛选内持仓精简 bars，按权重裁剪控 token）、`news_meta`（`{as_of_datetime, stocks:[{code,name}]}`）、以及可选的 `news_reports` / `tech_reports` 摘要。
- `_PORTFOLIO_OUTPUT_SCHEMA`：加 `dimensions`（共享 5 维 + `structure` + `tag_fit`）、`action`、`risk`、`confidence`。
- `_normalize_portfolio_report`：`score` clamp、`grade`/维度 `grade` 走 `_check_rating` 非法兜底 `C`、`action` 非法兜底 `hold`、`risk` clamp、整块缺失返回 `{}`。
- `_PORTFOLIO_SYSTEM`：要求把消息面/技术面与资金面并列纳入，有专项报告优先采信，过时不写。

### 交易复盘（app/services/ai_scoring.py）

- `_DAILY_OUTPUT_SCHEMA`：加 `dimensions`（交易 4 维）、`action`、`risk`；逐笔项加 `action`。
- `_normalize_daily_report`：同样的枚举兜底与 clamp；逐笔 `trade_id` 对齐逻辑不变。
- `_stock_factors`：挂精简 bars 与 `as_of`（与 `_fundflow_factor` 提供「当时环境」定位一致）。

### 数据库

三张报告表（`ai_reports` / `ai_portfolio_reports` / `ai_daily_reports`）都是自由 `report_json`，**无需迁移**。

## 前端统一组件

在 [`static/js/common.js`](../static/js/common.js) 新增 `renderScoreCard(card, opts)`，三页复用，替换/包住现有 `aiScoreHeader` + `renderAiReportBlock`：

- 顶部：大分数 + `grade` 徽章 + `action` 药丸 + 风险条（0-100，颜色随 `risk_level`）+ `confidence` 小字 + `stale` chip
- 中部：维度横条列表（分数着色，点击展开该维 `analysis`）
- 底部：建议 / 风险 / 理由 + 「查看详细报告」（沿用 `aiHtmlButton` / `wireAiHtmlButton`）

配色统一走 `static/css/style.css` 已有的 `--grade-*` 令牌；按钮文案统一「AI 打分」/「重新打分」；弹窗与强度选择沿用 `openPromptEditor`。

落点：

- [`static/portfolio.html`](../static/portfolio.html) `renderAiScore` 改调 `renderScoreCard`
- [`static/stock.html`](../static/stock.html) 诊股面板改调 `renderScoreCard`（维度块由组件统一渲染，`AI_DIM_CN` 并入公共维度名表）
- [`static/trade.html`](../static/trade.html) 当日汇总用 `renderScoreCard`，逐笔沿用现有列表 + 每笔小徽章

## 可下钻（体验加分）

- 组合某维点开 → 列出该维拖累最大的持仓（按权重 × 分差排序），数据来自各股同名维度分。
- 个股页显示「该股在组合中的权重，以及对组合总分的边际影响」。
- 交易复盘每笔挂当时那只股的评分卡快照（扩展 `_stock_factors` 的 as-of 因子为精简 card）。

下钻是纯前端聚合 + 已落库数据读取，不额外调用 AI。

## 兼容与迁移

- 全部为新增字段，老报告渐进降级：无 `dimensions` 不渲染该块；无 `action` 不显示药丸；个股老报告无 `score` 时头部显示「—」并保留 `rating`/`risk_score` 原样。
- 后端读取层做一次 `_upgrade_legacy_card(report)`：把老字段映射到新键（个股 `rating`→`grade`、`risk_score`→`risk`），只在内存中补，不回写历史数据。
- `profile_hash` 组成规则不变。

## 分步实施

1. **公共定义**：`GRADE_NAMES` / `ACTION_NAMES` / `RISK_LEVELS` / `SHARED_DIMENSIONS` / `_SCORE_RULES` 落地并被两个 service 复用。
2. **个股诊股**：schema 加 `score`/`action`、`rating`→`grade`、维度扩到 10 维、context 加 `as_of`/`bars`、快照哈希与 `stale`。
3. **组合打分**：context 加三块喂数、schema 加 `dimensions`/`action`/`risk`/`confidence`、规整函数同步。
4. **交易复盘**：schema 加交易 4 维与 `action`/`risk`，`_stock_factors` 挂 bars 与 `as_of`。
5. **前端组件**：`renderScoreCard` + 三页接入 + 老报告降级。
6. **下钻**：组合维度 → 持仓明细；个股 → 组合贡献。
7. **测试与全量回归**。

## 测试计划

- **枚举与兜底**：`grade` 非法兜底 `C`（沿用 `_check_rating`）；`action` 非法兜底 `hold`（交易侧 `cautious`）；`risk` 走 `_clamp_score`。
- **自洽告警**：`score` 85 + `grade` D → 记 warning 但数据不改。
- **老报告降级**：构造无 `dimensions` / 无 `action` / 个股无 `score` 的历史 `report_json`，GET 与渲染均不报错。
- **三处一致**：同一份 mock 输出经三条规整函数后，顶层四要素字段名与取值域一致。
- **打分喂数**：未跑专项面板时 `build_portfolio_context` 仍含 `technical` / `news_meta`；有专项报告时含摘要。
- **新鲜度**：个股快照哈希变化 → `stale=true`；组合 `profile_hash` 行为不变（回归现有用例）。
- **下钻**：组合维度拖累排序按权重 × 分差正确。
- **诊股 10 维**：`DIMENSIONS` 长度与键名；中文键别名映射仍生效。

回归命令：

```bash
.venv/Scripts/python.exe -m pytest tests/ -q -p no:cacheprovider --basetemp=.tmp_test
```

## 明确不做

- 不引入公式化打分（分数仍由 AI 给，后端只规整与校验）。
- 不做分项加权算总分（总分仍是 AI 的整体判断，分项只作解释）。
- 不改 `profile_hash` 组成，不回写历史报告。
- 不为统一化改动持仓、估值、资金流等业务口径。

## 验收清单

- 三个页面的评分卡外观与字段一致，A/B/C/D 在任何页面都只表示质量。
- 个股有总分；组合与交易有风险分；三处都有 `confidence`。
- 组合能看到资金面 / 消息面 / 技术面等分项评级，并可下钻到具体持仓。
- 「好公司但现在贵」能被正确表达（高 `grade` + 观望 `action`）。
- 未跑专项面板时，组合打分依然能纳入消息面与技术面；已有报告时优先采信。
- 老报告打开不报错、不出现空白块。
- 全量 pytest 绿。

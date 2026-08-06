# 个人自建 ETF 持仓分析

把自己的一篮子持仓（A 股 + 港股 + 场内 ETF）当成一个「自建 ETF」来整体分析与拆分分析：录入持仓、记录买卖、自动移动加权成本、每日操作合理性评分（冻结快照）、穿透式组合估值与分位、港股人民币折算、分红除权自动处理与累计分红。

> 数据来自 akshare（新浪行情/财务、雪球总股本、百度估值分位、东财分红、中行汇率）。项目详情见 [CLAUDE.md](CLAUDE.md)（含架构与核心口径）。

---

## 目录

- [功能清单](#功能清单)
- [技术栈](#技术栈)
- [快速开始](#快速开始)
- [使用指南](#使用指南)
- [核心逻辑](#核心逻辑)
- [API 一览](#api-一览)
- [数据库表](#数据库表)
- [目录结构](#目录结构)
- [测试](#测试)
- [已知限制](#已知限制)

---

## 功能清单

**持仓与交易**
- 批量初始化持仓，移动加权成本（费用计入成本）；空仓时可导入 `汇总持仓.xlsx`
- 交易增删改查；重放法保证持仓一致；卖出超量自动拒绝
- 每次交易自动生成**冻结评分快照** + 当日综合评分
- **持仓调整**（💰）：调整成本（补记/摊薄）与/或股数（拆股/送股）
- **分红除权自动处理**：启动/全量刷新时扫描，今天除权自动摊薄成本（幂等），并累计个股与组合分红

**行情与数据（缓存优先）**
- 动态刷新：实时价格/估值/股息率/市值（不含汇率）
- 全量刷新：日K/财务/估值分位/组合序列；**汇率启动时强制拉一次，仅全量刷新重拉**（新浪实时 HKD/CNY）
- **前瞻指标**（修正口径）：预测年末净资产 = 上年末净资产 + 预测净利 − 预测分红，含 high/medium/low/invalid 置信度
- **亏损股全程按负值参与**：负 PE/ROE/利润/预测利润，仅分母为零才"不适用"
- 收盘定格、增量拉取、读兜底 stale、数据源降级（东财→新浪/巨潮）

**个股分析**
- PE/PB 实时值 + 1y/3y/5y 分位（分段排序：正→0→负）、前瞻 PE/PB 及分位
- PE/PB 历史折线（标注实时/前瞻值）、资金流、财务、预期增速/支付率持久化
- **港股**：现价/市值卡显示港币 + `≈¥` 人民币换算；买卖弹窗实时换算成交金额
- **缓存缺失弹窗**：未下载股票返回 409 CACHE_MISS，前端「下载并打开 / 仅打开已有数据 / 取消」

**组合分析（穿透式，人民币口径）**
- 综合 PE/PB/ROE（归属利润/净资产汇总，`ROE% = PB/PE×100`）、净利/营收增长、股息率（`Σ分红÷总市值`）、波动率（人民币复权，纳入股票/ETF/港股）
- 首页 PE/PB 分位 = 打包组合历史序列分位；分段排序；覆盖率门槛
- 组合动态打分（穿透式指标加权，覆盖不足的指标不参与）
- 权重分布、累计分红、港股 `missing_fx` 剔除

**评分模型（冻结快照）**
- 买入/卖出六因子，评分 = `50 + (已知因子得分−50)×覆盖率`；覆盖率 <60% 不评级
- 快照不可变：刷新行情/财务/改权重不改变历史快照；日评分按人民币金额加权

**前端**：持仓 / 组合分析 / 个股 / 交易评分 四页，原生 JS + ECharts，无构建步骤
- **港股券商口径**：现价/成本显示港币（交易单价），市值/盈亏/成交金额显示人民币（看资产/收益）
- **交易评分**：左侧可滚动日期目录 + 右侧选中日详情（自适应 70vh、各自内部滚动），综合因子图 + 每笔明细
- **全局加载**：耗时操作（页面加载/刷新/导入/下载）显示全屏 spinner 屏障，快速操作不打扰

---

## 技术栈

| 层 | 选型 |
|---|---|
| 语言/运行 | Python 3.12 + venv |
| Web 框架 | FastAPI + Uvicorn |
| 数据库 | SQLite（标准库 `sqlite3`，版本化迁移） |
| 数据源 | akshare（新浪/雪球/百度/东财/中行） |
| 前端 | 原生 HTML/JS + 本地内置 ECharts 5.6.0 |
| 测试 | pytest + httpx TestClient（全程离线） |

---

## 快速开始

### Windows 一键安装（推荐）

从 [GitHub Releases](https://github.com/niuyueyang1996/stock/releases) 下载
`StockAnalyzer-Setup-<version>-x64.exe`，双击后按中文向导安装。安装包支持 Windows
10/11 x64，已经内置 Python 和全部依赖，不需要用户安装开发工具或打开命令行。

安装完成后会在桌面和开始菜单创建“股票持仓分析”快捷方式。双击后程序进入系统托盘，
服务就绪时自动用默认浏览器打开首页。托盘菜单可重新打开首页、重启服务、查看日志或退出。

> 首版安装包未做代码签名，SmartScreen 可能显示“Windows 已保护你的电脑”。请先核对
> Release 同时提供的 SHA-256，然后点击“更多信息”→“仍要运行”。

个人数据库和日志保存在 `%LOCALAPPDATA%\StockAnalyzer`。覆盖安装不会删除数据；卸载时
会询问是否删除，默认选择保留。详细构建说明见
[`packaging/windows/README.md`](packaging/windows/README.md)。

### 源码运行（开发者）

```bash
# 1) 创建虚拟环境并安装依赖（首次）
python3.12 -m venv .venv
source .venv/bin/activate   # Windows: .venv\Scripts\activate
pip install -r requirements.txt

# 2) 启动服务（默认 127.0.0.1:8000，自动建表/迁移）
./run.sh                 # 或 start.cmd / uvicorn app.main:app --port 8000
```

**首次使用**：进「交易评分」页录入持仓 → 点「🔄 全量刷新」→ 查看持仓/组合页。
不需要旧数据时删除 `data/etf.db` 重启即可重建。

---

## 使用指南

| 按钮 | 作用 |
|---|---|
| ⚡ 刷新动态数据 | 实时价格 + 当前 PE/PB + 港股汇率 |
| 🔄 全量刷新 | 日K + 财务 + 估值分位 + 汇率 + 组合序列 |

> 任何 GET 都只读缓存、零网络。想更新就点刷新。

- **持仓页 `/`**：指标卡（市值/成本/盈亏/PE/PB 分位/股息率/ROE/增长/波动率/累计分红）、明细表、权重环形图
- **组合分析页 `/static/portfolio.html`**：PE/PB 分位仪表盘、打包序列折线、穿透式盈利与成长、动态打分横柱状图（标签与条形严格对应）、权重/逐股贡献
- **个股页 `/static/stock.html`**：搜索切换、缓存缺失下载弹窗、行情卡、分位条形、历史折线、前瞻指标、预期增速/支付率、资金流
- **交易评分页 `/static/trade.html`**：录入/改/删交易、调整成本/股数（含分红除权按钮）、当日综合评分与历史、交易流水（显示名称）

---

## 核心逻辑

### 数据层：缓存优先 + 手动刷新
- GET 只读缓存，绝不联网/写库；缺失返回 HTTP 409 CACHE_MISS。
- 缓存三类：原始缓存（长期保留）、不可变快照（评分）、派生缓存（组合序列，带 `portfolio_hash`）。
- 清仓只改状态不删原始缓存；再次开仓复用。
- 刷新增量：日K 只拉上次缓存+1 至今；收盘定格二次刷新零请求。

### 评分模型（冻结快照）
- 每笔交易生成不可变快照（因子原值/数据日期/权重/模型版本/覆盖率/状态 frozen|estimated|insufficient）。
- `评分 = 50 + (已知因子得分−50)×覆盖率`；覆盖率 60%~80% 低置信度，<60% 不评级。
- 历史回填只用交易日及以前数据；改权重生成新模型版本只作用于之后交易；`/scoring/rebuild` 只重建日聚合。
- 当日综合分 = 快照按人民币金额加权；可评分金额 <80% 当日不评级。

### 港股折算
- 五位代码 → HKD；港股不再复用 ETF 分类。
- 汇率中行牌价缓存；交易存 `fx_rate/amount_cny`；持仓双币种成本。
- 缺汇率绝不 1:1（`missing_fx` 剔除）；波动率用人民币复权价格。

### 组合穿透式指标
- 归属利润 = 持股数÷总股本×TTM利润；综合 PE = Σ市值/Σ归属利润；PB/ROE 同口径且 `ROE% = PB/PE×100`。
- 亏损股负值全程参与；每指标 `coverage_weight`（<70% 覆盖不足）。
- 首页分位 = 打包组合历史序列分位（分段排序、当前日期前样本、覆盖≥90%、≥60天）。
- `portfolio_hash` 只基于持仓（勿纳入估值序列 updated_at，否则缓存频繁失效）。

### 前瞻指标
- 预测年末净资产 = 上年末净资产 + 预测净利 − 预测分红；前瞻 PB = 市值/预测年末净资产。
- 降级链（增速/支付率/上年末净资产）+ 置信度（high/medium/low/invalid）。
- 组合前瞻 PE/PB 用穿透汇总（Σ市值/Σ归属预测净利/净资产）。

### 成本调整与分红除权
- `POST /holdings/{code}/cost-adjust`：成本 amount（正加/负减）+ 股数 delta_qty（拆股送股）。
- 自动除权：启动/全量刷新扫描，今天除权自动摊薄成本（幂等，`dividend_adjustments` 表）。
- 手动除权按钮查最近除权填入；`is_dividend=1` 计入累计分红（个股 + 组合）。

### 今日盈亏（券商口径）
- 前日持仓 `(现价−昨收)`；当日买入 `(现价−买入均价)`；当日卖出 FIFO；减当日费用。
- 按现价买入当日盈亏 = 0。

---

## API 一览

统一 `{ok, data}`；错误 `detail`。前缀 `/api`。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/health` | 本地健康检查（不访问数据源，含应用标识/版本） |
| GET | `/status` | 服务状态 + 数据源探活 |
| POST | `/refresh` | 动态刷新（价格/估值/汇率） |
| POST | `/refresh/full` | 全量刷新（日K/财务/估值/汇率/组合序列/自动除权） |
| GET | `/holdings` | 持仓（含 currency/avg_cost_cny/missing_fx/total_dividend） |
| POST | `/holdings` | 批量初始化 |
| POST | `/holdings/import-excel` | 一键导入持仓 Excel |
| POST | `/holdings/{code}/cost-adjust` | 调整成本/股数（is_dividend 标记除权） |
| POST | `/trades` | 录入交易（+冻结快照+当日评分） |
| GET | `/trades` | 交易流水（含名称） |
| PUT | `/trades/{id}` | 修改交易 |
| DELETE | `/trades/{id}` | 撤销交易 |
| GET | `/stocks/search` | 代码/名称搜索（A 股+港股） |
| GET | `/stocks/{code}/cache-status` | 缓存状态（纯读） |
| GET | `/stocks/{code}/dividend` | 最近除权信息（手动除权按钮） |
| GET | `/stocks/{code}` | 单股全套；缓存缺失 409 CACHE_MISS（`?partial=1` 打开已有） |
| POST | `/stocks/{code}/refresh[/full]` | 单股动态/全量刷新 |
| GET/PUT | `/stocks/{code}/expected-growth` | 预期净利增速 |
| GET/PUT | `/stocks/{code}/expected-revenue-growth` | 预期营收增速 |
| GET/PUT | `/stocks/{code}/expected-payout` | 预期支付率 |
| PUT | `/stocks/{code}/tag` | 个股标签 |
| GET | `/portfolio` | 组合整体+逐股（穿透式指标/coverage/missing_fx/累计分红） |
| GET | `/portfolio/weights` | 权重分布 |
| GET | `/scoring/rules` | 评分权重 + 模型版本 |
| PUT | `/scoring/rules` | 更新权重（生成新版本） |
| GET | `/scoring/daily?date=` | 某日综合评分 |
| GET | `/scoring/history` | 评分历史 |
| POST | `/scoring/rebuild` | 只重建日聚合 |
| POST | `/data/reset` | 一键清空（含汇率/快照/预期表/除权记录） |

---

## 数据库表

`data/etf.db`（自动建表 + 版本化迁移，迁移前备份）。

| 表 | 内容 |
|---|---|
| `stocks` | 代码/名称/市场/标签/币种 currency |
| `holdings` | 持仓（原币+人民币成本、累计分红口径） |
| `trades` | 交易（side buy/sell/adjust，fx_rate/amount_cny/is_dividend） |
| `daily_price_cache` | 日K（收盘定格 is_closed、pct_change） |
| `daily_valuation_cache` | 当前 PE/PB |
| `valuation_quantile_cache` | 1y/3y/5y 分位 |
| `daily_fundflow_cache` / `fundflow_15m_cache` | 资金流 |
| `financial_cache` | 财务（多期利润/营收序列、上年末净资产） |
| `valuation_history_cache` | 百度 PE/PB 历史序列（updated_at 作版本） |
| `portfolio_valuation_cache` | 组合打包序列（coverage、portfolio_hash） |
| `fx_rate_cache` | 汇率 |
| `trade_score_snapshots` | 评分冻结快照 |
| `daily_scores` | 每日综合评分 |
| `dividend_adjustments` | 已自动除权记录（幂等） |
| `stock_expected_*` | 用户预期增速/支付率 |
| `config` | 权重/模型版本/schema 版本 |

---

## 目录结构

```
stock/
├── run.sh / start.cmd        # 源码开发启动脚本
├── packaging/windows/        # PyInstaller + Inno Setup + 一键构建脚本
├── README.md / CLAUDE.md     # 文档 + 项目上下文
├── app/
│   ├── main.py               # 入口：init_db、自动除权线程、路由
│   ├── models/db.py          # SQLite + 版本化迁移
│   ├── data/                 # base/cache/fx/source_*
│   ├── market/calendar.py    # 交易日历 + 收盘判断
│   ├── services/             # holdings/fx/dividend/quote/refresh
│   ├── analysis/             # valuation/scoring/portfolio/volatility
│   ├── api/                  # system/holdings/trades/stocks/portfolio/scoring
│   └── scripts/              # 数据源探测
├── static/                   # 前端（index/portfolio/stock/trade + js/）
├── data/etf.db               # SQLite 数据库
└── tests/                    # pytest 145 用例（离线）
```

---

## 测试

```bash
python -m pytest tests/ -q -p no:cacheprovider --basetemp=.tmp_test
```

覆盖：移动加权成本、评分冻结快照（不可变/estimated/覆盖率公式/日聚合门槛）、港股折算（币种/汇率/双币种成本/missing_fx）、组合穿透式（ROE=PB/PE、亏损负贡献、分段分位、覆盖门槛、今日盈亏、股息率稀释）、前瞻 PB（降级链/置信度/负净资产）、成本调整与分红除权（幂等/累计分红）、缓存生命周期（清仓保留/GET 零写入/409 CACHE_MISS）、数据库迁移。

---

## 已知限制

- 数据源依赖新浪/雪球/百度/东财/中行，某源失败对应数据跳过。
- 15 分钟资金流无可用源，界面占位。
- Windows 安装包未签名，首次运行可能出现 SmartScreen 提示。
- 汇率仅实现 HKD/CNY。
- 送转股不自动处理持仓数量（需手动调整股数）。

# 股票持仓分析系统

把一篮子持仓（A 股 + 港股 + 场内 ETF）当成组合做整体与拆分分析：交易流水与成本管理、穿透式组合估值与分位、资金流、指数对照、港股人民币折算、分红除权，以及组合 / 每日 / 诊股 / 资金流 / 消息面 / 技术面的 AI 分析。

当前版本：**v0.2.0**。双后端架构：**Go 后端为生产实现**（零 CGO，可交叉编译 Android / Windows），**Python 后端为兼容性参照物**（FastAPI），两者共享同一 SQLite 数据库与同一套 `static/` 前端（原生 JS + ECharts，无构建），JSON 响应逐字段对齐。

---

## 目录

- [功能](#功能)
- [技术栈](#技术栈)
- [架构与分层](#架构与分层)
- [代码结构](#代码结构)
- [快速开始](#快速开始)
- [核心口径](#核心口径)
- [API 概要](#api-概要)
- [测试](#测试)
- [已知限制](#已知限制)

---

## 功能

**持仓与交易**
- 交易增删改查；重放法维护持仓（移动加权成本）；卖出超量拒绝
- Excel 空仓一键导入；成本调整（补记 / 摊薄）与股数调整（拆股 / 送股）
- 分红除权：启动 / 全量刷新扫描当日除权并幂等摊薄成本；累计分红入账

**行情与缓存（GET 零网络）**
- 右上角「动态 / 全量」是唯一联网入口；长任务进**异步任务队列**（可排队、可取消、顶栏进度，WebSocket 实时推送）
- 动态：实时价 / 估值 / 股息；全量：日 K / 周月K / 财务 / 分位 / 汇率 / 组合序列 / 除权
- 缓存缺失个股返回 `409 CACHE_MISS`，前端询问是否下载

**个股 / ETF / 指数**
- PE·PB 实时、前瞻、历史与分位；财务；预期增速 / 支付率；K 线（日 / 周 / 月）
- 资金流（分时 + 日级）与独立股价 / 净值图；AI 诊股、消息面 / 技术面 / 资金流分析
- 港股：单价港币、金额人民币（`≈¥`）；缺汇率不按 1:1（`missing_fx`）
- 指数页：多选汇总、量价资金面、成交额与较昨同时段放量 / 缩量

**组合分析（穿透、人民币）**
- 综合 PE / PB / ROE（`ROE% = PB/PE×100`）、成长、股息率、波动率、分位仪表与历史序列
- 标签筛选子组合；AI 组合打分（`grade` 质量评级 + `action` 操作建议由 AI 直接给出）
- 长面板可折叠，标题旁色标摘要

**AI**
- 公式评分已移除；标签偏好：短句 → AI 补全指引 → 确认后生效
- 分析强度：快速 / 普通 / 深入（深入出 HTML 详细报告）；思考级别 / 输出预算 / 超时可配

**前端页面**：持仓 · 组合 · 个股 · 交易评分 · 指数（原生 JS + 本地 ECharts，无构建）

---

## 技术栈

| 层 | 选型 |
|---|---|
| 生产后端 | **Go 1.21+**（gin + gorm + `glebarez/sqlite`，零 CGO，可交叉编译 Android/Windows） |
| 参照后端 | Python 3.12 + FastAPI + Uvicorn（同一 SQLite，兼容性基准） |
| 数据库 | SQLite（版本化迁移，`data/etf.db`） |
| 数据源 | 腾讯 / 新浪 / 东财 / 百度 / 乐咕 / 中行（HTTP 直连，无 akshare 依赖） |
| 前端 | 原生 JS + ECharts（`static/`，无构建） |
| WebSocket | gorilla/websocket（任务进度 + 数据更新推送） |
| 打包 | Windows 托盘（PyInstaller/Inno Setup，仓库 `packaging/windows`）；Android APK 壳 |

---

## 架构与分层

### 数据接入分层（最高优先级）

数据相关代码统一三层结构（所有数据代码必须遵守）：

```
app/data/                 Python 侧数据分层（Go 侧对应 backend/internal/raw + service/*）
  base.py          对外门面：DataSource 抽象 + SourceManager 降级链 + Financials 标准模型
  raw/             接口层：真实访问底层接口，只做请求与最小解析
  providers.py     接口转换层：平台选择 + 降级链（哪平台先试、失败换下一个）
  normalizers.py   数据转换层：raw → 标准模型，统一字段名、单位、货币（人民币）、报告期口径
```

- **对外屏蔽**：唯一入口 `build_manager()` 返回的 SourceManager，外部只调 `manager.financials(code)` / `quote()`，不直接依赖具体源或层
- **货币统一在数据转换层**：各平台原始货币一律折算人民币（Financials 全是人民币口径）；任何「市值÷净利」「每股股息÷股价」等比率必须先同货币
- **口径下沉**：TTM 计算、同比、货币折算、单位统一只允许在数据转换层做

### Go 后端分层（route→service→db→raw）

```
route（internal/route）    第1层：gin 路由 + handler（参数校验 → 调 service → 组响应）
  ↓ 只依赖 service 接口（零 db/dao 触点，Services 结构不暴露 DB/DAO 句柄）
service（internal/service）第2层：业务逻辑（对齐 app/services + app/analysis 语义）
  ↓
db/dao（internal/db/dao）  第3层：数据访问（gorm 查询，对齐 app/data/cache.py）
raw（internal/raw）        第4层：真实接口访问（腾讯/新浪/东财/百度/乐咕 HTTP）
```

**分层红线**：route 层禁止直接持有 `*gorm.DB` / DAO 句柄；所有数据访问必须经 service 方法。service 依赖通过 main 装配注入（构造注入 + 函数回调）。

---

## 代码结构

```
├── backend/                    # Go 生产后端（零 CGO，主线）
│   ├── cmd/
│   │   ├── server/             # 服务入口：配置→连库→迁移→装配→路由→后台任务
│   │   └── apidiff/            # 双服务逐接口对比工具（Python vs Go 深度 diff）
│   ├── internal/
│   │   ├── config/             # 运行时配置（STOCK_APP_HOME / 端口 / 目录）
│   │   ├── db/                 # 建表 + 版本化迁移（schema.go / migrate.go / models.go）
│   │   │   └── dao/            # 数据访问层：缓存/持仓/交易/AI 报告/汇率/周期K
│   │   ├── raw/                # 接口层：腾讯/新浪/东财/百度/乐咕 HTTP 客户端
│   │   ├── route/              # 路由层：85 端点注册 + handler（零 db/dao 触点）
│   │   └── service/            # 服务层（业务逻辑，对齐 Python 语义）
│   │       ├── ai/             # AI：模型配置/诊股/消息面/技术面/资金流/组合与每日打分
│   │       ├── calendar/       # 交易日历
│   │       ├── datamanage/     # 一键清空数据 / 批量初始化持仓
│   │       ├── detail/         # 个股/指数详情统一组装（409/as_of 回看/分位重算）
│   │       ├── dividend/       # 分红除权自动调整（幂等）
│   │       ├── finance/        # 财务数据子包（多态降级链：A股/港股）
│   │       ├── fx/             # 汇率服务（折算/刷新/回填）
│   │       ├── holdings/       # 持仓/交易（重放法）、成本调整、标签
│   │       ├── indices/        # 指数注册表 + 刷新 + 估值序列
│   │       ├── jobs/           # 异步任务系统（双车道 worker + batch + 预热 + ws 广播）
│   │       ├── market/         # 行情/资金流子包（多态降级链 + 聚合）
│   │       ├── model/          # 标准模型（Bar/Quote/Financials）
│   │       ├── portfolio/      # 组合穿透式指标 + 打包序列 + 标签分组 + 资金流穿透
│   │       ├── quote/          # 纯缓存行情读取（零网络）+ K线 + 全市场搜索
│   │       ├── refresh/        # 刷新编排（动态/全量/单股/周月K/港股分时）
│   │       ├── settings/       # 全局配置（界面模式/刷新间隔/静态TTL）
│   │       ├── stockmeta/      # 个股预期数据（增速/营收增速/支付率）
│   │       ├── valuation/      # 实时估值 + 前瞻 PB/PE + 分位 + 序列
│   │       ├── volatility/     # 人民币波动率
│   │       └── ws/             # WebSocket hub（任务推送 + data_updated 广播）
├── app/                        # Python 参照后端（FastAPI，兼容性基准）
│   ├── api/                    # 路由（system/holdings/trades/stocks/portfolio/index/ai）
│   ├── services/               # 业务逻辑（refresh/holdings/ai_scoring/job_runners…）
│   ├── data/                   # 数据三层（base/raw/providers/normalizers + cache/fx）
│   ├── analysis/               # 组合/估值/波动率分析
│   ├── instruments/            # 判型与各品种数据接入（ashares/hk/etf/index）
│   ├── models/                 # SQLite 建表 + 版本化迁移
│   └── main.py                 # FastAPI 入口
├── static/                     # 前端五页（持仓/组合/个股/交易/指数）+ js/（api.js common.js charts.js），两端共用
├── tests/                      # Python 侧 pytest（367 用例，全程离线）
├── packaging/windows/          # Windows 托盘程序 + PyInstaller/Inno Setup 构建链
├── data/                       # 运行时数据（etf.db + 市场列表缓存，个人数据不入库）
└── docs/                       # 设计文档
```

### Go 服务装配链（cmd/server/main.go）

```
config.Load → db.Open（迁移）→ DAO 层（Config/Holdings/Cache/Fx/AI…）
  → raw 客户端（腾讯/新浪/东财/百度/乐咕/巨潮）
  → service 装配（fx → holdings → finance/market/valuation 子包 → jobs → refresh
    → indices → portfolio → ai → detail/stockmeta/datamanage → ws hub）
  → route.Setup（85 端点）→ 静态资源 /static → /ws 挂载
  → 后台任务（预热汇率/除权 → 盘中动态刷新循环 → 每日收盘全量同步）
```

---

## 快速开始

### Go 后端（生产）

```bash
cd backend
STOCK_APP_HOME=../data go run ./cmd/server          # 默认端口 8000，监听 127.0.0.1
# 或指定端口：STOCK_PORT=8081
```

启动后访问 http://127.0.0.1:8000/（自动跳转 `/static/index.html`）。

### Python 参照后端（兼容性对比用）

```bash
.venv/bin/python -m uvicorn app.main:app --host 127.0.0.1 --port 8082
```

### 双服务对比验证

```bash
cd backend && go run ./cmd/apidiff http://127.0.0.1:8082 http://127.0.0.1:8081
# 或用 Python 深度 diff 脚本（忽略运行时字段，数值宽容）
python3 /tmp/aicmp.py /api/holdings /api/portfolio ...
```

### 测试

```bash
# Go（backend 目录）
GOCACHE=/tmp/gocache go test ./...          # 15 个包全部离线
# Python（仓库根；Windows 需 --basetemp）
.venv/bin/python -m pytest tests/ -q -p no:cacheprovider --basetemp=.tmp_test
```

### 零 CGO 交叉编译（Android / Windows）

```bash
cd backend
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build ./...   # Android
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...   # Windows
```

---

## 核心口径

完整口径与踩坑记录见 [CLAUDE.md](CLAUDE.md)。要点：

- **缓存优先 + 手动刷新**：GET 只读缓存，零网络、零数据库写入；唯一数据入口是右上角刷新按钮
- **今日盈亏（券商口径）**：前日持仓用 `(现价−昨收)`；当日买入用 `(现价−买入均价)`；当日卖出 FIFO
- **组合穿透式指标**：全部人民币口径；`归属利润 = 持股数÷总股本×公司TTM利润`；亏损股负值全程参与；每指标带 `coverage_weight`，<70% 前端提示"覆盖不足"
- **前瞻 PB/PE**：预测年末净资产 = 上年年末净资产 + 预测净利 − 预测分红；降级链与置信度分级
- **港股**：五位代码 → HKD；单价港币、金额人民币；缺汇率返回 `missing_fx` 绝不按 1:1
- **资金流**：A股/ETF 腾讯分笔五档；港股腾讯分时分钟量价按价向派生（tick rule），近 5 日窗口；指数腾讯分时量价
- **AI ScoreCard**：`grade` 质量评级（A优秀/B良好/C一般/D较差）+ `action` 操作建议解耦；五维共享 + 专项维度
- **组合分位**：来自打包组合历史序列（`portfolio_valuation_cache`，带 `portfolio_hash` 失效重建），非个股加权

---

## API 概要

85 个端点（Go 与 Python 完全对齐，JSON 逐字段一致）：

| 分组 | 端点 |
|---|---|
| 系统 | `/api/health`、`/api/status`、`/api/status/jobs`、`/api/status/prewarm`、`/api/refresh[/full]`、`/api/data/reset` |
| 持仓/交易 | `/api/holdings`、`/api/holdings/{code}/cost-adjust`、`/api/holdings/import-excel`、`/api/trades[/{id}]` |
| 个股 | `/api/stocks/{code}`（详情，409 CACHE_MISS）、`/kline`、`/cache-status`、`/tag`、`/search`、`/expected-*`、`/dividend`、`/refresh[/full]`、`/ai-report` |
| 组合 | `/api/portfolio[?tags=&lite=&code=]`、`/api/portfolio/weights`、`/api/portfolio/fundflow` |
| 指数 | `/api/indices`、`/api/indices/{code}`、`/api/indices/series`、`/api/indices/fundflow`、`/api/indices/etf-map[/auto]`、`/api/indices/refresh-all` |
| 设置 | `/api/settings/refresh`（mode/static_ttl/dynamic_interval） |
| AI | `/api/ai/models[/available|activate]`、`/reasoning`、`/runtime`、`/prompts`、`/news-*`、`/tech-*`、`/fundflow-*`、`/ai-scoring/*` |
| 实时推送 | `GET /ws`（任务进度 `jobs` + 数据更新 `data_updated`） |

错误约定：非 2xx 一律 `{"detail": "中文错误消息"}`（对齐 FastAPI `HTTPException`）；缓存缺失 `409 CACHE_MISS`。

---

## 测试

- Go：15 个包，重点覆盖 detail（409/as_of/分位重算）、holdings（重放/交易/标签）、portfolio（穿透/lite/标签分组）、refresh（items 过滤/周月K/batch 扇出）、ai（ScoreCard/每日自动打分/消息面技术面）、jobs（任务/批/ws 广播）、settings、datamanage、ws hub
- Python：367 用例（ai_scoring/ai/ai_news_tech/portfolio/fx/dividend/migration/api/instruments/indices/fundflow/Windows 打包）
- 兼容性：`cmd/apidiff` + `/tmp/aicmp.py` 双服务逐字段对比（忽略运行时字段与 1e-12 级浮点尾数）

---

## 已知限制

- 组合分位 / 打包序列依赖全量刷新触发重建（持仓画像 hash 变化后旧序列不参与）
- `dv/dv_static` 存在 1e-12 级浮点尾数差异（float64 累加顺序，前端 round 后渲染一致）
- Python 参照服务在部分环境下 `/ws` 不可用（缺 uvicorn standard 扩展）；Go 始终可用
- 指数分时资金面（`index_intraday_cache`）依赖盘中刷新同步；非交易日均为空（两端一致）
- 市场列表缓存（A股/ETF/港股全市场搜索）由 Python 启动预热或既有文件提供；Go 只读不主动下载

# 股票持仓分析系统

把一篮子持仓（A 股 + 港股 + 场内 ETF）当成组合做整体与拆分分析：交易流水与成本管理、穿透式组合估值与分位、资金流、指数对照、港股人民币折算、分红除权，以及组合 / 每日 / 诊股 / 资金流 / 消息面 / 技术面的 AI 分析。

当前版本：**v0.2.0**。**Go 单后端**（gin + gorm + `glebarez/sqlite`，零 CGO，可交叉编译 Android），前端为原生 JS + ECharts（`static/`，无构建），Android APK 由 GitHub Actions 自动打包。

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
- [打包（APK）](#打包apk)
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
- 消息面 / 技术面 / 资金流专项分析；AI 按钮收敛：个股「🤖 AI诊股」、组合「🤖 AI诊组合」

**前端页面**：持仓 · 组合 · 个股 · 交易评分 · 指数（原生 JS + 本地 ECharts，无构建）

---

## 技术栈

| 层 | 选型 |
|---|---|
| 后端 | **Go 1.21+**（gin + gorm + `glebarez/sqlite`，零 CGO，可交叉编译 Android） |
| 数据库 | SQLite（版本化迁移，`data/etf.db`） |
| 数据源 | 腾讯 / 新浪 / 东财 / 百度 / 乐咕 / 中行（HTTP 直连） |
| 前端 | 原生 JS + ECharts（`static/`，无构建） |
| WebSocket | gorilla/websocket（任务进度 + 数据更新推送） |
| 打包 | GitHub Actions 打 Android APK（WebView 壳 + 内嵌 Go 二进制） |

---

## 架构与分层

### Go 后端分层（route→service→db→raw，红线强制）

```
route（internal/route）    第1层：gin 路由 + handler（参数校验 → 调 service → 组响应）
  ↓ 只依赖 service 接口（Services 结构不暴露 DB/DAO/缓存句柄，零 db 触点）
service（internal/service）第2层：业务逻辑（多态子包：行情/财务/资金流/组合/AI…）
  ↓
db/dao（internal/db/dao）  第3层：数据访问（gorm 查询）
raw（internal/raw）        第4层：真实接口访问（腾讯/新浪/东财/百度/乐咕 HTTP）
```

**分层红线**：route 层禁止直接持有 `*gorm.DB` / DAO 句柄；所有数据访问必须经 service 方法。service 依赖通过 main 装配注入（构造注入 + 函数回调）。

### 数据接入原则

- 数据访问统一走 `raw`（接口请求）→ `db/dao`（缓存落库）→ `service`（口径计算），**口径只允许在 service 层算**，不重复折算
- **货币统一**：各平台原始货币一律折算人民币；任何「市值÷净利」「每股股息÷股价」等比率必须先同货币（港股财务为公司记账本位币、市值恒为港元，混币出错的教训）
- 判定功能货币：东财港股主指标带 EM 计算的 PE_TTM/PB_TTM（币种自洽），作锚点重算比对判定报表货币

---

## 代码结构

```
├── backend/                    # Go 生产后端（零 CGO，唯一后端）
│   ├── cmd/
│   │   ├── server/             # 服务入口：配置→连库→迁移→装配→路由→后台任务（支持 --listen）
│   │   ├── dbcheck/            # 数据库巡检工具
│   │   ├── rawprobe/           # 数据源接口探测工具
│   │   └── serviceprobe/       # 服务层探测工具
│   ├── internal/
│   │   ├── config/             # 运行时配置（STOCK_APP_HOME / STOCK_PORT / 目录）
│   │   ├── db/                 # 建表 + 版本化迁移（schema.go / migrate.go / models.go）
│   │   │   └── dao/            # 数据访问层：缓存/持仓/交易/AI 报告/汇率/周期K
│   │   ├── raw/                # 接口层：腾讯/新浪/东财/百度/乐咕 HTTP 客户端
│   │   ├── route/              # 路由层：85 端点注册 + handler（零 db/dao 触点）
│   │   └── service/            # 服务层（业务逻辑）
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
│   │       ├── refresh/        # 刷新编排（动态/全量/单股/周月K/港股分时/指数）
│   │       ├── settings/       # 全局配置（界面模式/刷新间隔/静态TTL）
│   │       ├── stockmeta/      # 个股预期数据（增速/营收增速/支付率）
│   │       ├── valuation/      # 实时估值 + 前瞻 PB/PE + 分位 + 序列
│   │       ├── volatility/     # 人民币波动率
│   │       └── ws/             # WebSocket hub（任务推送 + data_updated 广播）
├── static/                     # 前端五页（持仓/组合/个股/交易/指数）+ js/（api.js common.js charts.js）
├── packaging/android/          # Android APK 构建链（Kotlin WebView 壳 + Gradle 8.13 + wrapper）
├── data/                       # 运行时数据（etf.db + 市场列表缓存，个人数据不入库）
└── .github/workflows/          # CI：android-apk.yml（Go 编译 + Gradle 打包 + Release）
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

```bash
cd backend
STOCK_APP_HOME=../data go run ./cmd/server          # 默认端口 8000，监听 127.0.0.1
# 或指定端口：STOCK_PORT=8081；或 --listen 127.0.0.1:8081
```

启动后访问 http://127.0.0.1:8000/（自动跳转 `/static/index.html`）。

### 测试

```bash
cd backend
GOCACHE=/tmp/gocache go test ./...                  # 全部离线
```

### 零 CGO 交叉编译

```bash
cd backend
CGO_ENABLED=0 GOOS=android GOARCH=arm64 go build ./...   # Android（APK 构建链见下）
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

85 个端点：

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

错误约定：非 2xx 一律 `{"detail": "中文错误消息"}`；缓存缺失 `409 CACHE_MISS`。

---

## 测试

- Go：15 个包，重点覆盖 detail（409/as_of/分位重算）、holdings（重放/交易/标签）、portfolio（穿透/lite/标签分组）、refresh（items 过滤/周月K/batch 扇出）、ai（ScoreCard/每日自动打分/消息面技术面）、jobs（任务/批/ws 广播）、settings、datamanage、ws hub

---

## 打包（APK）

`.github/workflows/android-apk.yml`（对齐仓库内 Windows 打包的 CI 风格）：

- **触发**：手动 `workflow_dispatch`；push 到 `main` / `refactor/golang-backend`；`v*` 标签
- **流程**：setup-go → JDK 17 → 安装 Android SDK（cmdline-tools + platform 35 + build-tools 34/35）→ 交叉编译 Go（`CGO_ENABLED=0 GOOS=android GOARCH=arm64`）→ `./gradlew assembleDebug` → 上传 artifact
- **打 `vX.Y.Z` 标签**：校验与 `versionName` 一致后自动发 GitHub Release
- 本地一键构建：`packaging/android/build.sh`（交叉编译 + Gradle），产物在 `dist/android/`

APK 结构：`assets/bin/stockanalyzer-server`（Go 后端，启动时解压执行，监听 127.0.0.1:8080）+ `assets/`（前端四页，同步到 `filesDir/static`，`STOCK_PROJECT_ROOT` 指向 `filesDir` 供静态目录解析）。

---

## 已知限制

- 组合分位 / 打包序列依赖全量刷新触发重建（持仓画像 hash 变化后旧序列不参与）
- `dv/dv_static` 存在 1e-12 级浮点尾数差异（float64 累加顺序，前端 round 后渲染一致）
- 指数分时资金面（`index_intraday_cache`）依赖盘中刷新同步；非交易日均为空
- 市场列表缓存（A股/ETF/港股全市场搜索）依赖启动预热下载；断网环境搜索为空
- Android APK 仅 arm64（`abiFilters`）；需要 x86 模拟器支持时需扩展构建矩阵

# 个人自建 ETF 持仓分析

把一篮子持仓（A 股 + 港股 + 场内 ETF）当成「自建 ETF」做整体与拆分分析：录入交易、移动加权成本、穿透式组合估值与分位、资金流、指数对照、港股人民币折算、分红除权，以及组合 / 每日 / 诊股 / 资金流的 AI 分析。

当前版本：**v0.2.0**。口径与架构细节见 [CLAUDE.md](CLAUDE.md)。

---

## 目录

- [功能](#功能)
- [技术栈](#技术栈)
- [快速开始](#快速开始)
- [使用说明](#使用说明)
- [核心口径](#核心口径)
- [API 概要](#api-概要)
- [目录结构](#目录结构)
- [测试](#测试)
- [已知限制](#已知限制)

---

## 功能

**持仓与交易**
- 交易增删改查；重放法维护持仓；卖出超量拒绝
- 空仓可导入 `汇总持仓.xlsx`；成本调整（补记 / 摊薄）与股数调整（拆股 / 送股）
- 分红除权：启动 / 全量刷新扫描当日除权并幂等摊薄成本；累计分红入账

**行情与缓存（GET 零网络）**
- 右上角「动态 / 全量」是唯一联网入口；长任务进**异步任务队列**（可排队、可取消、顶栏进度）
- 动态：实时价 / 估值 / 股息；全量：日 K / 财务 / 分位 / 汇率 / 组合序列 / 除权
- 缓存缺失个股返回 `409 CACHE_MISS`，前端询问是否下载

**个股 / ETF / 指数**
- PE·PB 实时、前瞻、历史与分位；财务；预期增速 / 支付率
- 资金流（分时 + 日级）与独立股价 / 净值图；AI 诊股与资金流分析
- 港股：单价港币、金额人民币（`≈¥`）；缺汇率不按 1:1（`missing_fx`）
- 指数页：多选汇总、量价资金面、成交额与**较昨同时段放量 / 缩量**（有昨分时则实算）

**组合分析（穿透、人民币）**
- 综合 PE / PB / ROE（`ROE% = PB/PE×100`）、成长、股息率、波动率、分位仪表与历史序列
- 标签筛选子组合；AI 组合打分（得分 + A/B/C/D 评级由 AI 直接给出）
- 长面板可折叠，标题旁色标摘要（评级色块、涨跌、分位高低）

**AI**
- 公式评分已移除；标签偏好：短句 → AI 补全指引 → 确认后生效
- 分析强度：快速 / 普通 / 深入（深入可出 HTML 详细报告）；思考级别可配

**前端页面**：持仓 · 组合 · 个股 · 交易评分 · 指数（原生 JS + 本地 ECharts，无构建）

---

## 技术栈

| 层 | 选型 |
|---|---|
| 运行 | Python 3.12 + venv |
| Web | FastAPI + Uvicorn |
| 库 | SQLite（版本化迁移） |
| 数据 | 三层：`raw` → `providers` / `normalizers` → `SourceManager`（akshare / 新浪 / 腾讯 / 百度 / 东财等） |
| 前端 | 原生 HTML/JS + ECharts 5.6（vendor 内置） |
| 任务 | 进程内双通道队列（刷新车道 / AI 车道） |
| 测试 | pytest + httpx（离线，conftest 打桩） |
| 桌面 | Windows 托盘 + PyInstaller / Inno Setup |

---

## 快速开始

### Windows 安装包

从 [Releases](https://github.com/niuyueyang1996/stock/releases) 下载 `StockAnalyzer-Setup-<version>-x64.exe`。双击安装后托盘启动，浏览器打开本机服务（仅 `127.0.0.1`）。

数据与日志在 `%LOCALAPPDATA%\StockAnalyzer`。构建说明见 [`packaging/windows/README.md`](packaging/windows/README.md)。

> 安装包未代码签名时，SmartScreen 可能拦截；核对 Release 的 SHA-256 后「仍要运行」。

### 源码运行

```bash
python -m venv .venv
# Windows
.venv\Scripts\activate
pip install -r requirements.txt

# 启动（默认 127.0.0.1:8000）
.venv\Scripts\python.exe -m uvicorn app.main:app --host 127.0.0.1 --port 8000
# 或 start.cmd
```

首次：交易页录入 →「全量」刷新 → 看持仓 / 组合。清空库可删 `data/etf.db` 后重启。

---

## 使用说明

| 操作 | 作用 |
|---|---|
| ⚡ 动态 | 价格 / 当前估值等（快） |
| 🔄 全量 | 日 K、财务、分位、汇率、序列、除权 |
| 顶栏任务条 | 进度、名称、队列列表；可取消（启动预热除外） |
| 指数「刷新全部」 | 日 K + 点位 + 分时量价（顺带落昨分时供额较昨） |

- **持仓** `/static/index.html`：KPI、列表 / 卡片、标签分组
- **组合** `/static/portfolio.html`：标签侧栏、穿透指标、序列、资金流、AI 打分
- **个股** `/static/stock.html`：诊股 / 估值 / 资金流；指数代码进指数模式
- **交易** `/static/trade.html`：录入、除权调整、按日 AI 评分
- **指数** `/static/indices.html`：多选、资金面量价、权重、列表

---

## 核心口径

**缓存优先**：GET 只读缓存、零网络、零写库；刷新走队列。

**货币**：财务与组合汇总人民币；港股功能货币在数据转换层折算；比率禁止混币。

**组合穿透**：归属利润 = 持股 ÷ 总股本 × 公司 TTM；亏损股负值参与；分位分段排序（正 → 0 → 负）。

**前瞻 PB**：预测年末净资产 = 上年末净资产 + 预测净利 − 预测分红。

**今日盈亏**：券商口径（前日昨收、当日买入均价、卖出 FIFO）。

**汇率**：新浪实时 HKD/CNY；启动强制拉一次，全量刷新重拉。

---

## API 概要

统一 `{ok, data}`，前缀 `/api`。

| 区域 | 代表路径 |
|---|---|
| 系统 | `GET /health` · `GET /status` · `GET /status/jobs` · `DELETE /jobs/{id}` · `POST /refresh[/full]` · `POST /data/reset` |
| 持仓 / 交易 | `/holdings` · `/trades` · `/holdings/{code}/cost-adjust` · 导入 Excel |
| 个股 | `/stocks/search` · `/stocks/{code}` · `/stocks/{code}/refresh[/full]` · 预期增速 / 标签 |
| 组合 | `/portfolio` · `/portfolio/fundflow` · `/portfolio/weights` |
| 指数 | `/indices` · `/indices/fundflow` · `/indices/refresh-all` · ETF 映射 |
| AI | `/ai/*`（模型、诊股、资金流）· `/ai-scoring/*`（偏好、组合、每日） |

---

## 目录结构

```
stock/
├── start.cmd / run.sh
├── README.md / CLAUDE.md
├── app/
│   ├── main.py              # FastAPI 入口
│   ├── jobs.py              # 异步任务队列
│   ├── models/db.py         # SQLite + 迁移
│   ├── data/                # raw / providers / normalizers / cache / fx
│   ├── services/            # holdings / refresh / ai* / indices / job_runners
│   ├── analysis/            # valuation / portfolio / volatility / fundflow
│   └── api/                 # system / holdings / trades / stocks / portfolio / ai*
├── static/                  # 五页前端 + js/ + css/ + vendor/
├── packaging/windows/       # 托盘与安装包构建
├── data/                    # 本地 SQLite（勿提交个人库）
└── tests/                   # pytest（离线）
```

---

## 测试

```bash
.venv\Scripts\python.exe -m pytest tests/ -q -p no:cacheprovider --basetemp=.tmp_test
```

重点：AI 评分与资金流、组合穿透与分位、港股汇率、除权幂等、指数、任务队列、409 CACHE_MISS、Windows 打包路径 / 单实例。

---

## 已知限制

- 外网源不稳定时对应字段会跳过或显示暂无
- 汇率仅 HKD/CNY
- 送转股数量需手动调整
- Windows 安装包默认未签名，可能触发 SmartScreen

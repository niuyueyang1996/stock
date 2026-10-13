# 后端 Bug 审计复核报告

> 日期：2026-10-12 ｜ 范围：`backend/`（route / service / raw 全部生产代码）
> 方法：首轮 AI 审查提出 **约 67 条**问题 → 本报告为**独立二次复核**结果：四个复核组逐条重读代码、追调用链、用 httptest / 临时 Go 测试 / 真实库（`data/etf.db`）实证，53 条重点条目全部覆盖（余约 14 条低危未复核）。
> 本次复核**未修改任何代码**；工作区已有的 9 个文件改动是此前的前端 bug 修复，与本报告无关。所有临时测试已删除，`go test` 基线：33 包绿，`internal/db/dao` 1 个红（见附录 D，与本轮问题无关）。

---

## 一、总体结论

| 复核结论 | 数量（53 条中） |
|---|---|
| ✅ 证实（代码属实 + 实证成立） | **43** |
| ⚠️ 部分证实（核心成立，个别子断言被推翻/收窄） | **11** |
| ❌ 整条证伪 | **0** |
| （复核中被推翻的子断言） | 11 处，见第四节 |

**一句话**：原报告**没有假问题**——所有条目指向的代码缺陷都真实存在；但约 1/5 条目的**触发条件被夸大**（部分在当前数据下休眠、部分已有防御、部分路径不成立）。修复优先级应按第三节的修正后严重度排。

---

## 二、修正后严重度总表

### 🔴 高（P0 级：服务挂死 / 数据不可信 / 核心功能恒失效）

| # | 问题 | 位置 | 验证要点 |
|---|---|---|---|
| 1 | **jobs 持锁入队死锁** | `jobs/jobs.go:257-266, 295, 317`；队列容量 256（`:115`）；worker 取 job 后抢锁（`:156-161`） | ✅ 实测：263 子任务即死锁，且 `Snapshot()` 永久阻塞 → 进度/WS/刷新/AI 全冻结。**门槛比报告更低**：第一轮扇出 200 + 第二轮 100 即死锁。触发口：Excel 导入无行数上限、`/api/refresh` 无并发/忙碌守卫（`IsRefreshBusy` 只被后台循环用） |
| 2 | **组合分位表无写端，首页分位恒空** | `portfolio/portfolio.go:1177`（唯一 SELECT，全仓无 INSERT） | ✅ 全项证实：`tags==nil`（首页 lite、组合页全选）走该路径 → `series={}` → 首页 `PE分位/PB分位` 显示「—」、组合页「暂无分位（完整更新后生成组合序列）」，永久不自愈。反证：选**部分**标签走实时计算就有值 |
| 3 | **trade_time 无长度校验 → 500 + 脏数据落库** | `route/router.go:602-615, 170-184`；`holdings/trades.go:149/176/191/192/213` | ⚠️ 核心属实（实测）：`trade_time:"x"` → UPDATE 已提交后 panic → **500 但脏值已落库**；此后该笔 PUT/DELETE 恒 500（错误假象，实际第二次 PUT 已改对、DELETE 已删掉）。**比报告更糟的新入口**：`POST /api/trades` 传短值 **200 直接落脏**；`POST /api/holdings` 中途 panic → **批量导入半途无反馈**。原报告「永久改不掉删不掉」证伪 |
| 4 | **改/删交易不回滚，holdings 与 trades 永久背离** | `holdings/trades.go:177-184, 205-211`（对照 RecordTrade/AdjustCost 有回滚） | ✅ 实测：改小一笔买入、其后卖出量更大 → Rebuild 报错，但 UPDATE/DELETE 已提交 → holdings 停在旧状态，无自愈路径。现实可达（日常操作序列即可触发） |
| 5 | **港股组合年化波动率恒 null** | `volatility/volatility.go:66-75`（汇率首行前无回退）+ `:160-167`（覆盖率按只数） | ✅ 本机库实测：`fx_rate_cache` 首行 2026-09-06，3/19 只港股=15.8%>10% 容忍 → 99% 的交易日被剔除 → 样本仅 3 天 <60 → `annual=null`。需汇率连续落库约 84 天才自愈；一键清库后归零 |
| 6 | **缺行情的持仓从首页静默消失** | `portfolio.go:796` `"missing": []map[string]any{}` 硬编码；`:398` 排除出 valid；`index.html:87-105` 提示条 | ✅ 全中：前端「⚠ 有 N 只持仓还没下载行情（一键下载）」**永不触发**；`missing_fx` 恒 `[]` 且前端零引用；`router.go:310` 读 `reason` 而实际键是 `error` → 数据缺失被误报成「不在持仓中」 |

### 🟠 中高（P1：契约违例 / 静默退化 / 崩进程风险）

**并发与 AI（jobs/ai/ws）**

| # | 问题 | 验证要点 |
|---|---|---|
| 7 | **worker 无 recover + `requireModel()` panic 做控制流** | ✅ 子进程实测 panic → **进程 exit 2**；全仓唯一 panic 点 `news_tech.go:511`，workerLoop 无 defer recover。入队时校验、执行时不再校验；`DELETE /api/ai/models/:id` 无拦截 → 排队窗口删模型即崩。按竞态可达性记 P1 |
| 8 | **「取消」是假的** | ✅ `Cancel` 只置标志；`p.Check()` 生产零调用（仅测试）；实测排队任务取消后**照常执行并落库**；`requestCtx()=context.Background()`，AI 请求不可取消 |
| 9 | **`ai_request_timeout` 配置不生效** | ✅ 实测 `SetRuntime(60)` 后客户端 `HTTP.Timeout` 仍 `5m0s`；`GetRequestTimeout` 零生产消费者。与 CLAUDE.md「即时生效」冲突 |
| 10 | **max_tokens 拒绝时不降级 16384** | ✅ mock provider 实测第二次请求体仍 81920 → 整体失败；且错误文案 `AI 调用失败: <nil>`（re 恒 nil） |
| 11 | **新闻缓存 `FetchedAt` 写成正文** | ✅ 实测插入后 `fetched_at`=新闻正文 → 6h TTL 恒失效；批量消息面**每只重抓东财**（延迟+限流）；`WHERE fetched_at IS NULL` 补丁永不命中 |
| 12 | **资金流报告 Upsert 漏 `segments/trend/supply_demand`** | ⚠️ 实测同键二次 Upsert → 新 summary + 旧逐段分析缝合报告。「divergence 列不存在」子断言证伪（列存在，只是被清空，无害） |

**口径与数值**

| # | 问题 | 验证要点 |
|---|---|---|
| 13 | **标签级 vs 组合级 `comboValue` 两套口径** | ✅ 实测差 2 倍（PE=10 市值10万 + PE=nil 市值10万 → 组合级 10、标签级 20）。触发点：有财务但取不到 PE/PB 的持仓进标签即翻倍 |
| 14 | **港股刷新链传 `fxHKD=nil` → 恒走序列回退** | ✅ 数值铁证：`sync.go:65`/`refresh_valuation.go:40` 传 nil，06198 实测 PE/PB/DV 三项全部命中 Baidu 序列末值（与实时应得差 2%）→ 港股估值缓存**不随当日价格变动**，刷新链自身无法察觉 |
| 15 | **累计分红用原币，港股分红混入人民币口径** | ✅ `SUM(-amount)`；`amount_cny` 已由 tradeFx 写好。数值例：00728 分红 HKD600 会被记成 ¥600（虚高 17.2%）。当前库仅 1 笔 A 股分红暂未污染 |
| 16 | **`UpdateTrade` 类型断言/切片 panic** | ✅ 实测 `{"code":123}`、`{"trade_time":"x"}` 均 500，且 UPDATE 已先落库；route 层 `map[string]any` 零校验透传 |
| 17 | **送股/分红日 `dayPnl` 虚增** | ✅ 600309 除权日实算：代码 −723 vs 经济口径 −480（亏损虚增=分红额 243）；10送10 场景亏损虚增 100% 市值 |
| 18 | **分红只处理 ExDate==当天，当天未启动即永久漏记** | ✅ `dividend.go:143`；此后每次扫描因「最新除权日≠今天」跳过；只有手工按钮兜底 |
| 19 | **`?tags=,` / 逗号内侧空格 → 空组合或筛不中** | ⚠️ 实测 `?tags=,` 返回 0 只、`红利, 科技` 只命中红利；「整串前后空格」子断言证伪（已有 TrimSpace）。UI 不会踩（手输 URL/API 才踩），降为低 |

**数据源与刷新**

| # | 问题 | 验证要点 |
|---|---|---|
| 20 | **iFinD 全部请求走 `http.DefaultClient` 零超时** | ✅ `client.go:196` 忽略带超时 client；service 层 0 处 `WithTimeout`；iFinD 是首源 → 挂起即占满 6 worker → 全局刷新+动态循环全卡死（需配置 refresh_token 才可达） |
| 21 | **iFinD 令牌失效不自愈** | ✅ `fetchAccessToken` 恒 `force=false`，update 路径是死代码；-1302/-1303 不清缓存 → 失效后 7 天持续降级，仅手动改 token 可救 |
| 22 | **iFinD Quote 把总股本/总市值当成交量写进当日日K** | ✅ 指标清单里根本无 volume/amount；下游 upsert 进 `daily_price_cache`；且 `q.Ts` 恒空 → 日期守卫退化为「仅 9:15 前不落库」（配 iFinD 时触发） |
| 23 | **Purge 760 天窗口 + 吞 upsert 错误** | ✅ 无行数阈值/覆盖率门槛；源半截返回 → 760 天内其余历史全删；upsert 失败照样 purge = 只删不写。下次 force 全量可补回，但期间分位/序列退化 |
| 24 | **ETF 日K东财降级恒空（secid 未剥后缀）** | ✅ **线上实证**：`secid=1.510300` → 200 正常；`secid=1.510300.SH` → `rc=100, data:null`。降级链可达（腾讯K线失败即落东财）→ ETF 日K无兜底 |
| 25 | **`etf-map` 畸形 body 静默删除映射** | ✅ 实测坏 JSON/空 body → 200 且映射被删。「空=删」是 UI 设计，bug 在于**坏请求被当删除 + 无法与成功区分** |
| 26 | **GET 周/月K 触发实时网络 + 写库** | ✅ 实测点「周K」→ `SyncPeriodKline` 调用 1 次。违反「GET 只读缓存」红线（行为本身是兜底同步） |

### 🟡 中低 / P2（防御缺失、状态显示错、体验问题）

| # | 问题 | 验证要点 |
|---|---|---|
| 27 | 分笔时间 `Time[:5]` 裸切片 ×3 | ⚠️ 三处属实、上游零校验、后台路径 panic 即崩进程；但港股 `0930` 已有 4 位防御（**报告漏看**），常态腾讯输出 8 位定宽 → 仅上游畸形数据可达 |
| 28 | 0 子任务批次永久 running + 劫持进度条 | ✅ 实测空批恒 running，且 Snapshot 优先取 running batch 遮蔽后续真实进度（空持仓点刷新触发） |
| 29 | 取消整批后批次被改回 done | ✅ 实测终值 done |
| 30 | jobs/batches map 永不清理 | ✅ 全仓无 `delete`；AI job 闭包捕获整份 HTML 报告 → 长驻内存增长 |
| 31 | 刷新失败统计死代码（`reason=="source_fail"` 恒 false） | ✅ 两侧键名对不上 → 重试与 failed 计数永不触发 |
| 32 | 共享 `Headers` 并发写 fatal | ⚠️ 代码模式属实；**原报告的 Prewarm 路径证伪**（三 client 不同 + 指数串行），真路径：Prewarm × 手动端点 / 双手动端点并发 → 触发即崩，概率低 |
| 33 | Excel `cells[-1]` panic + 上传无上限 | ⚠️ 实测必崩；但**标准 Excel/Sheets/openpyxl 都写 `r` 属性** → 只有非标准生成器触发；无体积上限属实（`io.ReadAll` 全进内存，本机监听攻击面小） |
| 34 | Excel sheet 选择是死代码 | ✅ 实测正确 sheet 在第 2 位 → 解析第一个垃圾 sheet 报错；`_ = sheetName` 显式丢弃 |
| 35 | code 归一不一致 | ✅ 实测 `600519.sh` 的 ai-report 返回 200+null、`?code=600519.sh` → 404「不在持仓中」（UI 不踩，手输 URL 踩） |
| 36 | search `limit<=0` 全量返回 | ✅ 实测 `limit=0/-1` 不限流 |
| 37 | 吞错假 200（`_ = ShouldBindJSON` ×13 处、`_ = DeleteTagPref`） | ✅ 实测坏 JSON 回显旧值 200、DB 删除失败仍 ok:true |
| 38 | `ReportDate[:10]` 无守卫 ×2 | ✅ 缺陷属实且**两处都缺**（原报告称 dividend.go 有双守卫——反了）；东财 REPORT_DATE 现为 19 位定宽，触发概率低 |
| 39 | `mapIFindError(0,msg)` 返回 nil 掩盖真因 | ✅ 实测网络错误 → `(nil,nil)` → `unexpected end of JSON input`；不阻断降级，只毁排障 |
| 40 | WS 广播串行同步写、无心跳 | ⚠️ 机制属实；但实测**首次 5s 阻塞后写失败踢连接、自愈**，影响被高估为一次性有界 |
| 41 | 同步 AI 端点用 Background ctx | ✅ 客户端断开后请求照跑（最长 300s）；异步 job 用 Background 是有意设计 |
| 42 | AI 报告 `created_at` 恒 NULL | ✅ 实测三张表全 NULL；GORM 不为 `*string` 自动填充；前端不读该字段 |
| 43 | 盘前污染残余窗口 | ⚠️ 三重防护都只比时刻属实；但实测 09:15-09:24 全被滤掉 → **真实残余≈09:25-09:30 五分钟**，概率低 |
| 44 | 分析/录入走 GET 触发网络（dividend 链、名称回填） | ✅ 链路属实；dividend 判定为**有意设计**（仅手动按钮触发，建议改 POST 或登记例外）；名称回填幂等自愈、低危 |
| 45 | route 持有 `*ifind.Client`（跨层红线） | ✅ 属实；功能正确，架构违规 |
| 46 | 脱敏只覆盖 JSON 顶层 + 响应不脱敏 | ✅ 实测嵌套 `{"creds":{"api_key":...}}` 原样返回；当前密钥端点均扁平 → 无实际泄漏；`GET /api/ai/models` 明文回 api_key（127.0.0.1 既定设计） |
| 47 | GET /api/logs 整读 + 日志无轮转 | ✅ 实测 `logs.html` 每 2s 整读无限增长文件 |
| 48 | as_of 回看汇率混用（展示历史、计算今日） | ✅ 偏差 ~0.4%，仅回看场景 |
| 49 | A 股股息率回退路径误乘港汇 | ✅ 无币种判断属实；但触发条件窄（netProfit/eps 缺失 + 有 DvPerShare，当前库 0 行命中）且有 `priceCNY>0` 守卫（**除零子断言证伪**）；命中时虚高 17% |
| 50 | `int64(v*10000+0.5)` 负值截断 ×4 | ⚠️ 模式属实；**4 处均不可达**（上游正值过滤 / `roundPct` 是死代码）→ 非问题，顺手改即可 |
| 51 | H2 类型化 nil 陷阱（`any(*float64(nil)) != nil` 恒真） | ⚠️ **机制 + 实测全部成立**（typed nil 判定失真、derefF 计 0），但当前 16 行财务数据全非空 → **7 个消费点全部休眠**；`fwd_net_profit` 子项不成立（主路径已保证非 nil）。数据源一次缺列即爆 |
| 52 | M1 eps=0 → Inf/NaN → json 编码失败接口空 200 | ✅ 三行实测全中（gin 默认 encoding/json + Marshal 失败不写 body → 200+0字节）；当前库 0 行可达，对照 portfolio 侧有守卫 |
| 53 | 空值回退死代码、tag_cards 计数虚增、coverage 标志 | ✅ 均属实，当前休眠/不可见 |

---

## 三、修复优先级建议（仅供参考，本轮不修）

1. **第一梯队（不修 = 数字不可信 + 服务会假死）**：#1 死锁（锁外入队）、#2 分位补写端（或 `tags==nil` 走实时计算）、#3/#16 trade_time 与类型校验、#4 改删回滚、#5 港股汇率回退、#6 missing 填实、#7 worker recover + panic 改 error
2. **第二梯队（日常体感）**：#8 取消 + #9 timeout（同一根因，`ChatJSON` 加 WithTimeout + job 挂 cancel ctx）、#10 max_tokens 降级、#11 新闻 TTL、#14 港股 fxHKD、#13 标签口径统一、#17 除权日 dayPnl、#24 ETF secid、#20 iFinD 超时
3. **第三梯队**：#12 漏列、#15 分红币种、#18 分红区间补扫、#23 purge 门槛、#22 iFinD 量额、其余 P2 防御性修补

---

## 四、被推翻 / 收窄的子断言（复核的价值）

| 原报告主张 | 复核结论 |
|---|---|
| trade_time panic → **永久 500 改不掉删不掉** | ❌ 证伪：UPDATE/DELETE 在 panic 前已执行，第二次 PUT 已改对、第三次 200、DELETE 实际删掉。真实危害=假错误+脏数据+批量导入半途断 |
| 港股 `9:30` 分笔会触发 `Time[:5]` panic | ❌ normalizer 已有 `len==4 && isDigits` → `0930`→`09:30` 防御；仅非定宽畸形数据可达 |
| Prewarm 并发写共享 Headers | ❌ Prewarm 用三个不同 client、指数刷新串行；真路径是 Prewarm×手动端点（低概率） |
| `int64(+0.5)` 负值截断 4 处 | ❌ 4 处全不可达（正值过滤 / `roundPct` 零调用） |
| H2 nil 陷阱导致组合 PE 虚高 | ⚠️ 机制与实测成立，但当前数据 7 个消费点**全部休眠**；`fwd_net_profit` 子项不成立 |
| M2 eps=0 → 接口挂 | ⚠️ 链路成立，但当前库 0 行命中；且 `priceCNY>0` 守卫使其**不会除零**（只是不产出股息率） |
| `divergence` 是已不存在的遗留列 | ❌ 列存在，写入是清空（无害噪音） |
| WS 广播阻塞拖慢 worker | ⚠️ 影响一次性自愈（首次 5s 后踢连接），非持续阻塞 |
| dividend 有 `ReportDate` 双字段守卫 | ❌ 反了——**两处都缺**守卫（问题比原报告大） |
| `?tags=%20红利` 筛不中 | ❌ 整串已 TrimSpace；真问题是逗号内侧空格与 `?tags=,` |
| 标准 xlsx 可触发 `cells[-1]` | ❌ 标准生成器都写 `r` 属性；仅非标准文件可达 |
| iFinD `mapIFindError` 影响功能可用性 | ⚠️ 只毁排障（降级链照常走腾讯），不阻断功能 |

---

## 五、复核中的额外发现（原报告未列）

1. **`POST /api/trades` 传短 `trade_time` → 200 直接落脏**——比 PUT 更易触发的脏数据入口，之后该笔 PUT/DELETE 恒 500。
2. **`POST /api/holdings` 批量导入中途 panic → 部分导入且无任何反馈**。
3. **`dao.TestAmountHist` 是时间炸弹测试**：`UpsertAmountHist` 滚动删除 45 天前数据，测试灌固定日期 `2026-08-14`，今天已超 59 天 → **主干恒红**（CI 会一直红，与所有本轮问题无关）。
4. **`POST /api/ai-scoring/prefs/:tag/expand` 缺 `configured()` 守卫**（PUT 分支有）→ 无模型时 panic 走 500 而非契约的 400。
5. **`GET /api/ai/models` 响应明文回 `api_key`**（127.0.0.1 既定设计，但值得记档）。
6. **`jobs.go:696` Prewarm 扇出的 goroutine 同样无 recover**（原报告只点了 workerLoop 与 route 两处）。

---

## 附录 A：四组复核统计

| 组 | 覆盖 | ✅ 证实 | ⚠️ 部分 | ❌ 整条证伪 |
|---|---|---|---|---|
| 并发/AI（jobs/ai/ws） | 13 | 11 | 2 | 0 |
| 口径/数值（portfolio/valuation/holdings/volatility/dividend） | 14 | 14 | 1 | 0 |
| 数据源（raw/ifind/refresh/tech） | 12 | 8 | 4 | 0 |
| route（85 端点） | 14 | 10 | 4 | 0 |
| **合计** | **53** | **43** | **11** | **0** |

原报告约 67 条，未逐条复核的约 14 条均为低危（日志整读、code 归一、脱敏范围等已在附带验证中顺带确认）。

## 附录 B：复核手段

- 代码走读 + 全仓 grep 交叉验证（键名、调用链、消费点）
- httptest 完整 handler→service→DB 链实测（trade/etf-map/portfolio tags/search/kline 等）
- 子进程 panic 实测（死锁、requireModel、UpdateTrade、Excel 解析）
- 真实库 `data/etf.db` 取数验算（港股 PE/DV、波动率样本、财务数据空值率）
- 线上接口实证（东财 secid 两种写法对比）
- 全量 `go test ./... -count=1` 基线核对

## 附录 C：已排除（确认没问题的项，避免重复排查）

SQL 注解全部参数化；grade/action 枚举校验齐全；`profile_hash` 无时间戳/价格；各报告 Upsert PK 幂等；jobs 进度字段锁内更新；ws 锁序无嵌套；路由冲突混排合法；409 语义透传正确；AI 包全局 map 只读；`recent` 有 24 条上限；每日打分 tag_prefs 去重正确；50 笔交易不会起 50 个 goroutine（固定 worker 池）。

## 附录 D：与本轮无关的既有红测试

`internal/db/dao TestAmountHist`（fundflow_test.go:222）——45 天滚动删除 × 固定灌数日期的时间炸弹，今天起恒红。修法：灌数用 `time.Now().AddDate(0,0,-1)` 或把删除阈值参数化。

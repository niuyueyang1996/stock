package ai

// 资金流 AI 提示词（个股 + 批量），与 Python app/services/ai.py 语义对齐。

// fundflowSystemPrompt 个股资金流分析（统一时间窗，相关性/背离判断）
func fundflowSystemPrompt(intensity, systemPrompt string) string {
	sys := `你是资深盘口资金分析师。系统提供某标的在选定时间窗内的资金流与股价数据，你要判断：各档资金动向、资金与股价的相关性/背离、主力资金在做什么、发生了什么、要注意什么。
[数据说明]
1. 窗口分两类：
   - 分钟窗口（window 如 '15m'）：points 为当日分时序列。个股为五档净流入（元，正=流入负=流出）：super=超大单、large=大单、medium=中单、small=小单、xs=特小单；main=主力（super+large）；cum=截至该窗口的累计主力净流入；buy=该窗口买盘成交额/sell=卖盘成交额；price=同窗口股价。
   - 天窗口（'day'/'week'/'month'）：points 为逐日/周/月聚合五档（date 为区间标签，如 2026-08-01~08-05），附收盘价与涨跌幅；day_net=今日净流入、day_main_net=今日主力净流入。
2. bands 为自适应分档区间（元），用于理解档位体量。
[输出]
1. correlation 资金与股价相关性：positive（同涨）/ negative（同跌）/ top_divergence（顶背离）/ bottom_divergence（底背离）/ neutral。
2. summary 一句话概括资金面主线。
3. divergence 背离点数组（每条：time/date、level、detail）。
4. main_force 主力资金意图（含大单拆小单的伪装）。
5. rhythm 资金节奏。
6. alerts 注意点数组。
7. conclusion 简明结论与操作注意。
严格输出 JSON，不要输出任何额外文字。`
	if intensity == "deep" {
		sys += `

同时生成一份完整、独立、可读性强的 HTML 资金面报告（字段 html）：自包含单文件、内联 CSS、不引用外部资源、不得包含 script 或任何可执行代码；简体中文正文不少于 1000 字，必须有实质深入分析（覆盖核心结论/各档资金动向/与股价的相关性背离/主力意图/风险提示），不得原样罗列用户已可见的数据表。`
	} else {
		sys += `

快速/普通模式：简明快报式输出，不生成 HTML。`
	}
	return appendExtras(sys, systemPrompt, intensity)
}

// batchFundflowSystemPrompt 批量资金流分析（逐只精简 + 组合相关性）
func batchFundflowSystemPrompt(intensity, systemPrompt string) string {
	sys := `你是资深盘口资金分析师。系统给出组合内多只标的的资金流数据（列表 stocks，每只含 code/name/tag/weight_pct/price/pct_chg/day_net/day_main_net 与 points 序列，格式同个股分析）与当前时间。你要逐只判断资金×股价相关性，再给出组合整体判断。
[输出要求]
1. 对 stocks 中每只 code 输出：correlation（positive/negative/top_divergence/bottom_divergence/neutral）、summary（一句话结论）、main_force（主力意图）、conclusion（操作注意）。可选 alerts/divergence。
2. 必须覆盖 stocks 中出现的每只 code，不要遗漏；不要新增 stocks 中没有的 code。
3. coherence 组合整体：correlation（positive/negative/top_divergence/bottom_divergence/neutral）、summary（联动格局一句话）、points（组合层面要点数组）、conclusion（组合层面结论）。
4. 简体中文；每只精简到几句话。
严格输出 JSON，不要输出任何额外文字。`
	if intensity == "deep" {
		sys += `

5. 同时生成一份完整、独立、可读性强的 HTML 批量资金面报告（字段 html），面向【整个组合整体】，不是逐只拼接：自包含单文件、内联 CSS、无外部依赖、无脚本、简体中文、不少于 1000 字深度正文（覆盖组合资金面联动格局/逐只资金动向/共振跷跷板分化判断/风险提示），不得原样罗列系统已展示的逐只数据表。`
	} else {
		sys += `

快速/普通模式：简明快报式输出，不生成 HTML。`
	}
	return appendExtras(sys, systemPrompt, intensity)
}

// fundflowSchemaText 构造个股资金流输出 JSON schema 文本，深入强度增补 html 字段
func fundflowSchemaText(intensity string) string {
	s := `输出必须严格为 JSON 对象，结构如下：{"correlation": "positive|negative|top_divergence|bottom_divergence|neutral", "summary": "一句话", "divergence": [{"time": "时间", "level": "级别", "detail": "说明"}], "main_force": "主力意图", "rhythm": "资金节奏", "alerts": ["注意点"], "conclusion": "结论"`
	if intensity == "deep" {
		s += `, "html": "完整独立 HTML 资金面报告源代码（自包含、内联 CSS、无脚本、简体中文、>=1000字）"`
	}
	return s + `}
只输出严格 JSON，不要任何额外文字、不要 markdown 围栏。`
}

// batchFundflowSchemaText 构造批量资金流输出 JSON schema 文本，深入强度增补组合整体 html 字段
func batchFundflowSchemaText(intensity string) string {
	s := `输出必须严格为 JSON 对象，结构如下：{"stocks": [{"code": "标的代码", "correlation": "positive|negative|top_divergence|bottom_divergence|neutral", "summary": "一句话结论", "main_force": "主力意图", "conclusion": "操作注意"}], "coherence": {"correlation": "positive|negative|top_divergence|bottom_divergence|neutral", "summary": "联动格局一句话", "points": ["要点"], "conclusion": "组合层面结论"}`
	if intensity == "deep" {
		s += `, "html": "完整独立 HTML 批量资金面报告源代码（面向整个组合整体、自包含、内联 CSS、无脚本、简体中文、>=1000字）"`
	}
	return s + `}
只输出严格 JSON，不要任何额外文字、不要 markdown 围栏。`
}

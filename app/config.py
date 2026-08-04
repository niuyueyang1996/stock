"""全局配置：路径、收盘确认、评分权重、数据源开关。"""
from pathlib import Path

# 项目根目录（app/ 的上一级）
BASE_DIR = Path(__file__).resolve().parent.parent
DATA_DIR = BASE_DIR / "data"
DB_PATH = DATA_DIR / "etf.db"
STATIC_DIR = BASE_DIR / "static"

# 收盘确认：交易日 now >= 15:00+5min 视为已收盘，用当日收盘价定格
CLOSE_CONFIRM_MINUTES = 5
MARKET_CLOSE = "15:00"

# 网络请求
REQUEST_TIMEOUT = 10
HTTP_HEADERS = {
    "User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
}

# 数据源开关：sina / baidu / mock
SOURCE = "sina"

# 估值分位样本不足阈值（天）
QUANTILE_MIN_SAMPLES = 60

# 评分模型：买入/卖出因子权重（和=1），可经 /api/scoring/rules 修改
BUY_WEIGHTS = {
    "pe_pct": 0.25,      # PE分位低 → 得分高
    "pb_pct": 0.20,      # PB分位低
    "dv_ratio": 0.15,    # 股息率
    "fund_flow": 0.15,   # 主力资金净流入
    "pct_chg": 0.10,     # 当日涨跌（回踩得分高）
    "roe": 0.15,         # ROE质量
}
SELL_WEIGHTS = {
    "pe_pct": 0.25,      # PE分位高 → 得分高
    "pb_pct": 0.20,      # PB分位高
    "dv_ratio": 0.10,    # 股息率低
    "fund_flow": 0.15,   # 主力净流出
    "pct_chg": 0.10,     # 当日涨幅大
    "concentration": 0.20,  # 组合集中度（仓位>20%该减仓）
}

# 评级阈值
RATING_LEVELS = [
    (85, "A", "优秀"),
    (70, "B", "良好"),
    (55, "C", "一般"),
    (0, "D", "较差"),
]

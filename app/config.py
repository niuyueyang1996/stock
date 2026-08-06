"""全局配置：路径、收盘确认、评分权重、数据源开关。"""
import os
import sys
from pathlib import Path

# 项目根目录（app/ 的上一级）与只读资源目录。
# PyInstaller 中 sys._MEIPASS 指向解包后的程序资源目录；个人数据绝不写入这里。
BASE_DIR = Path(__file__).resolve().parent.parent
IS_PACKAGED = bool(getattr(sys, "frozen", False))
RESOURCE_DIR = Path(getattr(sys, "_MEIPASS", BASE_DIR))


def resolve_app_home(
    *,
    env: dict[str, str] | None = None,
    is_packaged: bool | None = None,
    os_name: str | None = None,
    base_dir: Path | None = None,
) -> Path:
    """Resolve the writable application home for development or a packaged app."""
    values = os.environ if env is None else env
    override = values.get("STOCK_APP_HOME", "").strip()
    if override:
        return Path(override).expanduser()

    packaged = IS_PACKAGED if is_packaged is None else is_packaged
    platform_name = os.name if os_name is None else os_name
    root = BASE_DIR if base_dir is None else base_dir
    if packaged and platform_name == "nt":
        local_app_data = values.get("LOCALAPPDATA", "").strip()
        if local_app_data:
            return Path(local_app_data) / "StockAnalyzer"
        return Path.home() / "AppData" / "Local" / "StockAnalyzer"
    return root


APP_HOME = resolve_app_home()
DATA_DIR = APP_HOME / "data"
DB_PATH = DATA_DIR / "etf.db"
LOG_DIR = APP_HOME / "logs"
RUNTIME_DIR = APP_HOME / "runtime"
STATIC_DIR = RESOURCE_DIR / "static"

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

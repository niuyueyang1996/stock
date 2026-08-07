"""接口层：乐咕乐股指数估值历史原始接口（乐咕 HTTP，非 akshare 封装）。

乐咕 `index-basic-pe|pb` 接口支持任意 indexCode（含科创50=000688.SH、中证红利=000922.CSI、
恒生=HSI），比 akshare 的 stock_index_pe_lg（仅 12 个中文名）覆盖更全。
返回 DataFrame（保留英文列 date/close/ttmPe/addLyrPe/pb/...），过滤/取列在 normalizers。
空/异常统一 return None（调用方按「无此指数估值」处理，不抛）。
"""
import hashlib
import re
from datetime import date

import pandas as pd
import requests

from app.config import HTTP_HEADERS, REQUEST_TIMEOUT

_BASE = "https://legulegu.com/api/stockdata"
_CSRF_PAGE = "https://legulegu.com/stockdata/sz50-ttm-lyr"


def _legu_token() -> str:
    """乐咕 token = md5(今日 iso 日期)。"""
    return hashlib.md5(date.today().isoformat().encode()).hexdigest()


def _legu_csrf() -> tuple[requests.Session, dict]:
    """乐咕 csrf + cookie。优先复用 akshare 的 get_cookie_csrf；失败时内联正则兜底。"""
    try:
        from akshare.stock_feature.stock_a_indicator import get_cookie_csrf

        cc = get_cookie_csrf(_CSRF_PAGE)
        return cc["cookies"], cc["headers"]
    except Exception:  # noqa: BLE001 akshare 不可用时内联兜底
        session = requests.Session()
        session.headers.update(HTTP_HEADERS)
        r = session.get(_CSRF_PAGE, timeout=REQUEST_TIMEOUT)
        m = re.search(r'<meta name="_csrf" content="([^"]+)"', r.text)
        if not m:
            raise RuntimeError("乐咕 csrf 获取失败")
        headers = dict(HTTP_HEADERS)
        headers["X-CSRF-Token"] = m.group(1)
        return r.cookies, headers


def _fetch(endpoint: str, legu_code: str) -> pd.DataFrame | None:
    """请求乐咕 index-basic-pe|pb，返回 DataFrame（英文列）；空/异常统一 None。"""
    try:
        cookies, headers = _legu_csrf()
        resp = requests.get(
            f"{_BASE}/{endpoint}",
            params={"token": _legu_token(), "indexCode": legu_code},
            headers=headers, cookies=cookies, timeout=REQUEST_TIMEOUT,
        )
        resp.raise_for_status()
        data = resp.json().get("data") or []
    except Exception:  # noqa: BLE001 单指数估值失败不中断预热
        return None
    if not data:
        return None
    return pd.DataFrame(data)


def index_pe_hist(legu_code: str) -> pd.DataFrame | None:
    """指数 PE 历史。legu_code 为乐咕指数代码（如 '000300.SH'/'000922.CSI'/'HSI'）。"""
    return _fetch("index-basic-pe", legu_code)


def index_pb_hist(legu_code: str) -> pd.DataFrame | None:
    """指数 PB 历史。legu_code 为乐咕指数代码。"""
    return _fetch("index-basic-pb", legu_code)

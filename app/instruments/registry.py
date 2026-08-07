"""标的工厂：判型（唯一入口）+ 按 code 缓存实例。

type_of 路由顺序：指数 → 港股 → ETF → A股。
is_index_code 自带 stocks 冲突防护（000001 已登记个股则让位给个股）。
get_instrument 返回可复用的 Instrument 实例（数据方法无 code 参数）。
测试通过 monkeypatch _FACTORY 覆盖工厂（所有绑定 get_instrument 的模块都会生效，
因为 get_instrument 在调用时动态读 _FACTORY 全局）。
"""
from app.data.base import is_etf_code, is_hk_code, is_index_code

from app.instruments.ashares import AshareInstrument
from app.instruments.base import Instrument
from app.instruments.etf import EtfInstrument
from app.instruments.hk import HkInstrument
from app.instruments.index import IndexInstrument

_KINDS: dict[str, type] = {
    "ashare": AshareInstrument,
    "hk": HkInstrument,
    "etf": EtfInstrument,
    "index": IndexInstrument,
}

# 已构造实例缓存：同一 code 复用（避免重复判型/构造）。
# 指数注册表/ETF 映射变化时用 invalidate/clear_cache 失效。
_CACHE: dict[str, Instrument] = {}


def type_of(code: str) -> str:
    """判型：index → hk → etf → ashare。"""
    if is_index_code(code):
        return "index"
    if is_hk_code(code):
        return "hk"
    if is_etf_code(code):
        return "etf"
    return "ashare"


def _default_factory(code: str) -> Instrument:
    return _KINDS[type_of(code)](code)


# 工厂钩子：测试 monkeypatch 覆盖为 MockInstrument 工厂。
# 必须通过此全局而非 get_instrument 本体，保证已 `from ... import get_instrument`
# 绑定的模块（refresh/holdings/api/...）也能生效。
_FACTORY = _default_factory


def get_instrument(code: str) -> Instrument:
    """标的工厂（缓存）：返回该 code 的 Instrument 实例。"""
    inst = _CACHE.get(code)
    if inst is None:
        inst = _FACTORY(code)
        _CACHE[code] = inst
    return inst


def invalidate(code: str) -> None:
    """失效单个缓存实例（指数注册表/ETF 映射变更时调用）。"""
    _CACHE.pop(code, None)


def clear_cache() -> None:
    """清空全部缓存实例（测试换库后调用，避免旧实例跨测试泄漏）。"""
    _CACHE.clear()

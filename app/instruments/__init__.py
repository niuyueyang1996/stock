"""标的类型层：唯一判型入口 + 各类型数据实现 + 详情统一组装。

分层（用户确认）：
- registry.get_instrument(code)  对外统一入口（工厂，按 code 缓存实例）
- base.Instrument                抽象基类：业务属性 + 数据方法接口
- transform                      类型规则表 + 平台代码/估值源/东财 secid 转换
- ashares/hk/etf/index           四类型具体实现（数据获取直接调 raw 层，降级链内部自管）
- detail.build_detail            个股/指数详情统一组装（去重复用）

业务层（refresh/holdings/api/ai/portfolio/ai_scoring）只依赖 get_instrument + Instrument
的通用属性与方法，不写 if index / elif hk / elif etf 分支。
"""
from app.instruments.registry import get_instrument, invalidate, type_of
from app.instruments.base import Instrument

__all__ = ["get_instrument", "invalidate", "type_of", "Instrument"]

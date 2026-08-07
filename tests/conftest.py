"""pytest 共享 fixture：每个测试用独立临时数据库，且全程无真实网络。"""
import pathlib

import pytest

import app.models.db as db


@pytest.fixture(autouse=True)
def temp_db(monkeypatch, tmp_path):
    """将 db 模块指向临时数据库并初始化建表。"""
    monkeypatch.setattr(db, "DATA_DIR", pathlib.Path(tmp_path))
    monkeypatch.setattr(db, "DB_PATH", pathlib.Path(tmp_path) / "test.db")
    db.init_db()

    # 打桩网络依赖：标的工厂用 MockInstrument、行情固定返回，保证测试离线可跑
    # get_instrument 在调用时动态读 registry._FACTORY 全局 → 已绑定 get_instrument 的模块同样生效。
    from app.instruments import registry as reg

    from mock_instrument import MockInstrument

    monkeypatch.setattr(
        reg, "_FACTORY", lambda code: MockInstrument(code, reg.type_of(code))
    )
    # 换库后清空已缓存实例，防旧测试实例跨测试泄漏（尤其 index 的注册表绑定）
    reg.clear_cache()

    # 打桩网络依赖：行情固定返回（quote.get_quote 与各模块已绑定的 get_quote）
    from app.services import quote as quote_mod

    _mock_quote = lambda code, now=None, stale=None: {  # noqa: E731
        "code": code, "name": f"模拟股{code}", "price": 10.0, "pct_chg": 0.5,
        "prev_close": 9.95, "open": 9.96, "high": 10.1, "low": 9.9,
        "volume": 100000, "amount": 1000000, "ts": "2026-08-04 15:00:00", "stale": False,
    }
    monkeypatch.setattr(quote_mod, "get_quote", _mock_quote)
    # 指数预热走 raw 层真实网络——测试环境打桩为秒完成，
    # 否则 create_app 的启动后台线程会联网写缓存，污染「GET 零写入/一键清空」等断言
    import app.services.indices as imod

    monkeypatch.setattr(
        imod, "refresh_all_indices", lambda: {"codes": [], "ok": 0, "fail": []}
    )

    # 覆盖已绑定的模块引用（这些模块可能在 import 时已绑定原函数）
    import app.analysis.portfolio as pmod

    monkeypatch.setattr(pmod, "get_quote", _mock_quote)
    import app.api.stocks as smod

    monkeypatch.setattr(smod, "get_quote", _mock_quote)
    import app.instruments.detail as dmod

    monkeypatch.setattr(dmod, "get_quote", _mock_quote)
    # 全市场代码名称列表：测试打桩为空，避免名称回填/搜索读开发机真实列表文件
    monkeypatch.setattr(smod, "_load_stock_list", lambda: [])
    monkeypatch.setattr(smod, "_load_hk_stock_list", lambda: [])
    monkeypatch.setattr(smod, "_read_stock_list_cache", lambda: [])
    monkeypatch.setattr(smod, "_read_hk_stock_list_cache", lambda: [])
    # AI 自动打分后台线程在测试环境会逃逸：monkeypatch 还原后可能打到真实库/真实网络。
    # 测试里 maybe_auto_score_daily 退化为「只失效、不后台打分」，打分一律由测试显式调用。
    import app.services.ai_scoring as aisc

    monkeypatch.setattr(aisc, "maybe_auto_score_daily", lambda d: aisc.invalidate_daily(d))
    yield


@pytest.fixture
def client():
    """TestClient（共享，供各 API 测试模块使用）。"""
    from fastapi.testclient import TestClient

    from app.main import create_app

    # raise_server_exceptions=False：让服务端异常由全局 handler 转成 500+detail 响应
    return TestClient(create_app(), raise_server_exceptions=False)

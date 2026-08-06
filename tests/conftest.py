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

    # 打桩网络依赖：数据源用 mock，行情固定返回，保证测试离线可跑
    # 注意：各分析模块以 `from x import y` 在加载期绑定函数，需在 patch 后覆盖其绑定
    from app.data import base as base_mod
    from app.data.providers import MockProvider
    from app.services import quote as quote_mod

    _mock_quote = lambda code, now=None, stale=None: {  # noqa: E731
        "code": code, "name": f"模拟股{code}", "price": 10.0, "pct_chg": 0.5,
        "prev_close": 9.95, "open": 9.96, "high": 10.1, "low": 9.9,
        "volume": 100000, "amount": 1000000, "ts": "2026-08-04 15:00:00", "stale": False,
    }
    monkeypatch.setattr(base_mod, "build_manager", lambda: base_mod.SourceManager([MockProvider()]))
    monkeypatch.setattr(quote_mod, "get_quote", _mock_quote)

    # 覆盖已绑定的模块引用（这些模块可能在 import 时已绑定原函数）
    import app.analysis.portfolio as pmod

    monkeypatch.setattr(pmod, "get_quote", _mock_quote)
    import app.analysis.valuation as vmod
    import app.services.refresh as rmod

    monkeypatch.setattr(vmod, "build_manager", lambda: base_mod.SourceManager([MockProvider()]))
    monkeypatch.setattr(rmod, "build_manager", lambda: base_mod.SourceManager([MockProvider()]))
    import app.api.stocks as smod

    monkeypatch.setattr(smod, "get_quote", _mock_quote)
    # 全市场代码名称列表：测试打桩为空，避免名称回填/搜索读开发机真实列表文件
    monkeypatch.setattr(smod, "_load_stock_list", lambda: [])
    monkeypatch.setattr(smod, "_load_hk_stock_list", lambda: [])
    monkeypatch.setattr(smod, "_read_stock_list_cache", lambda: [])
    monkeypatch.setattr(smod, "_read_hk_stock_list_cache", lambda: [])
    yield


@pytest.fixture
def client():
    """TestClient（共享，供各 API 测试模块使用）。"""
    from fastapi.testclient import TestClient

    from app.main import create_app

    # raise_server_exceptions=False：让服务端异常由全局 handler 转成 500+detail 响应
    return TestClient(create_app(), raise_server_exceptions=False)

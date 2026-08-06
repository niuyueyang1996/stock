import hashlib
import json
from pathlib import Path

from app import config
from app.version import APP_ID, APP_VERSION


def test_resolve_app_home_development(tmp_path):
    assert config.resolve_app_home(
        env={}, is_packaged=False, os_name="posix", base_dir=tmp_path
    ) == tmp_path


def test_resolve_app_home_packaged_windows():
    home = config.resolve_app_home(
        env={"LOCALAPPDATA": r"C:\Users\小明\AppData\Local"},
        is_packaged=True,
        os_name="nt",
    )
    assert home == Path(r"C:\Users\小明\AppData\Local") / "StockAnalyzer"


def test_resolve_app_home_override(tmp_path):
    target = tmp_path / "自定义 数据"
    assert config.resolve_app_home(
        env={"STOCK_APP_HOME": str(target)}, is_packaged=True, os_name="nt"
    ) == target


def test_health_has_stable_identity(client):
    response = client.get("/api/health")
    assert response.status_code == 200
    assert response.json() == {"ok": True, "app_id": APP_ID, "version": APP_VERSION}


def test_untrusted_host_is_rejected(client):
    response = client.get("/api/health", headers={"host": "malicious.example"})
    assert response.status_code == 400


def test_untrusted_cors_origin_is_not_allowed(client):
    response = client.options(
        "/api/data/reset",
        headers={
            "origin": "https://malicious.example",
            "access-control-request-method": "POST",
        },
    )
    assert "access-control-allow-origin" not in response.headers


def test_launcher_state_round_trip(tmp_path):
    from app.windows_launcher import load_state, save_state

    path = tmp_path / "runtime" / "launcher.json"
    save_state(path, pid=1234, port=8001)
    assert load_state(path) == {
        "app_id": APP_ID,
        "version": APP_VERSION,
        "pid": 1234,
        "port": 8001,
    }
    assert json.loads(path.read_text(encoding="utf-8"))["port"] == 8001


def test_find_available_port_falls_back(monkeypatch):
    from app import windows_launcher as launcher

    class FakeSocket:
        def setsockopt(self, *_args):
            return None

        def bind(self, address):
            if address[1] == 8000:
                raise OSError("occupied")

        def close(self):
            return None

    monkeypatch.setattr(launcher.socket, "socket", lambda *_args: FakeSocket())
    assert launcher.find_available_port(ports=[8000, 8001]) == 8001


def test_duplicate_launch_opens_existing_server(monkeypatch):
    from app import windows_launcher as launcher

    opened = []
    monkeypatch.setattr(
        launcher,
        "load_state",
        lambda: {"app_id": APP_ID, "version": APP_VERSION, "pid": 123, "port": 8002},
    )
    monkeypatch.setattr(
        launcher, "is_our_server", lambda port, timeout=1.0: port == 8002
    )
    monkeypatch.setattr(
        launcher.webbrowser, "open", lambda url, new=0: opened.append((url, new))
    )

    assert launcher.open_existing_instance() is True
    assert opened == [("http://127.0.0.1:8002/", 2)]


def test_static_pages_use_pinned_local_echarts():
    root = Path(__file__).resolve().parent.parent
    vendor = root / "static" / "vendor" / "echarts.min.js"
    assert hashlib.sha256(vendor.read_bytes()).hexdigest() == (
        "bf4a223524e40b77c304bec67e1222cf551f14880cf42c69dc046558e11c07b1"
    )
    for name in ("index.html", "portfolio.html", "stock.html", "trade.html"):
        html = (root / "static" / name).read_text(encoding="utf-8")
        assert 'src="/static/vendor/echarts.min.js"' in html
        assert "cdn.jsdelivr.net/npm/echarts" not in html

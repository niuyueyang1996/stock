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


def test_packaged_smoke_test_starts_checks_and_stops(monkeypatch):
    from app import windows_launcher as launcher

    events = []

    class FakeController:
        port = 8003

        def __init__(self, state_file):
            events.append(("init", state_file.name))

        def start(self):
            events.append(("start", self.port))
            return True

        def stop(self):
            events.append(("stop", self.port))

    monkeypatch.setattr(launcher, "setup_logging", lambda: events.append(("logging", None)))
    monkeypatch.setattr(launcher, "is_our_server", lambda port: port == 8003)

    assert launcher.run_smoke_test(FakeController) == 0
    assert events == [
        ("logging", None),
        ("init", "smoke-test.json"),
        ("start", 8003),
        ("stop", 8003),
    ]


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


def test_windows_powershell_build_script_is_ascii():
    """Windows PowerShell 5.1 misreads BOM-less UTF-8 and can corrupt quotes."""
    root = Path(__file__).resolve().parent.parent
    assert (root / "packaging" / "windows" / "build.ps1").read_bytes().isascii()


def test_echarts_is_marked_binary_for_cross_platform_checkout():
    root = Path(__file__).resolve().parent.parent
    attributes = (root / ".gitattributes").read_text(encoding="ascii")
    assert "static/vendor/echarts.min.js -text" in attributes.splitlines()


def test_inno_language_file_is_vendored_and_wired():
    root = Path(__file__).resolve().parent.parent
    language = root / "packaging" / "windows" / "languages" / "ChineseSimplified.isl"
    assert hashlib.sha256(language.read_bytes()).hexdigest() == (
        "e0b0b350e2245f3c5e65586dfe43d574f6e7f06f2261149aba284954b3fc9a8d"
    )
    assert "Inno Setup version 6.5.0+ Chinese Simplified messages" in language.read_text(
        encoding="utf-8"
    )

    installer = (root / "packaging" / "windows" / "StockAnalyzer.iss").read_text(
        encoding="utf-8"
    )
    assert 'MessagesFile: "{#LanguageFile}"' in installer
    assert "compiler:Languages" not in installer

    attributes = (root / ".gitattributes").read_text(encoding="ascii")
    assert "packaging/windows/languages/*.isl -text" in attributes.splitlines()


def test_workflow_pins_inno_setup_version():
    root = Path(__file__).resolve().parent.parent
    workflow = (root / ".github" / "workflows" / "windows-installer.yml").read_text(
        encoding="utf-8"
    )
    assert "choco install innosetup --version=6.7.1" in workflow
    assert "--allow-downgrade" in workflow


def test_windows_build_runs_end_to_end_smoke_tests():
    root = Path(__file__).resolve().parent.parent
    script = (root / "packaging" / "windows" / "build.ps1").read_text(
        encoding="ascii"
    )
    assert "VersionInfo.ProductVersion" not in script
    assert "--smoke-test" in script
    assert "/VERYSILENT /SUPPRESSMSGBOXES /NORESTART" in script
    assert "Silent uninstaller smoke test" in script


def test_workflow_uploads_failure_diagnostics():
    root = Path(__file__).resolve().parent.parent
    workflow = (root / ".github" / "workflows" / "windows-installer.yml").read_text(
        encoding="utf-8"
    )
    assert "if: failure()" in workflow
    assert "warn-StockAnalyzer.txt" in workflow
    assert "bundle-smoke-home/logs/" in workflow
    assert 'branches:\n      - "main"' in workflow

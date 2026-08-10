"""updater 测试：版本比较 + GitHub Release 检查（mock 网络），全程离线。"""
from app import updater


def test_parse_version():
    assert updater.parse_version("0.2.0") == (0, 2, 0)
    assert updater.parse_version("1.10.3") == (1, 10, 3)
    assert updater.parse_version("bogus") == (0, 0, 0)
    assert updater.parse_version("") == (0, 0, 0)


def test_compare_versions():
    assert updater.compare_versions("0.2.0", "0.3.0") == -1
    assert updater.compare_versions("0.3.0", "0.2.0") == 1
    assert updater.compare_versions("0.2.0", "0.2.0") == 0
    assert updater.compare_versions("1.0.0", "1.0.1") == -1
    assert updater.compare_versions("1.9.9", "1.10.0") == -1
    assert updater.compare_versions("bogus", "0.1.0") == -1


def _release(tag: str, asset_names: list[str]) -> dict:
    return {
        "tag_name": tag,
        "assets": [
            {"name": name, "browser_download_url": f"https://example.com/{name}"}
            for name in asset_names
        ],
    }


def test_check_newer_version(monkeypatch):
    monkeypatch.setattr(
        updater, "_fetch_json",
        lambda *a, **k: _release(
            "v0.3.0",
            ["StockAnalyzer-Setup-0.3.0-x64.exe", "StockAnalyzer-Setup-0.3.0-x64.exe.sha256"],
        ),
    )
    r = updater.check_for_update()
    assert r and r["version"] == "0.3.0"
    assert r["download_url"].endswith("StockAnalyzer-Setup-0.3.0-x64.exe")
    assert r["sha256_url"].endswith(".sha256")


def test_check_same_version(monkeypatch):
    monkeypatch.setattr(
        updater, "_fetch_json",
        lambda *a, **k: _release("v0.2.0", ["StockAnalyzer-Setup-0.2.0-x64.exe"]),
    )
    assert updater.check_for_update() is None


def test_check_lower_version(monkeypatch):
    monkeypatch.setattr(
        updater, "_fetch_json",
        lambda *a, **k: _release("v0.1.0", ["StockAnalyzer-Setup-0.1.0-x64.exe"]),
    )
    assert updater.check_for_update() is None


def test_check_request_failure(monkeypatch):
    monkeypatch.setattr(updater, "_fetch_json", lambda *a, **k: None)
    assert updater.check_for_update() is None


def test_check_asset_not_matching(monkeypatch):
    """有新版本但没有匹配的 x64 安装包资产 → None。"""
    monkeypatch.setattr(
        updater, "_fetch_json",
        lambda *a, **k: _release("v0.3.0", ["other-file.zip", "StockAnalyzer-Setup-0.3.0-arm64.exe"]),
    )
    assert updater.check_for_update() is None


def test_check_no_tag(monkeypatch):
    monkeypatch.setattr(updater, "_fetch_json", lambda *a, **k: {"assets": []})
    assert updater.check_for_update() is None

"""应用自动更新：检查 GitHub Releases 最新版、下载安装包、校验、启动静默安装。

平台无关的核心逻辑放这里（版本比较 / 检查 / 下载 / 校验 / 安装命令），可在 macOS/Linux 单测；
Windows-only 的托盘集成在 windows_launcher.py。GitHub 公开仓库免认证，任何失败静默降级不打扰。
"""
from __future__ import annotations

import hashlib
import json
import logging
import re
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request
from pathlib import Path
from typing import Callable

from app.version import APP_ID, APP_VERSION

logger = logging.getLogger("updater")

# 公开仓库；安装包发布走 GitHub Releases（CI 打 tag v* 自动发布 exe + sha256）
REPO = "niuyueyang1996/stock"
RELEASES_LATEST_URL = f"https://api.github.com/repos/{REPO}/releases/latest"
_ASSET_PATTERN = re.compile(r"^StockAnalyzer-Setup-(\d+\.\d+\.\d+)-x64\.exe$")
_SHA256_PATTERN = re.compile(r"^StockAnalyzer-Setup-.*-x64\.exe\.sha256$")


def _ua() -> str:
    return f"{APP_ID}-updater/{APP_VERSION}"


def parse_version(v: str) -> tuple[int, int, int]:
    """'x.y.z' → (x, y, z)；非法返回 (0,0,0)（视为无版本）。"""
    m = re.match(r"^(\d+)\.(\d+)\.(\d+)$", str(v or "").strip())
    if not m:
        return (0, 0, 0)
    return (int(m[1]), int(m[2]), int(m[3]))


def compare_versions(a: str, b: str) -> int:
    """a vs b → -1/0/1（非法版本按 0.0.0 参与比较）。"""
    va, vb = parse_version(a), parse_version(b)
    return -1 if va < vb else (1 if va > vb else 0)


def _fetch_json(url: str, timeout: float) -> dict | None:
    request = urllib.request.Request(url, headers={"User-Agent": _ua()})
    try:
        with urllib.request.urlopen(request, timeout=timeout) as resp:
            text = resp.read().decode("utf-8")
    except (OSError, urllib.error.URLError, ValueError):
        logger.info("更新检查网络失败（静默降级）：%s", url)
        return None
    try:
        data = json.loads(text)
    except (ValueError, TypeError):
        return None
    return data if isinstance(data, dict) else None


def check_for_update(timeout: float = 10.0) -> dict | None:
    """查 GitHub Releases 最新版。

    返回 {"version": "0.3.0", "download_url": "..."} 表示有新版本；
    无新版 / 请求失败 / 资产不匹配 → None（调用方静默，不打扰）。
    """
    data = _fetch_json(RELEASES_LATEST_URL, timeout)
    if not data:
        return None
    tag = str(data.get("tag_name") or "")
    version = tag[1:] if tag.startswith("v") else tag
    if compare_versions(version, APP_VERSION) <= 0:
        return None  # 最新版 <= 当前，无需更新
    download_url = None
    sha256_url = None
    for asset in data.get("assets") or []:
        name = str(asset.get("name") or "")
        if not download_url and _ASSET_PATTERN.match(name):
            download_url = asset.get("browser_download_url")
        elif _SHA256_PATTERN.match(name):
            sha256_url = asset.get("browser_download_url")
    if not download_url:
        logger.info("最新版 %s 没有匹配的 x64 安装包资产", version)
        return None
    out = {"version": version, "download_url": str(download_url)}
    if sha256_url:
        out["sha256_url"] = str(sha256_url)
    return out


def download_installer(
    url: str,
    dest: Path | None = None,
    progress_cb: Callable[[int, int], None] | None = None,
) -> Path:
    """流式下载安装包到 %TEMP%（缺省），返回文件路径；失败抛异常由调用方处理。"""
    dest = dest or Path(tempfile.gettempdir()) / (
        f"StockAnalyzer-Setup-update-{APP_VERSION}-x64.exe"
    )
    request = urllib.request.Request(url, headers={"User-Agent": _ua()})
    with urllib.request.urlopen(request, timeout=30) as resp:
        total = int(resp.headers.get("Content-Length") or 0)
        downloaded = 0
        with open(dest, "wb") as f:
            while True:
                chunk = resp.read(64 * 1024)
                if not chunk:
                    break
                f.write(chunk)
                downloaded += len(chunk)
                if progress_cb:
                    try:
                        progress_cb(downloaded, total)
                    except Exception:  # noqa: BLE001 进度回调失败不影响下载
                        pass
    return dest


def sha256_of(path: Path) -> str:
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    return h.hexdigest()


def verify_sha256(exe_path: Path, expected_hex: str) -> bool:
    return sha256_of(exe_path).lower() == str(expected_hex or "").lower()


def fetch_sha256(url: str, timeout: float = 10.0) -> str | None:
    """从 release 的 .sha256 资产拉取校验值（内容形如 'hex  filename'）。失败/缺省返回 None。"""
    request = urllib.request.Request(url, headers={"User-Agent": _ua()})
    try:
        with urllib.request.urlopen(request, timeout=timeout) as resp:
            text = resp.read().decode("ascii", errors="replace")
    except (OSError, urllib.error.URLError, ValueError):
        return None
    m = re.match(r"^([0-9a-fA-F]{64})", text.strip())
    return m.group(1) if m else None


def install_and_restart(installer: Path) -> bool:
    """启动安装包静默安装（detached，不等待）。

    调用方（托盘）先退出当前实例再调它；Inno Setup 静默完成后 [Run] 自动启动新版本。
    """
    if not installer.exists():
        logger.error("安装包不存在：%s", installer)
        return False
    if sys.platform != "win32":
        logger.warning("自动更新仅在 Windows 生效，跳过安装：%s", installer)
        return False
    args = [str(installer), "/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/SP-"]
    try:
        subprocess.Popen(args, close_fds=True)  # detached：父进程退出后继续安装
        logger.info("已启动静默安装：%s", installer.name)
        return True
    except OSError as e:  # noqa: BLE001
        logger.error("启动安装程序失败：%s", e)
        return False

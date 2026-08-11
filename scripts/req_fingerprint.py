#!/usr/bin/env python3
"""依赖指纹工具（启动器共用，run.sh / start.cmd）。

把 requirements 声明文件（txt / dev / runtime）的联合内容算成 sha256，
启动器用它判断依赖是否变更、从而决定是否自动重装。

用法：
  python scripts/req_fingerprint.py [print|check|write]
    print  输出当前指纹（默认）
    check  与 .venv/.req.sha256 比对，一致退出码 0，否则 1
    write  把当前指纹写入 .venv/.req.sha256
"""
import hashlib
import os
import sys

REQ_FILES = (
    "requirements.txt",
    "requirements-dev.txt",
    "requirements-runtime.txt",
)
STAMP = os.path.join(".venv", ".req.sha256")


def fingerprint() -> str:
    """对参与声明的文件做联合哈希（任一文件存在即参与）。"""
    h = hashlib.sha256()
    for name in REQ_FILES:
        if os.path.exists(name):
            with open(name, "rb") as f:
                h.update(name.encode())
                h.update(b"\x00")
                h.update(f.read())
            h.update(b"\x01")
    return h.hexdigest()


def main() -> int:
    action = sys.argv[1] if len(sys.argv) > 1 else "print"
    if action == "print":
        print(fingerprint())
        return 0
    if action == "check":
        if not os.path.exists(STAMP):
            return 1
        with open(STAMP) as f:
            return 0 if f.read().strip() == fingerprint() else 1
    if action == "write":
        with open(STAMP, "w") as f:
            f.write(fingerprint())
        return 0
    print(f"usage: {sys.argv[0]} [print|check|write]", file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())

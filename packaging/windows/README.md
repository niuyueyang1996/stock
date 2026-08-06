# Windows 一站式安装包

最终用户只需要运行 `StockAnalyzer-Setup-<version>-x64.exe`。安装包已经包含
Python 3.12、全部依赖和静态资源，不会在用户电脑上运行 pip，也不要求管理员权限。

## 本地构建

构建机要求：

- Windows 10/11 x64
- 64 位 Python 3.12，且 `python` 在 PATH 中
- Inno Setup 6
- 可访问 Python 包索引（仅构建时需要）

在仓库根目录执行：

```powershell
powershell -ExecutionPolicy Bypass -File .\packaging\windows\build.ps1
```

脚本会创建独立构建虚拟环境、安装锁定依赖、运行测试、生成 PyInstaller
`onedir` 程序目录，再编译出：

```text
dist\windows\StockAnalyzer-Setup-<version>-x64.exe
dist\windows\StockAnalyzer-Setup-<version>-x64.exe.sha256
```

只在已经单独跑过完整测试时，才可使用 `-SkipTests`。

## 发布与数据安全

- 手动触发 GitHub Actions 会生成可下载的构建产物。
- 推送与 `app/version.py` 一致的 `v<version>` 标签会创建 GitHub Release。
- 构建脚本拒绝包含 `etf.db`、`stock_list.json` 或 `hk_stock_list.json` 的程序目录。
- 用户数据库位于 `%LOCALAPPDATA%\StockAnalyzer\data`，不在安装目录中。
- 覆盖安装保留数据；卸载时默认保留，并询问是否一并删除。

## 未签名安装包

首版没有代码签名。Windows SmartScreen 可能显示“Windows 已保护你的电脑”。确认安装包
来自本项目 Release 且 SHA-256 校验一致后，可点击“更多信息”，再点击“仍要运行”。

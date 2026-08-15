// Package config 全局配置：路径、端口、超时、数据源开关。
// 对齐 app/config.py 的关键语义（STOCK_APP_HOME 覆盖、开发/打包路径）。
package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// 常量：收盘确认 / 网络 / 数据源开关（与 Python 端一致）
const (
	CloseConfirmMinutes = 5 // 交易日 now >= 15:00+5min 视为已收盘
	MarketClose         = "15:00"
	MarketOpen          = "09:15" // 集合竞价起算；now < 09:15 视为未开盘
	RequestTimeoutSec   = 10
	Source              = "sina" // 数据源开关：sina / baidu / mock

	DefaultPort = 8000
)

// HTTPHeaders 统一请求头（对齐 app/config.py HTTP_HEADERS）
var HTTPHeaders = map[string]string{
	"User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36",
}

// Config 运行时配置（开发态由环境变量覆盖，打包态由 AppHome 决定）
type Config struct {
	// AppHome 可写应用主目录（对齐 resolve_app_home）
	AppHome string
	// DataDir 数据目录
	DataDir string
	// DBPath SQLite 数据库路径
	DBPath string
	// StaticDir 静态资源目录（前端四页）
	StaticDir string
	// Port 监听端口
	Port int
	// ListenHost 监听地址（Windows 启动器要求仅 127.0.0.1）
	ListenHost string
}

// ResolveAppHome 对齐 Python resolve_app_home：STOCK_APP_HOME 优先；
// 打包态（Windows）用 %LOCALAPPDATA%/StockAnalyzer；开发态用仓库根。
func ResolveAppHome() string {
	if v := strings.TrimSpace(os.Getenv("STOCK_APP_HOME")); v != "" {
		return v
	}
	if IsPackaged() && runtime.GOOS == "windows" {
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			return filepath.Join(la, "StockAnalyzer")
		}
		home, _ := os.UserHomeDir()
		return filepath.Join(home, "AppData", "Local", "StockAnalyzer")
	}
	return projectRoot()
}

// IsPackaged 打包态判断（Go 无 _MEIPASS 概念，用 env STOCK_PACKAGED=1 标记）
func IsPackaged() bool {
	return os.Getenv("STOCK_PACKAGED") == "1"
}

// projectRoot 返回仓库根（backend/ 的上一级），开发态数据与静态目录所在处。
// 通过可执行文件路径推断不可靠（go run 时在临时目录），用 env STOCK_PROJECT_ROOT 优先。
func projectRoot() string {
	if v := strings.TrimSpace(os.Getenv("STOCK_PROJECT_ROOT")); v != "" {
		return v
	}
	// 回退：当前工作目录向上找 static/ 与 backend/ 并存处
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	dir := wd
	for {
		if _, err := os.Stat(filepath.Join(dir, "static")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "backend")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return wd
		}
		dir = parent
	}
}

// Load 构建默认配置（端口可用环境变量 STOCK_PORT 覆盖）
func Load() *Config {
	appHome := ResolveAppHome()
	root := projectRoot()
	port := DefaultPort
	if v := os.Getenv("STOCK_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n < 65536 {
			port = n
		}
	}
	return &Config{
		AppHome:    appHome,
		DataDir:    filepath.Join(appHome, "data"),
		DBPath:     filepath.Join(appHome, "data", "etf.db"),
		StaticDir:  filepath.Join(root, "static"),
		Port:       port,
		ListenHost: "127.0.0.1",
	}
}

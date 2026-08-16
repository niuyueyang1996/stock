#!/bin/bash
# 安卓模拟器回归验证：启动模拟器（内置 DNS 修复）→ 装 APK → 验证预热/搜索/ws/下载估值。
# 作为单测回归的一环：bash scripts/verify_android.sh
# 依赖：ANDROID_HOME、AVD（默认 test35）、APK 已构建（packaging/android/app/build/outputs/apk/debug/app-debug.apk）
set -e
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
AVD="${AVD:-test35}"
APK="${APK:-$ROOT/packaging/android/app/build/outputs/apk/debug/app-debug.apk}"
export ANDROID_HOME="${ANDROID_HOME:-$HOME/Library/Android/sdk}"
export PATH="$ANDROID_HOME/platform-tools:$ANDROID_HOME/emulator:$PATH"

echo "==== 安卓模拟器回归验证（AVD=$AVD）===="
[ -f "$APK" ] || { echo "错误：APK 不存在 $APK（先跑 packaging/android/build.sh）"; exit 1; }

# ---- 0) 启动模拟器（禁用 netsim 避免 DNS 丢失；无头模式） ----
adb emu kill 2>/dev/null || true
nohup emulator -avd "$AVD" -no-window -no-audio -no-boot-anim \
  -gpu swiftshader_indirect -no-snapshot -memory 3072 -cores 4 \
  -feature -Netsim -dns-server 8.8.8.8 > /tmp/verify_emulator.log 2>&1 &
echo "[0] 等待模拟器开机..."
adb wait-for-device
BOOTED=0
for i in $(seq 1 60); do
  if [ "$(adb shell getprop sys.boot_completed 2>/dev/null | tr -d '\r')" = "1" ]; then BOOTED=1; break; fi
  sleep 10
done
[ "$BOOTED" = "1" ] || { echo "✗ 模拟器开机超时"; exit 1; }
echo "    ✓ 开机完成"

# ---- 1) 修复 DNS（模拟器常见坑：netd 配 10.0.2.3 但 slirp/netsim DNS 服务未监听 →
#      域名解析 no such host、预热列表/指数全挂）。三重保险：
#      a) 启用 WiFi（AndroidWifi 自带 10.0.2.3 DNS）
#      b) 启动参数禁用 netsim + 直连外部 DNS（-feature -Netsim -dns-server 8.8.8.8）
#      c) root + iptables DNAT：全部 UDP:53 转发到 8.8.8.8:53（本机实测有效）
adb shell svc wifi enable >/dev/null 2>&1 || true
sleep 3
adb shell cmd wifi connect-network AndroidWifi open >/dev/null 2>&1 || true
sleep 8
adb root >/dev/null 2>&1 || true
sleep 2
adb wait-for-device
adb shell "iptables -t nat -A OUTPUT -p udp --dport 53 -j DNAT --to-destination 8.8.8.8:53" >/dev/null 2>&1 || true
DNS_OK=$(adb shell "dumpsys connectivity 2>/dev/null | grep -oE 'DnsAddresses: \[[^]]*\]' | head -1" | tr -d '\r')
echo "[1] DNS 配置：$DNS_OK（已加 iptables DNAT → 8.8.8.8:53）"
RESOLVE=$(adb shell "ping -c 1 -W 5 push2delay.eastmoney.com 2>&1 | tail -1" | tr -d '\r')
case "$RESOLVE" in
  *rtt*) echo "    ✓ DNS 解析验证通过" ;;
  *) echo "    ⚠ DNS 解析验证异常（$RESOLVE），继续尝试" ;;
esac

# ---- 2) 安装并启动 App ----
echo "[2] 安装 APK..."
adb install -r "$APK" >/dev/null
adb shell pm clear com.stockanalyzer.app >/dev/null
adb logcat -c
adb shell am start -n com.stockanalyzer.app/.MainActivity >/dev/null
echo "    ✓ 已启动，等待预热（约 90s）..."
sleep 90

FAIL=0
check() { # check <名称> <0/1 成功标记>
  if [ "$2" = "1" ]; then echo "    ✓ $1"; else echo "    ✗ $1"; FAIL=1; fi
}

# ---- 3) 验证：App 存活 / 预热 / 列表 / 指数 / ws / 下载估值 ----
PID=$(adb shell pidof com.stockanalyzer.app | tr -d '\r')
[ -n "$PID" ] && check "App 进程存活 (pid=$PID)" 1 || check "App 进程存活" 0

PREWARM=$(adb logcat -d -s GoServer | grep -c "市场列表就绪" || true)
[ "$PREWARM" -ge 1 ] && check "市场列表预热就绪" 1 || check "市场列表预热就绪" 0
IDX=$(adb logcat -d -s GoServer | grep -c "指数行情 ok" || true)
[ "$IDX" -ge 1 ] && check "指数行情预热 ok" 1 || check "指数行情预热 ok" 0

LISTS=$(adb shell "ls /data/data/com.stockanalyzer.app/files/data/data/stock_list.json 2>/dev/null" | tr -d '\r')
[ -n "$LISTS" ] && check "列表 JSON 落盘" 1 || check "列表 JSON 落盘" 0

SEARCH=$(adb shell "echo -e 'GET /api/stocks/search?q=茅台 HTTP/1.0\r\nHost: x\r\n\r\n' | nc 127.0.0.1 8080 2>/dev/null | tail -1" | tr -d '\r')
echo "$SEARCH" | grep -q "600519" && check "搜索（茅台→600519）" 1 || check "搜索（茅台→600519）" 0

WS=$(adb shell "printf 'GET /ws HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n' | nc 127.0.0.1 8080 2>/dev/null" | head -1 | tr -d '\r')
echo "$WS" | grep -q "101" && check "WebSocket 握手 101" 1 || check "WebSocket 握手 101" 0

LOG=$(adb shell "echo -e 'GET /api/logs?lines=2 HTTP/1.0\r\nHost: x\r\n\r\n' | nc 127.0.0.1 8080 2>/dev/null" | tail -1 | tr -d '\r')
echo "$LOG" | grep -q '"ok":true' && check "日志端点 /api/logs" 1 || check "日志端点 /api/logs" 0

# 下载 + 估值落库（601857：日K+财务+百度序列+分位）
JOB=$(adb shell "printf 'POST /api/stocks/601857/refresh/full HTTP/1.0\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 13\r\n\r\n{\"auto\":true}' | nc 127.0.0.1 8080 2>/dev/null" | tail -1 | tr -d '\r')
echo "$JOB" | grep -q "job_id" && check "单股下载任务已触发" 1 || check "单股下载任务已触发" 0
echo "    等待下载完成（约 120s）..."
sleep 120
DETAIL=$(adb shell "echo -e 'GET /api/stocks/601857 HTTP/1.0\r\nHost: x\r\n\r\n' | nc 127.0.0.1 8080 2>/dev/null" | tail -1 | tr -d '\r')
echo "$DETAIL" | grep -q '"ok":true' && check "601857 详情 200（非 409）" 1 || check "601857 详情 200（非 409）" 0
echo "$DETAIL" | grep -q '"quantiles"' && check "估值分位 quantiles 存在" 1 || check "估值分位 quantiles 存在" 0

adb emu kill >/dev/null 2>&1 || true
echo ""
if [ "$FAIL" = "1" ]; then
  echo "✗✗ 安卓模拟器回归验证失败（详见上方 ✗ 项）"
  exit 1
fi
echo "✅✅ 安卓模拟器回归验证全部通过"

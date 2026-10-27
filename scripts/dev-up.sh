#!/usr/bin/env bash
# 一键起本机开发环境：后端（.env 的 API_PORT，默认 18080）+ 前端（5173）。
#
# 为什么需要它：本仓开发固定 18080，一行命令把后端和前端按同一端口约定起起来，
# 避免手动起时后端用了别的端口、前端代理还指着 18080，
# 表现就是「页面能打开、一登录就说账号或口令不正确」。
#
# 用法：
#   bash scripts/dev-up.sh                          # 前台守着两个进程，Ctrl+C 一起停
#   INDEX_ON_STARTUP=0 bash scripts/dev-up.sh       # 启动时不补索引（省嵌入额度）
#
# 前置：MySQL80 在跑（net start MySQL80）；frontend/node_modules 已安装。

set -uo pipefail
cd "$(dirname "$0")/.."

[ -f .env ] || { echo "❌ 缺少 .env —— 先 cp .env.example .env 并填好" >&2; exit 1; }
set -a
# shellcheck disable=SC1091
. ./.env
set +a

PORT="${API_PORT:-18080}"
NODE="$(command -v node || true)"
if [ -z "$NODE" ] && [ -x "C:/Program Files/nodejs/node.exe" ]; then
  NODE="C:/Program Files/nodejs/node.exe"
fi
[ -n "$NODE" ] || { echo "❌ 找不到 node —— 装 Node 或把它加进 PATH" >&2; exit 1; }

if [ ! -x ./backend/bin/server.exe ]; then
  echo "== 未找到 ./backend/bin/server.exe，先编译"
  ( cd backend && go build -o bin/server.exe ./cmd/server ) || exit 1
fi

mkdir -p .workbuddy/tmp

# 端口先探一下：被占就直接说清楚是谁，别让前端连错后端还以为自己好了。
if curl -s --noproxy '*' -o /dev/null --max-time 2 "http://127.0.0.1:${PORT}/health"; then
  echo "❌ :${PORT} 已被占用 —— 本仓后端可能已经在跑；要换端口改 .env 的 API_PORT" >&2
  exit 1
fi

./backend/bin/server.exe > .workbuddy/tmp/dev-backend.log 2>&1 &
BE=$!
( cd frontend && exec "$NODE" node_modules/vite/bin/vite.js --port 5173 --strictPort --host 127.0.0.1 \
    > ../.workbuddy/tmp/dev-frontend.log 2>&1 ) &
FE=$!

cleanup() {
  trap - INT TERM EXIT
  kill "$BE" "$FE" 2>/dev/null
  wait 2>/dev/null
  echo
  echo "已停止后端(:${PORT}) 与前端(:5173)"
}
trap cleanup INT TERM EXIT

echo "后端 :${PORT}　前端 :5173"
echo "打开 http://127.0.0.1:5173"

ok=0
for _ in $(seq 1 40); do
  b="$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:${PORT}/health" 2>/dev/null)"
  f="$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' --max-time 2 http://127.0.0.1:5173/ 2>/dev/null)"
  if [ "$b" = "200" ] && [ "$f" = "200" ]; then ok=1; break; fi
  sleep 1
done
if [ "$ok" = "1" ]; then
  echo "✅ 两端就绪（日志：.workbuddy/tmp/dev-backend.log / dev-frontend.log）"
else
  echo "⚠️ 40 秒内没等到两端就绪，看日志：.workbuddy/tmp/dev-backend.log / dev-frontend.log" >&2
fi

wait

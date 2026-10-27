#!/usr/bin/env bash
# §8.4 / R8.4 验收：`/health` 不查库。
#
# 要证的三件事：
#   1. DB 不可用时**进程仍存活**，`/health` 仍返回 200；
#   2. 同一进程下 `/api/*` 返回 503（`{"error":"db_unavailable"}`），
#      而不是把「数据库挂了」说成「服务挂了」；
#   3. DB 恢复后业务接口**自动**恢复（不重启进程）。
#
# 三个场景：
#
#   场景 A  DB 从进程启动起就不可达（DB_PORT 指向无人监听的端口）
#   场景 B  DB 在**运行中途**掉线，随后恢复
#           → 用 scripts/tcp-proxy.mjs 做中转：杀掉转发器 = DB 掉线，
#             拉起来 = DB 恢复。比停服务更严格，连池里的长连接都会被真掐断。
#   场景 C  真的 `net stop MySQL80`（需要管理员窗口）
#           → 本机实测非管理员执行 `net stop` 会得到「系统错误 5 / 拒绝访问」，
#             脚本会把这一点明确报出来，不会伪装成通过。
#
# 用法：
#   bash scripts/verify-health-decoupled.sh                 # 场景 A + B（不需要管理员）
#   bash scripts/verify-health-decoupled.sh --stop-service  # 追加场景 C（管理员窗口）
#
# 输出即验收证据：命令与输出一起留档。

set -uo pipefail

export PATH="/usr/bin:/bin:/mingw64/bin:/c/Users/TANGWENJIAN/.local/bin:$PATH"
cd "$(dirname "$0")/.."

if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

# node 路径按候选探测（同 verify-all.sh：写死版本号会随本机升级失效）。
NODE=""
for _cand in $(ls -d C:/Users/TANGWENJIAN/.workbuddy/binaries/node/versions/*/node.exe 2>/dev/null | sort -V -r); do
  [ -x "$_cand" ] && { NODE="$_cand"; break; }
done
[ -n "$NODE" ] || NODE="$(command -v node)"
[ -n "$NODE" ] || { printf '❌ 找不到 node\n'; exit 2; }

PROBE_PORT=8091
PROXY_PORT=3399
DB_GOOD_PORT="${DB_PORT:-3306}"
DB_DEAD_PORT=3307   # 故意指向没人监听的端口

pass=0
fail=0
ok()  { printf '  ✅ %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  ❌ %s\n' "$1"; fail=$((fail+1)); }
say() { printf '\n=== %s\n' "$1"; }

# 落盘用**仓库内相对路径**：本机 curl 是 Windows 原生 exe，对 MSYS 的 /tmp
# 映射不可靠（verify-upload.sh 里踩过同一个坑）。
BODY_FILE=".verify-health-body.txt"
status_of() {
  local url="$1"; shift
  curl -s -o "$BODY_FILE" -w '%{http_code}' --max-time 6 --noproxy '*' "$@" "$url"
}
body_of() { cat "$BODY_FILE" 2>/dev/null; }

TMP_DIR=".verify-health-tmp"
rm -rf "$TMP_DIR"; mkdir -p "$TMP_DIR"

SRV_PID=""
PROXY_PID=""

stop_proxy() {
  if [ -n "$PROXY_PID" ]; then
    kill "$PROXY_PID" 2>/dev/null || true
    sleep 1
    kill -9 "$PROXY_PID" 2>/dev/null || true
    PROXY_PID=""
  fi
}

start_proxy() {
  "$NODE" scripts/tcp-proxy.mjs "$PROXY_PORT" "$DB_GOOD_PORT" > "$TMP_DIR/proxy.log" 2>&1 &
  PROXY_PID=$!
  for _ in $(seq 1 20); do
    if "$NODE" -e "
      const net=require('net');const s=net.connect($PROXY_PORT,'127.0.0.1');
      s.on('connect',()=>{s.destroy();process.exit(0)});
      s.on('error',()=>process.exit(1));
      setTimeout(()=>process.exit(1),800);
    " >/dev/null 2>&1; then return 0; fi
    sleep 0.3
  done
  return 1
}

cleanup() {
  stop_proxy
  if [ -n "$SRV_PID" ]; then
    kill "$SRV_PID" 2>/dev/null || true
    sleep 1
    kill -9 "$SRV_PID" 2>/dev/null || true
  fi
  rm -rf "$TMP_DIR"
  rm -f "$BODY_FILE"
}
trap cleanup EXIT

[ -x ./backend/bin/server.exe ] || { echo "缺 ./backend/bin/server.exe，先 cd backend && go build -o bin/server.exe ./cmd/server" >&2; exit 1; }

# ═══════════════════════════════════════════════════════════════
# 场景 A：DB 从启动起就不可达
# ═══════════════════════════════════════════════════════════════
say "场景 A：另起实例，DB 端口指向 ${DB_DEAD_PORT}（无人监听）"
printf '  主实例（:%s）不动；本场景的探针实例监听 :%s\n' "${API_PORT:-8080}" "$PROBE_PORT"

LOG="$TMP_DIR/probe-a.log"
API_PORT="$PROBE_PORT" DB_PORT="$DB_DEAD_PORT" ./backend/bin/server.exe > "$LOG" 2>&1 &
SRV_PID=$!
printf '  探针实例 PID = %s\n' "$SRV_PID"

say "A-1 启动 6 秒内 /health 就应可应答（不等 DB）"
# 关键观测点：**不能**等建表/种子跑完才监听。
# 修 main.go 之前这里 6 秒后 curl 得到 000（连接被拒）而非 200。
sleep 6
code="$(status_of "http://127.0.0.1:$PROBE_PORT/health")"
printf '     GET /health → %s %s\n' "$code" "$(body_of)"
[ "$code" = "200" ] && ok "/health 返回 200（DB 不可达也没被拖住）" \
  || bad "期望 200，实际 $code —— HTTP 监听被数据库等待阻塞了"
grep -q '"status":"ok"' "$BODY_FILE" 2>/dev/null && ok '/health 响应体为 {"status":"ok"}' || bad "响应体异常：$(body_of)"

say "A-2 同一进程下 /api/* 应返回 503 db_unavailable"
code="$(status_of "http://127.0.0.1:$PROBE_PORT/api/materials")"
printf '     GET /api/materials → %s %s\n' "$code" "$(body_of)"
[ "$code" = "503" ] && ok "/api/materials → 503" || bad "期望 503，实际 $code"
grep -q 'db_unavailable' "$BODY_FILE" 2>/dev/null && ok "响应体标明 db_unavailable" || bad "响应体未标明原因"

code="$(status_of "http://127.0.0.1:$PROBE_PORT/api/me")"
printf '     GET /api/me        → %s %s\n' "$code" "$(body_of)"
[ "$code" = "503" ] && ok "/api/me → 503" || bad "期望 503，实际 $code"

code="$(status_of "http://127.0.0.1:$PROBE_PORT/api/login" -H 'Content-Type: application/json' -d '{"username":"x","password":"y"}')"
printf '     POST /api/login    → %s %s\n' "$code" "$(body_of)"
[ "$code" = "503" ] && ok "/api/login → 503（不假装登录成功）" || bad "期望 503，实际 $code"

say "A-3 进程仍存活：5 秒后 /health 再问一次"
sleep 5
if kill -0 "$SRV_PID" 2>/dev/null; then
  ok "进程 $SRV_PID 仍在（没有因为 DB 连不上退出）"
else
  bad "进程已退出 —— DB 不可用被当成了致命错误"
fi
code="$(status_of "http://127.0.0.1:$PROBE_PORT/health")"
printf '     再问 GET /health → %s %s\n' "$code" "$(body_of)"
[ "$code" = "200" ] && ok "/health 持续 200" || bad "期望 200，实际 $code"

say "A-4 启动日志"
for _ in $(seq 1 25); do
  grep -q '后台重试' "$LOG" && break
  sleep 1
done
sed -n '1,16p' "$LOG" | sed 's/^/     /'
grep -q '数据库暂不可用' "$LOG" && ok "日志明确记下「数据库暂不可用」" || bad "日志未记录 DB 不可用"
grep -q '后台重试' "$LOG" && ok "日志显示后台重试已启动" || bad "未见后台重试日志"
grep -q '/health 已可应答 200' "$LOG" && ok "日志点明存活判定与业务判定的分离" || bad "日志未说明 /health 语义"

kill "$SRV_PID" 2>/dev/null || true
sleep 1
kill -9 "$SRV_PID" 2>/dev/null || true
SRV_PID=""

# ═══════════════════════════════════════════════════════════════
# 场景 B：运行中途掉线 + 恢复
# ═══════════════════════════════════════════════════════════════
say "场景 B：DB 指向转发器 :$PROXY_PORT → :$DB_GOOD_PORT，杀掉转发器模拟掉线"
LOG="$TMP_DIR/probe-b.log"
API_PORT="$PROBE_PORT" DB_PORT="$PROXY_PORT" ./backend/bin/server.exe > "$LOG" 2>&1 &
SRV_PID=$!
printf '  探针实例 PID = %s（DB 走转发器）\n' "$SRV_PID"

say "B-0 转发器未起：应停在 503（等于 DB 不可达）"
sleep 12   # 跨过 WaitReady 的 10 秒宽限
code="$(status_of "http://127.0.0.1:$PROBE_PORT/api/materials")"
printf '     GET /api/materials → %s %s\n' "$code" "$(body_of)"
[ "$code" = "503" ] && ok "转发器未起时业务接口 503" || bad "期望 503，实际 $code"

say "B-1 拉起转发器 → 业务接口应自动恢复（无需重启进程）"
start_proxy && ok "转发器已监听 127.0.0.1:$PROXY_PORT" || bad "转发器启动失败"
recovered=0
for i in $(seq 1 20); do
  sleep 2
  code="$(status_of "http://127.0.0.1:$PROBE_PORT/api/materials")"
  printf '     第 %s 次探测：/api/materials → %s\n' "$i" "$code"
  # 无 Cookie 时鉴权层先答 401 —— 说明 DB 门已经放行
  if [ "$code" = "401" ]; then recovered=1; break; fi
done
[ "$recovered" = "1" ] && ok "DB 可用后业务接口自动恢复（鉴权层已放行）" || bad "40 秒内未恢复"

say "B-2 杀掉转发器 → /health 仍 200，/api/* 回到 503"
stop_proxy
sleep 4   # 探测间隔 2 秒，留两轮余量
code="$(status_of "http://127.0.0.1:$PROBE_PORT/health")"
printf '     GET /health        → %s %s\n' "$code" "$(body_of)"
[ "$code" = "200" ] && ok "DB 掉线时 /health 仍 200" || bad "期望 200，实际 $code"
code="$(status_of "http://127.0.0.1:$PROBE_PORT/api/materials")"
printf '     GET /api/materials → %s %s\n' "$code" "$(body_of)"
[ "$code" = "503" ] && ok "/api/materials → 503（掉线后转 503）" || bad "期望 503，实际 $code"

say "B-3 再次拉起转发器 → 恢复（第二轮回）"
start_proxy >/dev/null 2>&1
recovered2=0
for i in $(seq 1 20); do
  sleep 2
  code="$(status_of "http://127.0.0.1:$PROBE_PORT/api/materials")"
  if [ "$code" = "401" ]; then recovered2=1; printf '     第 %s 次探测 → %s，已恢复\n' "$i" "$code"; break; fi
done
[ "$recovered2" = "1" ] && ok "第二次恢复同样自动完成" || bad "第二次未在 40 秒内恢复"

say "B-4 全程进程未退出"
if kill -0 "$SRV_PID" 2>/dev/null; then
  ok "进程 $SRV_PID 在整个「掉线—恢复—掉线—恢复」过程中一直存活"
else
  bad "进程中途退出"
fi

say "B-5 状态切换日志（只在变化时打印，不刷屏）"
# 日志自第 7 课起是一行一条 JSON，判定仍按 msg 里的中文关键词
grep -E '数据库不可用|数据库可用|后台重试成功|retry_succeeded' "$LOG" | sed 's/^/     /'
# 期望的条数：进程启动时初始状态就是「未就绪」，所以不会为「不可用」打第一条；
# 此后每次**变化**打一条 → 本场景是 可用(1) → 不可用(2) → 可用(3)，
# 即「数据库可用」≥2 条、「数据库不可用」≥1 条。
# 这正是「只在状态变化时打日志」这个设计的直接体现：掉一次线只多一条日志。
n_down="$(grep -c '数据库不可用' "$LOG")"
n_up="$(grep -c '数据库可用' "$LOG")"
printf '     「数据库不可用」%s 条，「数据库可用」%s 条（期望 ≥1 / ≥2）\n' "$n_down" "$n_up"
if [ "$n_down" -ge 1 ] && [ "$n_up" -ge 2 ]; then
  ok "掉线与两次恢复都被探测器记录到，且每次状态变化只打一条（无刷屏）"
else
  bad "状态切换日志不足：down=$n_down up=$n_up"
fi

stop_proxy
kill "$SRV_PID" 2>/dev/null || true
sleep 1
kill -9 "$SRV_PID" 2>/dev/null || true
SRV_PID=""

# ═══════════════════════════════════════════════════════════════
# 场景 C（可选）：真的停掉 MySQL80 服务
# ═══════════════════════════════════════════════════════════════
if [ "${1:-}" = "--stop-service" ]; then
  say "场景 C：对运行中的主实例真的 net stop MySQL80"
  stop_out="$(net stop MySQL80 2>&1)"
  printf '     net stop MySQL80 → %s\n' "$(printf '%s' "$stop_out" | tr -d '\r' | tr '\n' ' ')"
  if printf '%s' "$stop_out" | grep -qE '错误 5|拒绝访问|Access is denied'; then
    bad "没有停服务的权限（系统错误 5）"
    printf '\n  说明：这不是被测代码的缺陷，是执行环境权限不足。\n'
    printf '  场景 A/B 已在**不提权**的前提下覆盖了同一段代码路径；\n'
    printf '  若要以本场景额外取证，请用管理员身份打开终端后重跑：\n'
    printf '      bash scripts/verify-health-decoupled.sh --stop-service\n'
  else
    ok "MySQL80 已停止"
    BASE="http://127.0.0.1:${API_PORT:-8080}"
    sleep 4
    code="$(status_of "$BASE/health")"
    printf '     GET /health        → %s %s\n' "$code" "$(body_of)"
    [ "$code" = "200" ] && ok "/health 仍 200" || bad "期望 200，实际 $code"
    code="$(status_of "$BASE/api/materials")"
    printf '     GET /api/materials → %s %s\n' "$code" "$(body_of)"
    [ "$code" = "503" ] && ok "/api/materials → 503" || bad "期望 503，实际 $code"

    say "场景 C 恢复：net start MySQL80"
    start_out="$(net start MySQL80 2>&1)"
    printf '     net start MySQL80 → %s\n' "$(printf '%s' "$start_out" | tr -d '\r' | tr '\n' ' ')"
    rec=0
    for i in $(seq 1 20); do
      sleep 2
      code="$(status_of "$BASE/api/materials")"
      if [ "$code" = "401" ]; then rec=1; printf '     第 %s 次探测 → %s，已恢复\n' "$i" "$code"; break; fi
    done
    [ "$rec" = "1" ] && ok "DB 恢复后业务接口自动恢复" || bad "40 秒内未恢复"
  fi
else
  printf '\n（场景 C 未执行：需要管理员窗口。加 --stop-service 可启用。）\n'
fi

# ═══════════════════════════════════════════════════════════════
say "汇总"
printf '  通过 %d 项，失败 %d 项\n' "$pass" "$fail"
exit $((fail == 0 ? 0 : 1))

#!/usr/bin/env bash
# 认证相关 Scenario 的端到端验证脚本（add-token-auth 版）。
#
# 依据：openspec/changes/add-token-auth/specs/auth-upload/spec.md
#   R1.1 登录成功签发三段式 JWT，不再 Set-Cookie
#   R1.2 三类失败同形（账号不存在 / 口令错误 / 被限流），且不含 429
#   R1.3 篡改 payload / 篡改签名 / alg=none 的 token 一律 401
#   R1.4 登出 200；已签发 token 在 exp 前仍有效（无状态 JWT 的既定权衡）
#   R1.5 401 带 WWW-Authenticate: Bearer
#   R2.1 /api/me 返回角色与班级（取自服务端用户表）
#   R8.4 /health 不查库仍 200
#   R9.2 未登录访问受保护接口 401 且响应体不含业务数据
#
# 用法：
#   bash scripts/verify-auth.sh
#   CC_USER=teacher_a PASSWORD=... BASE=http://127.0.0.1:18080 bash scripts/verify-auth.sh
#
# 变量名刻意用 CC_USER 而非 USERNAME：Windows 上有同名系统变量
# USERNAME（= 当前登录用户名），会让 `${USERNAME:-默认值}` 的兜底永远失效。
#
# ── 为什么默认要对一个**一次性实例**跑 ──────────────────────────
# 本脚本的 R1.2 会**故意**把账号打到限流锁定期（阈值次失败后锁定
# LOGIN_LOCK_MINUTES 分钟），这是 R1.2 本身要求验证的行为。
# 但限流器在**进程内存**里，于是同一个后端上再跑一次本脚本时，
# teacher_a 还在锁定期内 → R1.1 直接 401 → 脚本自我毒化、不可重跑。
# 实测踩到过：连跑第二次得到「通过 13 项，失败 6 项」，全是锁定期造成的假红。
#
# 修法不是「测试里跳过限流」，而是让它作用在一个用完即弃的进程上：
# 未显式给 BASE 时，脚本自己起一个监听 :8092 的实例（用的是同一套 .env 与同一个库），
# 它的限流器是**空的**，跑完就 kill。这样脚本幂等，也不会污染主实例的限流状态。
# 显式给 BASE 时沿用传入值（用于对着主实例复现）。
#
# 输出即验收证据：命令与输出一起留档。

set -uo pipefail

# 显式前置标准目录：本机 WorkBuddy 的 bash shim 会间歇性丢 PATH，
# 表现为 `line 3: dirname: command not found`（同一条命令重跑通常就好）。
export PATH="/usr/bin:/bin:/mingw64/bin:/c/Users/TANGWENJIAN/.local/bin:$PATH"

# 必须在仓库根下运行，且临时文件一律用**相对路径**。
# 原因：本机 curl 是 Windows 原生 exe，不认 MSYS 的 /tmp 映射 ——
# `-D /tmp/x` 会被它当字面路径写去 C:\tmp，随后 bash 的 cat/grep 又找不到，
# 表现为登录请求直接 000 失败。相对路径对两边都可解析。
cd "$(dirname "$0")/.."

if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

USERNAME="${CC_USER:-teacher_a}"
# 口令默认取 .env 里的种子口令 —— 上面已经 source 过 .env。
# 原来强制外部传 PASSWORD，等于每跑一次都要去 .env 里抄一遍（实测很烦）。
PASSWORD="${PASSWORD:-${SEED_TEACHER_PASSWORD:?缺口令：.env 里没有 SEED_TEACHER_PASSWORD，也没传 PASSWORD}}"
LIMIT="${LOGIN_FAIL_THRESHOLD:-5}"

TMP=".verify-tmp"
rm -rf "$TMP"
mkdir -p "$TMP"

PROBE_PID=""
cleanup() {
  if [ -n "$PROBE_PID" ]; then
    kill "$PROBE_PID" 2>/dev/null || true
    sleep 1
    kill -9 "$PROBE_PID" 2>/dev/null || true
  fi
  rm -rf "$TMP"
}
trap cleanup EXIT

if [ -n "${BASE:-}" ]; then
  printf '对抗实例（BASE 由外部指定）：%s\n' "$BASE"
else
  ISO_PORT="${CC_ISOLATED_PORT:-8092}"
  if [ ! -x ./backend/bin/server.exe ]; then
    printf '❌ 缺 ./backend/bin/server.exe，先：cd backend && go build -o bin/server.exe ./cmd/server\n' >&2
    exit 1
  fi
  API_PORT="$ISO_PORT" ./backend/bin/server.exe > "$TMP/isolated.log" 2>&1 &
  PROBE_PID=$!
  BASE="http://127.0.0.1:$ISO_PORT"
  printf '一次性实例：%s（PID %s，限流器为空）\n' "$BASE" "$PROBE_PID"
  ready=0
  for _ in $(seq 1 30); do
    if [ "$(curl -s -o "$TMP/probe.txt" -w '%{http_code}' --max-time 2 "$BASE/health")" = "200" ]; then
      ready=1; break
    fi
    sleep 0.5
  done
  if [ "$ready" != "1" ]; then
    printf '❌ 一次性实例未在 15 秒内就绪，日志：\n' >&2
    sed -n '1,20p' "$TMP/isolated.log" >&2
    exit 1
  fi
  printf '就绪。\n'
fi

pass=0
fail=0

say()  { printf '\n=== %s\n' "$1"; }
ok()   { printf '  ✅ %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  ❌ %s\n' "$1"; fail=$((fail+1)); }

# 毫秒级计时：用于给「等价耗时」一条定量证据
timed() {
  local out
  out=$("$@" -o /dev/null -s -w '%{http_code} %{time_total}')
  echo "$out"
}

# 从登录响应体提取 token 字段（无 jq 依赖，纯 grep）
token_of() { # $1=响应体
  printf '%s' "$1" | grep -o '"access_token":"[^"]*"' | head -1 | cut -d'"' -f4
}

# b64url <json>：JSON → base64url（无填充）。GNU base64 → URL 安全字母表。
# ⚠️ tr 的集合参数以 '-' 开头会被当成选项，必须用 `--` 隔开（实测踩过）。
b64url() {
  printf '%s' "$1" | base64 -w0 | tr -- '+/' '-_' | tr -d '='
}

# ── R8.4 /health 不查库 ────────────────────────────────────────
say "R8.4 GET /health 应 200 且不查库"
read -r code t < <(timed curl "$BASE/health")
[ "$code" = "200" ] && ok "状态码 200（$t s）" || bad "期望 200，实际 $code"
curl -s "$BASE/health" | sed 's/^/     body: /'

# ── R9.2 未登录访问受保护接口 + R1.5 challenge ────────────────
say "R9.2 未登录访问 GET /api/me 应 401（无 Authorization 头）"
me_headers="$TMP/me-headers.txt"
me_body="$(curl -s -D "$me_headers" -w '\n%{http_code}' "$BASE/api/me")"
me_code="$(printf '%s' "$me_body" | tail -1)"
me_json="$(printf '%s' "$me_body" | head -n -1)"
[ "$me_code" = "401" ] && ok "状态码 401" || bad "期望 401，实际 $me_code"
echo "     body: $me_json"

# 响应体不得含任何材料数据
if printf '%s' "$me_json" | grep -qiE '公约|班级|role|class_id|uploads|\.md'; then
  bad "401 响应体含业务/路径信息：$me_json"
else
  ok "401 响应体不含材料标题、正文、角色字段或磁盘路径"
fi

say "R1.5 401 应带 WWW-Authenticate: Bearer"
www_auth="$(grep -i '^www-authenticate:' "$me_headers" | tr -d '\r')"
echo "     $www_auth"
printf '%s' "$www_auth" | grep -qi 'bearer' && ok "带 Bearer challenge（关闭 RFC 9110 偏差）" \
  || bad "缺 WWW-Authenticate: Bearer"

# ── R1.1 登录成功：签发 JWT ───────────────────────────────────
say "R1.1 POST /api/login 应 200 且响应体含三段式 JWT、无 Set-Cookie"
login_headers="$TMP/login-headers.txt"
login_resp="$(curl -s -D "$login_headers" \
  -H 'Content-Type: application/json' \
  -d "{\"username\":\"$USERNAME\",\"password\":\"$PASSWORD\"}" \
  -w '\n%{http_code}' "$BASE/api/login")"
login_code="$(printf '%s' "$login_resp" | tail -1)"
login_json="$(printf '%s' "$login_resp" | head -n -1)"
[ "$login_code" = "200" ] && ok "状态码 200" || bad "期望 200，实际 $login_code"
echo "     body: $(printf '%s' "$login_json" | head -c 120)…"

TOKEN="$(token_of "$login_json")"
if [ -n "$TOKEN" ]; then
  ok "响应体含 token 字段（长度 ${#TOKEN}）"
else
  bad "响应体缺 token 字段：$login_json"
fi

seg_count="$(printf '%s' "$TOKEN" | awk -F'.' '{print NF}')"
[ "$seg_count" = "3" ] && ok "token 为三段式（header.payload.signature）" \
  || bad "token 段数 = ${seg_count:-0}，期望 3"

# base64url → 标准 base64 → 补 padding → 解码（GNU base64 -d 不认 URL 安全字母表）
jwt_header_decoded="$(printf '%s' "$TOKEN" | cut -d. -f1 | tr -- '-_' '+/' | awk '{while(length($0)%4) $0=$0"="; print}' | base64 -d 2>/dev/null)"
echo "     header: $jwt_header_decoded"
printf '%s' "$jwt_header_decoded" | grep -q '"alg":"HS256"' \
  && ok "header 声明 alg=HS256" || bad "header 缺 alg:HS256：$jwt_header_decoded"

if grep -qi '^set-cookie:' "$login_headers"; then
  bad "登录响应带了 Set-Cookie —— token 方案不应再下发 Cookie"
else
  ok "无 Set-Cookie（凭据只在响应体，由前端保存）"
fi

printf '%s' "$login_json" | grep -q '"token_type":"bearer"' \
  && ok "token_type = bearer" || bad "缺 token_type:bearer"

# ── R2.1 /api/me（Bearer）────────────────────────────────────
say "R2.1 GET /api/me 带 Bearer 应返回角色与班级（取自服务端用户表）"
me_after="$(curl -s -H "Authorization: Bearer $TOKEN" -w '\n%{http_code}' "$BASE/api/me")"
me_after_code="$(printf '%s' "$me_after" | tail -1)"
me_after_json="$(printf '%s' "$me_after" | head -n -1)"
[ "$me_after_code" = "200" ] && ok "状态码 200" || bad "期望 200，实际 $me_after_code"
echo "     body: $me_after_json"
printf '%s' "$me_after_json" | grep -q '"role"'     && ok "含 role"     || bad "缺 role"
printf '%s' "$me_after_json" | grep -q '"class_id"' && ok "含 class_id" || bad "缺 class_id"

# ── R1.3 篡改的 token 一律 401 ────────────────────────────────
say "R1.3 篡改 payload / 篡改签名 / alg=none 的 token 应全部 401"

HDR="$(printf '%s' "$TOKEN" | cut -d. -f1)"
SIG="$(printf '%s' "$TOKEN" | cut -d. -f3)"

# ① 改 payload：user_id 改成 999（重编码但保留原签名 → 验签必须失败）
evil_payload="$(b64url '{"user_id":999,"iat":1700000000,"exp":1900000000}')"
c1="$(curl -s -o "$TMP/b1.txt" -w '%{http_code}' -H "Authorization: Bearer $HDR.$evil_payload.$SIG" "$BASE/api/me")"
[ "$c1" = "401" ] && ok "篡改 payload（user_id=999）→ 401" || bad "期望 401，实际 $c1"

# ② 改签名：最后一字符替换为字母表内的另一个字符
alt='A'; [ "${SIG: -1}" = "A" ] && alt='B'
c2="$(curl -s -o "$TMP/b2.txt" -w '%{http_code}' -H "Authorization: Bearer $HDR.$(printf '%s' "$TOKEN" | cut -d. -f2).${SIG%?}$alt" "$BASE/api/me")"
[ "$c2" = "401" ] && ok "篡改签名 → 401" || bad "期望 401，实际 $c2"

# ③ alg=none：结构合法但声明无算法（防算法混淆）
none_header="$(b64url '{"alg":"none","typ":"JWT"}')"
c3="$(curl -s -o "$TMP/b3.txt" -w '%{http_code}' -H "Authorization: Bearer $none_header.$evil_payload." "$BASE/api/me")"
[ "$c3" = "401" ] && ok "alg=none → 401（算法白名单生效）" || bad "期望 401，实际 $c3"

# ── R1.4 登出：无状态语义 ─────────────────────────────────────
say "R1.4 登出应 200；已签发 token 在 exp 前仍有效（design D2 既定权衡）"
lo_code="$(curl -s -o "$TMP/lo.txt" -w '%{http_code}' -X POST -H "Authorization: Bearer $TOKEN" "$BASE/api/logout")"
[ "$lo_code" = "200" ] && ok "POST /api/logout → 200" || bad "期望 200，实际 $lo_code"
printf '     body: %s\n' "$(cat "$TMP/lo.txt")"

still="$(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" "$BASE/api/me")"
[ "$still" = "200" ] && ok "登出后旧 token 仍有效（200）—— 无状态 JWT 无服务端吊销，已写进 spec" \
  || bad "旧 token 得 $still —— 若实现了服务端吊销请同步改 spec R1.4 与本脚本"

# ── R1.2 三类失败同形 ─────────────────────────────────────────
say "R1.2 账号不存在 / 口令错误 / 被限流 三者响应同形，且不含 429"
# ⚠️ 本节会把 $USERNAME 打到限流锁定期（这是 R1.2 本身要验的行为）。
# 默认模式下锁落在那台**一次性实例**的内存限流器上，脚本结束即随进程消失，
# 主实例不受影响、脚本可反复跑。若你显式传了 BASE 对着主实例跑，
# 请知悉该账号会在 LOGIN_LOCK_MINUTES 内被锁定。

# 结果通过全局变量 PROBE_RESULT 返回，**不用命令替换**。
# 命令替换会连 stdout 里的人类可读日志一起捕获，标签不同会让
# 「三者是否同形」的比较永远为假 —— 那是脚本的假失败，不是实现的问题。
PROBE_RESULT=""
probe() { # $1=标签 $2=用户名 $3=口令
  local out code_only body_time body
  out="$(curl -s -o "$TMP/b.txt" -w '%{http_code} %{time_total}' \
        -H 'Content-Type: application/json' \
        -d "{\"username\":\"$2\",\"password\":\"$3\"}" "$BASE/api/login")"
  read -r code_only body_time <<<"$out"
  body="$(cat "$TMP/b.txt")"
  printf '     %-16s → %s  %ss  %s\n' "$1" "$code_only" "$body_time" "$body"
  PROBE_RESULT="$code_only|$body"
}

probe "账号不存在" "no_such_user_zzz" "whatever"
r_nouser="$PROBE_RESULT"
probe "口令错误" "$USERNAME" "definitely-wrong"
r_wrongpass="$PROBE_RESULT"

if [ "$r_nouser" = "$r_wrongpass" ]; then
  ok "两者状态码与响应体完全一致（$r_nouser）"
else
  bad "两者不一致：$r_nouser  vs  $r_wrongpass"
fi

# 触发限流：连打 LIMIT 次
for _ in $(seq 1 "$LIMIT"); do
  curl -s -o /dev/null -H 'Content-Type: application/json' \
    -d "{\"username\":\"$USERNAME\",\"password\":\"definitely-wrong\"}" "$BASE/api/login"
done
probe "被限流(正确口令)" "$USERNAME" "$PASSWORD"
r_locked="$PROBE_RESULT"

if [ "$r_locked" = "$r_nouser" ]; then
  ok "被限流时的响应与凭据错误完全一致（未使用 429）"
else
  bad "被限流时响应不同：$r_locked —— 这会暴露账号处于锁定状态"
fi
printf '%s' "$r_locked" | grep -q '^429' && bad "用了 429，违反 R1.2" || ok "状态码不是 429"

# ── 汇总 ──────────────────────────────────────────────────────
say "汇总"
printf '  通过 %d 项，失败 %d 项\n' "$pass" "$fail"
[ "$fail" -eq 0 ] || exit 1

#!/usr/bin/env bash
# 班级隔离（R5）Scenario 的端到端验证脚本。
#
# 依据：openspec/changes/add-auth-rbac-class-knowledge/specs/auth-upload/spec.md
#   R5.1 列表按会话班级过滤（含：用别班标题搜索，结果为空）
#   R5.2 跨班按 ID 访问被拒（详情与文件），且与「ID 不存在」**完全同形 404**
#   R5.3 详情不泄漏磁盘存储路径
#   R9.2 未登录访问受保护接口 401 且响应体不含材料数据
#
# 用法：
#   bash scripts/verify-isolation.sh
#   （口令从 .env 读取；也可用 SEED_TEACHER_PASSWORD / SEED_STUDENT_PASSWORD 覆盖）
#
# 设计要点：**不硬编码材料 ID**。B 班材料 ID 由 student_b1 自己查出来，
# 这样脚本不依赖自增顺序，也不需要在脚本里写死种子数据的形态。
#
# 输出即验收证据：命令与输出一起留档。

set -uo pipefail

# 临时文件一律用仓库内相对路径 —— 本机 curl 是 Windows 原生 exe，
# 不认 MSYS 的 /tmp 映射（详见 verify-auth.sh 的注释）。
cd "$(dirname "$0")/.."

if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

BASE="${BASE:-http://127.0.0.1:${API_PORT:-18080}}"
TEACHER_PASS="${SEED_TEACHER_PASSWORD:?缺 SEED_TEACHER_PASSWORD（应来自 .env）}"
STUDENT_PASS="${SEED_STUDENT_PASSWORD:?缺 SEED_STUDENT_PASSWORD（应来自 .env）}"

TMP=".verify-iso-tmp"
rm -rf "$TMP"
mkdir -p "$TMP"
pass=0
fail=0

say() { printf '\n=== %s\n' "$1"; }
ok()  { printf '  ✅ %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  ❌ %s\n' "$1"; fail=$((fail+1)); }

# login <用户名> <口令> <token文件>：登录并把 JWT 存进文件（add-token-auth）
login() {
  curl -s -H 'Content-Type: application/json' \
    -d "{\"username\":\"$1\",\"password\":\"$2\"}" "$BASE/api/login" \
    | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4 > "$3"
}

# code_body <url> [额外 curl 参数...]：输出「状态码\t响应体」，结果放全局变量
# 不用命令替换返回，避免把人类可读日志一起捕获（verify-auth.sh 里的坑）
CB_RESULT=""
code_body() {
  local url="$1"; shift
  local code
  code="$(curl -s -o "$TMP/body.txt" -w '%{http_code}' "$@" "$url")"
  CB_RESULT="$code|$(cat "$TMP/body.txt")"
}

# ── R9.2 未登录 ───────────────────────────────────────────────
say "R9.2 未登录访问 GET /api/materials 应 401，且响应体不含材料数据"
code_body "$BASE/api/materials"
printf '     %s\n' "$CB_RESULT"
case "$CB_RESULT" in
  401\|*) ok "状态码 401" ;;
  *)      bad "期望 401，实际 ${CB_RESULT%%|*}" ;;
esac
printf '%s' "$CB_RESULT" | grep -qiE '公约|uploads|seed-class' \
  && bad "401 响应体含材料数据或磁盘路径" \
  || ok "401 响应体不含标题、正文或磁盘路径"

# ── 取 token ──────────────────────────────────────────────────
say "准备：登录 A 班教师、A 班学生、B 班学生"
JA="$TMP/ja.txt"; JB="$TMP/jb.txt"; JSA="$TMP/jsa.txt"
rm -f "$JA" "$JB" "$JSA"
login teacher_a  "$TEACHER_PASS" "$JA"
login student_b1 "$STUDENT_PASS" "$JB"
login student_a1 "$STUDENT_PASS" "$JSA"
for f in "$JA" "$JB" "$JSA"; do
  if [ -s "$f" ]; then
    printf '  ✅ %s 已取得 token（%s 字符）\n' "$f" "$(wc -c < "$f" | tr -d ' ')"
    pass=$((pass+1))
  else
    printf '  ❌ %s 未取得 token\n' "$f"; fail=$((fail+1))
  fi
done

# ── R5.1 列表按班级过滤 ───────────────────────────────────────
say "R5.1 A 班教师的列表应只含本班材料"
list_a="$(curl -s -H "Authorization: Bearer $(cat "$JA")" "$BASE/api/materials")"
echo "     $list_a" | head -c 400; echo
ids_a="$(printf '%s' "$list_a" | grep -o '"id":[0-9]*' | cut -d: -f2 | tr '\n' ' ')"
count_a="$(printf '%s' "$list_a" | grep -o '"count":[0-9]*' | cut -d: -f2)"
printf '     A 班可见材料 ID：%s\n' "$ids_a"

list_b="$(curl -s -H "Authorization: Bearer $(cat "$JB")" "$BASE/api/materials")"
ids_b="$(printf '%s' "$list_b" | grep -o '"id":[0-9]*' | cut -d: -f2 | tr '\n' ' ')"
printf '     B 班可见材料 ID：%s\n' "$ids_b"

if [ -n "$count_a" ] && [ "$count_a" -ge 1 ]; then
  ok "A 班列表非空（count=$count_a），数据来自数据库查询"
else
  bad "A 班列表为空或未返回 count —— 隔离结果无从判定"
fi

# 集合交集必须为空：这是「隔离」最直接的机器判据
overlap=""
for i in $ids_a; do
  for j in $ids_b; do
    [ "$i" = "$j" ] && overlap="$overlap $i"
  done
done
if [ -z "$overlap" ]; then
  ok "两班可见 ID 集合交集为空（A ∩ B = ∅）"
else
  bad "存在两班都可见的材料 ID：$overlap"
fi

# 跨班 ID：取 B 班第一条，且确认它不在 A 班列表里
B_ID="$(printf '%s' "$ids_b" | awk '{print $1}')"
if [ -z "$B_ID" ]; then
  bad "未能从 B 班列表取到材料 ID，后续跨班验证无法进行"
else
  printf '     选用 B 班材料 ID = %s（用于跨班验证）\n' "$B_ID"
fi

say "R5.1+ 在 A 班列表里用 B 班材料的标题搜索，结果应为空"
# 注意：响应体会回显 query 字段，所以判据看 count，不看「是否出现标题字样」
# 搜索词先落文件（UTF-8），用 --data-urlencode q@file 上送 ——
# 中文直接写进 curl 的 argv 会被 Windows ANSI 代码页转成 GBK 乱码，
# 服务端解出 U+FFFD：期望 0 的一侧会「假通过」、期望命中的一侧必红
#（2026-10-07 实测踩过，详见验收报告）。
printf '%s' 'B班-班级公约' > "$TMP/q.txt"
search_a="$(curl -s -G --data-urlencode "q@$TMP/q.txt" -H "Authorization: Bearer $(cat "$JA")" "$BASE/api/materials")"
echo "     $search_a" | head -c 300; echo
search_count="$(printf '%s' "$search_a" | grep -o '"count":[0-9]*' | cut -d: -f2)"
if [ "$search_count" = "0" ]; then
  ok "搜索结果 count=0（搜索条件与班级条件是 AND 关系）"
else
  bad "搜索结果 count=$search_count —— 搜索越过了班级边界"
fi

say "R5.1++ 在 B 班列表里用同一关键词搜索，结果应命中（证明搜索本身有效，不是恒空）"
search_b="$(curl -s -G --data-urlencode "q@$TMP/q.txt" -H "Authorization: Bearer $(cat "$JB")" "$BASE/api/materials")"
search_b_count="$(printf '%s' "$search_b" | grep -o '"count":[0-9]*' | cut -d: -f2)"
echo "     B 班搜索 count=$search_b_count"
if [ -n "$search_b_count" ] && [ "$search_b_count" -ge 1 ]; then
  ok "B 班同关键词命中 $search_b_count 条 —— 排除「搜索被写成恒不匹配」的假通过"
else
  bad "B 班搜索也为空（count=$search_b_count）—— 搜索实现可能有问题"
fi

# ── R5.2 跨班按 ID 访问 ───────────────────────────────────────
say "R5.2 A 班教师请求 B 班材料的详情，应 404 且不泄漏 B 班信息"
if [ -n "$B_ID" ]; then
  code_body "$BASE/api/materials/$B_ID" -H "Authorization: Bearer $(cat "$JA")"
  cross_detail="$CB_RESULT"
  printf '     跨班详情 → %s\n' "$cross_detail"
  case "$cross_detail" in
    404\|*) ok "状态码 404" ;;
    *)      bad "期望 404，实际 ${cross_detail%%|*}" ;;
  esac
  printf '%s' "$cross_detail" | grep -q 'B班' \
    && bad "响应体泄漏了 B 班材料标题" || ok "响应体不含 B 班标题"
  printf '%s' "$cross_detail" | grep -q 'seed-class' \
    && bad "响应体泄漏了磁盘存储名" || ok "响应体不含磁盘存储名"

  say "R5.2 A 班教师请求 B 班材料的文件，应 404"
  code_body "$BASE/api/materials/$B_ID/file" -H "Authorization: Bearer $(cat "$JA")"
  printf '     跨班文件 → %s\n' "$CB_RESULT"
  case "$CB_RESULT" in
    404\|*) ok "状态码 404" ;;
    *)      bad "期望 404，实际 ${CB_RESULT%%|*}" ;;
  esac
fi

say "R5.2+ 不存在的 ID 应与跨班返回**完全同形**（关闭枚举信道）"
code_body "$BASE/api/materials/99999999" -H "Authorization: Bearer $(cat "$JA")"
ghost="$CB_RESULT"
printf '     不存在 ID  → %s\n' "$ghost"
if [ -n "$B_ID" ] && [ "$ghost" = "$cross_detail" ]; then
  ok "两次 404 逐字节相同：$ghost"
else
  bad "两者不同形 —— 跨班 vs 不存在可被区分。跨班=$cross_detail 不存在=$ghost"
fi

say "R5.2++ ID 形态非法（非数字）也走同一响应"
code_body "$BASE/api/materials/abc" -H "Authorization: Bearer $(cat "$JA")"
printf '     非法 ID    → %s\n' "$CB_RESULT"
[ "$CB_RESULT" = "$ghost" ] && ok "非数字 ID 与不存在 ID 同形" || bad "非数字 ID 响应不同：$CB_RESULT"

# ── R5.3 详情不泄漏磁盘路径 ───────────────────────────────────
say "R5.3 本班材料详情应 200，含正文，且不含磁盘路径"
A_ID="$(printf '%s' "$ids_a" | awk '{print $1}')"
if [ -n "$A_ID" ]; then
  code_body "$BASE/api/materials/$A_ID" -H "Authorization: Bearer $(cat "$JA")"
  own_detail="$CB_RESULT"
  printf '     %s\n' "$own_detail"
  case "$own_detail" in
    200\|*) ok "状态码 200" ;;
    *)      bad "期望 200，实际 ${own_detail%%|*}" ;;
  esac
  printf '%s' "$own_detail" | grep -q '"body"'   && ok "返回知识库正文（body）" || bad "缺 body"
  printf '%s' "$own_detail" | grep -q '"title"'  && ok "返回标题"               || bad "缺 title"
  if printf '%s' "$own_detail" | grep -qiE 'uploads|stored_name|D:/|D:\\\\|seed-class'; then
    bad "响应体含磁盘路径或存储名：$own_detail"
  else
    ok "响应体不含 stored_name、uploads 或任何磁盘路径"
  fi

  say "本班文件应可下载（步 4 的下载走鉴权 API，/uploads 不做静态暴露）"
  dl_headers="$TMP/dl-headers.txt"
  dl_code="$(curl -s -H "Authorization: Bearer $(cat "$JA")" -D "$dl_headers" -o "$TMP/dl.bin" -w '%{http_code}' \
    "$BASE/api/materials/$A_ID/file")"
  printf '     下载 → %s（%s 字节）\n' "$dl_code" "$(wc -c < "$TMP/dl.bin" | tr -d ' ')"
  [ "$dl_code" = "200" ] && ok "状态码 200" || bad "期望 200，实际 $dl_code"
  grep -i 'content-disposition' "$dl_headers" | tr -d '\r' | sed 's/^/     /'
  grep -qi 'attachment' "$dl_headers" && ok "强制下载（Content-Disposition: attachment）" \
    || bad "未强制下载 —— 材料正文可能被浏览器当页面渲染"
  grep -i 'x-content-type-options' "$dl_headers" | tr -d '\r' | sed 's/^/     /'
  grep -qi 'nosniff' "$dl_headers" && ok "带 X-Content-Type-Options: nosniff" || bad "缺 nosniff"
fi

say "R5.3+ 未登录直接猜测文件下载 URL（/uploads 静态暴露检查）"
static_code="$(curl -s -o /dev/null -w '%{http_code}' \
  "$BASE/uploads/seed-class-a-convention.md")"
printf '     GET /uploads/seed-class-a-convention.md → %s\n' "$static_code"
[ "$static_code" = "404" ] && ok "静态路径不存在（文件只能经鉴权 API 读出）" \
  || bad "静态路径返回 $static_code —— 上传目录疑似被暴露"

# ── R3 学生只读 ───────────────────────────────────────────────
say "R3 A 班学生可查看本班材料（只读），但看不到 B 班"
code_body "$BASE/api/materials" -H "Authorization: Bearer $(cat "$JSA")"
printf '     %s\n' "$CB_RESULT"
case "$CB_RESULT" in
  200\|*) ok "学生会话可读取列表（200）" ;;
  *)      bad "期望 200，实际 ${CB_RESULT%%|*}" ;;
esac
stu_ids="$(printf '%s' "$CB_RESULT" | grep -o '"id":[0-9]*' | cut -d: -f2 | tr '\n' ' ')"
printf '     学生可见材料 ID：%s\n' "$stu_ids"
if [ -n "$B_ID" ] && printf '%s' "$stu_ids" | grep -qw "$B_ID"; then
  bad "学生列表里出现 B 班材料 ID $B_ID"
else
  ok "学生列表不含 B 班材料 ID"
fi

# ── 汇总 ──────────────────────────────────────────────────────
say "汇总"
printf '  通过 %d 项，失败 %d 项\n' "$pass" "$fail"
rm -rf "$TMP"
[ "$fail" -eq 0 ] || exit 1

#!/usr/bin/env bash
# 上传入库（R3、R4）Scenario 的端到端验证脚本。
#
# 依据：openspec/changes/add-auth-rbac-class-knowledge/specs/auth-upload/spec.md
#   R3.1  学生上传被拒绝（403，且两表与目录均无变化）
#   R3.2  角色判定在服务端（绕过界面直接调接口仍 403）
#   R4.1  上传成功两表同增，class_id = 会话班级
#   R4.2  扩展名白名单 .txt / .md
#   R4.3  超限在读完请求体之前拒绝（413）
#   R4.4  非空且合法 UTF-8
#   R4.5  客户端文件名不参与路径构造
#   R4.6  入库失败无残留（本脚本用「非法输入不落盘」间接覆盖）
#   R4.7  表单里的 class_id 被忽略
#   R9.2  未登录上传应 401
#
# 用法：
#   bash scripts/verify-upload.sh
#   （口令从 .env 读取；也可用 SEED_TEACHER_PASSWORD / SEED_STUDENT_PASSWORD 覆盖）
#
# 输出即验收证据：命令与输出一起留档。

set -uo pipefail

# 先修 PATH：本机 WorkBuddy 的 bash shim 会间歇性丢失 PATH，
# 表现为 `line 3: dirname: command not found`（同一条命令重跑通常就好）。
# 显式前置标准目录比「重跑碰运气」可靠。
export PATH="/usr/bin:/bin:/mingw64/bin:/c/Users/TANGWENJIAN/.local/bin:$PATH"

# 临时文件一律用仓库内相对路径 —— 本机 curl 是 Windows 原生 exe，
# 不认 MSYS 的 /tmp 映射。
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
UPLOAD_DIR="${UPLOAD_DIR:?缺 UPLOAD_DIR（应来自 .env）}"
MAX_BYTES="${MAX_UPLOAD_BYTES:?缺 MAX_UPLOAD_BYTES（应来自 .env）}"
# MySQL 客户端路径按候选探测：这台机器实际装在 C:/Program Files 下，
# 写死 D:/MySQL 会让所有「库内行数」断言拿到空值（实测 2026-10-27）。
if [ -z "${MYSQL:-}" ]; then
  for cand in \
    "C:/Program Files/MySQL/MySQL Server 8.0/bin/mysql.exe" \
    "D:/MySQL/MySQL Server 8.0/bin/mysql.exe" \
    "$(command -v mysql 2>/dev/null)"; do
    [ -n "$cand" ] && [ -x "$cand" ] && { MYSQL="$cand"; break; }
  done
fi
MYSQL="${MYSQL:?找不到 mysql 客户端（可用 MYSQL=... 指定）}"

TMP=".verify-upload-tmp"
rm -rf "$TMP"
mkdir -p "$TMP"
pass=0
fail=0
# 断言 201 成功的次数。汇总时用它跟实测行数变化对账，避免报一个凑出来的数。
UPLOADED=0

say() { printf '\n=== %s\n' "$1"; }
ok()  { printf '  ✅ %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  ❌ %s\n' "$1"; fail=$((fail+1)); }

# login <用户名> <口令> <token文件>：登录并把 JWT 存进文件（add-token-auth）
login() {
  curl -s -H 'Content-Type: application/json' \
    -d "{\"username\":\"$1\",\"password\":\"$2\"}" "$BASE/api/login" \
    | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4 > "$3"
}

# code_body <url> [额外 curl 参数...]：结果放全局变量，返回「状态码|响应体」
CB_RESULT=""
code_body() {
  local url="$1"; shift
  local code
  code="$(curl -s -o "$TMP/body.txt" -w '%{http_code}' "$@" "$url")"
  CB_RESULT="$code|$(cat "$TMP/body.txt")"
}

# sqlq <SQL>：无表头输出，供机器判定
#
# ⚠️ `--default-character-set=utf8mb4` 不可省。本机 MySQL 客户端默认跟随系统代码页
# （CP936/GBK），服务端会把 utf8mb4 转码成 GBK 再返回 —— 实测「这是一份…」
# 取回的是 GBK 字节 `D5 E2 CA C7`，与 UTF-8 的 grep 模式永不匹配，
# 表现为「库里明明是对的、脚本却报不一致」。显式声明后取回 `E8 BF 99`（UTF-8）。
sqlq() { "$MYSQL" -uroot -ppassword --default-character-set=utf8mb4 -N -B -e "USE campusclaw; $1" 2>/dev/null; }

count_rows()  { sqlq "SELECT COUNT(*) FROM $1;"; }
count_files() { ls -1 "$UPLOAD_DIR" 2>/dev/null | wc -l | tr -d ' '; }

# upload <jar> <文件名> [额外 -F 参数...]：结果放 CB_RESULT
upload() {
  local jar="$1" file="$2"; shift 2
  code_body "$BASE/api/materials" -H "Authorization: Bearer $(cat "$jar")" -F "file=@$file" "$@"
}

say "准备：造测试文件、取会话"
printf '这是一份上传验证材料。\n' > "$TMP/ok.md"
printf '纯文本材料\n'                > "$TMP/ok.txt"
printf '可执行内容\n'                > "$TMP/bad.exe"
printf '\377\376\000\001'            > "$TMP/bad-utf8.md"
: > "$TMP/empty.md"
printf '穿越测试\n'                  > "$TMP/traversal.md"
# 带 UTF-8 BOM（EF BB BF）—— Windows 记事本存 UTF-8 的默认形态
printf '\357\273\277# 带 BOM 的标题\n\n正文内容。\n' > "$TMP/bom.md"
head -c $((MAX_BYTES + 1048576)) /dev/zero | tr '\0' 'x' > "$TMP/too-big.md"

JT="$TMP/teacher.txt"; JS="$TMP/student.txt"
rm -f "$JT" "$JS"
login teacher_a  "$TEACHER_PASS" "$JT"
login student_a1 "$STUDENT_PASS" "$JS"
for f in "$JT" "$JS"; do
  if [ -s "$f" ]; then
    printf '  ✅ 取得 token：%s（%s 字符）\n' "$f" "$(wc -c < "$f" | tr -d ' ')"
    pass=$((pass+1))
  else
    printf '  ❌ 未取得 token：%s\n' "$f"; fail=$((fail+1))
  fi
done
printf '     上传上限 MAX_UPLOAD_BYTES = %s；目录现有文件 %s 个\n' "$MAX_BYTES" "$(count_files)"

# ── R9.2 未登录 ───────────────────────────────────────────────
say "R9.2 未登录 POST /api/materials 应 401"
code_body "$BASE/api/materials" -F "file=@$TMP/ok.md"
printf '     %s\n' "$CB_RESULT"
case "$CB_RESULT" in
  401\|*) ok "状态码 401" ;;
  *)      bad "期望 401，实际 ${CB_RESULT%%|*}" ;;
esac

# ── R3.1 / R3.2 学生不能上传 ──────────────────────────────────
say "R3.1/R3.2 学生会话上传（格式完全合法）应 403，且两表与目录均无变化"
m0="$(count_rows materials)"; k0="$(count_rows knowledge_entries)"; f0="$(count_files)"
printf '     前置基线：materials=%s knowledge_entries=%s 目录文件=%s\n' "$m0" "$k0" "$f0"
upload "$JS" "$TMP/ok.md"
printf '     %s\n' "$CB_RESULT"
case "$CB_RESULT" in
  403\|*) ok "状态码 403（授权在中间件判定，绕过界面也无效）" ;;
  *)      bad "期望 403，实际 ${CB_RESULT%%|*}" ;;
esac
m1="$(count_rows materials)"; k1="$(count_rows knowledge_entries)"; f1="$(count_files)"
if [ "$m1" = "$m0" ] && [ "$k1" = "$k0" ] && [ "$f1" = "$f0" ]; then
  ok "两表行数与目录文件数均未变化（$m1 / $k1 / $f1）"
else
  bad "学生上传产生了副作用：materials $m0→$m1，knowledge $k0→$k1，文件 $f0→$f1"
fi

# ── R4.1 教师上传成功 ─────────────────────────────────────────
say "R4.1 教师上传合法 .md，应 201 且两表各增一行、class_id = 会话班级"
upload "$JT" "$TMP/ok.md"
printf '     %s\n' "$CB_RESULT"
case "$CB_RESULT" in
  201\|*) ok "状态码 201"; UPLOADED=$((UPLOADED+1)) ;;
  *)      bad "期望 201，实际 ${CB_RESULT%%|*}" ;;
esac
m2="$(count_rows materials)"; k2="$(count_rows knowledge_entries)"; f2="$(count_files)"
[ "$m2" = "$((m0+1))" ] && ok "materials 行数 $m0 → $m2（+1）" || bad "materials 行数 $m0 → $m2，期望 +1"
[ "$k2" = "$((k0+1))" ] && ok "knowledge_entries 行数 $k0 → $k2（+1）" || bad "knowledge_entries 行数 $k0 → $k2，期望 +1"
[ "$f2" = "$((f0+1))" ] && ok "上传目录文件数 $f0 → $f2（+1）" || bad "目录文件数 $f0 → $f2，期望 +1"

NEW_ID="$(sqlq "SELECT id FROM materials ORDER BY id DESC LIMIT 1;")"
NEW_CLASS="$(sqlq "SELECT class_id FROM materials WHERE id = $NEW_ID;")"
KE_CLASS="$(sqlq "SELECT class_id FROM knowledge_entries WHERE material_id = $NEW_ID ORDER BY id DESC LIMIT 1;")"
KE_BODY="$(sqlq "SELECT LEFT(body, 20) FROM knowledge_entries WHERE material_id = $NEW_ID ORDER BY id DESC LIMIT 1;")"
printf '     新材料 id=%s materials.class_id=%s knowledge_entries.class_id=%s\n' "$NEW_ID" "$NEW_CLASS" "$KE_CLASS"
printf '     知识库正文首 20 字符：%s\n' "$KE_BODY"
TEACHER_CLASS="$(curl -s -H "Authorization: Bearer $(cat "$JT")" "$BASE/api/me" | grep -o '"class_id":[0-9]*' | cut -d: -f2)"
if [ "$NEW_CLASS" = "$TEACHER_CLASS" ] && [ "$KE_CLASS" = "$TEACHER_CLASS" ]; then
  ok "两表 class_id 均等于会话班级（$TEACHER_CLASS）"
else
  bad "class_id 不匹配：会话=$TEACHER_CLASS materials=$NEW_CLASS knowledge=$KE_CLASS"
fi
printf '%s' "$KE_BODY" | grep -q '上传验证材料' \
  && ok "知识库正文与上传内容一致（同事务写入）" || bad "知识库正文与上传内容不一致"

# ── R4.2 扩展名白名单 ─────────────────────────────────────────
say "R4.2 上传 .exe 应 400，两表与目录不变"
mb="$(count_rows materials)"; fb="$(count_files)"
upload "$JT" "$TMP/bad.exe"
printf '     %s\n' "$CB_RESULT"
case "$CB_RESULT" in
  400\|*) ok "状态码 400" ;;
  *)      bad "期望 400，实际 ${CB_RESULT%%|*}" ;;
esac
printf '%s' "$CB_RESULT" | grep -q 'unsupported_type' && ok "响应体标明 unsupported_type" || bad "响应体未标明原因"
if [ "$(count_rows materials)" = "$mb" ] && [ "$(count_files)" = "$fb" ]; then
  ok "两表与目录均无变化"
else
  bad "非法扩展名上传产生了副作用"
fi

say "R4.2+ 扩展名大小写应归一化：.MD 必须通过白名单"
printf '大写扩展名\n' > "$TMP/UPPER.MD"
upload "$JT" "$TMP/UPPER.MD"
printf '     %s\n' "$CB_RESULT"
case "$CB_RESULT" in
  201\|*) ok "状态码 201（ToLower 生效，filepath.Ext 会保留 \".MD\" 原样）"; UPLOADED=$((UPLOADED+1)) ;;
  *)      bad "期望 201，实际 ${CB_RESULT%%|*} —— 大小写未归一化" ;;
esac

# ── R4.3 超限 ─────────────────────────────────────────────────
say "R4.3 上传超过 MAX_UPLOAD_BYTES 应 413，两表与目录不变"
mb="$(count_rows materials)"; fb="$(count_files)"
upload "$JT" "$TMP/too-big.md"
printf '     %s\n' "$CB_RESULT"
case "$CB_RESULT" in
  413\|*) ok "状态码 413" ;;
  *)      bad "期望 413，实际 ${CB_RESULT%%|*}" ;;
esac
printf '%s' "$CB_RESULT" | grep -q 'payload_too_large' && ok "响应体标明 payload_too_large" || bad "响应体未标明原因"
printf '%s' "$CB_RESULT" | grep -q "\"limit_bytes\":$MAX_BYTES" && ok "响应体回显上限 $MAX_BYTES" || bad "响应体未回显上限"
if [ "$(count_rows materials)" = "$mb" ] && [ "$(count_files)" = "$fb" ]; then
  ok "两表与目录均无变化"
else
  bad "超限上传产生了副作用"
fi

# ── R4.4 编码与空内容 ─────────────────────────────────────────
say "R4.4 非合法 UTF-8 应 400，两表与目录不变"
mb="$(count_rows materials)"; fb="$(count_files)"
upload "$JT" "$TMP/bad-utf8.md"
printf '     %s\n' "$CB_RESULT"
case "$CB_RESULT" in
  400\|*) ok "状态码 400" ;;
  *)      bad "期望 400，实际 ${CB_RESULT%%|*}" ;;
esac
printf '%s' "$CB_RESULT" | grep -q 'invalid_encoding' && ok "响应体标明 invalid_encoding" || bad "响应体未标明原因"
if [ "$(count_rows materials)" = "$mb" ] && [ "$(count_files)" = "$fb" ]; then
  ok "两表与目录均无变化"
else
  bad "非法编码上传产生了副作用"
fi

say "R4.4+ 空内容应 400"
upload "$JT" "$TMP/empty.md"
printf '     %s\n' "$CB_RESULT"
case "$CB_RESULT" in
  400\|*) ok "状态码 400（empty_content）" ;;
  *)      bad "期望 400，实际 ${CB_RESULT%%|*}" ;;
esac

# ── R4.4++ 开头的 UTF-8 BOM 必须被剥掉 ────────────────────────
# 这不是洁癖：Windows 记事本保存 UTF-8 默认带 BOM，教师拿它写 .md 再上传是常规路径。
# 不剥的直接后果是 markdown-it 把「# 标题」渲染成普通段落（实测），一级标题退化。
say "R4.4++ 带 UTF-8 BOM 的 .md：BOM 应被剥掉（磁盘与知识库正文都不含 EF BB BF）"
mb="$(count_rows materials)"; fb="$(count_files)"
upload "$JT" "$TMP/bom.md"
printf '     %s\n' "$CB_RESULT"
case "$CB_RESULT" in
  201\|*) ok "状态码 201（带 BOM 是合法 UTF-8，不该拒）"; UPLOADED=$((UPLOADED+1)) ;;
  *)      bad "期望 201，实际 ${CB_RESULT%%|*}" ;;
esac
bom_stored="$(sqlq "SELECT stored_name FROM materials ORDER BY id DESC LIMIT 1;")"
bom_head="$(od -An -tx1 -N3 "$UPLOAD_DIR/$bom_stored" | tr -d ' \n')"
printf '     落盘文件前 3 字节 = %s（应为内容首字节，不是 efbbbf）\n' "$bom_head"
[ "$bom_head" != "efbbbf" ] && ok "磁盘内容已剥掉 BOM" || bad "磁盘内容仍以 BOM 开头"
bom_body_hex="$(sqlq "SELECT HEX(LEFT(body,3)) FROM knowledge_entries ORDER BY id DESC LIMIT 1;")"
printf '     知识库正文前 3 字节 HEX = %s（不应为 EFBBBF）\n' "$bom_body_hex"
[ "$bom_body_hex" != "EFBBBF" ] && ok "知识库正文已剥掉 BOM" || bad "知识库正文仍以 BOM 开头"
# 反向对照：内容本身不能被误伤 —— 正文开头必须是原来的「# 」
printf '     落盘文件前 1 字节应为 23（'#'）\n'
[ "$(od -An -tx1 -N1 "$UPLOAD_DIR/$bom_stored" | tr -d ' \n')" = "23" ] \
  && ok "剥的是 BOM，正文首字符 '#' 完好" \
  || bad "正文被误伤（首字节不是 #）"

say "R4.4+++ 只有 BOM、没有正文的文件应 400，不入库不落盘"
printf '\357\273\277' > "$TMP/only-bom.md"
mb="$(count_rows materials)"; fb="$(count_files)"
upload "$JT" "$TMP/only-bom.md"
printf '     %s\n' "$CB_RESULT"
case "$CB_RESULT" in
  400\|*empty_content*) ok "状态码 400（empty_content —— 剥掉 BOM 之后确实为空）" ;;
  400\|*)               bad "返回 400 但不是 empty_content：$CB_RESULT" ;;
  *)                    bad "期望 400，实际 ${CB_RESULT%%|*}" ;;
esac
if [ "$(count_rows materials)" = "$mb" ] && [ "$(count_files)" = "$fb" ]; then
  ok "两表与目录均无变化"
else
  bad "纯 BOM 上传产生了副作用"
fi

# ── R4.5 客户端文件名不参与路径构造 ───────────────────────────
say "R4.5 原始文件名形如 ../../evil.md 时，存储名由服务端生成、uploads 之外不产生文件"
mb="$(count_rows materials)"; fb="$(count_files)"
upload "$JT" "$TMP/traversal.md" -F "file=@$TMP/traversal.md;filename=../../evil.md"
printf '     %s\n' "$CB_RESULT"
case "$CB_RESULT" in
  201\|*) ok "状态码 201（扩展名仍是 .md，白名单放行 —— 说明白名单本身不防穿越）"; UPLOADED=$((UPLOADED+1)) ;;
  *)      bad "期望 201，实际 ${CB_RESULT%%|*}" ;;
esac
STORED="$(sqlq "SELECT stored_name FROM materials ORDER BY id DESC LIMIT 1;")"
printf '     落库的 stored_name = %s\n' "$STORED"
if printf '%s' "$STORED" | grep -qE '^m-[0-9a-f]{32}\.(md|txt)$'; then
  ok "存储名由服务端随机生成，与客户端文件名无关"
else
  bad "存储名形态异常：$STORED"
fi
[ "$(count_files)" = "$((fb+1))" ] && ok "文件确实落在 uploads 内（$fb → $(count_files)）" \
  || bad "目录文件数 $fb → $(count_files)，期望 +1"
escaped=0
for p in evil.md ./evil.md backend/evil.md ../evil.md; do
  [ -e "$p" ] && { bad "uploads 之外出现文件：$p"; escaped=1; }
done
[ "$escaped" = "0" ] && ok "uploads/ 之外未产生任何文件（穿越未生效）"

# ── R4.7 表单 class_id 被忽略 ─────────────────────────────────
say "R4.7 表单塞入别班 class_id=2，落库仍应是会话班级"
mb="$(count_rows materials)"
upload "$JT" "$TMP/ok.txt" -F "class_id=2"
printf '     %s\n' "$CB_RESULT"
case "$CB_RESULT" in
  201\|*) ok "状态码 201"; UPLOADED=$((UPLOADED+1)) ;;
  *)      bad "期望 201，实际 ${CB_RESULT%%|*}" ;;
esac
INJ_ID="$(sqlq "SELECT id FROM materials ORDER BY id DESC LIMIT 1;")"
INJ_CLASS="$(sqlq "SELECT class_id FROM materials WHERE id = $INJ_ID;")"
printf '     新材料 id=%s 实际落库 class_id=%s（伪造值为 2）\n' "$INJ_ID" "$INJ_CLASS"
[ "$INJ_CLASS" = "$TEACHER_CLASS" ] \
  && ok "落库 class_id=$INJ_CLASS，等于会话班级而非表单值" \
  || bad "表单 class_id 生效了：落库 $INJ_CLASS"

# ── 上传后列表可见 + 学生只读 ─────────────────────────────────
say "R4.1+ 上传后本班列表可见（主路径：上传 → 列表 → 详情 → 下载）"
list="$(curl -s -H "Authorization: Bearer $(cat "$JT")" "$BASE/api/materials")"
printf '     %s\n' "$(printf '%s' "$list" | head -c 260)"
printf '%s' "$list" | grep -q "\"id\":$NEW_ID" && ok "本班教师列表能看到刚上传的材料 id=$NEW_ID" \
  || bad "列表里找不到刚上传的材料 id=$NEW_ID"
list_b="$(curl -s -H "Authorization: Bearer $(cat "$JS")" "$BASE/api/materials")"
printf '%s' "$list" | grep -o '"id":[0-9]*' | cut -d: -f2 | sort > "$TMP/ids-teacher.txt"
printf '%s' "$list_b" | grep -o '"id":[0-9]*' | cut -d: -f2 | sort > "$TMP/ids-student.txt"
if diff -q "$TMP/ids-teacher.txt" "$TMP/ids-student.txt" >/dev/null; then
  ok "学生看到的材料 ID 集合与教师一致（同班，均可读）"
else
  bad "同班师生可见集合不一致"
fi

say "R3 学生可读不可写：能看详情与下载，上传仍 403"
code_body "$BASE/api/materials/$NEW_ID" -H "Authorization: Bearer $(cat "$JS")"
case "$CB_RESULT" in
  200\|*) ok "学生可查看详情（200）" ;;
  *)      bad "期望 200，实际 ${CB_RESULT%%|*}" ;;
esac
dl_code="$(curl -s -H "Authorization: Bearer $(cat "$JS")" -o "$TMP/dl.bin" -w '%{http_code}' "$BASE/api/materials/$NEW_ID/file")"
printf '     学生下载 → %s（%s 字节）\n' "$dl_code" "$(wc -c < "$TMP/dl.bin" | tr -d ' ')"
[ "$dl_code" = "200" ] && ok "学生可下载本班文件（只读）" || bad "学生下载失败：$dl_code"

say "重新确认 /uploads 未被静态暴露（含新上传的存储名）"
static_code="$(curl -s -o /dev/null -w '%{http_code}' "$BASE/uploads/$STORED")"
printf '     GET /uploads/%s → %s\n' "$STORED" "$static_code"
[ "$static_code" = "404" ] && ok "新上传的文件也无法经静态路径读取" \
  || bad "静态路径返回 $static_code —— 上传目录疑似被暴露"

# ── 汇总 ──────────────────────────────────────────────────────
say "汇总"
printf '  通过 %d 项，失败 %d 项\n' "$pass" "$fail"
# ⚠️ 这个数必须来自「断言 201 的次数」或汇总时刻的实测行数，不能从 m2/f2 反推 ——
# m2/f2 是在第一次上传之后就采的样本。曾经写成 $((m2 - m0 + 2))，公式里的 +2 是硬凑的
# 常数，于是脚本实际传了 4 次却报「新增 3 条」。结论对不上证据，比没结论更糟。
mf="$(count_rows materials)"; kf="$(count_rows knowledge_entries)"; ff="$(count_files)"
printf '  本轮上传成功 %s 次；materials %s→%s、knowledge_entries %s→%s、目录文件 %s→%s（三者应同步）\n' \
  "$UPLOADED" "$m0" "$mf" "$k0" "$kf" "$f0" "$ff"
rm -rf "$TMP"
[ "$fail" -eq 0 ] || exit 1

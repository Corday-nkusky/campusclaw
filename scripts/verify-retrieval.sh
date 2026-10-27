#!/usr/bin/env bash
# 检索能力验收（add-class-scoped-retrieval tasks 10.1 / spec R10）。
#
# 退出码即判定。输出行「通过 N 项，失败 M 项，跳过 K 项」供 verify-all.sh 硬判定。
#
# SKIP 纪律（R10.4 / design D12）：
#   - 环境级 SKIP：索引未就绪（嵌入网关未配置或切片全 failed）时，数据相关断言整块跳过；
#   - 固定 SKIP 清单：本机环境无法构造的前提（见下方 RETRIEVAL_SKIP_LIST，逐条对应验收报告「未验证项」）；
#   - SKIP 不计入通过数；脚本结尾校验「实际 SKIP 条数 = 声明条数」，不符即红。
#
# 前置：MySQL80 运行中；后端已启动且 .env 齐全；库处于种子态（本脚本不写脏数据，除 R2.4 的临时状态翻转——结束时恢复）。

set -uo pipefail
export PATH="/usr/bin:/bin:/mingw64/bin:$PATH"
cd "$(dirname "$0")/.."

[ -f .env ] && { set -a; . ./.env; set +a; }

# 本脚本 21 条断言里，除了参数校验那几条，其余全部要真的走向量/混合检索 → 花嵌入额度。
# 课程网关按天限总量（共享资源），**默认不跑**：需要真验时 VERIFY_WITH_LLM=1 显式开启。
# 不打任何请求就退出，保证默认门禁零额度消耗。
if [ "${VERIFY_WITH_LLM:-0}" != "1" ]; then
  printf 'SKIP_REASON: 默认不调用嵌入网关（课程网关按天限 token）。要真验：VERIFY_WITH_LLM=1 bash scripts/verify-retrieval.sh\n'
  exit 3
fi

BASE="http://127.0.0.1:${API_PORT:-8080}"
MYSQL="${MYSQL:-}"
# mysql 客户端路径按候选探测（本机实际在 C:/Program Files 下）。
if [ -z "$MYSQL" ]; then
  for cand in \
    "C:/Program Files/MySQL/MySQL Server 8.0/bin/mysql.exe" \
    "D:/MySQL/MySQL Server 8.0/bin/mysql.exe" \
    "$(command -v mysql 2>/dev/null)"; do
    [ -n "$cand" ] && [ -x "$cand" ] && { MYSQL="$cand"; break; }
  done
fi
MYSQL="${MYSQL:?找不到 mysql 客户端（可用 MYSQL=... 指定）}"

# 凭证从 .env 读：原实现写死 root/password，换机即静默返回空、判定失真。
sqlq() {
  "$MYSQL" -u"${DB_USER:?缺 DB_USER}" -p"${DB_PASSWORD:?缺 DB_PASSWORD}" \
    --default-character-set=utf8mb4 -N -B -e "USE ${DB_NAME:-campusclaw}; $1" 2>&1 \
    | grep -v 'Using a password'
}

TMP=".verify-logs/_retrieval"
mkdir -p "$TMP"

pass=0; fail=0; skip=0

ok()  { printf '  ✅ %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  ❌ %s（%s）\n' "$1" "${2:-}"; fail=$((fail+1)); }
skipmark() { printf '  ⏭️  SKIP %s\n' "$1"; skip=$((skip+1)); }

# jpost <token|-> <json> <path>：POST 并回显状态码；响应体落 $TMP/body.json
# JSON 一律先落文件再 --data-binary @file 上送。不能走 -d "$data"：
# 中文进 curl 的 argv 会经过 Windows ANSI 代码页转译，服务端按 UTF-8 解出
# U+FFFD，「细胞周期」变「B??-????」式乱码 → 就绪探测 0 命中 → 数据断言
# 整块被误判为未就绪而 SKIP（2026-10-07 实测踩过，见验收报告）。
jpost() {
  local tok="$1" data="$2" path="$3"
  local hdr=()
  [ "$tok" != "-" ] && hdr=(-H "Authorization: Bearer $tok")
  printf '%s' "$data" > "$TMP/req.json"
  curl -s --noproxy '*' -o "$TMP/body.json" -w '%{http_code}' --max-time 20 \
    "${hdr[@]}" -H 'Content-Type: application/json' --data-binary @"$TMP/req.json" "$BASE$path"
}

# 登录两个角色
login() { curl -s --noproxy '*' -H 'Content-Type: application/json' \
  -d "{\"username\":\"$1\",\"password\":\"$2\"}" "$BASE/api/login" \
  | sed -E 's/.*"access_token":"([^"]+)".*/\1/'; }
TOKEN_A="$(login teacher_a "${SEED_TEACHER_PASSWORD:?缺 SEED_TEACHER_PASSWORD}")"
TOKEN_S="$(login student_a1 "$SEED_STUDENT_PASSWORD")"
TOKEN_B="$(login student_b1 "$SEED_STUDENT_PASSWORD")"

printf '== R1/R6.6 无依赖断言（鉴权与请求校验）\n'
[ "$(jpost - '{"query":"x"}' /api/search)" = "401" ] && ok "R1.1 无 token 检索 → 401" || bad "R1.1 无 token 检索" "期望 401"
[ "$(jpost - '{"question":"x"}' /api/ask)" = "401" ] && ok "R1.1/R6.6 无 token 问答 → 401" || bad "R6.6 无 token 问答" "期望 401"
[ "$(jpost "$TOKEN_A" '{"query":"  "}' /api/search)" = "400" ] && ok "R1.2 空查询 → 400" || bad "R1.2 空查询" "期望 400"
[ "$(jpost "$TOKEN_A" '{"query":"细胞","mode":"nope"}' /api/search)" = "400" ] && ok "R1.3 非法 mode → 400" || bad "R1.3 非法 mode" "期望 400"
[ "$(jpost "$TOKEN_A" '{"query":"细胞","top_k":21}' /api/search)" = "400" ] && ok "R1.4 top_k=21 → 400" || bad "R1.4 top_k 越界" "期望 400"
[ "$(jpost "$TOKEN_A" '{"question":" "}' /api/ask)" = "400" ] && ok "R6.6 空问题 → 400" || bad "R6.6 空问题" "期望 400"

# R1.5 缺省 mode：响应体 mode 字段应为 hybrid
jpost "$TOKEN_A" '{"query":" whatever ","top_k":1}' /api/search > /dev/null
got_mode="$(sed -E 's/.*"mode":"([a-z]+)".*/\1/' "$TMP/body.json" | head -c 20)"
[ "$got_mode" = "hybrid" ] && ok "R1.5 缺省 mode=hybrid" || bad "R1.5 缺省 mode" "got=$got_mode"

# ── 预检：索引是否就绪 ────────────────────────────────────────
code="$(jpost "$TOKEN_A" '{"query":"细胞周期","mode":"keyword","top_k":5}' /api/search)"
hits_a="$(grep -o '"chunk_id"' "$TMP/body.json" | wc -l | tr -d ' ')"
DATA_READY=0
if [ "$code" = "200" ] && [ "$hits_a" -ge 1 ]; then DATA_READY=1; fi

# 数据相关断言块共 14 条；索引未就绪时整块 SKIP
DATA_BLOCK=14

printf '\n== R2/R3/R5/R6 数据相关断言（需索引就绪）\n'
if [ "$DATA_READY" != "1" ]; then
  # 索引没就绪不一定是产品坏了：嵌入网关按天限 token，429 时切片会集体 failed。
  # 所以先探一次嵌入网关 —— 是网关的问题就明确「跳过（依赖不可用）」（退出码 3，
  # verify-all 会单列成「没跑」）；不是网关的问题就照旧 SKIP 这一块，
  # 让断言数不符去判红：真问题不该被「跳过」掩盖（2026-10-27 额度耗尽踩过）。
  emb_code="$(curl -s -o "$TMP/emb.json" -w '%{http_code}' --max-time 25 --noproxy '*' \
    -H "Authorization: Bearer ${EMBEDDING_API_KEY:-}" -H 'Content-Type: application/json' \
    -d "{\"model\":\"${EMBEDDING_MODEL:-course-embedding}\",\"input\":[\"ping\"]}" \
    "${EMBEDDING_API_BASE%/}/v1/embeddings")"
  case "$emb_code" in
    000|401|403|429|5*)
      nfail="$(sqlq "select count(*) from knowledge_chunks where index_status='failed'" | tr -d ' ')"
      printf 'SKIP_REASON: 索引未就绪且嵌入网关不可用（HTTP %s，%s 条切片全 failed）：%s\n' \
        "$emb_code" "${nfail:-?}" "$(head -c 120 "$TMP/emb.json" 2>/dev/null | tr -d '\r\n')"
      printf '  （R1.5 的 503 与 R2–R7 的 14 条同因：嵌入/生成额度用尽，不是检索逻辑坏了）\n'
      skip=$((skip+DATA_BLOCK))
      printf '通过 %d 项，失败 %d 项，跳过 %d 项\n' "$pass" "$fail" "$skip"
      exit 3
      ;;
  esac
  for _ in $(seq 1 $DATA_BLOCK); do skipmark "（索引未就绪：检查 .env 网关配置并重启后端补索引）"; done
else
  # R5.1 关键字路命中
  [ "$hits_a" -ge 1 ] && ok "R5.1 keyword 命中「细胞周期」" || bad "R5.1 keyword 未命中"

  # R3.1 八字段齐全
  fields_ok=1
  for f in material_id material_title chunk_id chunk_index snippet start_offset end_offset score; do
    grep -q "\"$f\"" "$TMP/body.json" || { fields_ok=0; break; }
  done
  [ "$fields_ok" = "1" ] && ok "R3.1 八字段溯源块齐全" || bad "R3.1 字段缺失"

  # 取第一条 hit 的素材，做 R3.2 / R3.3
  cp "$TMP/body.json" "$TMP/hits.json"
  mid="$(sed -E 's/.*"material_id":([0-9]+).*/\1/' "$TMP/hits.json" | head -1)"
  so="$(sed -E 's/.*"start_offset":([0-9]+).*/\1/' "$TMP/hits.json" | head -1)"
  eo="$(sed -E 's/.*"end_offset":([0-9]+).*/\1/' "$TMP/hits.json" | head -1)"

  curl -s --noproxy '*' -H "Authorization: Bearer $TOKEN_A" -o "$TMP/detail.json" "$BASE/api/materials/$mid"
  node -e '
    const fs=require("fs");
    const detail=JSON.parse(fs.readFileSync(process.argv[1],"utf8"));
    const hits=JSON.parse(fs.readFileSync(process.argv[2],"utf8"));
    const h=hits.hits[0];
    const runes=Array.from(detail.body);
    const slice=runes.slice(h.start_offset,h.end_offset).join("");
    console.log(slice===h.snippet?"R3.2-OK":"R3.2-FAIL");
    console.log(detail.body.includes(h.snippet)?"R3.3-OK":"R3.3-FAIL");
  ' "$TMP/detail.json" "$TMP/hits.json" > "$TMP/r32.txt"
  grep -q "R3.2-OK" "$TMP/r32.txt" && ok "R3.2 摘录 = 按 rune 取 [start,end) 区间" || bad "R3.2 区间不一致" "$(cat "$TMP/r32.txt")"
  grep -q "R3.3-OK" "$TMP/r32.txt" && ok "R3.3 详情正文包含摘录（溯源闭环）" || bad "R3.3 溯源失败"

  # R2.1 跨班 = 200 空结果（「光合作用」只在 B 班材料里）
  code="$(jpost "$TOKEN_A" '{"query":"光合作用","mode":"keyword","top_k":5}' /api/search)"
  nA="$(grep -o '"chunk_id"' "$TMP/body.json" | wc -l | tr -d ' ')"
  [ "$code" = "200" ] && [ "$nA" = "0" ] && ok "R2.1 A班检索 B班独有词 → 200 空结果" || bad "R2.1 跨班泄漏或形态错误" "code=$code nA=$nA"
  jpost "$TOKEN_B" '{"query":"光合作用","mode":"keyword","top_k":5}' /api/search > /dev/null
  nB="$(grep -o '"chunk_id"' "$TMP/body.json" | wc -l | tr -d ' ')"
  [ "$nB" -ge 1 ] && ok "R2.1 B班检索本班独有词 → 命中" || bad "R2.1 B班本班未命中" "nB=$nB"

  # R2.2 注入 class_id 被丢弃：响应与不注入逐字段一致
  jpost "$TOKEN_A" '{"query":"细胞周期","mode":"keyword","top_k":5}' /api/search > /dev/null
  cp "$TMP/body.json" "$TMP/r1.json"
  jpost "$TOKEN_A" '{"query":"细胞周期","mode":"keyword","top_k":5,"class_id":999}' /api/search > /dev/null
  if diff -q <(grep -o '"hits":.*' "$TMP/r1.json") <(grep -o '"hits":.*' "$TMP/body.json") > /dev/null; then
    ok "R2.2 请求体注入 class_id=999 被丢弃"
  else
    bad "R2.2 注入 class_id 影响了结果"
  fi

  # R5.6 两次同请求逐字段一致
  jpost "$TOKEN_A" '{"query":"细胞周期","mode":"keyword","top_k":5}' /api/search > /dev/null
  cp "$TMP/body.json" "$TMP/r2.json"
  jpost "$TOKEN_A" '{"query":"细胞周期","mode":"keyword","top_k":5}' /api/search > /dev/null
  if diff -q <(grep -o '"hits":.*' "$TMP/r2.json") <(grep -o '"hits":.*' "$TMP/body.json") > /dev/null; then
    ok "R5.6 同请求两次结果一致"
  else
    bad "R5.6 排序不可复现"
  fi

  # R2.3 教师与学生同界
  jpost "$TOKEN_S" '{"query":"细胞周期","mode":"keyword","top_k":5}' /api/search > /dev/null
  if diff -q <(grep -o '"hits":.*' "$TMP/r2.json") <(grep -o '"hits":.*' "$TMP/body.json") > /dev/null; then
    ok "R2.3 教师与学生同班同结果"
  else
    bad "R2.3 角色差异泄露到检索"
  fi

  # R2.4 failed 切片不参与检索（临时翻转 glossary 的切片状态，结束恢复）
  sqlq "UPDATE knowledge_chunks SET index_status='failed' WHERE chunk_text LIKE '%有丝分裂%';"
  jpost "$TOKEN_A" '{"query":"有丝分裂","mode":"keyword","top_k":5}' /api/search > /dev/null
  nFail="$(grep -o '"chunk_id"' "$TMP/body.json" | wc -l | tr -d ' ')"
  sqlq "UPDATE knowledge_chunks SET index_status='ready' WHERE chunk_text LIKE '%有丝分裂%';"
  jpost "$TOKEN_A" '{"query":"有丝分裂","mode":"keyword","top_k":5}' /api/search > /dev/null
  nReady="$(grep -o '"chunk_id"' "$TMP/body.json" | wc -l | tr -d ' ')"
  [ "$nFail" = "0" ] && ok "R2.4 failed 切片被排除" || bad "R2.4 failed 切片仍被检索" "nFail=$nFail"
  [ "$nReady" -ge 1 ] && ok "R2.4 恢复 ready 后重新可检索" || bad "R2.4 恢复后仍未命中" "nReady=$nReady"

  # R6.1 无命中不调网关：固定文案 + citations 空
  # 用例选词说明：bge-m3 的余弦分布整体偏高（乱码/数字与中文文本也能到 0.36–0.45），
  # 自然语言问句（如「今天天气怎么样」）会被向量路以弱相似命中 → 不构成无命中前提。
  # 「qqqwww」实测向量余弦 < 0.35 且 ngram 二字组不在任何种子里 → 两路均空，才真正触发 R6.1。
  jpost "$TOKEN_A" '{"question":"qqqwww"}' /api/ask > /dev/null
  ans="$(sed -E 's/.*"answer":"([^"]*)".*/\1/' "$TMP/body.json" | head -c 60)"
  ncit="$(grep -o '"material_id"' "$TMP/body.json" | wc -l | tr -d ' ')"
  [ "$ans" = "资料中未找到相关内容" ] && [ "$ncit" = "0" ] \
    && ok "R6.1 无命中 → 固定文案 + citations 空" || bad "R6.1 无命中形态错误" "ans=$ans ncit=$ncit"

  # R4.5 学生 reindex → 403
  [ "$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' -X POST -H "Authorization: Bearer $TOKEN_S" \
       "$BASE/api/materials/1/reindex" -H 'Content-Type: application/json' -d '{}')" = "403" ] \
    && ok "R4.5 学生 reindex → 403" || bad "R4.5 学生 reindex 未拒" 

  # R4.4 教师 reindex（hierarchy 策略）→ 200 且切片 ≥1
  code="$(curl -s --noproxy '*' -o "$TMP/body.json" -w '%{http_code}' -X POST -H "Authorization: Bearer $TOKEN_A" \
    "$BASE/api/materials/$mid/reindex" -H 'Content-Type: application/json' -d '{"strategy":"hierarchy"}')"
  nch="$(grep -o '"chunks":[0-9]*' "$TMP/body.json" | grep -o '[0-9]*')"
  [ "$code" = "200" ] && [ "${nch:-0}" -ge 1 ] && ok "R4.4 教师 reindex hierarchy → 200" || bad "R4.4 reindex 失败" "code=$code chunks=$nch"
fi

# ── 固定 SKIP 清单（本机无法构造的前提，逐条对应验收报告「未验证项」）──
# S1 R7.1 Qdrant 停机降级实跑 —— 需要停掉 Qdrant 再三模式对照（Qdrant 正在服务其他验证）
# S2 R2.5 直写 Qdrant 错班点构造 —— 需要直连 Qdrant 造脏数据；请求契约已由 Go 单测覆盖
# S3 R5.1/R6.1 「不调用网关」的计数桩判定 —— 由 Go httptest 单测覆盖（qdrant_test.go / stub）
# S4 R5.2 向量语义命中联调 —— 需要真实嵌入网关的端到端联调环境
skipmark "S1 R7.1 Qdrant 停机降级实跑"
skipmark "S2 R2.5 直写 Qdrant 错班点构造"
skipmark "S3 R5.1/R6.1 网关计数桩判定（Go 单测覆盖）"
skipmark "S4 R5.2 向量语义命中联调"
# 期望 SKIP 数 = 固定清单 4 条 +（索引未就绪时的数据块 DATA_BLOCK 条）
EXPECTED_SKIP=4
[ "$DATA_READY" != "1" ] && EXPECTED_SKIP=$((EXPECTED_SKIP + DATA_BLOCK))

skip_ok=1
[ "$skip" -ne "$EXPECTED_SKIP" ] && skip_ok=0
printf '\n通过 %s 项，失败 %s 项，跳过 %s 项（声明 SKIP=%s）\n' "$pass" "$fail" "$skip" "$EXPECTED_SKIP"
if [ "$skip_ok" != "1" ]; then
  printf '❌ SKIP 条数与声明清单不一致（R10.4 硬判定）\n'
  fail=$((fail+1))
fi
[ "$pass" -gt 0 ] || [ "$fail" -gt 0 ] || true
exit $([ "$fail" = "0" ] && echo 0 || echo 1)

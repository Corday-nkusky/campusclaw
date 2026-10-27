#!/usr/bin/env bash
# 一条命令跑完迭代 1 的全部验收（§8.5「证据可脱离本机复现」的落点）。
#
# 顺序不是随意的：几个脚本会真的写数据，而有的脚本要求库处于**种子状态**。
# 所以编排是：
#   reset → 重启后端（补索引）→ 认证 → 隔离 → 上传（会写数据）
#         → reset → 重启后端（检索验收要求种子态）→ 检索 → reset
#         → 存活判定 → Compose 静态 → 浏览器断言 → 端到端取证 → 取证采集
#         → reset → 重启后端（收尾后仍可用）
#
# 为什么 reset 后要重启：种子补索引只在启动时跑，复位会清空 knowledge_chunks；
# 不重启的话检索/隔离脚本对种子内容全 0 命中（见 restart-backend.sh 头注释）。
#
# 前置：
#   1) MySQL80 在跑
#   2) 前端 dev server 在跑：cd frontend && npm run dev   （浏览器类脚本需要它）
#   3) .env 已填好
#
# 用法：
#   bash scripts/verify-all.sh
#   bash scripts/verify-all.sh --no-browser   # 跳过需要前端的三个脚本
#
# 退出码：0 = 全部通过；1 = 有任一脚本失败（会打印是哪几个）。
# 每步的完整输出落在 .verify-logs/（已 gitignore）。

set -uo pipefail

export PATH="/usr/bin:/bin:/mingw64/bin:/c/Users/TANGWENJIAN/.local/bin:$PATH"
cd "$(dirname "$0")/.."

# node 路径按候选探测：原先写死 22.22.2-3，本机升级到 -6 后 -3 已不存在，
# 落到 `command -v node` 兜底才没炸。直接把版本号写死迟早再踩，改 glob 取最新。
NODE=""
for _cand in $(ls -d C:/Users/TANGWENJIAN/.workbuddy/binaries/node/versions/*/node.exe 2>/dev/null | sort -V -r); do
  [ -x "$_cand" ] && { NODE="$_cand"; break; }
done
[ -n "$NODE" ] || NODE="$(command -v node)"
[ -n "$NODE" ] || { printf '❌ 找不到 node（可用 PATH 里任意 node 22+）\n'; exit 2; }

SKIP_BROWSER=0
[ "${1:-}" = "--no-browser" ] && SKIP_BROWSER=1

# 破坏性复位开关（2026-10-10 事故后新增）：默认【不】执行任何 reset——
# 不清库、不删 uploads 里的非种子文件。部分断言依赖"只有种子"的已知状态，
# 残留数据会让对应段如实失败——失败可见，好过悄悄删数据。
# 确要复位：VERIFY_ALL_WIPE=1 bash scripts/verify-all.sh
WIPE="${VERIFY_ALL_WIPE:-0}"

# 统一走这里调 reset；WIPE≠1 时只打印跳过说明。
maybe_reset() { # maybe_reset <显示名>
  if [ "$WIPE" = "1" ]; then
    # ⚠️ WIPE 会清空 knowledge_chunks + Qdrant，随后 restart-backend 触发**全量补索引**，
    # 每条切片一次嵌入请求 —— 也就是说 WIPE 是花嵌入额度的操作。日常回归不需要它，
    # 只在「库被 E2E 污染、要回种子态」时才用，且用一次就够，不要每轮都 WIPE。
    run "$1" "-" bash scripts/reset-demo-data.sh --yes --wipe
  else
    printf '  ⏭ 跳过「%s」——VERIFY_ALL_WIPE=0，不删任何数据与文件\n' "$1"
  fi
}

if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

LOG_DIR=".verify-logs"
rm -rf "$LOG_DIR"; mkdir -p "$LOG_DIR"

API="http://127.0.0.1:${API_PORT:-8080}"
WEB="http://127.0.0.1:5173"

# 课程网关是共享的**按天限量**资源（token 额度，耗尽回 429）。它不该被日常回归消耗。
# 所以默认 VERIFY_WITH_LLM=0：完全不碰网关，需要真验对话/引用时显式 VERIFY_WITH_LLM=1。
#
# 2026-10-27 的教训（自己的设计缺陷，不是网关的问题）：
#   ① 为了「判断能不能跳过」而每次跑门禁都先探一次 —— 探针本身也在花 token；
#   ② VERIFY_ALL_WIPE=1 每轮清库 → 重启后端触发全量补索引（每条切片一次 embedding），
#      把已经建好的索引花钱删了重算；
#   ③ 脚本还在调试阶段就直接打真网关，5 轮全量下来把当天额度打干。
#   正确顺序：先把脚本调通（默认不碰网关，跳过的项如实记 ⏭），最后**只跑一轮** VERIFY_WITH_LLM=1。
WITH_LLM="${VERIFY_WITH_LLM:-0}"
PROBE_CACHE=".verify-logs-cache/llm-probe-$(date +%Y%m%d).txt"

llm_probe() { # 回显状态码；当天已探过就直接复用，不重复花钱
  if [ -s "$PROBE_CACHE" ]; then cat "$PROBE_CACHE"; return; fi
  if [ -z "${CHAT_API_BASE:-}" ] || [ -z "${CHAT_API_KEY:-}" ]; then printf '000'; return; fi
  local code
  code="$(curl -s -o "$LOG_DIR/_llm_probe.json" -w '%{http_code}' --max-time 25 --noproxy '*' \
    -H "Authorization: Bearer ${CHAT_API_KEY}" -H 'Content-Type: application/json' \
    -d "{\"model\":\"${CHAT_MODEL:-course-chat}\",\"messages\":[{\"role\":\"user\",\"content\":\"ping\"}],\"max_tokens\":1}" \
    "${CHAT_API_BASE%/}/v1/chat/completions")"
  mkdir -p .verify-logs-cache && printf '%s' "$code" > "$PROBE_CACHE"
  printf '%s' "$code"
}
llm_probe_reason() { # llm_probe_reason <状态码>
  printf 'LLM 网关不可用（HTTP %s）：%s' "${1:-000}" \
    "$(head -c 120 "$LOG_DIR/_llm_probe.json" 2>/dev/null | tr -d '\r\n')"
}

results=()
failed=()
skipped=()

run() { # run <显示名> <期望通过数|-> <命令...>
  local name="$1" expect="$2"; shift 2
  local slug log
  slug="$(printf '%s' "$name" | tr ' /' '__')"
  log="$LOG_DIR/$slug.log"
  printf '\n\033[1m▶ %s\033[0m\n' "$name"
  "$@" > "$log" 2>&1
  local code=$?
  local got
  # 两种输出格式都要吃：shell 脚本是「通过 N 项，失败 M 项」，
  # .mjs 脚本是「N/M 项断言通过」（踩过：只匹配前者会让两行断言数恒为 -）。
  got="$(grep -oE '[0-9]+/[0-9]+ 项断言通过|通过 [0-9]+ 项' "$log" | tail -1 | grep -oE '[0-9]+(/[0-9]+)?' | head -1)"
  [ -z "$got" ] && got="-"
  if [ "$code" = "0" ]; then
    printf '  ✅ 退出码 0（断言 %s 项通过）→ %s\n' "${got}" "$log"
    results+=("$name|PASS|${got}|${expect}")
  elif [ "$code" = "3" ]; then
    # 退出码 3 = 约定：**依赖不可用**（不是被测对象的问题），如实记为跳过。
    # 不允许「静默跳过」：脚本必须打出 `SKIP_REASON: …` 说明为什么，且汇总里单列。
    local reason
    reason="$(grep -m1 '^SKIP_REASON:' "$log" 2>/dev/null | sed 's/^SKIP_REASON:[[:space:]]*//')"
    [ -z "$reason" ] && reason="$(head -1 "$log" 2>/dev/null)"
    printf '  ⏭ 跳过（依赖不可用）：%s → %s\n' "$reason" "$log"
    results+=("$name|SKIP|${got}|${expect}")
    skipped+=("$name（$reason）")
  else
    printf '  ❌ 退出码 %s → %s\n' "$code" "$log"
    grep -E '❌' "$log" | head -5 | sed 's/^/       /'
    results+=("$name|FAIL|${got}|${expect}")
    failed+=("$name")
  fi
}

run_skip() { # run_skip <显示名> <原因>：依赖不可用，明确记为「没跑」，不冒充失败也不冒充通过
  local name="$1" reason="$2" slug log
  slug="$(printf '%s' "$name" | tr ' /' '__')"
  log="$LOG_DIR/$slug.log"
  printf '\n\033[1m▶ %s\033[0m\n' "$name"
  printf '%s\n' "$reason" > "$log"
  printf '  ⏭ 跳过（依赖不可用）：%s → %s\n' "$reason" "$log"
  results+=("$name|SKIP|-|-")
  skipped+=("$name（$reason）")
}

printf '前置检查\n'
# ⚠️ 探测不要把响应体 `-o /dev/null`：本机 curl 是 Windows 原生 exe，
# 偶尔会对 /dev/null 报 CURLE_WRITE_ERROR（退出码 23），把「服务好好的」
# 判成不可达。写成文件再比状态码才稳（踩过）。
probe() { # probe <url>：回显状态码
  curl -s -o "$LOG_DIR/_probe.txt" -w '%{http_code}' --max-time 4 --noproxy '*' "$1"
}
if [ "$(probe "$API/health")" = "200" ]; then
  printf '  ✅ 后端 %s 可达\n' "$API"
else
  printf '  ❌ 后端 %s 不可达 —— 先跑 bash scripts/dev-run.sh\n' "$API"
  exit 1
fi
WEB_OK=0
if [ "$(probe "$WEB/")" = "200" ]; then
  printf '  ✅ 前端 %s 可达\n' "$WEB"
  WEB_OK=1
else
  printf '  ⚠️ 前端 %s 不可达 —— 浏览器类脚本会被跳过（启动：cd frontend && npm run dev）\n' "$WEB"
fi
if [ "$SKIP_BROWSER" = "1" ]; then
  printf '  ⚠️ --no-browser：按参数要求跳过浏览器类脚本\n'
fi

maybe_reset "reset：复位到种子状态"
run "restart-backend：复位后重启补索引" "-" bash scripts/restart-backend.sh
# ── 预期断言数（add-token-auth 后）────────────────────────────
# verify-auth 21：R8.4×1 + R9.2×2 + R1.5×1 + R1.1×6 + R2.1×3 + R1.3×3 + R1.4×2 + R1.2×3
# browser-verify 19：原 21 项去掉 B12/B13（Cookie 属性两条，token 方案下无对象可验）
# verify-retrieval 21：R1/R6.6 无依赖 7 条 + 数据相关 14 条（需嵌入网关已配置、索引就绪）
# e2e-tutor 16：第 5 课解题助手 T1–T16（T12 两条分支只跑一条）——需 CHAT 网关可用
# 合计 210 = 21+25+40+21+19+18+19+31+16（check-docs-freshness.sh 以此为准；
# compose 从 17 变 18 是补了 docker 可用性探测那条，改数请同步改注释与文档）
run "verify-auth（R1/R2/R8.4/R9.2）" "21" env PASSWORD="$SEED_TEACHER_PASSWORD" bash scripts/verify-auth.sh
run "verify-isolation（R5/R6）" "25" bash scripts/verify-isolation.sh
run "verify-upload（R3/R4/R9.2）" "40" bash scripts/verify-upload.sh
# verify-retrieval 的前置是「库处于种子态」（脚本头注释），upload 的产物必须清掉：
# 踩过：upload 的测试材料留在库里，R6.1 的「无命中」探针 qqqwww 被向量路以
# ≥0.35 弱相似命中，固定文案路径没触发，断言红（2026-10-07）。
maybe_reset "reset：为检索验收恢复种子态"
run "restart-backend：复位后重启补索引" "-" bash scripts/restart-backend.sh
run "verify-retrieval（R1–R7 检索）" "21" bash scripts/verify-retrieval.sh
maybe_reset "reset：清掉上传产物"
run "verify-health-decoupled（R8.4 存活判定）" "19" bash scripts/verify-health-decoupled.sh
run "verify-compose-config（§7 静态配置）" "18" bash scripts/verify-compose-config.sh

# ⚠️ 上面那次 reset 会清空 knowledge_chunks 与 Qdrant 集合，而补索引**只在进程启动时**做。
# 不重启就往下跑浏览器脚本，会出现「只有 e2e-tutor 挂、别的都绿」的假象：
# T10 引用 0 条 + T16 等不到引用芯片，而这条脚本单独跑（在别处的 restart 之后）却是全绿
# —— 2026-10-27 第一次跑全量时踩到，排查了半天才发现是编排顺序，不是产品问题。
if [ "$WEB_OK" = "1" ] && [ "$SKIP_BROWSER" = "0" ]; then
  run "restart-backend：浏览器脚本前恢复索引" "-" bash scripts/restart-backend.sh
  run "browser-verify（登录/token/深色模式）" "19" \
    "$NODE" scripts/browser-verify.mjs --user teacher_a --pass "$SEED_TEACHER_PASSWORD"
  run "e2e-acceptance（§8.2/§8.3 主路径与失败路径）" "31" \
    "$NODE" scripts/e2e-acceptance.mjs \
      --teacher-pass "$SEED_TEACHER_PASSWORD" --student-pass "$SEED_STUDENT_PASSWORD"
  # e2e-tutor 要真调模型（流式回答 + 引用芯片）。默认**不去碰**网关：
  # 日常回归记「未跑（需显式开启）」，需要真验时 VERIFY_WITH_LLM=1 才探 + 跑。
  if [ "$WITH_LLM" != "1" ]; then
    run_skip "e2e-tutor（第 5 课解题助手：配置/建会话/流式/引用）" \
      "默认不调用 LLM 网关（课程网关按天限 token）。要真验：VERIFY_WITH_LLM=1 bash scripts/verify-all.sh"
  else
    llm_code="$(llm_probe)"
    case "$llm_code" in
      # 这些状态码明确是网关侧不可用
      000|401|403|429|5*) llm_ok=0 ;;
      # 其它状态码（如 400 不认 max_tokens）不当作不可用，照跑 —— 别拿探测结果否掉被测对象
      *) llm_ok=1 ;;
    esac
    if [ "$llm_ok" = "1" ]; then
      run "e2e-tutor（第 5 课解题助手：配置/建会话/流式/引用）" "16" \
        "$NODE" scripts/e2e-tutor.mjs \
          --teacher-pass "$SEED_TEACHER_PASSWORD" --student-pass "$SEED_STUDENT_PASSWORD"
    else
      run_skip "e2e-tutor（第 5 课解题助手：配置/建会话/流式/引用）" "$(llm_probe_reason "$llm_code")"
    fi
  fi
  run "capture-identity-evidence（作业取证报文）" "-" \
    "$NODE" scripts/capture-identity-evidence.mjs --user teacher_a --pass "$SEED_TEACHER_PASSWORD"
else
  printf '\n（浏览器类脚本已跳过：browser-verify / e2e-acceptance / e2e-tutor / capture-identity-evidence）\n'
fi

maybe_reset "reset：收尾复位"
run "restart-backend：收尾后恢复种子索引" "-" bash scripts/restart-backend.sh

printf '\n\033[1m═══ 汇总 ═══\033[0m\n'
# 名称里全是中文，printf 的列宽对不齐（CJK 是双宽字符），索性不做表格对齐。
#
# ⚠️ 「预期」这一列**不只是显示**：断言数与预期不符即为失败。
# 理由：脚本退出码 0 只说明「跑过的那些断言都过了」，**不说明该跑的都跑了**。
# 一个被误删的断言、或因为前置缺失而被静默跳过的分支（如 verify-auth 在缺
# SEED_STUDENT_PASSWORD 时会跳过 R1.3+），都能得到「退出码 0 + 断言变少」——
# 光看退出码会把它判成通过。所以数量对不上必须红，而不是只打出来让人自己注意。
mismatch=()
for line in "${results[@]}"; do
  IFS='|' read -r name status got expect <<<"$line"
  note=""
  # 归一化：.mjs 脚本报的是「21/21」，shell 脚本报的是「19」。
  # 取分子再比 —— 分母（失败数）为 0 已由退出码保证；不归一化会把自己判成不符
  # （这个检查加上来第一次跑就抓到了这个 bug：21/21 != 21）。
  gotn="${got%%/*}"
  # SKIP 不参与断言数核对：它本来就没跑完，拿「跑过的条数」去比预期只会再加一条假红。
  # 但 SKIP 会在下面单独列出，仍然可见 —— 跳过不等于通过。
  if [ "$status" != "SKIP" ] && [ "$expect" != "-" ] && [ "$gotn" != "-" ] && [ "$gotn" != "$expect" ]; then
    note="  ← ⚠️ 断言数与预期不符"
    mismatch+=("$name")
  fi
  case "$status" in
    PASS) mark="✅" ;;
    SKIP) mark="⏭" ;;
    *) mark="❌" ;;
  esac
  if [ "$status" = "SKIP" ]; then
    printf '  %s %s（未跑，依赖不可用）\n' "$mark" "$name"
    continue
  fi
  printf '  %s %s（断言 %s / 预期 %s）%s\n' "$mark" "$name" "$got" "$expect" "$note"
done

if [ "${#skipped[@]}" -gt 0 ]; then
  # 跳过必须显眼：不能让人把「没跑」当成「跑过且通过」。
  printf '\n  ⏭ 下列脚本**没有跑**（依赖不可用，非被测对象的问题）：\n'
  printf '     %s\n' "${skipped[@]}"
fi

if [ "${#failed[@]}" -eq 0 ] && [ "${#mismatch[@]}" -eq 0 ]; then
  if [ "${#skipped[@]}" -gt 0 ]; then
    printf '\n  已跑的全部通过；但有 %d 项因依赖不可用未跑（见上）。日志目录：%s\n' "${#skipped[@]}" "$LOG_DIR"
  else
    printf '\n  全部通过。日志目录：%s\n' "$LOG_DIR"
  fi
  exit 0
fi
if [ "${#mismatch[@]}" -gt 0 ]; then
  printf '\n  ❌ 下列脚本的断言数与预期不符（脚本可能被改过，或被静默跳过了分支）：\n'
  printf '     %s\n' "${mismatch[@]}"
  printf '     预期值在 scripts/verify-all.sh 的 run 调用里。改动断言数后请同步更新它 ——\n'
  printf '     否则「全部通过」这四个字会掩盖掉没跑的那几条。\n'
fi
if [ "${#failed[@]}" -gt 0 ]; then
  printf '\n  失败脚本：%s\n' "${failed[*]}"
fi
printf '  日志目录：%s\n' "$LOG_DIR"
exit 1

#!/usr/bin/env bash
# 文档状态对账 —— 判定「各文档里抄写的状态数字」是否仍等于真相源。
#
# 为什么需要它：状态数字（59/61、172/172）被手抄进过多份文档，必然漂移，
# 而漂移没人能发现（根 README 曾停在 25/60 长达一周）。本脚本把「文档过期」
# 从「靠人记得更新」变成「跑一下就红」。
#
# 真相源只有两个，都不在文档里：
#   ① 归档 tasks.md 的方括号        → 59 / 61
#   ② verify-all.sh 的预期断言数之和 → 209
#
# 判定规则：
#   - 分母 = 61  → 分子必须 = 已完成数（59）
#   - 分母 = 209 → 分子必须 = 209（全通过写法）
#   - 其它分数一律放过：403/404、8080/3306、400/413 不是状态数字
#   - 命中「已知过期说法」黑名单 → 判红（旧快照 25/60 靠这条抓）
#   - 文件级 <!-- status:historical --> → 整份跳过；行尾加同一标记 → 只跳过该行
#
# ⚠️ 能力边界（别高估它）：抓得住「数字漂移」，抓不住「叙述过期」。
#    有人写「上传还没做」但不写数字，脚本看不见 —— 那条只能靠纪律：
#    文档里不写状态，只写指针（唯一真相源 = docs/课程进度与后续路线.md）。
#
# ⚠️ 实现注意（改这个脚本前必读，踩过三次）：
#   1. MSYS/Git Bash 下**嵌套进程替换 `< <(...)` 会死锁**，一律用临时文件。
#   2. Windows 起一个进程约 150ms；按文件起 grep = 数百次进程 = 几十秒。
#      所以全文只跑**一个 awk**，文件列表由 awk 自己 getline 读（也顺带解决路径带空格）。
#   3. 输出落到临时文件再 cat，别直接管道（要拿退出码和计数）。
#
# 用法：bash scripts/check-docs-freshness.sh     退出码：0 绿 / 1 红 / 2 环境错

set -uo pipefail
export PATH="/usr/bin:/bin:/mingw64/bin:$PATH"

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WS="$(cd "$REPO/.." && pwd)"
TMP="$REPO/.tmp"; mkdir -p "$TMP"

# ───────── 真相源 ─────────
TASKS_FILE="$(ls -1 "$REPO"/openspec/changes/archive/*/tasks.md 2>/dev/null | sort | tail -1)"
if [ -z "${TASKS_FILE:-}" ]; then echo "❌ 找不到归档的 tasks.md，真相源缺失" >&2; exit 2; fi
TASKS_DONE=$(grep -c '^[[:space:]]*- \[x\]' "$TASKS_FILE")
TASKS_TOTAL=$(grep -c '^[[:space:]]*- \[.\]' "$TASKS_FILE")

if [ ! -f "$REPO/scripts/verify-all.sh" ]; then echo "❌ 找不到 verify-all.sh" >&2; exit 2; fi
# run 调用的第 2 个参数：纯数字 = 期望断言数，"-" = 不计数
TOTAL_ASSERT=$(grep -E '^[[:space:]]*run "' "$REPO/scripts/verify-all.sh" \
  | grep -oE '"[0-9]+"' | tr -d '"' | awk '{s+=$1} END{print s+0}')

echo "── 真相源 ─────────────────────────────"
printf '  tasks.md : %s / %s   （%s）\n' "$TASKS_DONE" "$TASKS_TOTAL" "${TASKS_FILE#$WS/}"
printf '  断言合计 : %s\n' "$TOTAL_ASSERT"

# ───────── 待扫文件清单 ─────────
find "$WS" -name '*.md' \
  -not -path '*/node_modules/*' \
  -not -path '*/.workbuddy/*' \
  -not -path '*/.git/*' \
  -not -path '*/_归档文档/*' \
  -not -path '*/openspec/changes/archive/*' \
  2>/dev/null | sort > "$TMP/files.txt"

# 史实标记分两级，都由 awk 判定，不再单独起 grep：
#   文件级 = 标记出现在**首行** → 整份跳过（当轮史实快照用这个）
#   行级   = 标记出现在行尾     → 只跳该行（真相源文档里的"当时是 X"叙述用这个）
# ⚠️ 不能"文件里出现了标记就整份跳过"——那样真相源文档会被自己跳过，等于没校验。

# ───────── 已知过期说法黑名单（增量维护：发现一次加一条）─────────
BANNED=(
  "25 / 60"
  "25/60"
  "显式 501"
  "步 5：上传入库"
  "⏳ 未做 —— 归档前"
)
BANNED_JOINED="$(printf '%s\n' "${BANNED[@]}" | paste -sd'|' -)"

echo "── 扫描文档 ───────────────────────────"

# ───────── 单进程扫全部文件 ─────────
awk -v done="$TASKS_DONE" -v tot="$TASKS_TOTAL" -v as="$TOTAL_ASSERT" \
    -v ws="$WS/" -v banned="$BANNED_JOINED" \
    -v listfile="$TMP/files.txt" '
BEGIN {
  nb = split(banned, B, "|")

  while ((getline f < listfile) > 0) {
    rel = (index(f, ws) == 1) ? substr(f, length(ws) + 1) : f
    ln = 0
    while ((getline line < f) > 0) {
      ln++
      # 首行带标记 = 整份史实快照
      if (ln == 1 && index(line, "status:historical") > 0) {
        printf "  ⏭  %s  （首行标记 historical，整份跳过）\n", rel
        break
      }
      if (index(line, "status:historical") > 0) continue
      for (i = 1; i <= nb; i++)
        if (index(line, B[i]) > 0)
          printf "  ❌ %s:%d  命中过期说法「%s」\n", rel, ln, B[i]
      s = line
      while (match(s, /[0-9]+[[:space:]]*\/[[:space:]]*[0-9]+/)) {
        p = substr(s, RSTART, RLENGTH)
        split(p, ab, "/")
        gsub(/[[:space:]]/, "", ab[1]); gsub(/[[:space:]]/, "", ab[2])
        a = ab[1] + 0; b = ab[2] + 0
        if      (b == tot && a != done) printf "  ❌ %s:%d  「%s」≠ 真相源 %d/%d\n", rel, ln, p, done, tot
        else if (b == as  && a != as )  printf "  ❌ %s:%d  「%s」≠ 全通过 %d/%d\n", rel, ln, p, as, as
        s = substr(s, RSTART + RLENGTH)
      }
    }
    close(f)
  }
}' > "$TMP/out.txt"

cat "$TMP/out.txt"

CHECKED=$(wc -l < "$TMP/files.txt" | tr -d ' ')
SKIPPED=$(grep -c "⏭" "$TMP/out.txt" 2>/dev/null || true)
FAIL=$(grep -c '❌' "$TMP/out.txt" 2>/dev/null || true)
case "${FAIL:-}" in ''|*[!0-9]*) FAIL=0 ;; esac

echo "── 汇总 ───────────────────────────────"
printf '  扫描 %s 份 · 跳过(historical) %s · 失败 %s\n' "${CHECKED:-0}" "${SKIPPED:-0}" "$FAIL"
if [ "$FAIL" -gt 0 ]; then
  echo "  ❌ 文档状态与真相源不一致。改文档；若该改真相源，得能说明理由。"
  exit 1
fi
echo "  ✅ 全部一致"
exit 0

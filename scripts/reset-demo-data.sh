#!/usr/bin/env bash
# 把演示数据恢复到「只有种子」的干净状态。
#
# 为什么需要它：§8 的验收证据要能**脱离本机复现**。`verify-upload.sh` 会真实
# 上传若干材料（这是故意的 —— 它要证明两表真的各增一行），跑几轮之后 A 班里
# 就会堆着一批 `ok` / `UPPER` / `traversal`。截图取证前先复位，证据才干净、
# 材料编号才能对上文档。
#
# 判定「种子」的唯一依据是 `stored_name LIKE 'seed-%'`（见 internal/db/seed.go），
# 不按 id 或标题硬编码 —— 那样一旦种子调整，本脚本会误删。
#
# 用法：
#   bash scripts/reset-demo-data.sh          # 打印将要删除的内容，等确认
#   bash scripts/reset-demo-data.sh --yes    # 直接执行
#
# 只删非种子数据，不动 classes / users / 种子材料。

set -uo pipefail

export PATH="/usr/bin:/bin:/mingw64/bin:/c/Users/TANGWENJIAN/.local/bin:$PATH"
cd "$(dirname "$0")/.."

if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

UPLOAD_DIR="${UPLOAD_DIR:?缺 UPLOAD_DIR（应来自 .env）}"
# 2026-10-10 修正：原默认路径 D:/MySQL/... 在本机不存在，且凭证硬编码 root/password、
# stderr 被 2>/dev/null 吞掉——三项叠加导致状态查询静默返回空、删除是否生效无从判断。
# 现从 .env 的 DB_* 读取（脚本头部已 source .env），错误可见。
MYSQL="${MYSQL:-C:/Program Files/MySQL/MySQL Server 8.0/bin/mysql.exe}"
DB_NAME="${DB_NAME:-campusclaw}"

sqlq() {
  # 不吞 stderr：ERROR 文本直接可见（只过滤 mysql 的密码警告行）
  "$MYSQL" -u"${DB_USER:?缺 DB_USER}" -p"${DB_PASSWORD:?缺 DB_PASSWORD}" \
    --default-character-set=utf8mb4 -N -B -e "USE ${DB_NAME}; $1" 2>&1 | grep -v 'Using a password'
}

echo "== 当前状态"
printf '   materials（非种子）         : %s 行\n' "$(sqlq "SELECT COUNT(*) FROM materials WHERE stored_name NOT LIKE 'seed-%';")"
printf '   knowledge_entries（非种子） : %s 行\n' "$(sqlq "SELECT COUNT(*) FROM knowledge_entries WHERE material_id NOT IN (SELECT id FROM materials WHERE stored_name LIKE 'seed-%');")"
printf '   skills / agents / 会话      : %s / %s / %s 行\n' \
  "$(sqlq "SELECT COUNT(*) FROM skills;")" \
  "$(sqlq "SELECT COUNT(*) FROM agents;")" \
  "$(sqlq "SELECT COUNT(*) FROM conversations;")"
printf '   uploads 目录文件            : %s 个\n' "$(ls -1 "$UPLOAD_DIR" 2>/dev/null | wc -l | tr -d ' ')"

echo
echo "== 将被删除"
sqlq "SELECT CONCAT('   materials id=', id, '  ', title) FROM materials WHERE stored_name NOT LIKE 'seed-%';"
for f in "$UPLOAD_DIR"/*; do
  [ -e "$f" ] || continue
  case "$(basename "$f")" in
    seed-*) ;;
    *) echo "   $f" ;;
  esac
done
echo "   （以上材料与其知识库正文）"

if [ "${1:-}" != "--yes" ]; then
  echo
  echo "以上内容将被删除（预览模式，未删除任何东西）。确认请重跑：VERIFY_ALL_WIPE=1 bash scripts/reset-demo-data.sh --yes"
  exit 0
fi

# 2026-10-10 事故新增的第二道锁：仅 --yes 不再够——曾有验收编排"顺手"清掉
# 用户真实上传的材料。--yes 必须搭配显式授权（--wipe 参数或 VERIFY_ALL_WIPE=1）。
if [ "${VERIFY_ALL_WIPE:-0}" != "1" ] && ! printf '%s\n' "$@" | grep -q -- '--wipe'; then
  echo
  echo "⛔ 拒绝执行：本脚本会删除全部非种子材料、知识库正文与上传文件。"
  echo "   （verify-all 曾借此反复清空真实数据，且当时 uploads 不在 git 里无法找回）"
  echo "   确认要删：VERIFY_ALL_WIPE=1 bash scripts/reset-demo-data.sh --yes"
  exit 1
fi

echo
echo "== 执行"
# knowledge_chunks / knowledge_entries 必须先删 —— 对 materials 有外键（未设 CASCADE）。
# refresh_tokens 独立于材料，清空以复位登录态（add-refresh-token）。
sqlq "DELETE FROM knowledge_chunks;"
sqlq "DELETE FROM knowledge_entries WHERE material_id NOT IN (SELECT id FROM materials WHERE stored_name LIKE 'seed-%');"
sqlq "DELETE FROM materials WHERE stored_name NOT LIKE 'seed-%';"
sqlq "DELETE FROM refresh_tokens;"

# 第 5 课运行期数据：技能 / 智能体 / 会话 / 消息 / 班级提示词。
# 它们同样是「非种子」—— apitest 每跑一轮就会留下 skills/agents/conversations，
# 不清掉的话演示界面里会堆满上一轮的「助手-N」，截图与文档的编号也对不上。
# 删除顺序按外键依赖来：消息 → 会话 → 智能体关联 → 智能体 / 技能。
sqlq "DELETE FROM messages;"
sqlq "DELETE FROM conversations;"
sqlq "DELETE FROM agent_skills;"
sqlq "DELETE FROM agent_materials;"
sqlq "DELETE FROM agents;"
sqlq "DELETE FROM skills;"
sqlq "DELETE FROM class_prompts;"

# 把自增值拨回种子之后，让**每次复位后的新增材料 id 都一样**（种子占 1–4 →
# 下次新增从 5 开始）。不拨回去的话 AUTO_INCREMENT 只增不减，
# 跑几轮之后同一个场景的材料 id 变成 39、87…，截图与文档里的编号就对不上了。
# 第 5 课几张表同理：编号稳定，证据才可复现。
sqlq "ALTER TABLE materials AUTO_INCREMENT = 5;"
sqlq "ALTER TABLE knowledge_entries AUTO_INCREMENT = 5;"
sqlq "ALTER TABLE knowledge_chunks AUTO_INCREMENT = 1;"
sqlq "ALTER TABLE skills AUTO_INCREMENT = 1;"
sqlq "ALTER TABLE agents AUTO_INCREMENT = 1;"
sqlq "ALTER TABLE conversations AUTO_INCREMENT = 1;"
sqlq "ALTER TABLE messages AUTO_INCREMENT = 1;"

# Qdrant 集合同步清空（add-class-scoped-retrieval tasks 2.4）。
# 不可达时打印警告而非失败——keyword 路与 MySQL 复位不受影响。
QDRANT_URL="${QDRANT_URL:-http://127.0.0.1:6333}"
QDRANT_COLLECTION="${QDRANT_COLLECTION:-campusclaw_chunks}"
if curl -s --noproxy '*' --max-time 3 -X POST "$QDRANT_URL/collections/$QDRANT_COLLECTION/points/delete" \
     -H 'Content-Type: application/json' -d '{"filter":{"must":[]}}' > /dev/null 2>&1; then
  echo "   Qdrant 集合 $QDRANT_COLLECTION 已清空"
else
  echo "   ⚠️ Qdrant（$QDRANT_URL）不可达，集合未清空——不影响 MySQL 侧复位"
fi
echo "   注：种子切片已一并清空，重启后端后由启动补索引自动重建"

removed_files=0
for f in "$UPLOAD_DIR"/*; do
  [ -e "$f" ] || continue
  case "$(basename "$f")" in
    seed-*) ;;
    *) rm -f "$f" && removed_files=$((removed_files + 1)) ;;
  esac
done

echo
echo "== 复位后"
printf '   materials 行数 : %s（应为 4）\n' "$(sqlq "SELECT COUNT(*) FROM materials;")"
printf '   knowledge 行数 : %s（应为 4）\n' "$(sqlq "SELECT COUNT(*) FROM knowledge_entries;")"
printf '   chunks 行数    : %s（复位后由服务端补索引重建）\n' "$(sqlq "SELECT COUNT(*) FROM knowledge_chunks;")"
printf '   skills / agents / 会话      : %s / %s / %s（应为 0 / 0 / 0）\n' \
  "$(sqlq "SELECT COUNT(*) FROM skills;")" \
  "$(sqlq "SELECT COUNT(*) FROM agents;")" \
  "$(sqlq "SELECT COUNT(*) FROM conversations;")"
printf '   uploads 文件   : %s 个（应为 4，均为 seed-*）\n' "$(ls -1 "$UPLOAD_DIR" 2>/dev/null | wc -l | tr -d ' ')"
printf '   本次删除磁盘文件 %s 个\n' "$removed_files"

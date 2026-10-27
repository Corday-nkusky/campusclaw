#!/usr/bin/env bash
# 用 root 身份创建 campusclaw 数据库与应用账号。
#
# 用法：
#   ./scripts/init-db.sh <root口令> <应用账号口令>
#
# 说明：应用账号**不是** root —— Compose 里的 api 服务同理，
# 管理员口令不下发给业务进程（spec.md R8.3）。

set -euo pipefail

# mysql 客户端路径按候选探测（本机实际在 C:/Program Files 下）。
if [ -z "${MYSQL_BIN:-}" ]; then
  for cand in \
    "C:/Program Files/MySQL/MySQL Server 8.0/bin/mysql.exe" \
    "D:/MySQL/MySQL Server 8.0/bin/mysql.exe" \
    "$(command -v mysql 2>/dev/null)"; do
    [ -n "$cand" ] && [ -x "$cand" ] && { MYSQL_BIN="$cand"; break; }
  done
fi
MYSQL_BIN="${MYSQL_BIN:?找不到 mysql 客户端（可用 MYSQL_BIN=... 指定）}"
ROOT_PASS="${1:?用法: init-db.sh <root口令> <应用账号口令>}"
APP_PASS="${2:?用法: init-db.sh <root口令> <应用账号口令>}"
APP_USER="${APP_USER:-campusclaw}"
DB_NAME="${DB_NAME:-campusclaw}"

echo "== 使用 mysql 客户端：$MYSQL_BIN"
"$MYSQL_BIN" --version

echo "== 创建数据库 $DB_NAME 与账号 $APP_USER"
"$MYSQL_BIN" -h 127.0.0.1 -P 3306 -u root -p"$ROOT_PASS" <<SQL
CREATE DATABASE IF NOT EXISTS \`$DB_NAME\`
  CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

CREATE USER IF NOT EXISTS '$APP_USER'@'localhost' IDENTIFIED BY '$APP_PASS';
CREATE USER IF NOT EXISTS '$APP_USER'@'127.0.0.1' IDENTIFIED BY '$APP_PASS';

GRANT ALL PRIVILEGES ON \`$DB_NAME\`.* TO '$APP_USER'@'localhost';
GRANT ALL PRIVILEGES ON \`$DB_NAME\`.* TO '$APP_USER'@'127.0.0.1';
FLUSH PRIVILEGES;
SQL

echo "== 校验：用应用账号连一次"
"$MYSQL_BIN" -h 127.0.0.1 -P 3306 -u "$APP_USER" -p"$APP_PASS" -e "SELECT DATABASE() AS db, CURRENT_USER() AS who;" "$DB_NAME"

echo "✅ 完成"

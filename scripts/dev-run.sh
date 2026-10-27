#!/usr/bin/env bash
# 本机开发启动：加载 .env → 运行后端（迁移与种子在进程内自动完成）。
#
# 用法：
#   bash scripts/dev-run.sh
#
# 前置：
#   1) MySQL 服务在跑（Windows：`net start MySQL80`，需管理员窗口）
#   2) 已执行 scripts/init-db.sh 建库建账号
#   3) 已 cp .env.example .env 并填好真实值
#
# 说明：本脚本**不做任何默认值兜底**。缺 .env 或缺必填项
#      由 Go 侧的 config.Load 报错退出（spec.md R7.1）。

set -euo pipefail

cd "$(dirname "$0")/.."

if [ ! -f .env ]; then
  echo "❌ 缺少 .env —— 先执行：cp .env.example .env，再填入真实值" >&2
  exit 1
fi

# .env 是 shell 语法（含空格的值必须带引号），直接 source。
# 不用 set -a 的替代写法，保持与生产「环境变量注入」语义一致。
set -a
# shellcheck disable=SC1091
. ./.env
set +a

mkdir -p "${UPLOAD_DIR}"

if [ ! -x ./backend/bin/server.exe ]; then
  echo "== 未找到 ./backend/bin/server.exe，先编译"
  ( cd backend && go build -o bin/server.exe ./cmd/server )
fi

echo "== 启动后端（Ctrl+C 停止）"
exec ./backend/bin/server.exe

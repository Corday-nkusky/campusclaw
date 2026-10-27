#!/usr/bin/env bash
# 复位后重启后端：让种子材料的切片重建出来。
#
# 为什么存在：种子补索引**只在启动时**执行（indexer 对无切片行的材料补齐），
# 而 reset-demo-data.sh 会清空 knowledge_chunks 并清 Qdrant 集合。复位之后
# 若不重启后端，库是「有材料、没切片」的空转状态 —— 关键词/向量/混合检索
# 对种子内容全部 0 命中，verify-isolation 的 B 班搜索哨兵和 verify-retrieval
# 的 14 条数据断言会全红（2026-10-07 实测踩过）。
#
# 用法：bash scripts/restart-backend.sh
# 退出码：0 = 后端已重启且 /health 返回 200；1 = 30s 内未就绪。

set -uo pipefail

export PATH="/usr/bin:/bin:/mingw64/bin:/c/Users/TANGWENJIAN/.local/bin:$PATH"
cd "$(dirname "$0")/.."

if [ -f .env ]; then
  set -a
  # shellcheck disable=SC1091
  . ./.env
  set +a
fi

# Git Bash 下 Windows 内建工具的开关要用双斜杠，否则被当路径转译
taskkill //F //IM server.exe >/dev/null 2>&1 || true
sleep 1

# 服务端日志落盘（.verify-logs/backend-restart.log），丢了 /dev/null 就查不了
# 「补索引为什么没跑/为什么失败」——2026-10-07 排障时吃过的亏。
mkdir -p .verify-logs
nohup ./backend/bin/server.exe > .verify-logs/backend-restart.log 2>&1 &
disown || true

# 探测不写 /dev/null：Windows 原生 curl 对 /dev/null 偶发 CURLE_WRITE_ERROR，
# 写文件再比状态码才稳（verify-all 前置检查同款教训）。
probe_file=".verify-logs/_restart_probe.txt"
for _ in $(seq 1 30); do
  code="$(curl -s -o "$probe_file" -w '%{http_code}' --max-time 2 --noproxy '*' \
    "http://127.0.0.1:${API_PORT:-8080}/health" 2>/dev/null)"
  if [ "$code" = "200" ]; then
    echo "  backend 已重启，/health 200（种子补索引随启动完成）"
    exit 0
  fi
  sleep 1
done

echo "  ❌ backend 重启后 30s 内 /health 未就绪" >&2
exit 1

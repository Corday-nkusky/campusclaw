#!/usr/bin/env bash
# §7 Compose 配置的**静态**校验。
#
# 为什么需要它：R8.1 / R8.2 / R8.5 要求 `docker compose up` 实跑留证，但本机
# **没有安装 Docker**（只有安装包，未安装），`docker compose config` 都跑不了。
# 于是把「不依赖 Docker 也能核」的部分抽出来做成可复现的机器判定：
#
#   · compose.yaml 是合法 YAML，且四服务齐备（api/db/qdrant/web）
#   · R8.3：只有 web 写 ports；db / api / qdrant 都不映射宿主端口
#   · R7.3：MYSQL_ROOT_PASSWORD 只出现在 db，不下发给 api
#   · R7.3/8.3：检索九项 env 齐备，两个 KEY 不给弱默认值
#   · R8.2：db-data / uploads / qdrant-data 都是命名卷（down 不加 -v 数据仍在的结构前提）
#   · 不缺必填环境变量（都以 ${VAR:?} 的形式声明）
#   · 反代配置里的三条硬约束：client_max_body_size ≥ 应用上限、
#     X-Forwarded-For 是赋等而非追加、/uploads 未对外暴露
#
# 这些只证明「配置写对了」，**不证明「跑得起来」**。实跑留证见 deploy/README.md。
#
# 用法：bash scripts/verify-compose-config.sh
# 退出码：0 = 全部通过；1 = 有失败项。

set -uo pipefail

export PATH="/usr/bin:/bin:/mingw64/bin:/c/Users/TANGWENJIAN/.local/bin:$PATH"
cd "$(dirname "$0")/.."

# Python 必须带 PyYAML：候选逐个探测 `import yaml`，谁行用谁。
# 踩过：写死 workbuddy 的 3.13.12 后它没装 yaml，三个解析段全炸成
# ModuleNotFoundError，把「配置其实是对的」判成红（实测 2026-10-07）。
py_has_yaml() { "$1" -c 'import yaml' >/dev/null 2>&1; }
PY=""
for cand in \
  "C:/Users/TANGWENJIAN/.workbuddy/binaries/python/versions/3.13.12/python.exe" \
  "D:/code/anaconda/envs/pytorch_env/python.exe" \
  "$HOME/anaconda3/python.exe" \
  "$USERPROFILE/anaconda3/python.exe" \
  "$(command -v python 2>/dev/null)"; do
  [ -n "$cand" ] && [ -x "$cand" ] && py_has_yaml "$cand" && { PY="$cand"; break; }
done
if [ -z "$PY" ]; then
  printf '  ❌ 找不到带 PyYAML 的 Python 解释器（探测了 workbuddy / pytorch_env / anaconda3 / PATH）\n'
  exit 1
fi

pass=0
fail=0
ok()  { printf '  ✅ %s\n' "$1"; pass=$((pass+1)); }
bad() { printf '  ❌ %s\n' "$1"; fail=$((fail+1)); }
say() { printf '\n=== %s\n' "$1"; }

for f in deploy/compose.yaml deploy/api.Dockerfile deploy/web.Dockerfile deploy/nginx.conf; do
  [ -f "$f" ] && ok "存在 $f" || bad "缺 $f"
done
[ "$fail" = "0" ] || { printf '\n  配置文件不齐，后续检查跳过。\n'; exit 1; }

say "1. compose.yaml 结构（用 Python 解析，不依赖 Docker）"
"$PY" - <<'PY'
import sys, yaml
d = yaml.safe_load(open('deploy/compose.yaml', encoding='utf-8'))
svc = d.get('services', {})
problems = []
if sorted(svc) != ['api', 'db', 'qdrant', 'web']:
    problems.append(f'服务集合应为 api/db/qdrant/web，实际 {sorted(svc)}')
vols = set(d.get('volumes', {}))
for v in ('db-data', 'uploads', 'qdrant-data'):
    if v not in vols:
        problems.append(f'缺命名卷 {v}（R8.2 的结构前提）')
if problems:
    for p in problems:
        print(f'  ❌ {p}')
    sys.exit(1)
print(f'  服务：{sorted(svc)}')
print(f'  命名卷：{sorted(vols)}')
print(f'  网络：{sorted(d.get("networks", {}))}')
PY
[ $? -eq 0 ] && ok "YAML 可解析，四服务 + 三个命名卷齐备" || bad "compose.yaml 结构不符"

say "2. R8.3 只有 web 映射宿主端口"
"$PY" - <<'PY'
import sys, yaml
d = yaml.safe_load(open('deploy/compose.yaml', encoding='utf-8'))
svc = d['services']
ok = True
for name in ('db', 'api', 'qdrant'):
    if 'ports' in svc[name]:
        print(f'  ❌ {name} 写了 ports：{svc[name]["ports"]} —— 违反 R8.3')
        ok = False
    else:
        print(f'  {name}: 无 ports（符合 R8.3）')
if 'ports' not in svc['web']:
    print('  ❌ web 没有 ports，宿主根本访问不到')
    ok = False
else:
    print(f'  web: ports={svc["web"]["ports"]}')
sys.exit(0 if ok else 1)
PY
[ $? -eq 0 ] && ok "只有 web 映射宿主端口" || bad "端口映射不符合 R8.3"

say "3. R7.3 管理员口令不下发给 api"
"$PY" - <<'PY'
import sys, yaml
d = yaml.safe_load(open('deploy/compose.yaml', encoding='utf-8'))
svc = d['services']
db_env = svc['db'].get('environment') or {}
api_env = svc['api'].get('environment') or {}
ok = True
if 'MYSQL_ROOT_PASSWORD' not in db_env:
    print('  ❌ db 未声明 MYSQL_ROOT_PASSWORD')
    ok = False
else:
    print(f'  db: MYSQL_ROOT_PASSWORD = {db_env["MYSQL_ROOT_PASSWORD"]}')
for key in ('MYSQL_ROOT_PASSWORD', 'MYSQL_PASSWORD'):
    if key in api_env:
        print(f'  ❌ api 拿到了 {key} —— 违反 R7.3')
        ok = False
print(f'  api 的 DB 相关键：{sorted(k for k in api_env if k.startswith(("DB_", "MYSQL_")))}')
required = ('DB_HOST', 'DB_PORT', 'DB_NAME', 'DB_USER', 'DB_PASSWORD',
            'SESSION_SECRET', 'UPLOAD_DIR', 'MAX_UPLOAD_BYTES',
            'SEED_TEACHER_PASSWORD', 'SEED_STUDENT_PASSWORD',
            'LOGIN_FAIL_THRESHOLD', 'LOGIN_LOCK_MINUTES', 'API_PORT',
            # 迭代 2 · 检索九项（R8.1：缺任一项 api 启动失败）
            'EMBEDDING_API_BASE', 'EMBEDDING_API_KEY', 'EMBEDDING_MODEL',
            'EMBEDDING_DIM', 'CHAT_API_BASE', 'CHAT_API_KEY', 'CHAT_MODEL',
            'QDRANT_URL', 'QDRANT_COLLECTION')
missing = [k for k in required if k not in api_env]
if missing:
    print(f'  ❌ api 缺必填环境变量：{missing}')
    ok = False
else:
    print(f'  api 必填项 {len(required)} 个齐备')
# QDRANT_URL 必须指向 compose 内部服务名，不能写成宿主端口（R8.3）
if str(api_env.get('QDRANT_URL', '')).strip() != 'http://qdrant:6333':
    print(f'  ❌ QDRANT_URL 应为 http://qdrant:6333（内部服务名），实际 {api_env.get("QDRANT_URL")!r}')
    ok = False
else:
    print('  QDRANT_URL 指向内部服务名 qdrant:6333')
# 必填项应显式声明为 ${VAR:?}，缺了就启动失败，不给弱默认值
weak = [k for k in ('SESSION_SECRET', 'DB_PASSWORD', 'SEED_TEACHER_PASSWORD', 'SEED_STUDENT_PASSWORD',
                    'EMBEDDING_API_KEY', 'CHAT_API_KEY')
        if ':?' not in str(api_env.get(k, '')) and ':?' not in str(db_env.get(k, ''))]
if weak:
    print(f'  ❌ 以下键没有用 ${{VAR:?}} 强制必填：{weak}')
    ok = False
else:
    print('  敏感项均以 ${VAR:?} 声明（缺失即失败，无弱默认值）')
sys.exit(0 if ok else 1)
PY
[ $? -eq 0 ] && ok "口令边界与必填项检查通过" || bad "环境变量配置不符合 R7.3"

say "4. nginx.conf 的三条硬约束"
nginx_sumsize="$(grep -oE 'client_max_body_size[[:space:]]+[0-9]+[a-zA-Z]?' deploy/nginx.conf | head -1)"
printf '     %s\n' "$nginx_sumsize"
"$PY" - <<'PY'
import re, sys
src = open('deploy/nginx.conf', encoding='utf-8').read()
body = src.split('client_max_body_size')[1].split(';')[0].strip() if 'client_max_body_size' in src else ''
m = re.match(r'^(\d+)([kKmMgG]?)$', body)
if not m:
    print(f'  ❌ 读不到 client_max_body_size（原文：{body!r}）')
    sys.exit(1)
n, unit = int(m.group(1)), m.group(2).lower()
limit = n * {'': 1, 'k': 1024, 'm': 1024 ** 2, 'g': 1024 ** 3}[unit]
APP = 2097152  # .env.example 里的 MAX_UPLOAD_BYTES 默认值
print(f'  nginx 上限 = {limit} 字节；应用上限 = {APP} 字节')
if limit < APP:
    print('  ❌ Nginx 上限小于应用上限 → 1~2MiB 的文件会被 Nginx 先拦成 413')
    sys.exit(1)
print('  ✅ Nginx 上限 ≥ 应用上限（应用才有机会返回自己的 payload_too_large）')
PY
[ $? -eq 0 ] && ok "client_max_body_size 不会先于应用拦掉合法上传" || bad "client_max_body_size 太小"

NGINX_CODE="$(grep -vE '^[[:space:]]*#' deploy/nginx.conf)"
grep -qE 'proxy_set_header[[:space:]]+X-Forwarded-For[[:space:]]+\$remote_addr' <<<"$NGINX_CODE" \
  && ok "X-Forwarded-For 用赋等（\$remote_addr），不是 \$proxy_add_x_forwarded_for 追加" \
  || bad "X-Forwarded-For 写法可疑 —— 追加写法可被客户端伪造 IP 绕过限流"
# 只看**非注释行**：注释里解释「为什么不用它」不算违规（踩过这个假红）。
grep -q 'proxy_add_x_forwarded_for' <<<"$NGINX_CODE" \
  && bad "仍出现 \$proxy_add_x_forwarded_for" || ok "代码中未使用 \$proxy_add_x_forwarded_for"

grep -q 'location /uploads/' <<<"$NGINX_CODE" && ok "/uploads/ 被显式堵死（R5.2 第二道口子）" \
  || bad "nginx 未处理 /uploads/"

say "5. Dockerfile 卫生"
grep -qE '^USER[[:space:]]+app' deploy/api.Dockerfile && ok "api 以非 root 用户运行" || bad "api 未降权运行"
grep -q 'CGO_ENABLED=0' deploy/api.Dockerfile && ok "api 静态编译（CGO_ENABLED=0）" || bad "api 未静态编译"
grep -q 'npm ci' deploy/web.Dockerfile && ok "web 用 npm ci（锁定依赖版本）" || bad "web 未用 npm ci"
grep -qE 'COPY[[:space:]]+.*\.env' deploy/api.Dockerfile deploy/web.Dockerfile 2>/dev/null \
  && bad "Dockerfile 拷贝了 .env" || ok "Dockerfile 未拷贝 .env（密钥不进镜像）"

say "6. .dockerignore 覆盖检查"
grep -q '^\.env' .dockerignore && ok ".env 已被 .dockerignore 排除" || bad ".env 未排除"
grep -q 'uploads/' .dockerignore && ok "uploads/ 已被排除（运行时挂卷）" || bad "uploads/ 未排除"

say "7. 实际可跑性（本机无 Docker，仅作如实记录）"
if command -v docker >/dev/null 2>&1; then
  if docker info >/dev/null 2>&1; then
    ok "docker 可用 —— 可以进一步跑 deploy/README.md 里的 up/down 验证"
  else
    bad "docker 命令存在但守护进程不可用（docker info 失败）"
  fi
else
  printf '     docker 不在 PATH，跳过。\n'
  printf '     ⚠️ 结论：**R8.1 / R8.2 / R8.5 的实跑留证尚未完成**，本脚本只覆盖静态配置。\n'
fi

say "汇总"
printf '  通过 %d 项，失败 %d 项\n' "$pass" "$fail"
exit $((fail == 0 ? 0 : 1))

# deploy/ —— 标准启动方式（R8）

三个服务：`web` / `api` / `db`。**只有 `web` 映射宿主端口**（R8.3）。

## 用法

```bash
# 1) 配置。compose 在**本文件所在目录**找 .env
cp .env.example deploy/.env
#    再往 deploy/.env 里补两行 compose 专属变量：
#      MYSQL_ROOT_PASSWORD=<给 MySQL root 的强口令>
#      WEB_PORT=8080            # 可选，默认 8080

# 2) 启动（在仓库根目录执行）
docker compose -f deploy/compose.yaml up -d

# 3) 打开 http://127.0.0.1:8080 —— 看到登录页即成功
```

停止与数据：

```bash
docker compose -f deploy/compose.yaml down      # 卷保留 → 再次 up 数据仍在（R8.2）
docker compose -f deploy/compose.yaml down -v   # ⚠️ 连卷一起删，数据没了
```

## 为什么 compose.yaml 里要写 `-f deploy/compose.yaml`

compose 默认在当前目录找 `compose.yaml`。本项目的 compose 文件放在 `deploy/`
（与两个 Dockerfile、nginx.conf 同处），build context 写的是 `..`（仓库根），
因为两个镜像都要同时看到 `backend/` / `frontend/` 和 `deploy/`。

## 验证清单（对应 tasks.md §7.5）

| 检查 | 命令 | 期望 |
| --- | --- | --- |
| 干净目录可启动、可见登录页（R8.1） | `docker compose -f deploy/compose.yaml up -d` 后浏览器打开 `http://127.0.0.1:8080` | 登录页 |
| 未登录 `/health` 返回 200（R8.1） | `curl -i http://127.0.0.1:8080/health` | `200 {"status":"ok"}` |
| 数据卷可存活（R8.2） | `down` → `up`（**不加 `-v`**）→ 重新登录看材料列表 | 材料仍在 |
| 只有 web 映射端口（R8.3） | `docker compose -f deploy/compose.yaml ps` | 只有 web 的 PORTS 列有 `0.0.0.0:…` |

## ⚠️ 本机未完成的部分（如实记录）

**本机没有安装 Docker**（只有 `D:/tools/Docker Desktop Installer.exe` 安装包，
未安装），因此：

- **R8.1 / R8.2 的 `up` / `down` 实跑留证尚未完成**；
- `docker compose config` 也没跑过，`golang:1.27-alpine` / `nginx:1.27-alpine` /
  `node:22-alpine` / `mysql:8.0` 这几个标签是否都存在**未验证**；
- 已能做的部分做成了机器判定：`bash scripts/verify-compose-config.sh`
  **17 项全通过**（YAML 可解析、三服务齐备、R8.3 端口边界、R7.3 口令边界、
  nginx 三条硬约束、Dockerfile 降权与卫生、.dockerignore 覆盖）。
  它只证明**配置写对了**，不证明**跑得起来**。

装好 Docker 后按上面的验证清单跑一遍，把命令与输出贴进迭代说明即可补齐。

## 配置里几个不显眼但会真出问题的点

1. **`client_max_body_size 4m`（nginx.conf）**
   Nginx 默认只有 `1m`，而应用侧 `MAX_UPLOAD_BYTES` 默认 2 MiB。
   不改这一行，1~2 MiB 的文件在应用看来完全合法，却永远传不进来 ——
   客户端拿到的是 Nginx 的 HTML 错误页，而不是应用的
   `{"error":"payload_too_large","limit_bytes":…}`。
   两个上限必须满足：**Nginx ≥ 应用**。

2. **`X-Forwarded-For` 用赋等而不是追加**
   应用的登录失败限流键是 `用户名 + clientIP()`，而 `clientIP()` 无条件采信
   XFF 的第一个值。用 `$proxy_add_x_forwarded_for`（在客户端带来的值后面追加）
   会让攻击者自带一个 XFF 头就伪造出任意 IP，按 IP 的限流直接失效。
   赋等 `$remote_addr` 是安全的 —— 前提是 api 没有任何其它入口，
   这一点由 R8.3（api 不映射宿主端口）保证。

3. **`uploads` 必须是命名卷**
   上传件落盘 + 元数据落库，两者必须同生共死。若上传目录不进卷，
   `down` 之后数据库行还在、文件没了，表现为「列表里有材料，点下载 404」。

4. **db 的 healthcheck 必须用 `CMD-SHELL`**
   `CMD` 是 exec 形式、不经过 shell，`$MYSQL_ROOT_PASSWORD` 不会被展开，
   `mysqladmin` 会收到字面量 `$MYSQL_ROOT_PASSWORD` 而一直失败。

5. **应用侧不会因 DB 不可用而退出**（R8.4）
   `depends_on: service_healthy` 只解决启动顺序。即便 DB 后挂，
   api 仍然存活、`/health` 仍 200、`/api/*` 返回 503，并且 DB 恢复后
   业务接口自动恢复（实测见 `scripts/verify-health-decoupled.sh` 场景 B）。

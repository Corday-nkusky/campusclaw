# CampusClaw · API 镜像（多阶段构建）
#
# 构建上下文是**仓库根目录**（不是 backend/），因为这两个 Dockerfile 都要
# 同时看到 backend/ 与 deploy/。compose.yaml 里写的是：
#     build:
#       context: ..
#       dockerfile: deploy/api.Dockerfile
#
# ── 关于 golang 标签 ────────────────────────────────────────────
# 标签版本必须 ≥ backend/go.mod 里的 `go` 指令（当前是 1.27），否则 go 会拒绝构建。
# ⚠️ 本文件**未在本机实跑验证**（本机没装 Docker，见 deploy/README.md 的说明）。
#    若该标签不存在，换成同大版本或更高的 `golang:<版本>-alpine` 即可。
FROM golang:1.27-alpine AS build

WORKDIR /src

# 先只拷依赖清单，再 go mod download —— 这样只要 go.mod/go.sum 没变，
# 改业务代码不会让依赖层缓存失效。
COPY backend/go.mod backend/go.sum ./
RUN go mod download

COPY backend/ ./

# CGO_ENABLED=0：产出静态链接的二进制，才能跑在 scratch/alpine 上；
# -trimpath 去掉构建机路径，-s -w 去掉符号表与调试信息（镜像更小）。
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath -ldflags="-s -w" \
        -o /out/server ./cmd/server

# ── 运行层 ─────────────────────────────────────────────────────
FROM alpine:3.20

# ca-certificates：将来要出网调 HTTPS 时需要，先装上；
# tzdata：日志时间戳按东八区读起来才不别扭。
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -g 10001 -S app \
 && adduser -u 10001 -S app -G app

WORKDIR /app

COPY --from=build /out/server /app/server

# 上传目录由卷挂载（compose 里是命名卷 uploads）。这里先建好并交给 app，
# 否则非 root 身份首次写盘会失败。
RUN mkdir -p /app/uploads && chown -R app:app /app

# 非 root 运行：即便将来某条路径被写穿，也拿不到容器内的 root。
USER app

# 注意：这里 EXPOSE 只是**声明**，不等于映射宿主端口。
# compose 里 api 只出现在内部网络，宿主端口只有 web 映射（R8.3）。
EXPOSE 8080

ENTRYPOINT ["/app/server"]

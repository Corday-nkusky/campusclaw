# CampusClaw · Web 镜像（构建前端静态资源 + Nginx 托管与反代）
#
# 构建上下文同样是**仓库根目录**，因为要同时取 frontend/ 与 deploy/nginx.conf。

FROM node:22-alpine AS build

WORKDIR /app

# 锁文件一起拷，用 npm ci 保证装出来的依赖与 package-lock.json 完全一致
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci

COPY frontend/ ./

# build 脚本是 `tsc --noEmit && vite build`：
# 类型错误会让镜像构建失败 —— 这是刻意的，镜像不应该带着类型错误上线。
RUN npm run build

# ── 运行层 ─────────────────────────────────────────────────────
FROM nginx:1.27-alpine

# 用我们自己的 server 块替换默认站点配置
COPY deploy/nginx.conf /etc/nginx/conf.d/default.conf

COPY --from=build /app/dist /usr/share/nginx/html

# 这是全栈里**唯一**映射宿主端口的服务（R8.3）
EXPOSE 80

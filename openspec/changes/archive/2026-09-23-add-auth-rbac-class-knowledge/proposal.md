# Proposal: add-auth-rbac-class-knowledge（CampusClaw 迭代 1）

## Why

第 2 课把需求写成了规约；本迭代是第一次「按规约落代码」。没有可运行、可隔离、可复现的迭代 1，`openspec/specs/` 会一直是空的，第 4 课的检索增量没有可继承的基线。

本迭代的判定标准不是界面好不好看，而是四件事：**能登录、能隔离、能上传入库、第三方能按 README 从零复现**。

## What Changes

- 新增能力 `auth-upload`：账号口令登录、服务端会话、角色能力边界、按班级隔离的材料读取、教师上传入库。
- 新增 Web 前端（登录页 + 材料页），身份一律以 `GET /api/me` 为准。
- 新增配置外置约定：全部凭据来自环境变量；`.env.example` 入库，`.env` 不入库。
- 新增可复现启动方式：Docker Compose 三服务（web / api / db），**只有 web 映射宿主端口**。

## Impact

**代码**

| 区域 | 变化 |
|---|---|
| `backend/cmd/server` | 新增入口 |
| `backend/internal/{config,db,auth,materials,knowledge}` | 新增 |
| `frontend/` | 新增（Vite + React + TypeScript） |
| `deploy/` | 新增 Dockerfile / compose.yaml / nginx.conf |
| 仓库根 | 新增 `.env.example`、`.gitignore`、`.dockerignore` |

**数据**（新增 5 表）

`classes`、`users`、`materials`、`knowledge_entries`、`sessions`。其中 `materials` 与 `knowledge_entries` 携带租户列 `class_id`，**NOT NULL + 索引**。

**接口**（对外契约）

| 方法与路径 | 鉴权 | 说明 |
|---|---|---|
| `POST /api/login` | 无 | 校验口令、限流、签发会话 |
| `POST /api/logout` | 会话 | 删除服务端会话并清 Cookie |
| `GET /api/me` | 会话 | 返回角色与班级，供前端决定是否渲染上传入口 |
| `GET /api/materials` | 会话 | 按会话班级过滤的列表与搜索 |
| `GET /api/materials/{id}` | 会话 + 班级 | 详情与知识库正文，**跨班 404** |
| `GET /api/materials/{id}/file` | 会话 + 班级 | 下载原文件，**跨班 404** |
| `POST /api/materials` | 会话 + 教师 | 上传入库 |
| `GET /health` | 无 | 存活判定 |

**对外契约（全文一致）**：跨班访问与「资源不存在」返回**完全同形的 404**。此约定同时写进 `README.md`。

## Non-goals（明确不做）

| 不做项 | 否决理由 |
|---|---|
| 知识库问答、向量检索、RAG | 隔离与入库尚未稳定时做问答，会造成跨班数据泄露。本迭代知识库只存可查正文，第 4 课再扩展检索 |
| 对话助手、作业布置与批改、成绩与错题本 | 不在迭代 1 范围 |
| JWT / OAuth / SSO | 会话用服务端 Session Cookie。JWT 常被存进 `localStorage`，XSS 即可读走；载荷里的 `role` / `class_id` 容易被当成授权依据；且无状态 JWT 在过期前难以作废，登出要另加黑名单，等于绕回服务端会话 |
| 注册改密、多校多租户、验证码 / 邮箱 / 短信登录 | 不在本迭代 |
| PDF / Word / 图片解析、在线预览编辑 | 首版只支持 `.txt` / `.md` |
| Kubernetes、CI、公网域名、HTTPS、多副本 HA | 当前单实例 Compose；进程内限流与会话在多副本下会出错 |
| **平台超级管理员** | 见下，单独说明 |

### 为什么本迭代不做平台超级管理员

1. 它与本迭代的隔离验收**直接冲突**——超管天然跨班，而本迭代把「跨班访问与记录不存在返回同一个 404」当作隔离成立的证据。先放进一个可合法跨班的角色，那个 404 就不再能作为证据。
2. 它的风险点与教师/学生不同——教师与学生的越权是「同角色、不同班级」；超管是**系统内唯一允许跨班的角色**，需要操作审计、二次确认与最小化授权，属于另一组规约。
3. 正确顺序：**先让隔离可判定并验收通过，再单独开一次变更引入它**。表结构可预留字段，本迭代不实现入口、不验收。

> 处理越界实现的姿势：先用规约更新把不做项与边界补进 proposal 与 delta，**再删代码**。只删代码不改规约，下一次生成还会把它补回来。

## 技术栈

Go + 标准库 `net/http` + MySQL 8.0 + React/Vite + Nginx + Docker Compose 三服务。

决议过程、备选方案与否决理由见 `design.md`；与第 2 课样板 `design.md`（Flask + SQLite）的冲突处理，见 `design.md` 的「技术栈决议」一节。

# CampusClaw · 项目约定

- 课程：《互联网软件开发技术与实践》（北京大学 · 张齐勋），教学案例 CampusClaw
- 学期仓：本目录（独立 git 仓，根仓 `Internet software development` 不收录）
- 本迭代能力：`auth-upload`（迭代 1：认证授权 + 材料上传入库 + 班级隔离）

## 目录约定

```
openspec/
├── project.md                        ← 本文件
├── specs/                            ← 长期真相源（source of truth），只由 Archive 更新
│   └── <capability>/spec.md
└── changes/
    ├── <change>/                     ← 活动变更（四件套）
    │   ├── proposal.md
    │   ├── design.md
    │   ├── tasks.md
    │   └── specs/<capability>/spec.md   ← delta（ADDED / MODIFIED / REMOVED）
    └── archive/YYYY-MM-DD-<change>/  ← 归档
```

**边界纪律**：Apply 期间只动 `changes/`。`openspec/specs/` 只由归档更新——一旦回写主规约，delta 就失去比对基线，归档时无法判断本次究竟改了什么。

## 术语

**Harness**：使模型能够作为智能体行动的那层系统——把输入交给模型、编排工具调用、把运行结果回传。业界概括为「除了模型，其余都是 harness」。Cursor、Codex、Claude Code 都是 harness；同一模型配不同 harness，行为差异主要来自 harness。本仓的 `AGENTS.md` 与 change 四件套**属于 harness 的一部分，不是参考意见**。

## 责任划分（课件原文要点）

| 事项 | 归属 |
|---|---|
| 范围 | 由 `proposal.md` 的 What Changes 与 Non-goals 决定 |
| 规约含糊时 | **先改规约**（走 delta），不得让模型自行裁量 |
| 「是否合格」 | 由 Scenario 与运行结果判定，**不由模型自己下的结论判定** |
| 产出责任 | 提交人对产出负责；审查责任不因使用工具而转移 |

## 判定语料的事实分级

本仓文档中的陈述分三级，避免把推断当结论：

- **【教材原文】** 课件里写明的要求
- **【我方定义】** 本仓自行补充、课件未给出的约定（编号、字段名、脚本实现等）
- **【未验证】** 尚未在本机实测，或缺少复核依据的陈述

## 本机环境（2026-09-23 实测）

| 组件 | 状态 |
|---|---|
| Go | 待装（教程要求；本机原本无 `go`） |
| MySQL | 8.0.41，装在 `D:\MySQL\MySQL Server 8.0`，服务 `MySQL80` 存在但 **STOPPED**；启动需管理员提权；数据目录 ACL 专属 `NetworkService` |
| Docker / Compose | **未安装**；`wsl -l -v` 无发行版 → Docker Desktop 前置未满足 |
| Node | 22.22.2 可用；npm 10.9.7 |

package db

import (
	"database/sql"
	"fmt"
)

// Migrate 建表。
//
// 约束要点（spec.md R6.4）：
//   - materials / knowledge_entries 携带租户列 class_id，**NOT NULL + 索引**。
//     非空约束防止出现无归属数据；索引支撑按班查询，也是第 4 课检索的前置条件。
//   - users.role 用 ENUM 限定为 teacher / student 两种，不给第三种留口子
//     （平台超级管理员不在本迭代，见 proposal.md Non-goals）。
//
// 迭代 1 的 sessions 表已随 add-token-auth（无状态 JWT）移除；
// add-refresh-token 重新引入一张 **refresh_tokens** 表存放 refresh 状态
// —— 这是本轮唯一新增的服务端认证状态，access 仍不落库。
// 已存在的旧 sessions 表不影响运行，可手动 DROP。
var ddl = []string{
	`CREATE TABLE IF NOT EXISTS classes (
		id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		name        VARCHAR(64)     NOT NULL,
		created_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		UNIQUE KEY uk_classes_name (name)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	`CREATE TABLE IF NOT EXISTS users (
		id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		username      VARCHAR(64)     NOT NULL,
		password_hash VARCHAR(100)    NOT NULL,
		role          ENUM('teacher','student') NOT NULL,
		class_id      BIGINT UNSIGNED NOT NULL,
		created_at    TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		UNIQUE KEY uk_users_username (username),
		KEY idx_users_class (class_id),
		CONSTRAINT fk_users_class FOREIGN KEY (class_id) REFERENCES classes (id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	`CREATE TABLE IF NOT EXISTS materials (
		id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		class_id    BIGINT UNSIGNED NOT NULL,
		title       VARCHAR(255)    NOT NULL,
		stored_name VARCHAR(128)    NOT NULL,
		size_bytes  BIGINT UNSIGNED NOT NULL,
		uploader_id BIGINT UNSIGNED NOT NULL,
		created_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		UNIQUE KEY uk_materials_stored (stored_name),
		KEY idx_materials_class (class_id),
		CONSTRAINT fk_materials_class FOREIGN KEY (class_id) REFERENCES classes (id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	`CREATE TABLE IF NOT EXISTS knowledge_entries (
		id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		material_id BIGINT UNSIGNED NOT NULL,
		class_id    BIGINT UNSIGNED NOT NULL,
		body        MEDIUMTEXT      NOT NULL,
		created_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		KEY idx_knowledge_class (class_id),
		KEY idx_knowledge_material (material_id),
		CONSTRAINT fk_knowledge_material FOREIGN KEY (material_id) REFERENCES materials (id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	// knowledge_chunks：检索层切片表（add-class-scoped-retrieval design.md 数据模型）。
	//   - index_status 默认 'failed'：只有嵌入与写 Qdrant 全部成功后才置 'ready'；
	//   - FULLTEXT ... WITH PARSER ngram 支撑关键字路（ngram 为内置插件，token_size 默认 2）；
	//   - 建表失败（如 ngram 不可用）会让 Migrate 返回错误 → 启动失败，不静默退化。
	`CREATE TABLE IF NOT EXISTS knowledge_chunks (
		id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		material_id  BIGINT UNSIGNED NOT NULL,
		class_id     BIGINT UNSIGNED NOT NULL,
		chunk_index  INT             NOT NULL,
		chunk_text   TEXT            NOT NULL,
		start_offset INT             NOT NULL,
		end_offset   INT             NOT NULL,
		index_status ENUM('ready','failed') NOT NULL DEFAULT 'failed',
		strategy     VARCHAR(16)     NOT NULL DEFAULT 'auto',
		created_at   TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		UNIQUE KEY uk_chunks_material_index (material_id, chunk_index),
		KEY idx_chunks_class (class_id),
		KEY idx_chunks_status (index_status),
		FULLTEXT KEY ft_chunk_text (chunk_text) WITH PARSER ngram,
		CONSTRAINT fk_chunks_material FOREIGN KEY (material_id) REFERENCES materials (id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	// refresh_tokens：refresh 状态表（add-refresh-token design D3）。
	//
	// 这是本迭代**唯一**的认证状态存储 —— access 仍是无状态 JWT，不落库。
	// 它的存在让「吊销」成为可能：登出 / 主动踢人只需删对应行。
	//   - token_hash 存 SHA-256 摘要（CHAR(64)），**不存原文**；
	//   - replaced_by 指向轮换后的新 token_hash（字符串，非外键，避免自引用删除顺序问题）；
	//     非空即表示「这条已被用过」—— 旧值再次出现就是重放（design D4）；
	//   - revoked_at 非空表示已吊销；
	//   - 对 users 的外键 ON DELETE CASCADE：删用户即连带清空其 refresh。
	`CREATE TABLE IF NOT EXISTS refresh_tokens (
		id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		user_id     BIGINT UNSIGNED NOT NULL,
		token_hash  CHAR(64)        NOT NULL,
		expires_at  TIMESTAMP       NOT NULL,
		created_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
		replaced_by CHAR(64)        NULL,
		revoked_at  TIMESTAMP       NULL,
		PRIMARY KEY (id),
		UNIQUE KEY uk_rt_hash (token_hash),
		KEY idx_rt_user (user_id),
		KEY idx_rt_expires (expires_at),
		CONSTRAINT fk_rt_user FOREIGN KEY (user_id) REFERENCES users (id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	// ── 第 5 课：提示词 / 技能 / 智能体 / 会话（对标官方 /week05/ 形态）──
	//
	// class_prompts：班级提示词，一班一行（PRM-001/002）。
	//   契约上限 2000 字（官方 openapi OpPRM002 描述），与 VARCHAR(2000) 一致。
	`CREATE TABLE IF NOT EXISTS class_prompts (
		class_id    BIGINT UNSIGNED NOT NULL,
		body        VARCHAR(2000)   NOT NULL,
		updated_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		PRIMARY KEY (class_id),
		CONSTRAINT fk_prompts_class FOREIGN KEY (class_id) REFERENCES classes (id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	// skills：技能（SKL-001~004）。source 是技能文件原文，其余列由服务端解析 source 得到。
	//   唯一键 (class_id, name) → 同班同名撞键，接口映射成 400（官方契约明写"不是 409"）。
	//   trigger_cond 列名加后缀：trigger 是 MySQL 保留字。
	`CREATE TABLE IF NOT EXISTS skills (
		id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		class_id     BIGINT UNSIGNED NOT NULL,
		name         VARCHAR(32)     NOT NULL,
		trigger_cond VARCHAR(120)    NOT NULL DEFAULT '',
		subject      VARCHAR(64)     NOT NULL DEFAULT '',
		tools        VARCHAR(255)    NOT NULL DEFAULT '',
		enabled      TINYINT(1)      NOT NULL DEFAULT 0,
		description  VARCHAR(2000)   NOT NULL DEFAULT '',
		source       TEXT            NOT NULL,
		created_at   TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		UNIQUE KEY uk_skills_class_name (class_id, name),
		KEY idx_skills_class (class_id),
		CONSTRAINT fk_skills_class FOREIGN KEY (class_id) REFERENCES classes (id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	// agents：本班智能体（AGT-001~004）。第 5 课的"几个角色扮演的老师"落在这张表：
	//   一个 agent = 名称 + 提示词 + 一组技能 + 一组材料。
	//   关联表用独立两张（skill_ids / material_ids），不塞进 JSON 列 —— 便于外键约束与跨班校验。
	`CREATE TABLE IF NOT EXISTS agents (
		id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		class_id    BIGINT UNSIGNED NOT NULL,
		name        VARCHAR(64)     NOT NULL,
		prompt      VARCHAR(2000)   NOT NULL,
		enabled     TINYINT(1)      NOT NULL DEFAULT 0,
		created_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		UNIQUE KEY uk_agents_class_name (class_id, name),
		KEY idx_agents_class (class_id),
		CONSTRAINT fk_agents_class FOREIGN KEY (class_id) REFERENCES classes (id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	// agent_skills / agent_materials：agent 与技能 / 材料的关联。
	//   复合主键天然去重；外键 ON DELETE CASCADE 让删 agent 自动清关联。
	`CREATE TABLE IF NOT EXISTS agent_skills (
		agent_id  BIGINT UNSIGNED NOT NULL,
		skill_id  BIGINT UNSIGNED NOT NULL,
		PRIMARY KEY (agent_id, skill_id),
		CONSTRAINT fk_as_agent FOREIGN KEY (agent_id) REFERENCES agents (id) ON DELETE CASCADE,
		CONSTRAINT fk_as_skill FOREIGN KEY (skill_id) REFERENCES skills (id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	`CREATE TABLE IF NOT EXISTS agent_materials (
		agent_id     BIGINT UNSIGNED NOT NULL,
		material_id  BIGINT UNSIGNED NOT NULL,
		PRIMARY KEY (agent_id, material_id),
		CONSTRAINT fk_am_agent FOREIGN KEY (agent_id) REFERENCES agents (id) ON DELETE CASCADE,
		CONSTRAINT fk_am_material FOREIGN KEY (material_id) REFERENCES materials (id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	// conversations：会话（CONV-001~003）。user_id + class_id 冗余存，支撑隔离断言单条 SQL。
	`CREATE TABLE IF NOT EXISTS conversations (
		id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		user_id     BIGINT UNSIGNED NOT NULL,
		class_id    BIGINT UNSIGNED NOT NULL,
		agent_id    BIGINT UNSIGNED NOT NULL,
		title       VARCHAR(64)     NOT NULL DEFAULT '新的提问',
		created_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		KEY idx_conv_user (user_id),
		KEY idx_conv_class (class_id),
		KEY idx_conv_agent (agent_id),
		CONSTRAINT fk_conv_user  FOREIGN KEY (user_id)  REFERENCES users (id),
		CONSTRAINT fk_conv_class FOREIGN KEY (class_id) REFERENCES classes (id),
		CONSTRAINT fk_conv_agent FOREIGN KEY (agent_id) REFERENCES agents (id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	// messages：会话消息（CONV-003/004）。citations 用 JSON 列存助手消息的引用数组。
	`CREATE TABLE IF NOT EXISTS messages (
		id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		conv_id     BIGINT UNSIGNED NOT NULL,
		role        ENUM('user','assistant') NOT NULL,
		content     MEDIUMTEXT      NOT NULL,
		citations   JSON            NULL,
		skill_name  VARCHAR(64)     NOT NULL DEFAULT '',
		created_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		KEY idx_msg_conv (conv_id, id),
		CONSTRAINT fk_msg_conv FOREIGN KEY (conv_id) REFERENCES conversations (id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	// ── 个人项目补齐项：提示词模板 / MCP 服务器与工具 / 工具调用审计 ──
	//
	// prompt_templates：提示词模板库（不只是"一班一段提示词"）。
	//   body 支持 {{变量}} 占位；智能体绑定模板后，每轮会话按变量渲染成系统提示词，
	//   因此**模板正文会随请求逐字进入 LLM 的 system 消息**（这一点在核验报告里给过实测）。
	`CREATE TABLE IF NOT EXISTS prompt_templates (
		id          BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		class_id    BIGINT UNSIGNED NOT NULL,
		name        VARCHAR(64)     NOT NULL,
		description VARCHAR(200)    NOT NULL DEFAULT '',
		body        TEXT            NOT NULL,
		created_at  TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		UNIQUE KEY uk_tpl_class_name (class_id, name),
		CONSTRAINT fk_tpl_class FOREIGN KEY (class_id) REFERENCES classes (id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	// mcp_servers：外部工具服务器登记（第 6 课 MCP-001~004）。
	//   auth_token **只存服务端**、只在打给对方的 Authorization 头里出现原文，
	//   任何响应只回 has_auth 布尔（第 6 课硬约束）。
	`CREATE TABLE IF NOT EXISTS mcp_servers (
		id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		class_id   BIGINT UNSIGNED NOT NULL,
		name       VARCHAR(32)     NOT NULL,
		base_url   VARCHAR(255)    NOT NULL,
		auth_token VARCHAR(255)    NOT NULL DEFAULT '',
		enabled    TINYINT(1)      NOT NULL DEFAULT 1,
		created_at TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		UNIQUE KEY uk_mcp_class_name (class_id, name),
		KEY idx_mcp_class (class_id),
		CONSTRAINT fk_mcp_class FOREIGN KEY (class_id) REFERENCES classes (id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	// mcp_tools：连接时由 tools/list 抓回的**工具缓存**。
	//   input_schema 原样保存，组 tools 参数时逐字发给模型（工具说明同样是攻击面）。
	`CREATE TABLE IF NOT EXISTS mcp_tools (
		id           BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		server_id    BIGINT UNSIGNED NOT NULL,
		name         VARCHAR(64)     NOT NULL,
		description  VARCHAR(1024)   NOT NULL DEFAULT '',
		input_schema JSON            NOT NULL,
		updated_at   TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		UNIQUE KEY uk_tool_server_name (server_id, name),
		CONSTRAINT fk_tool_server FOREIGN KEY (server_id) REFERENCES mcp_servers (id) ON DELETE CASCADE
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,

	// mcp_tool_calls：每次工具调用的审计行（参数、返回、耗时、成败）。
	//   server_name/tool_name 存**文本快照**而非外键：删服务器要保留历史记录
	//   （第 6 课：DELETE 删服务器与工具缓存但保留调用记录）。
	`CREATE TABLE IF NOT EXISTS mcp_tool_calls (
		id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
		class_id        BIGINT UNSIGNED NOT NULL,
		conversation_id BIGINT UNSIGNED NULL,
		skill_name      VARCHAR(64)     NOT NULL DEFAULT '',
		server_name     VARCHAR(32)     NOT NULL,
		tool_name       VARCHAR(64)     NOT NULL,
		arguments       MEDIUMTEXT      NOT NULL,
		result          MEDIUMTEXT      NOT NULL,
		status          ENUM('ok','error') NOT NULL DEFAULT 'ok',
		error_text      VARCHAR(500)    NOT NULL DEFAULT '',
		duration_ms     INT             NOT NULL DEFAULT 0,
		created_at      TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (id),
		KEY idx_calls_class (class_id, id),
		KEY idx_calls_conv (conversation_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`,
}

// alters 是「给已存在的旧库补列」的语句。
//
// 只用 CREATE TABLE IF NOT EXISTS 的话，已经建过的 agents 表不会长出新列；
// 课程场景没有迁移框架，这里用 information_schema 先探测再 ALTER，保持幂等。
var alters = []struct {
	table  string
	column string
	stmt   string
}{
	{
		table:  "agents",
		column: "template_id",
		stmt:   `ALTER TABLE agents ADD COLUMN template_id BIGINT UNSIGNED NULL AFTER prompt`,
	},
	{
		table:  "agents",
		column: "template_vars",
		stmt:   `ALTER TABLE agents ADD COLUMN template_vars JSON NULL AFTER template_id`,
	},
	{
		table:  "messages",
		column: "tool_calls",
		stmt:   `ALTER TABLE messages ADD COLUMN tool_calls JSON NULL AFTER skill_name`,
	},
}

// Migrate 顺序执行建表语句，再补缺失的列。
func Migrate(pool *sql.DB) error {
	for i, stmt := range ddl {
		if _, err := pool.Exec(stmt); err != nil {
			return fmt.Errorf("建表语句 %d 执行失败: %w", i+1, err)
		}
	}
	for _, a := range alters {
		has, err := columnExists(pool, a.table, a.column)
		if err != nil {
			return fmt.Errorf("探测列 %s.%s 失败: %w", a.table, a.column, err)
		}
		if has {
			continue
		}
		if _, err := pool.Exec(a.stmt); err != nil {
			return fmt.Errorf("补列 %s.%s 失败: %w", a.table, a.column, err)
		}
	}
	return nil
}

// columnExists 查 information_schema 判断列是否已存在。
func columnExists(pool *sql.DB, table, column string) (bool, error) {
	var n int
	err := pool.QueryRow(
		`SELECT COUNT(*) FROM information_schema.columns
		 WHERE table_schema = DATABASE() AND table_name = ? AND column_name = ?`,
		table, column,
	).Scan(&n)
	return n > 0, err
}

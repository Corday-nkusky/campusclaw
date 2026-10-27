package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/bcrypt"

	"github.com/Corday-nkusky/campusclaw/backend/internal/config"
	"github.com/Corday-nkusky/campusclaw/backend/internal/obslog"
)

// 种子常量。
//
// A/B 两班的材料标题**故意写成可区分**的（spec.md R6.2）：
// 若两班标题无法区分，跨班访问失败就无法被证明——验收依靠对比。
const (
	classNameA = "A班"
	classNameB = "B班"

	seedMaterialTitleA = "A班-班级公约（A班专用，非本班不可见）.md"
	seedMaterialTitleB = "B班-班级公约（B班专用，非本班不可见）.md"

	seedMaterialBodyA = "# A班班级公约\n\n这是 **A 班** 的材料正文，B 班用户不应看到本段文字。\n"
	seedMaterialBodyB = "# B班班级公约\n\n这是 **B 班** 的材料正文，A 班用户不应看到本段文字。\n"

	// tasks 2.5 的检索专用种子：
	//   - B 班独有主题材料（→ R2.1 跨班检索应 200 空结果）；
	//   - A 班中英混排材料（暴露 ngram 停用词影响；同时提供「有丝分裂」供语义检索演示）；
	//   - 对称文本：A/B 班公约正文互为镜像（→ R5.6 并列排序的可复现断言）。
	seedMaterialTitleA2 = "A班-细胞学名词中英对照表.md"
	seedMaterialBodyA2  = "# 细胞学名词中英对照（A班）\n\n" +
		"细胞周期 cell cycle：细胞从一次分裂结束到下一次分裂结束的全过程。\n\n" +
		"有丝分裂 mitosis：染色体复制后平均分配到两个子细胞的分裂方式。\n\n" +
		"细胞膜 cell membrane：细胞的边界，控制物质进出。\n"

	seedMaterialTitleB2 = "B班-植物学专题：光合作用（B班专用，非本班不可见）.md"
	seedMaterialBodyB2  = "# 光合作用专题（B班）\n\n" +
		"光合作用 photosynthesis：绿色植物利用光能，把二氧化碳和水合成有机物并释放氧气的过程。\n\n" +
		"叶绿体 chloroplast：光合作用的场所，内含类囊体与基粒。\n\n" +
		"光反应在类囊体膜上进行，暗反应（卡尔文循环）在基质中进行。\n"
)

// Seed 幂等初始化。
//
// 幂等机制 = **先查后插**（不是无条件插入，也不是靠唯一约束兜底）：
// 每次启动先查该行是否已存在，存在则跳过。
// 这样连续启动不会复制用户或材料，也**不会覆盖教师已上传的内容**（spec.md R6.1）。
//
// 口令的明文来自环境变量，落库时统一转 bcrypt 哈希（spec.md R6.3）。
func Seed(pool *sql.DB, cfg *config.Config) error {
	classIDs, err := seedClasses(pool)
	if err != nil {
		return err
	}

	teacherHash, err := bcrypt.GenerateFromPassword([]byte(cfg.SeedTeacherPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("计算教师口令哈希失败: %w", err)
	}
	studentHash, err := bcrypt.GenerateFromPassword([]byte(cfg.SeedStudentPassword), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("计算学生口令哈希失败: %w", err)
	}

	type seedUser struct {
		username string
		hash     []byte
		role     string
		classID  int64
	}
	users := []seedUser{
		{"teacher_a", teacherHash, "teacher", classIDs[classNameA]},
		{"student_a1", studentHash, "student", classIDs[classNameA]},
		{"student_b1", studentHash, "student", classIDs[classNameB]},
	}
	for _, u := range users {
		if err := seedUserRow(pool, u.username, u.hash, u.role, u.classID); err != nil {
			return err
		}
	}

	teacherID, err := lookupUserID(pool, "teacher_a")
	if err != nil {
		return err
	}

	// 两班各两条可区分的材料 + 各自的知识库正文（spec.md R6.2 / tasks 2.5）
	materials := []struct {
		classID int64
		title   string
		body    string
		stored  string
	}{
		{classIDs[classNameA], seedMaterialTitleA, seedMaterialBodyA, "seed-class-a-convention.md"},
		{classIDs[classNameA], seedMaterialTitleA2, seedMaterialBodyA2, "seed-class-a-glossary.md"},
		{classIDs[classNameB], seedMaterialTitleB, seedMaterialBodyB, "seed-class-b-convention.md"},
		{classIDs[classNameB], seedMaterialTitleB2, seedMaterialBodyB2, "seed-class-b-photosynthesis.md"},
	}
	for _, m := range materials {
		if err := seedMaterialRow(pool, cfg.UploadDir, m.classID, m.title, m.stored, m.body, teacherID); err != nil {
			return err
		}
	}

	// A 班一套可直接用的助手配置（技能 + 模板 + 智能体）。
	// 不种的话新库启动后「解题助手·配置」三块全空、学生建不了会话，
	// 得先手工配一遍才能演示——种子本来就该让系统一启动就是可用状态。
	if err := seedAssistantConfig(pool, classIDs[classNameA]); err != nil {
		return err
	}

	return nil
}

// seedAssistantConfig 幂等地建一份「能直接跑」的助手配置。
//
// 三者的关系：模板给骨架（`{{班级}}` 这类变量），技能给「这类题怎么引导」，
// 智能体把两者与材料绑在一起，成为学生可选的一个角色。
// 全用「先查后插」，教师改过名字后重跑不会重复建；关联表用 INSERT IGNORE。
func seedAssistantConfig(pool *sql.DB, classID int64) error {
	const (
		skillName = "解题引导"
		tplName   = "学科助教骨架"
		agentName = "生物助教"

		skillSource = "名称：解题引导\n" +
			"适用条件：题目,解题,不会做,怎么做\n" +
			"工具：knowledge_search\n" +
			"资料：\n" +
			"启用：是\n\n" +
			"先让学生说清自己卡在哪一步，再用追问把他推到下一步。\n" +
			"只在学生明确说「想不出来」时才给一个更小的提示，始终不报最终答案。"

		skillDesc = "先让学生说清卡在哪一步，再用追问推到下一步；只引导，不给最终答案。"

		tplBody = "你是{{班级}}的{{学科}}老师，负责带{{年级}}学生。\n" +
			"语气温和，先确认学生对题意的理解，再给下一步提示。\n" +
			"引用本班资料时说明出处。不要直接给出最终答案。"
	)

	// ── 技能 ──
	var skillID int64
	err := pool.QueryRow(`SELECT id FROM skills WHERE class_id=? AND name=?`, classID, skillName).Scan(&skillID)
	if errors.Is(err, sql.ErrNoRows) {
		res, insErr := pool.Exec(
			`INSERT INTO skills (class_id, name, trigger_cond, subject, tools, enabled, description, source)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			classID, skillName, "题目,解题,不会做,怎么做", "", "knowledge_search", 1, skillDesc, skillSource,
		)
		if insErr != nil {
			return fmt.Errorf("种子技能失败: %w", insErr)
		}
		if skillID, err = res.LastInsertId(); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// ── 模板 ──
	var tplID int64
	err = pool.QueryRow(`SELECT id FROM prompt_templates WHERE class_id=? AND name=?`, classID, tplName).Scan(&tplID)
	if errors.Is(err, sql.ErrNoRows) {
		res, insErr := pool.Exec(
			`INSERT INTO prompt_templates (class_id, name, description, body) VALUES (?, ?, ?, ?)`,
			classID, tplName, "可复用的角色骨架，绑定到智能体后每轮渲染", tplBody,
		)
		if insErr != nil {
			return fmt.Errorf("种子提示词模板失败: %w", insErr)
		}
		if tplID, err = res.LastInsertId(); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// ── 智能体 ──
	var agentID int64
	err = pool.QueryRow(`SELECT id FROM agents WHERE class_id=? AND name=?`, classID, agentName).Scan(&agentID)
	if errors.Is(err, sql.ErrNoRows) {
		res, insErr := pool.Exec(
			`INSERT INTO agents (class_id, name, prompt, enabled, template_id, template_vars)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			classID, agentName, "你是耐心的生物助教，只引导学生自己推导，不直接给答案。", 1,
			tplID, `{"班级":"A班","学科":"生物","年级":"高一"}`,
		)
		if insErr != nil {
			return fmt.Errorf("种子智能体失败: %w", insErr)
		}
		if agentID, err = res.LastInsertId(); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	// ── 关联：技能 + 本班全部种子材料（检索范围就在这些材料里）──
	if _, err := pool.Exec(`INSERT IGNORE INTO agent_skills (agent_id, skill_id) VALUES (?, ?)`, agentID, skillID); err != nil {
		return fmt.Errorf("种子智能体技能关联失败: %w", err)
	}
	if _, err := pool.Exec(
		`INSERT IGNORE INTO agent_materials (agent_id, material_id)
		 SELECT ?, id FROM materials WHERE class_id=?`, agentID, classID,
	); err != nil {
		return fmt.Errorf("种子智能体材料关联失败: %w", err)
	}
	return nil
}

// seedClasses 保证 A / B 两班存在，返回名称 → id 映射。
func seedClasses(pool *sql.DB) (map[string]int64, error) {
	for _, name := range []string{classNameA, classNameB} {
		if _, err := pool.Exec(`INSERT IGNORE INTO classes (name) VALUES (?)`, name); err != nil {
			return nil, fmt.Errorf("写入班级 %s 失败: %w", name, err)
		}
	}
	out := make(map[string]int64, 2)
	for _, name := range []string{classNameA, classNameB} {
		var id int64
		if err := pool.QueryRow(`SELECT id FROM classes WHERE name = ?`, name).Scan(&id); err != nil {
			return nil, fmt.Errorf("读取班级 %s 失败: %w", name, err)
		}
		out[name] = id
	}
	return out, nil
}

// seedUserRow 先查后插：用户名已存在则原样保留（不覆盖既有口令）。
func seedUserRow(pool *sql.DB, username string, hash []byte, role string, classID int64) error {
	var exists int
	if err := pool.QueryRow(`SELECT COUNT(*) FROM users WHERE username = ?`, username).Scan(&exists); err != nil {
		return fmt.Errorf("查询用户 %s 失败: %w", username, err)
	}
	if exists > 0 {
		obslog.Info("db.seed_skip", "种子已存在，跳过", "kind", "user", "name", username)
		return nil
	}
	_, err := pool.Exec(
		`INSERT INTO users (username, password_hash, role, class_id) VALUES (?, ?, ?, ?)`,
		username, string(hash), role, classID,
	)
	if err != nil {
		return fmt.Errorf("写入用户 %s 失败: %w", username, err)
	}
	obslog.Info("db.seed_created", "", "kind", "user", "name", username, "role", role, "class_id", classID)
	return nil
}

// seedMaterialRow 先查后插：同班同名材料已存在则跳过，保证不覆盖教师上传内容。
//
// 落盘放在查重**之前**：DB 行存在不代表磁盘文件存在（手工清理过 uploads/、
// 换机只带了库、上次写盘失败等）。缺文件的材料在 `/file` 上必然 404，
// 等于种子里埋了一个坏样本，会让「下载」这条验收链断掉。
func seedMaterialRow(pool *sql.DB, uploadDir string, classID int64, title, storedName, body string, uploaderID int64) error {
	if err := ensureSeedFile(uploadDir, storedName, body); err != nil {
		return err
	}

	var exists int
	err := pool.QueryRow(
		`SELECT COUNT(*) FROM materials WHERE class_id = ? AND title = ?`, classID, title,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("查询种子材料 %q 失败: %w", title, err)
	}
	if exists > 0 {
		obslog.Info("db.seed_skip", "种子已存在，跳过", "kind", "material", "name", title)
		return nil
	}

	tx, err := pool.Begin()
	if err != nil {
		return fmt.Errorf("开启种子事务失败: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	res, err := tx.Exec(
		`INSERT INTO materials (class_id, title, stored_name, size_bytes, uploader_id)
		 VALUES (?, ?, ?, ?, ?)`,
		classID, title, storedName, len(body), uploaderID,
	)
	if err != nil {
		return fmt.Errorf("写入种子材料 %q 失败: %w", title, err)
	}
	materialID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("读取种子材料自增 ID 失败: %w", err)
	}

	// 与材料同事务写入知识库正文（对应 spec.md R4.1 的事务边界约定）
	if _, err := tx.Exec(
		`INSERT INTO knowledge_entries (material_id, class_id, body) VALUES (?, ?, ?)`,
		materialID, classID, body,
	); err != nil {
		return fmt.Errorf("写入种子知识库正文失败: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交种子事务失败: %w", err)
	}
	obslog.Info("db.seed_created", "", "kind", "material", "name", title, "class_id", classID)
	return nil
}

// ensureSeedFile 幂等地把种子正文写到上传目录。
//
// 幂等判据 = **文件存在且大小相符**，不比对内容：
// 种子正文是常量，大小相符即可认定为本程序此前写入的那份。
func ensureSeedFile(uploadDir, storedName, body string) error {
	if uploadDir == "" {
		return errors.New("UPLOAD_DIR 为空，无法写入种子文件")
	}
	if err := os.MkdirAll(uploadDir, 0o750); err != nil {
		return fmt.Errorf("创建上传目录 %s 失败: %w", uploadDir, err)
	}

	// 与下载侧同一条防御：存储名只取基底名，绝不参与上层路径构造。
	full := filepath.Join(uploadDir, filepath.Base(storedName))
	if st, err := os.Stat(full); err == nil && !st.IsDir() && st.Size() == int64(len(body)) {
		obslog.Info("db.seed_skip", "种子已存在，跳过", "kind", "file", "name", storedName)
		return nil
	}
	if err := os.WriteFile(full, []byte(body), 0o640); err != nil {
		return fmt.Errorf("写入种子文件 %s 失败: %w", full, err)
	}
	obslog.Info("db.seed_created", "", "kind", "file", "name", storedName, "bytes", len(body))
	return nil
}

func lookupUserID(pool *sql.DB, username string) (int64, error) {
	var id int64
	if err := pool.QueryRow(`SELECT id FROM users WHERE username = ?`, username).Scan(&id); err != nil {
		return 0, fmt.Errorf("读取用户 %s 的 id 失败: %w", username, err)
	}
	return id, nil
}

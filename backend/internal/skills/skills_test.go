package skills

import "testing"

func TestParseSkillFile_OfficialExample(t *testing.T) {
	// 官方 openapi OpSKL002 example 原文
	src := "名称：方程解题引导\n适用条件：方程,移项\n工具：knowledge_search\n资料：数学\n启用：是\n\n先让学生说出等号两边要保持相等。\n"
	p, err := parseSkillFile(src)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if p.name != "方程解题引导" {
		t.Errorf("name = %q", p.name)
	}
	if p.trigger != "方程,移项" {
		t.Errorf("trigger = %q", p.trigger)
	}
	if p.subject != "数学" {
		t.Errorf("subject = %q", p.subject)
	}
	if !p.enabled {
		t.Errorf("enabled 应为 true")
	}
	if len(p.tools) != 1 || p.tools[0] != "knowledge_search" {
		t.Errorf("tools = %v", p.tools)
	}
	if p.description != "先让学生说出等号两边要保持相等。" {
		t.Errorf("description = %q", p.description)
	}
}

func TestParseSkillFile_NoBlankLine(t *testing.T) {
	_, err := parseSkillFile("名称：甲\n适用条件：乙\n工具：knowledge_search\n资料：丙\n启用：是")
	if err == nil {
		t.Fatal("缺空行应报错")
	}
}

func TestParseSkillFile_EnabledNo(t *testing.T) {
	src := "名称：甲\n适用条件：乙\n工具：knowledge_search\n资料：丙\n启用：否\n\n说明\n"
	p, err := parseSkillFile(src)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if p.enabled {
		t.Error("启用=否 应解析为 false")
	}
}

func TestParseSkillFile_BadEnabled(t *testing.T) {
	src := "名称：甲\n适用条件：乙\n工具：knowledge_search\n资料：丙\n启用：也许\n\n说明\n"
	if _, err := parseSkillFile(src); err == nil {
		t.Fatal("启用非「是/否」应报错")
	}
}

func TestParseSkillFile_BadTool(t *testing.T) {
	src := "名称：甲\n适用条件：乙\n工具：other_tool\n资料：丙\n启用：是\n\n说明\n"
	if _, err := parseSkillFile(src); err == nil {
		t.Fatal("工具非 knowledge_search 应报错")
	}
}

func TestParseSkillFile_MissingName(t *testing.T) {
	src := "适用条件：乙\n工具：knowledge_search\n资料：丙\n启用：是\n\n说明\n"
	if _, err := parseSkillFile(src); err == nil {
		t.Fatal("缺名称应报错")
	}
}

func TestParseSkillFile_MultiLineDescription(t *testing.T) {
	src := "名称：甲\n适用条件：乙\n工具：knowledge_search\n资料：丙\n启用：是\n\n第一行\n第二行\n\n第三段\n"
	p, err := parseSkillFile(src)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	want := "第一行\n第二行\n\n第三段"
	if p.description != want {
		t.Errorf("description = %q, want %q", p.description, want)
	}
}

func TestParseSkillFile_HalfWidthColon(t *testing.T) {
	// 容忍半角冒号
	src := "名称:甲\n适用条件:乙\n工具:knowledge_search\n资料:丙\n启用:是\n\n说明\n"
	p, err := parseSkillFile(src)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if p.name != "甲" {
		t.Errorf("name = %q", p.name)
	}
}

// 第 5 课课件 apply 页「常见故障」：缺字段、重复字段、未定义字段、空正文都应被拒。
func TestParseSkillFile_Strictness(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{"缺适用条件", "名称：甲\n工具：knowledge_search\n启用：是\n\n说明\n"},
		{"缺启用", "名称：甲\n适用条件：乙\n\n说明\n"},
		{"重复字段", "名称：甲\n名称：乙\n适用条件：乙\n启用：是\n\n说明\n"},
		{"未定义字段", "名称：甲\n适用条件：乙\n启用：是\n作者：丙\n\n说明\n"},
		{"空行下无正文", "名称：甲\n适用条件：乙\n启用：是\n\n   \n"},
	}
	for _, c := range cases {
		if _, err := parseSkillFile(c.src); err == nil {
			t.Errorf("%s：应报错但通过了", c.name)
		}
	}
}

// 元数据区的 `#` 注释行与空行应被忽略。
func TestParseSkillFile_IgnoresComments(t *testing.T) {
	src := "# 这是注释\n名称：甲\n适用条件：乙\n启用：是\n\n说明正文\n"
	p, err := parseSkillFile(src)
	if err != nil {
		t.Fatalf("注释行不该导致失败: %v", err)
	}
	if p.name != "甲" || p.description != "说明正文" {
		t.Errorf("解析结果不对: %+v", p)
	}
}

// 资料范围（subject）上限 32 字。
func TestParseSkillFile_SubjectTooLong(t *testing.T) {
	long := ""
	for i := 0; i < 33; i++ {
		long += "长"
	}
	src := "名称：甲\n适用条件：乙\n资料：" + long + "\n启用：是\n\n说明\n"
	if _, err := parseSkillFile(src); err == nil {
		t.Fatal("资料范围超 32 字应报错")
	}
}

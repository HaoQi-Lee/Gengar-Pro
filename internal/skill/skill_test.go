package skill

import (
	"context"
	"strings"
	"testing"

	"gengar-pro/internal/command"
)

func TestParseSkillFileValid(t *testing.T) {
	raw := "---\nname: inspect\ncommand: /inspect\naliases:\n  - /inspection\ndescription: 服务器巡检\n---\n# 巡检\n检查磁盘与内存\n"
	s, err := parseSkillFile("inspect.md", raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if s.Name != "inspect" || s.Command != "/inspect" {
		t.Errorf("got Name=%q Command=%q", s.Name, s.Command)
	}
	if len(s.Aliases) != 1 || s.Aliases[0] != "/inspection" {
		t.Errorf("aliases = %v", s.Aliases)
	}
	if !strings.Contains(s.Playbook, "检查磁盘与内存") {
		t.Errorf("playbook = %q", s.Playbook)
	}
}

func TestParseSkillFileMissingOpening(t *testing.T) {
	if _, err := parseSkillFile("x.md", "name: x\n---\nbody"); err == nil {
		t.Fatal("expected error for missing opening ---")
	}
}

func TestParseSkillFileMissingClosing(t *testing.T) {
	if _, err := parseSkillFile("x.md", "---\nname: x\nbody without close"); err == nil {
		t.Fatal("expected error for missing closing ---")
	}
}

func TestValidateRejectsInvalid(t *testing.T) {
	cases := []struct {
		label string
		s     Skill
	}{
		{"empty name", Skill{Command: "/x", Description: "d", Playbook: "p"}},
		{"empty command", Skill{Name: "x", Description: "d", Playbook: "p"}},
		{"command without slash", Skill{Name: "x", Command: "x", Description: "d", Playbook: "p"}},
		{"empty description", Skill{Name: "x", Command: "/x", Playbook: "p"}},
		{"empty playbook", Skill{Name: "x", Command: "/x", Description: "d"}},
	}
	for _, tc := range cases {
		if err := tc.s.validate(); err == nil {
			t.Errorf("%s: expected validation error, got nil", tc.label)
		}
	}
}

func TestValidateAcceptsValid(t *testing.T) {
	s := Skill{Name: "x", Command: "/x", Description: "d", Playbook: "p"}
	if err := s.validate(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestPromptIncludesMarkerAndPlaybook(t *testing.T) {
	s := Skill{Name: "inspect", Command: "/inspect", Description: "服务器巡检", Playbook: "STEP-1 查磁盘"}
	p := s.Prompt()
	for _, want := range []string{"[Skill]", "服务器巡检", "STEP-1 查磁盘"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q: %q", want, p)
		}
	}
}

// fakeAgent 满足 command.AgentLike，用于断言 ToCommand 触发时把 Playbook 注入了 Agent。
type fakeAgent struct{ got string }

func (f *fakeAgent) ProcessInput(_ context.Context, in string) string {
	f.got = in
	return ""
}

func TestToCommandInjectsPrompt(t *testing.T) {
	s := Skill{Name: "inspect", Command: "/inspect", Description: "服务器巡检", Playbook: "查磁盘"}
	cmd := s.ToCommand()
	if cmd.Name != "/inspect" {
		t.Fatalf("cmd.Name = %q, want /inspect", cmd.Name)
	}
	a := &fakeAgent{}
	var sb strings.Builder
	cmd.Run(command.Context{Agent: a, Out: &sb})
	if !strings.Contains(a.got, "查磁盘") {
		t.Errorf("ProcessInput not called with playbook: %q", a.got)
	}
	if !strings.Contains(sb.String(), "Skill") {
		t.Errorf("missing load hint: %q", sb.String())
	}
}

func TestLoadFindsInspectSkill(t *testing.T) {
	skills, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	var found *Skill
	for i := range skills {
		if skills[i].Command == "/inspect" {
			found = &skills[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("Load() did not return /inspect skill; got %d skills", len(skills))
	}
	if found.Name != "inspect" {
		t.Errorf("Name = %q, want inspect", found.Name)
	}
	if len(found.Aliases) != 2 {
		t.Errorf("Aliases len = %d, want 2 (%v)", len(found.Aliases), found.Aliases)
	}
	for _, want := range []string{"资源水位", "服务健康", "网络连通", "安全审计"} {
		if !strings.Contains(found.Playbook, want) {
			t.Errorf("inspect playbook missing section %q", want)
		}
	}
	// ToCommand 产出的命令名为 /inspect。
	cmd := found.ToCommand()
	if cmd.Name != "/inspect" {
		t.Errorf("ToCommand().Name = %q, want /inspect", cmd.Name)
	}
}

func TestPromptIsGenericNotReadonly(t *testing.T) {
	s := Skill{Name: "install", Command: "/install", Description: "软件安装", Playbook: "STEP-1 探测"}
	p := s.Prompt()
	for _, want := range []string{"[Skill]", "软件安装", "STEP-1 探测"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q: %q", want, p)
		}
	}
	for _, banned := range []string{"只读", "不得修改系统", "严格使用专用工具而非 execute_command"} {
		if strings.Contains(p, banned) {
			t.Errorf("generic prompt still contains inspect-only phrase %q: %q", banned, p)
		}
	}
}

func TestToCommandForwardsArgs(t *testing.T) {
	s := Skill{Name: "install", Command: "/install", Description: "软件安装", Playbook: "探测"}
	cmd := s.ToCommand()
	a := &fakeAgent{}
	var sb strings.Builder
	cmd.Run(command.Context{Agent: a, Out: &sb, Args: []string{"nginx"}})
	if !strings.Contains(a.got, "nginx") {
		t.Errorf("expected injected prompt to contain arg nginx, got %q", a.got)
	}
	if !strings.Contains(a.got, "用户通过命令传入的参数") {
		t.Errorf("expected args marker, got %q", a.got)
	}
}

func TestToCommandOmitsArgsSectionWhenEmpty(t *testing.T) {
	s := Skill{Name: "install", Command: "/install", Description: "软件安装", Playbook: "探测"}
	cmd := s.ToCommand()
	a := &fakeAgent{}
	var sb strings.Builder
	cmd.Run(command.Context{Agent: a, Out: &sb}) // Args 为 nil
	if strings.Contains(a.got, "用户通过命令传入的参数") {
		t.Errorf("empty args should not add args section, got %q", a.got)
	}
}

func TestLoadFindsInstallSkill(t *testing.T) {
	skills, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	var found *Skill
	for i := range skills {
		if skills[i].Command == "/install" {
			found = &skills[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("Load() did not return /install skill; got %d skills", len(skills))
	}
	if found.Name != "install" {
		t.Errorf("Name = %q, want install", found.Name)
	}
	wantAliases := map[string]bool{"/inst": false, "/pkg": false}
	for _, a := range found.Aliases {
		if _, ok := wantAliases[a]; ok {
			wantAliases[a] = true
		}
	}
	for a, seen := range wantAliases {
		if !seen {
			t.Errorf("missing alias %q in %v", a, found.Aliases)
		}
	}
	for _, section := range []string{"前置探测", "环境适配", "确认安装计划", "执行安装", "安装后测试", "服务启动", "汇总"} {
		if !strings.Contains(found.Playbook, section) {
			t.Errorf("install playbook missing section %q", section)
		}
	}
}

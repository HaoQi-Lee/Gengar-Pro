// Package skill 加载编译期嵌入的 Skill（过程性 Playbook），并把每个 Skill
// 暴露为一个斜杠命令。Skill 不新增工具，只通过注入 Playbook 指导 Agent
// 如何组合现有工具完成一类任务。
//
// Skill 文件为 Markdown + YAML frontmatter，经 go:embed 嵌入。
package skill

import (
	"embed"
	"fmt"
	"sort"
	"strings"

	"gengar-pro/internal/command"
	"gopkg.in/yaml.v3"
)

//go:embed data/*.md
var dataFS embed.FS

// Skill 表示一个可被斜杠命令触发的过程性 Playbook。
type Skill struct {
	Name        string   // 全局唯一，如 "inspect"
	Command     string   // 必须以 / 开头，全局唯一，如 "/inspect"
	Aliases     []string // 备选命令名
	Description string   // 供加载提示展示
	Playbook    string   // frontmatter 之后的 markdown 正文
}

const fmDelim = "---"

// parseSkillFile 解析 Markdown + YAML frontmatter 格式的 Skill 文件。
// 文件须以独占一行的 --- 开头，到下一个独占一行的 --- 为 frontmatter，
// 其余为 Playbook 正文。
func parseSkillFile(filename, raw string) (Skill, error) {
	var s Skill
	content := strings.TrimSpace(raw)
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) != fmDelim {
		return s, fmt.Errorf("skill %s: expected frontmatter to start with %q", filename, fmDelim)
	}
	closeIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == fmDelim {
			closeIdx = i
			break
		}
	}
	if closeIdx == -1 {
		return s, fmt.Errorf("skill %s: missing closing %q for frontmatter", filename, fmDelim)
	}
	fmText := strings.Join(lines[1:closeIdx], "\n")
	body := strings.TrimSpace(strings.Join(lines[closeIdx+1:], "\n"))

	var fm struct {
		Name        string   `yaml:"name"`
		Command     string   `yaml:"command"`
		Aliases     []string `yaml:"aliases"`
		Description string   `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(fmText), &fm); err != nil {
		return s, fmt.Errorf("skill %s: parse frontmatter: %w", filename, err)
	}
	s.Name = fm.Name
	s.Command = fm.Command
	s.Aliases = fm.Aliases
	s.Description = fm.Description
	s.Playbook = body
	return s, nil
}

// validate 检查单文件字段约束。跨文件唯一性由 Load 检查。
func (s Skill) validate() error {
	if s.Name == "" {
		return fmt.Errorf("name is empty")
	}
	if s.Command == "" {
		return fmt.Errorf("command is empty")
	}
	if !strings.HasPrefix(s.Command, "/") {
		return fmt.Errorf("command %q must start with /", s.Command)
	}
	if s.Description == "" {
		return fmt.Errorf("description is empty")
	}
	if strings.TrimSpace(s.Playbook) == "" {
		return fmt.Errorf("playbook is empty")
	}
	return nil
}

// Load 读取并解析所有嵌入的 Skill 文件（data/*.md）。任一文件格式错误、
// 字段非法，或 name / command / alias 之间冲突时返回错误。
func Load() ([]Skill, error) {
	entries, err := dataFS.ReadDir("data")
	if err != nil {
		return nil, fmt.Errorf("skill: read data dir: %w", err)
	}
	var skills []Skill
	seenName := map[string]string{} // name -> filename
	seenCmd := map[string]string{}  // command 或 alias -> filename
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		raw, err := dataFS.ReadFile("data/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("skill: read %s: %w", e.Name(), err)
		}
		s, err := parseSkillFile(e.Name(), string(raw))
		if err != nil {
			return nil, err
		}
		if err := s.validate(); err != nil {
			return nil, fmt.Errorf("skill %s: %w", e.Name(), err)
		}
		if prev, ok := seenName[s.Name]; ok {
			return nil, fmt.Errorf("skill %s: duplicate name %q (also in %s)", e.Name(), s.Name, prev)
		}
		seenName[s.Name] = e.Name()
		for _, c := range append([]string{s.Command}, s.Aliases...) {
			if prev, ok := seenCmd[c]; ok {
				return nil, fmt.Errorf("skill %s: duplicate command/alias %q (also in %s)", e.Name(), c, prev)
			}
			seenCmd[c] = e.Name()
		}
		skills = append(skills, s)
	}
	sort.Slice(skills, func(i, j int) bool { return skills[i].Command < skills[j].Command })
	return skills, nil
}

// ToCommand 把 Skill 转换为一个斜杠命令。触发时打印加载提示，并把
// Playbook 作为结构化用户指令注入 Agent（复用 ProcessInput）。
func (s Skill) ToCommand() command.Command {
	c := command.Command{
		Name:        s.Command,
		Aliases:     s.Aliases,
		Description: s.Description,
	}
	c.Run = func(ctx command.Context) {
		fmt.Fprintf(ctx.Out, "⚡ Skill: %s\n", s.Description)
		prompt := s.Prompt()
		if len(ctx.Args) > 0 {
			prompt += "\n\n用户通过命令传入的参数: " + strings.Join(ctx.Args, " ")
		}
		ctx.Agent.ProcessInput(ctx.Ctx, prompt)
	}
	return c
}

// Prompt 返回注入 Agent 的用户指令文本。框架文本对所有 Skill 通用：只保留
// [Skill] 标记、按 Playbook 执行、输出格式（Markdown 汇总——Web 对话区按
// Markdown 渲染）三件事；操作约束（只读 / 写操作 / 用哪些工具）由各 Skill
// 的 Playbook 自带。
func (s Skill) Prompt() string {
	return fmt.Sprintf(
		"[Skill] %s\n请按以下 Playbook 执行，逐项完成，最后用 Markdown 汇总"+
			"（标题分节、列表列要点、对比数据用表格）。\n\n%s",
		s.Description, s.Playbook,
	)
}

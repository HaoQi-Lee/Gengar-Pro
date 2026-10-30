// Package consensus 加载并暴露按 profile 切分的环境共识数据。
//
// 共识数据在编译期通过 go:embed 写入二进制；运行时根据 GENGAR_ENV
// 环境变量（默认 dev）选择激活的 profile。
package consensus

import (
	"embed"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed data/*.yaml
var dataFS embed.FS

// DefaultEnv 是未设置 GENGAR_ENV 时使用的默认 profile。
const DefaultEnv = "dev"

// Consensus 表示一个 profile 的完整环境共识。
type Consensus struct {
	Profile     string      `yaml:"profile"`
	Description string      `yaml:"description"`
	Guidelines  []Guideline `yaml:"guidelines"`
}

// Guideline 表示一条共识条目，可以是基础设施事实（如 YUM 源地址）
// 或运维规范（如备份策略）。
type Guideline struct {
	ID      string `yaml:"id"`
	Title   string `yaml:"title"`
	Summary string `yaml:"summary"`
	Content string `yaml:"content"`
}

// Load 根据 env 读取对应的 profile YAML 并解析。env 为空时使用 DefaultEnv。
// 解析失败、文件缺失、字段非法时返回错误。
func Load(env string) (*Consensus, error) {
	if env == "" {
		env = DefaultEnv
	}
	available, err := availableEnvs()
	if err != nil {
		return nil, fmt.Errorf("consensus: list profiles: %w", err)
	}
	if !containsStr(available, env) {
		return nil, fmt.Errorf(
			"consensus: unknown GENGAR_ENV %q; available profiles: %s",
			env, strings.Join(available, ", "))
	}

	raw, err := dataFS.ReadFile("data/" + env + ".yaml")
	if err != nil {
		return nil, fmt.Errorf("consensus: read %s.yaml: %w", env, err)
	}

	var c Consensus
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("consensus: parse %s.yaml: %w", env, err)
	}

	if err := validate(&c, env); err != nil {
		return nil, fmt.Errorf("consensus: %s.yaml: %w", env, err)
	}
	return &c, nil
}

func availableEnvs() ([]string, error) {
	entries, err := dataFS.ReadDir("data")
	if err != nil {
		return nil, err
	}
	var envs []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasSuffix(name, ".yaml") {
			envs = append(envs, strings.TrimSuffix(name, ".yaml"))
		}
	}
	sort.Strings(envs)
	return envs, nil
}

func validate(c *Consensus, env string) error {
	if c.Profile == "" {
		return fmt.Errorf("profile field empty")
	}
	if c.Profile != env {
		return fmt.Errorf("profile field %q does not match filename %q", c.Profile, env)
	}
	seen := map[string]bool{}
	for i, g := range c.Guidelines {
		if g.ID == "" {
			return fmt.Errorf("guideline[%d]: id empty", i)
		}
		if g.Title == "" {
			return fmt.Errorf("guideline %q: title empty", g.ID)
		}
		if g.Summary == "" {
			return fmt.Errorf("guideline %q: summary empty", g.ID)
		}
		if g.Content == "" {
			return fmt.Errorf("guideline %q: content empty", g.ID)
		}
		if seen[g.ID] {
			return fmt.Errorf("duplicate guideline id %q", g.ID)
		}
		seen[g.ID] = true
	}
	return nil
}

func containsStr(s []string, x string) bool {
	for _, v := range s {
		if v == x {
			return true
		}
	}
	return false
}

// Catalog 渲染一段紧凑的目录文本，用于注入 [REDACTED]，让 Agent
// 知道当前 profile 有哪些可查阅条目。只包含 id 与 summary，不包含 content。
func (c *Consensus) Catalog() string {
	var b strings.Builder
	b.WriteString("=== 环境共识目录 (profile: ")
	b.WriteString(c.Profile)
	b.WriteString(") ===\n")
	b.WriteString("当前环境有以下可查阅的运维规范与基础设施事实。在涉及包源、")
	b.WriteString("时间同步、DNS、备份、命名等决策时，先调用 ")
	b.WriteString("query_consensus(topic=<id>) 获取详情，不要凭直觉编造：\n\n")
	for _, g := range c.Guidelines {
		b.WriteString("- ")
		b.WriteString(g.ID)
		b.WriteString(": ")
		b.WriteString(g.Summary)
		b.WriteString("\n")
	}
	return b.String()
}

// Find 按 id 精确查找一条 guideline，未命中返回 nil。
func (c *Consensus) Find(id string) *Guideline {
	for i := range c.Guidelines {
		if c.Guidelines[i].ID == id {
			return &c.Guidelines[i]
		}
	}
	return nil
}

// IDs 按 YAML 中出现的顺序返回所有 guideline id，用于 query_consensus
// 未命中时的错误提示。
func (c *Consensus) IDs() []string {
	ids := make([]string, 0, len(c.Guidelines))
	for _, g := range c.Guidelines {
		ids = append(ids, g.ID)
	}
	return ids
}

// Package command 提供斜杠命令注册表：Skill 命令（/inspect、/install 等）
// 经此注册并由 dispatcher 统一路由（Web 对话与 headless 共用）。
// 早期的内置命令（/help、/quit、/clear、/history）已随 CLI 形态退役移除。
package command

import (
	"context"
	"io"
	"sort"
	"strings"
)

// AgentLike is the subset of the agent surface that command implementations
// need. Defined here to keep the command package free of imports from agent.
type AgentLike interface {
	ProcessInput(ctx context.Context, input string) string
}

// Context is the dependency bundle passed to a Command's Run function.
type Context struct {
	Agent AgentLike
	Out   io.Writer
	Ctx   context.Context // 透传给 Agent.ProcessInput
	Args  []string        // 斜杠命令参数：dispatcher 把命令名之后的 token 透传给 Run（如 /install nginx 的 "nginx"）
}

// Command is a single slash command definition.
type Command struct {
	Name        string       // canonical name, e.g. "/inspect"
	Aliases     []string     // alternative spellings
	Description string       // 展示用描述
	Run         func(ctx Context) // 命中即执行；无返回值（/quit 的 REPL 终止语义已随内置命令移除）
}

// Registry holds a fixed set of commands.
type Registry struct {
	cmds []Command
}

// NewRegistry builds a Registry from the supplied commands.
func NewRegistry(cmds ...Command) *Registry {
	out := make([]Command, len(cmds))
	copy(out, cmds)
	return &Registry{cmds: out}
}

// All returns every command in registration order.
func (r *Registry) All() []Command {
	out := make([]Command, len(r.cmds))
	copy(out, r.cmds)
	return out
}

// Match returns the commands whose canonical Name starts with prefix,
// sorted lexicographically. A prefix that does not start with "/" returns nil.
func (r *Registry) Match(prefix string) []Command {
	if prefix == "" || prefix[0] != '/' {
		return nil
	}
	var out []Command
	for _, c := range r.cmds {
		if hasPrefix(c.Name, prefix) {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func hasPrefix(s, prefix string) bool {
	if len(prefix) > len(s) {
		return false
	}
	return s[:len(prefix)] == prefix
}

// Lookup returns a pointer to the command whose Name or one of whose Aliases
// equals the input. Returns nil when no command matches.
//
// Matching walks the registered commands in registration order; the first
// command whose Name or Aliases contain input wins. Callers should treat the
// returned *Command as read-only — mutating its fields aliases registry state.
func (r *Registry) Lookup(input string) *Command {
	if input == "" {
		return nil
	}
	for i := range r.cmds {
		if r.cmds[i].Name == input {
			return &r.cmds[i]
		}
		for _, a := range r.cmds[i].Aliases {
			if a == input {
				return &r.cmds[i]
			}
		}
	}
	return nil
}

// SplitArgs 把一行斜杠输入拆成命令名与参数。首个空白分隔 token 为命令名，
// 其余 token 作为参数原样保留（不做 shell 解析，保留引号）。单 token 或空输入
// 时 args 返回 nil。供 dispatcher 统一解析，避免交互模式与批处理模式各自重复
// strings.Fields 逻辑。
func SplitArgs(line string) (cmd string, args []string) {
	parts := strings.Fields(line)
	if len(parts) == 0 {
		return "", nil
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return parts[0], parts[1:]
}

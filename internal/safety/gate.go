package safety

import (
	"encoding/json"
	"regexp"
	"strings"
)

type Gate struct{}

func NewGate() *Gate {
	return &Gate{}
}

func (g *Gate) Classify(toolName string, input json.RawMessage) Level {
	if toolName == "execute_command" {
		var params struct {
			Command string `json:"command"`
		}
		json.Unmarshal(input, &params)
		return g.ClassifyCommand(params.Command)
	}
	return classifyStatic(toolName)
}

func (g *Gate) ClassifyStatic(toolName string) Level {
	return classifyStatic(toolName)
}

// SetStaticLevel 注册/覆盖一个工具名的静态分级（插件声明的 RiskLevel
// 经 plugin.Registry.SetGate 灌入——单一真相源，见 level.go 注释）。
func (g *Gate) SetStaticLevel(toolName string, level Level) {
	SetStaticLevel(toolName, level)
}

// splitSegments 把命令行按 shell 运算符 ; | && || 拆成可独立判定的段，
// 空段（如行尾分隔符残留）丢弃。不做引号解析——引号内的 ; 同样拆段：
// 误拆只意味着多弹一次确认（fail-safe 方向），绝不放行。
func splitSegments(cmd string) []string {
	var segs []string
	var cur strings.Builder
	i := 0
	for i < len(cmd) {
		c := cmd[i]
		// 运算符边界：优先长匹配（||、&&）再单字符（;、|）
		if c == '&' && i+1 < len(cmd) && cmd[i+1] == '&' {
			segs = append(segs, cur.String())
			cur.Reset()
			i += 2
			continue
		}
		if c == '|' && i+1 < len(cmd) && cmd[i+1] == '|' {
			segs = append(segs, cur.String())
			cur.Reset()
			i += 2
			continue
		}
		if c == ';' || c == '|' {
			segs = append(segs, cur.String())
			cur.Reset()
			i++
			continue
		}
		cur.WriteByte(c)
		i++
	}
	segs = append(segs, cur.String())

	var out []string
	for _, s := range segs {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// hasSubshell 报告命令是否含命令替换（$(...) 或反引号）——无静态可判，
// 一律 Risky（fail-safe）。
func hasSubshell(cmd string) bool {
	return strings.Contains(cmd, "$(") || strings.Contains(cmd, "`")
}

// hasRedirect 报告命令是否含输出重定向（> 或 >>）。重定向把白名单命令
// 变成写操作（`echo p > /etc/passwd`），白名单只证明「只读」，含重定向
// 必须弹确认。输入重定向 < 本身不写——但重定向目标解析复杂（>&2、2>&1
// 形态），统一按 Risky 处理（fail-safe）。
func hasRedirect(cmd string) bool {
	// 先剔除 heredoc 起始标记 <<（EOF heredoc 是输入侧，但 heredoc 内容
	// 仍可含任意命令序列——统一并入 Risky 判定，见下）。
	i := 0
	for i < len(cmd) {
		c := cmd[i]
		if c == '<' {
			// << heredoc：内容不可静态判定 → Risky
			if i+1 < len(cmd) && cmd[i+1] == '<' {
				return true
			}
			return true
		}
		if c == '>' {
			return true
		}
		i++
	}
	return false
}

func matchAny(seg string, res []*regexp.Regexp) bool {
	for _, re := range res {
		if re.MatchString(seg) {
			return true
		}
	}
	return false
}

// ClassifyCommand returns the safety level of a shell command string.
//
// In the two-level model, anything that is not provably read-only is Risky.
// 命令先按 shell 运算符拆段：**每一段**都要过白名单整串才 Safe——开头的
// `cat` 再也夹带不住结尾的 `rm`。含命令替换（$(...) / 反引号）直接 Risky。
// The high-risk (blacklist) regex set is still evaluated so callers / UI
// can read out which bucket fired (see MatchHighRisk below), but the gating
// result is binary: Safe or Risky.
func (g *Gate) ClassifyCommand(cmd string) Level {
	if cmd == "" {
		return LevelSafe
	}
	if hasSubshell(cmd) {
		return LevelRisky
	}
	if hasRedirect(cmd) {
		return LevelRisky
	}
	segs := splitSegments(cmd)
	if len(segs) == 0 {
		return LevelSafe
	}

	// High-risk patterns are checked first so the UI can flag them as "高危"
	// in the confirmation prompt, even though the gating level is still Risky.
	for _, seg := range segs {
		if matchAny(seg, blacklistRegexps) {
			return LevelRisky
		}
	}
	// Read-only whitelist: 每段都命中才整串免确认。
	for _, seg := range segs {
		if !matchAny(seg, whitelistRegexps) {
			return LevelRisky
		}
	}
	return LevelSafe
}

// MatchHighRisk reports whether cmd matches any high-risk (blacklist) pattern.
// Callers (e.g. the UI confirmation prompt) can use this to annotate a Risky
// command as "高危" without changing the gating behaviour.
func (g *Gate) MatchHighRisk(cmd string) bool {
	if cmd == "" {
		return false
	}
	for _, re := range blacklistRegexps {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}

package safety

import "fmt"

// Level represents a tool / command safety classification.
//
// The model has exactly two levels:
//   - LevelSafe   — read-only, auto-execute without prompting.
//   - LevelRisky  — has side effects, must be confirmed by the user before execution.
//
// Anything not provably safe falls through to LevelRisky. Sub-categories such
// as "high-risk" (formerly Forbidden) or "dangerous" are surfaced to the UI
// out-of-band (e.g. via the matched rule set) so confirmation prompts can be
// annotated, but the gating behaviour is binary.
type Level int

const (
	LevelSafe  Level = 0
	LevelRisky Level = 1
)

func (l Level) String() string {
	switch l {
	case LevelSafe:
		return "Safe"
	case LevelRisky:
		return "Risky"
	default:
		return fmt.Sprintf("Level(%d)", l)
	}
}

// staticLevels 是工具名 → 分级的静态表。历史手工条目已删除：现在由
// plugin.Registry.SetGate 在启动时注入插件声明（单一真相源），本表启动
// 时为空、未注册工具一律 Risky（fail-safe）。
// （静态只读诊断类工具的 Safe 级别随插件声明注入后自动恢复。）
var staticLevels = map[string]Level{}

// SetStaticLevel 注册/覆盖一个工具名的静态分级。plugin.Registry 在组装时
// 调用（SetGate），把插件 ToolDefinition 声明的 RiskLevel 灌进来——分级
// 的唯一真相源在插件声明处，safety 包不再手工维护第二份表（评审 ①-4：
// 双表曾漂移，find/grep/ping 等只读工具误弹确认）。
func SetStaticLevel(toolName string, level Level) {
	staticLevels[toolName] = level
}

func classifyStatic(toolName string) Level {
	if level, ok := staticLevels[toolName]; ok {
		return level
	}
	return LevelRisky
}

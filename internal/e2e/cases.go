//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gengar-pro/internal/audit"
	"gopkg.in/yaml.v3"
)

// cases：YAML 用例（schema v2）加载与断言求值。断言是纯函数——输入
// stdout 字符串 + []audit.Entry + 数据目录（跨进程文件断言），输出
// error；表驱动单测覆盖，不依赖 Docker / 桩 / headless。

// e2eCase 是 testcases/*.yaml 的内存形态。
type e2eCase struct {
	Name        string           `yaml:"name"`
	Description string           `yaml:"description"`
	LLM         string           `yaml:"llm"`      // stub | real（real 需 ANTHROPIC_API_KEY）
	Hosts       int              `yaml:"hosts"`    // sshd 容器数
	Tags        map[int][]string `yaml:"tags"`     // 可选：主机序号（0 起）→ 标签
	AutoYes     *bool            `yaml:"auto_yes"` // 可选：headless --yes 自动批准；缺省 true（E03 用 false 走人工确认拒绝路径）
	Script      []scriptTurn     `yaml:"script"`   // 单轮用例的桩脚本（runs 存在时忽略）
	Inputs      []string         `yaml:"inputs"`   // 单轮用例的输入
	Runs        []caseRun        `yaml:"runs"`     // 多轮（跨会话）用例：每轮一个 headless 进程，共用同一 DataDir
	Assertions  []assertionSpec  `yaml:"assertions"`
}

// caseRun 是一轮 headless 运行：一个进程 = 一个会话。多轮共 DataDir 正是
// E05 记忆跨会话的被测点。
type caseRun struct {
	Script []scriptTurn `yaml:"script"`
	Inputs []string     `yaml:"inputs"`
}

// scriptTurn / scriptToolCall 是 YAML 侧脚本形态（与桩的 Turn/ToolCall
// 分离，便于默认值加工：id 缺省补齐）。
type scriptTurn struct {
	ToolCalls  []scriptToolCall `yaml:"tool_calls"`
	FinalText  string           `yaml:"final_text"`
	Text       string           `yaml:"text"`
	StopReason string           `yaml:"stop_reason"` // 可选：覆盖默认停机原因（max_tokens 截断形态等）
}

type scriptToolCall struct {
	ID    string         `yaml:"id"`
	Name  string         `yaml:"name"`
	Input map[string]any `yaml:"input"`
}

// assertionSpec 是一条断言。九种类型（spec 字段按类型取用）：
//   - tool_called{name, min_count}        —— audit.jsonl Tool 字段命中次数
//   - tool_not_called{name}               —— audit 零记录
//   - response_contains{matches}          —— stdout 正则命中
//   - response_not_contains{matches}      —— stdout 正则不命中
//   - audit_hosts{tool, count}            —— 存在一条 audit Hosts 长度=count（fan-out）
//   - audit_host_names{tool, names}       —— 存在一条 audit Hosts 集合恰为 names（序无关）
//   - audit_rejected{tool}                —— 存在一条 approved=false（确认门控拒绝）
//   - memory_contains{host, matches}      —— dataDir/memory/<host>.json 内容正则命中
//   - audit_field{tool, user?, session_re?, is_error?, output_re?, input_re?, count?}
//     —— 字段级匹配：按 tool + 可选字段过滤 audit，断言命中条数
//     （count 缺省 >=1；显式 N 恰为 N；显式 0 = 不得存在）。
//     覆盖 login_ok/chat_input 等非工具事件与 User/Session/IsError/
//     Input/Output 字段
type assertionSpec struct {
	Type string   `yaml:"type"`
	Spec specBody `yaml:"spec"`
}

type specBody struct {
	Matches   string   `yaml:"matches"`
	Tool      string   `yaml:"tool"`
	MinCount  int      `yaml:"min_count"`  // 0 视为 1
	Count     *int     `yaml:"count"`      // audit_hosts：Hosts 长度（必填）；audit_field：命中条数三态——nil 缺省 >=1，显式 0 = 不得存在
	Names     []string `yaml:"names"`      // audit_host_names：Hosts 集合目标
	Host      string   `yaml:"host"`       // memory_contains：受管主机名
	User      *string  `yaml:"user"`       // audit_field：User 精确匹配；nil=不过滤，显式 ""（YAML user: ""）才匹配空（如未认证的 login_fail）
	SessionRE string   `yaml:"session_re"` // audit_field：Session 正则；空串=不过滤（与空正则语义等价，无需指针三态）
	IsError   *bool    `yaml:"is_error"`   // audit_field：IsError 布尔过滤；nil=不过滤
	OutputRE  string   `yaml:"output_re"`  // audit_field：Output 正则；空串=不过滤（同上）
	InputRE   string   `yaml:"input_re"`   // audit_field：Input 的 JSON 文本正则；空串=不过滤（同上）
}

// loadCase 读入并校验一个用例文件。
func loadCase(path string) (*e2eCase, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c e2eCase
	if err := yaml.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.Name == "" {
		return nil, fmt.Errorf("%s: missing name", path)
	}
	if c.LLM != "stub" && c.LLM != "real" {
		return nil, fmt.Errorf("%s: llm must be stub|real, got %q", path, c.LLM)
	}
	if c.Hosts < 1 {
		return nil, fmt.Errorf("%s: hosts must be >= 1", path)
	}
	// runs 缺省归一为单轮（顶层 script/inputs 形态），驱动层只看 Runs。
	if len(c.Runs) == 0 {
		c.Runs = []caseRun{{Script: c.Script, Inputs: c.Inputs}}
	}
	if c.LLM == "stub" {
		for i, r := range c.Runs {
			if len(r.Script) == 0 {
				return nil, fmt.Errorf("%s: llm=stub requires script in every run (run %d empty)", path, i)
			}
		}
	}
	for i := range c.Assertions {
		a := &c.Assertions[i]
		if a.Spec.MinCount == 0 {
			a.Spec.MinCount = 1
		}
		// 正则在加载期编译、必填字段在加载期查空：坏用例在启动 Docker /
		// headless 之前就失败，而不是跑完一轮才死在断言上。
		switch a.Type {
		case "response_contains", "response_not_contains", "memory_contains":
			if a.Spec.Matches == "" {
				return nil, fmt.Errorf("%s: assertion %d (%s): matches required", path, i, a.Type)
			}
			if _, err := regexp.Compile(a.Spec.Matches); err != nil {
				return nil, fmt.Errorf("%s: assertion %d (%s): bad matches pattern %q: %v", path, i, a.Type, a.Spec.Matches, err)
			}
			if a.Type == "memory_contains" && a.Spec.Host == "" {
				return nil, fmt.Errorf("%s: assertion %d (memory_contains): host required", path, i)
			}
		case "audit_host_names":
			if len(a.Spec.Names) == 0 {
				return nil, fmt.Errorf("%s: assertion %d (audit_host_names): names required", path, i)
			}
		case "audit_hosts":
			// count 改指针三态（audit_field 的「缺省 >=1 / 显式 0」需要）后，
			// audit_hosts 仍语义不变：count 必填，缺失在加载期报错而非静默
			// 匹配零主机条目。
			if a.Spec.Count == nil {
				return nil, fmt.Errorf("%s: assertion %d (audit_hosts): count required", path, i)
			}
		case "audit_field":
			if a.Spec.Tool == "" {
				return nil, fmt.Errorf("%s: assertion %d (audit_field): tool required", path, i)
			}
			for _, p := range []struct{ name, pattern string }{
				{"session_re", a.Spec.SessionRE},
				{"output_re", a.Spec.OutputRE},
				{"input_re", a.Spec.InputRE},
			} {
				if p.pattern == "" {
					continue
				}
				if _, err := regexp.Compile(p.pattern); err != nil {
					return nil, fmt.Errorf("%s: assertion %d (audit_field): bad %s pattern %q: %v", path, i, p.name, p.pattern, err)
				}
			}
		}
	}
	return &c, nil
}

// scriptToTurns 把 YAML 脚本转为桩 Turn；id 缺省按回合序补 "tc<N>"。
func scriptToTurns(script []scriptTurn) []Turn {
	turns := make([]Turn, 0, len(script))
	for _, st := range script {
		t := Turn{FinalText: st.FinalText, Text: st.Text, StopReason: st.StopReason}
		for i, sc := range st.ToolCalls {
			id := sc.ID
			if id == "" {
				id = fmt.Sprintf("tc%d_%d", len(turns)+1, i+1)
			}
			t.ToolCalls = append(t.ToolCalls, ToolCall{ID: id, Name: sc.Name, Input: sc.Input})
		}
		turns = append(turns, t)
	}
	return turns
}

// evalAssertion 对 stdout + audit 行 + 数据目录求值一条断言；nil = 通过。
// dataDir 供跨进程文件断言（memory_contains 直查 headless 落盘的文件）。
func evalAssertion(a assertionSpec, stdout string, entries []audit.Entry, dataDir string) error {
	switch a.Type {
	case "tool_called":
		minCount := a.Spec.MinCount
		if minCount <= 0 {
			minCount = 1 // loadCase 已归一；直构断言（单测）同样按 1 求值
		}
		n := 0
		for _, e := range entries {
			if e.Tool == a.Spec.Tool {
				n++
			}
		}
		if n < minCount {
			return fmt.Errorf("tool %q called %d time(s), want >= %d", a.Spec.Tool, n, minCount)
		}
		return nil
	case "tool_not_called":
		for _, e := range entries {
			if e.Tool == a.Spec.Tool {
				return fmt.Errorf("tool %q was called (%d hosts), want never", a.Spec.Tool, len(e.Hosts))
			}
		}
		return nil
	case "response_contains":
		return matchStdout(a.Spec.Matches, stdout, true)
	case "response_not_contains":
		return matchStdout(a.Spec.Matches, stdout, false)
	case "audit_hosts":
		count := 0
		if a.Spec.Count != nil {
			count = *a.Spec.Count // loadCase 已要求显式给出；直构断言（单测）缺省按 0，与旧 int 零值语义一致
		}
		for _, e := range entries {
			if e.Tool == a.Spec.Tool && len(e.Hosts) == count {
				return nil
			}
		}
		return fmt.Errorf("no audit entry for tool %q with %d host(s)", a.Spec.Tool, count)
	case "audit_host_names":
		for _, e := range entries {
			if e.Tool == a.Spec.Tool && sameNameSet(e.Hosts, a.Spec.Names) {
				return nil
			}
		}
		return fmt.Errorf("no audit entry for tool %q with hosts exactly %v", a.Spec.Tool, a.Spec.Names)
	case "audit_rejected":
		for _, e := range entries {
			if e.Tool == a.Spec.Tool && !e.Approved {
				return nil
			}
		}
		return fmt.Errorf("no rejected (approved=false) audit entry for tool %q", a.Spec.Tool)
	case "memory_contains":
		return matchMemoryFile(dataDir, a.Spec.Host, a.Spec.Matches)
	case "audit_field":
		return evalAuditField(a.Spec, entries)
	default:
		return fmt.Errorf("unknown assertion type %q", a.Type)
	}
}

// evalAuditField 是 audit_field 断言的求值体：按 tool（必填）+ 可选字段
// （user 精确、session/output/input 正则、is_error 布尔）过滤 audit 条目，
// 再核对命中条数——count 缺省要求 >=1；显式 N 要求恰为 N（显式 0 =
// 不得存在，用于「不得出现某类事件」的否定断言）。
func evalAuditField(sb specBody, entries []audit.Entry) error {
	sessionRe, err := compileFieldRE(sb.Tool, "session_re", sb.SessionRE)
	if err != nil {
		return err
	}
	outputRe, err := compileFieldRE(sb.Tool, "output_re", sb.OutputRE)
	if err != nil {
		return err
	}
	inputRe, err := compileFieldRE(sb.Tool, "input_re", sb.InputRE)
	if err != nil {
		return err
	}
	match := func(e audit.Entry) bool {
		if e.Tool != sb.Tool {
			return false
		}
		if sb.User != nil && e.User != *sb.User {
			return false
		}
		if sb.IsError != nil && e.IsError != *sb.IsError {
			return false
		}
		if sessionRe != nil && !sessionRe.MatchString(e.Session) {
			return false
		}
		if outputRe != nil && !outputRe.MatchString(e.Output) {
			return false
		}
		if inputRe != nil && !inputRe.MatchString(string(e.Input)) {
			return false
		}
		return true
	}
	n := 0
	for _, e := range entries {
		if match(e) {
			n++
		}
	}
	if sb.Count == nil {
		if n < 1 {
			return fmt.Errorf("audit_field [%s] matched %d entry(ies), want >= 1", describeFieldFilter(sb), n)
		}
		return nil
	}
	if n != *sb.Count {
		return fmt.Errorf("audit_field [%s] matched %d entry(ies), want == %d", describeFieldFilter(sb), n, *sb.Count)
	}
	return nil
}

// compileFieldRE 编译 audit_field 的一个可选正则；空模式返回 nil（不过滤）。
// 坏正则在求值期报错（loadCase 已拦截 YAML 来源，这里兜底直构断言）。
func compileFieldRE(tool, name, pattern string) (*regexp.Regexp, error) {
	if pattern == "" {
		return nil, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("audit_field tool %q: bad %s pattern %q: %v", tool, name, pattern, err)
	}
	return re, nil
}

// describeFieldFilter 拼出 audit_field 的匹配条件摘要（只列显式给出的
// 字段），报错信息自解释——不看 YAML 也能知道在断言什么。
func describeFieldFilter(sb specBody) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tool=%q", sb.Tool)
	if sb.User != nil {
		fmt.Fprintf(&b, " user=%q", *sb.User)
	}
	if sb.SessionRE != "" {
		fmt.Fprintf(&b, " session_re=%q", sb.SessionRE)
	}
	if sb.IsError != nil {
		fmt.Fprintf(&b, " is_error=%v", *sb.IsError)
	}
	if sb.OutputRE != "" {
		fmt.Fprintf(&b, " output_re=%q", sb.OutputRE)
	}
	if sb.InputRE != "" {
		fmt.Fprintf(&b, " input_re=%q", sb.InputRE)
	}
	return b.String()
}

// sameNameSet 报告两个名字切片是否为同一集合（序无关，多重集相等：
// 重复元素也须一一对应）。
func sameNameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	count := make(map[string]int, len(got))
	for _, n := range got {
		count[n]++
	}
	for _, n := range want {
		count[n]--
		if count[n] < 0 {
			return false
		}
	}
	return true
}

func matchStdout(pattern, stdout string, wantMatch bool) error {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("bad matches pattern %q: %v", pattern, err)
	}
	if re.MatchString(stdout) != wantMatch {
		verb := "to match"
		if !wantMatch {
			verb = "not to match"
		}
		return fmt.Errorf("stdout expected %s %q", verb, pattern)
	}
	return nil
}

// matchMemoryFile 正则匹配 dataDir/memory/<host>.json 的原始文件内容。
// 文件名清洗与 internal/memory Store.hostPath 的字符白名单对齐（e2e 的
// 主机名 e2e-host-N 本就落在白名单内，这里只防用例 YAML 手滑写错）。
func matchMemoryFile(dataDir, host, pattern string) error {
	safe := sanitizeHostFileStem(host)
	path := filepath.Join(dataDir, "memory", safe+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %v (memory_write 是否成功落盘?)", path, err)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Errorf("bad matches pattern %q: %v", pattern, err)
	}
	if !re.Match(raw) {
		return fmt.Errorf("memory file %s does not match %q", path, pattern)
	}
	return nil
}

// sanitizeHostFileStem 复刻 internal/memory hostPath 的字符映射（字母
// 数字 - _ . 之外的字符替换为 _）。
func sanitizeHostFileStem(host string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, host)
}

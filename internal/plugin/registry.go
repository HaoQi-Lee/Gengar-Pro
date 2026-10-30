package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"gengar-pro/internal/executor"
	"gengar-pro/internal/safety"
)

// Target 是解析后的执行目标：主机名 + 绑定该主机的执行器。
type Target struct {
	Name string
	Exec executor.Executor
}

// TargetResolver 由组装层（appcore）实现：把模型给出的引用解析为
// 具体目标。refs 为空表示用默认目标（UI 钉住集合）。
//
// 契约：Names 与 Resolve 对相同 refs、同一清单快照必须产出相同的
// 主机名集合（Names 是 Resolve 的免连接投影）——确认弹窗显示的主机
// 必须恒等于实际执行的主机。这是 ConfirmRequest.Hosts 特性的承重
// 不变量。
type TargetResolver interface {
	// Names 展开引用为主机名（不建立连接）；供确认弹窗等便宜路径使用。
	Names(refs []string) (names []string, unresolved []string)
	// Resolve 展开引用并绑定执行器（建立/复用 SSH 连接）。
	Resolve(refs []string) (targets []Target, unresolved []string, err error)
}

// defaultMaxParallel 限制 fan-out 的逐主机并发数，避免一次多主机调用
// 打爆连接数或对目标机形成瞬时风暴。
const defaultMaxParallel = 8

// aggregateOutputBudget 是多目标聚合输出的总预算（字符）。每主机上限
// = budget / 主机数（下限 500），防几十台机器的日志类输出撑爆模型
// 上下文。单目标不截断（透传语义）。
const aggregateOutputBudget = 10000

type Registry struct {
	plugins    []Plugin
	toolMap    map[string]Plugin
	riskLevels map[string]safety.Level
	targeting  map[string]bool
	resolver   TargetResolver
}

func NewRegistry() *Registry {
	return &Registry{
		toolMap:    make(map[string]Plugin),
		riskLevels: make(map[string]safety.Level),
		targeting:  make(map[string]bool),
	}
}

func (r *Registry) Register(p Plugin) {
	r.plugins = append(r.plugins, p)
	for _, td := range p.Tools() {
		r.toolMap[td.Name] = p
		r.riskLevels[td.Name] = td.RiskLevel
		r.targeting[td.Name] = td.Targeting
	}
}

// SetResolver 注入目标解析器。目标型工具（Targeting=true）的 fan-out
// 依赖它；控制面工具不受影响。
func (r *Registry) SetResolver(res TargetResolver) { r.resolver = res }

// SetGate 注入安全门并把本注册表的 RiskLevel 单源化：插件 ToolDefinition
// 声明的 RiskLevel 是唯一真相，全部灌进 gate 的静态表——agent.Classify 不
// 再依赖 safety 包手工维护的第二份表（那份已删除，历史上曾漂移：find/
// grep/ping/dns_lookup/http_check/query_consensus 六个只读工具因缺表
// 误弹确认，评审 ①-4 修复）。必须在 Register 之后调用；未注入时
// ClassifyStatic 一律 Risky（fail-safe）。
func (r *Registry) SetGate(g *safety.Gate) {
	for _, td := range r.Tools() {
		g.SetStaticLevel(td.Name, td.RiskLevel)
	}
}

func (r *Registry) Tools() []ToolDefinition {
	var all []ToolDefinition
	for _, p := range r.plugins {
		all = append(all, p.Tools()...)
	}
	return all
}

// hostsInput 只提取 fan-out 需要的目标引用；其余字段原样留给插件。
type hostsInput struct {
	Hosts []string `json:"hosts"`
}

// Execute 分发工具调用。控制面工具（Targeting=false）原样直发（nil
// exec）；目标型工具由 resolver 解析 hosts 引用（refs 为空 = 默认目标），
// 有界并行（defaultMaxParallel）逐主机执行后聚合为单一结果。hosts 字段
// 类型错误显式拒绝（不静默改道默认目标）；单主机 panic/失败降级为该主机
// 的 [FAILED] 分节。
func (r *Registry) Execute(ctx context.Context, toolName string, input json.RawMessage) (ToolResult, error) {
	p, ok := r.toolMap[toolName]
	if !ok {
		return ToolResult{}, fmt.Errorf("unknown tool: %s", toolName)
	}
	if !r.targeting[toolName] {
		return p.Execute(ctx, toolName, input, nil)
	}
	if r.resolver == nil {
		return ToolResult{}, fmt.Errorf("no target resolver configured for targeting tool %s", toolName)
	}

	var hi hostsInput
	if err := json.Unmarshal(input, &hi); err != nil {
		// 解析失败必须显式拒绝：静默吞掉会让 Hosts 为 nil，被当成
		// 「未指定目标」改道 resolver 默认目标（钉住机器）执行。
		return ToolResult{Content: fmt.Sprintf("Error: invalid hosts field: %v", err), IsError: true}, nil
	}
	targets, unresolved, err := r.resolver.Resolve(hi.Hosts)
	if err != nil {
		return ToolResult{}, err
	}
	if len(targets) == 0 {
		if len(unresolved) > 0 {
			return ToolResult{Content: fmt.Sprintf("Error: no target hosts resolved (unresolved refs: %s)", strings.Join(unresolved, ", ")), IsError: true}, nil
		}
		return ToolResult{Content: "Error: no target hosts resolved (no default targets set)", IsError: true}, nil
	}

	results := make([]perHost, len(targets))
	sem := make(chan struct{}, defaultMaxParallel)
	var wg sync.WaitGroup
	for i, t := range targets {
		wg.Add(1)
		go func(i int, t Target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			// 插件 panic 不得杀死进程（桌面应用会整体崩）：
			// 降级为该主机的 [FAILED] 分节。
			defer func() {
				if r := recover(); r != nil {
					results[i] = perHost{name: t.Name, err: fmt.Errorf("plugin panic: %v", r)}
				}
			}()
			res, err := p.Execute(ctx, toolName, input, t.Exec)
			results[i] = perHost{name: t.Name, res: res, err: err}
		}(i, t)
	}
	wg.Wait()
	return aggregate(results, unresolved), nil
}

// perHost 是单主机的 fan-out 结果。
type perHost struct {
	name string
	res  ToolResult
	err  error
}

// aggregate 把逐主机结果拼成单一 ToolResult。
// 单目标：原样透传（与旧行为逐字节一致）。多目标：===== host ===== 分节，
// 失败主机标 [FAILED]；每主机内容超预算时头偏置截断（分节头与
// [FAILED] 标记永不截断）。全部失败才算工具级 IsError，部分失败不设
// 错误位；未解析的引用附在末尾回传给模型。
func aggregate(results []perHost, unresolved []string) ToolResult {
	// 多目标才设每主机截断上限；0 表示不截断（单目标透传）。
	perHostCap := 0
	if len(results) > 1 {
		perHostCap = max(500, aggregateOutputBudget/len(results))
	}
	var b strings.Builder
	hosts := make([]string, 0, len(results))
	failed := 0
	for i, r := range results {
		hosts = append(hosts, r.name)
		if len(results) > 1 {
			if i > 0 {
				b.WriteString("\n\n")
			}
			if r.err != nil || r.res.IsError {
				fmt.Fprintf(&b, "===== %s [FAILED] =====\n", r.name)
				failed++
			} else {
				fmt.Fprintf(&b, "===== %s =====\n", r.name)
			}
		}
		if r.err != nil {
			b.WriteString(truncateHostOutput(fmt.Sprintf("Error: %v", r.err), perHostCap))
			continue
		}
		b.WriteString(truncateHostOutput(r.res.Content, perHostCap))
	}
	var isError bool
	if len(results) == 1 {
		isError = results[0].err != nil || results[0].res.IsError
	} else {
		isError = failed == len(results) // 全部失败才算工具级错误
	}
	if len(unresolved) > 0 {
		fmt.Fprintf(&b, "\n\n未解析的目标引用（清单中不存在，已跳过）: %s", strings.Join(unresolved, ", "))
	}
	return ToolResult{Content: b.String(), IsError: isError, Hosts: hosts}
}

// truncateHostOutput 在超限时保留头部并标注省略量（cap<=0 不截断）。
// 截断点回退到 rune 边界：中文运维输出常见，按字节切开会产生乱码。
func truncateHostOutput(s string, cap int) string {
	if cap <= 0 || len(s) <= cap {
		return s
	}
	for cap > 0 && !utf8.RuneStart(s[cap]) {
		cap--
	}
	return s[:cap] + fmt.Sprintf("\n...[host output truncated, %d chars omitted]...", len(s)-cap)
}

// IsTargeting 报告工具是否目标型（输入带 hosts、按主机 fan-out）。
func (r *Registry) IsTargeting(toolName string) bool {
	return r.targeting[toolName]
}

// TargetNames 展开输入里的目标引用为主机名，供确认弹窗展示影响范围。
// 尽力而为：resolver 未配置、工具非目标型、或解析出错时返回 nil，
// 绝不阻塞确认流程。
func (r *Registry) TargetNames(toolName string, input json.RawMessage) []string {
	if !r.targeting[toolName] || r.resolver == nil {
		return nil
	}
	var hi hostsInput
	if err := json.Unmarshal(input, &hi); err != nil {
		// 解析失败必须显式返回 nil 而非落进 Names(nil)：nil refs 会
		// 被 resolver 当成「默认目标」，弹窗就会显示 Execute 实际
		// 会拒绝的幽灵主机清单（Execute 对同样输入显式拒绝）。
		return nil
	}
	names, _ := r.resolver.Names(hi.Hosts)
	return names
}

func (r *Registry) RiskLevel(toolName string) safety.Level {
	if level, ok := r.riskLevels[toolName]; ok {
		return level
	}
	return safety.LevelRisky
}

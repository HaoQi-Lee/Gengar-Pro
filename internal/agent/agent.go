package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"gengar-pro/internal/audit"
	"gengar-pro/internal/claude"
	"gengar-pro/internal/config"
	"gengar-pro/internal/consensus"
	"gengar-pro/internal/observability"
	"gengar-pro/internal/plugin"
	"gengar-pro/internal/safety"
	"gengar-pro/internal/ui"
)

const systemPrompt = `你是 gengar-pro，一个多主机 Linux 运维指挥台，运行在运维工程师的桌面控制端，通过 SSH 在远程服务器上执行操作。

你的职责：
- 理解用户的运维意图，在目标服务器上使用工具完成任务
- 故障排查时系统性逐步检查，先收集信息再下结论
- 变更操作前先检查当前状态；涉及配置文件修改时建议先备份
- 每次操作后简要说明做了什么、各主机结果如何
- 多主机结果要对比汇总，指出差异与异常（如「web-01 正常，web-02 失败」）

目标选择（重要）：
- 目标型工具都带 hosts 参数，取值可以是：主机名（如 web-01）、标签/机组名（如 web）、或 "all"
- 用户明确指定目标时，把指定值传给 hosts
- 用户说「这些机器 / 我选的 / 钉住的」或未提目标时，省略 hosts（系统会用默认目标）
- 默认目标为空或有歧义时，用 ask_user 问清楚，不要猜
- 变更类操作（重启、删除、安装）执行前，在回复里明确列出将影响的主机

工具优先级：
- 优先使用专用工具，下列场景必须用对应专用工具而非 execute_command：
  - 查端口 → check_port
  - 查进程 → check_process
  - 查磁盘/内存/网络 → check_disk / check_memory / check_network
  - 读写文件/列目录/查文件 → read_file / write_file / list_directory / find_files / grep_files
  - Docker 操作 → docker_ps / docker_logs / docker_exec / docker_compose_up / docker_compose_down
  - 服务管理 → systemctl_status / systemctl_start / systemctl_stop / systemctl_restart / journalctl_logs
  - 软件包 → package_install / package_remove / package_search / package_list
  - 网络连通性 → ping / dns_lookup / http_check（在目标机上发起，可测机器间连通性）
  - 系统信息 → system_info
  - 日志查看 → tail_log / journalctl_logs / docker_logs
- 仅当以上工具都不适用时，才使用 execute_command（作为 fallback）
- 不确定的情况下，询问用户而不是猜测

输出格式约束（纯文本环境）：
- 面向用户的文本不使用 Markdown 语法（无 # 标题、** 加粗、反引号代码块、表格管线）
- 列表用 "- " 或 "1) " 开头；命令、路径、文件名直接裸写
- 需要表格时用 ASCII 边框（+----+----+ 与 | 对齐），保证等宽字体下不串行
- 多主机结果用「主机名: 结果」逐行汇报

需求澄清指南：
- 当用户的意图不明确、存在多种理解方式时，使用 ask_user 工具提问
- 典型场景：操作目标不明确（哪台机器 / 哪个机组）、影响范围不明确、优先级不明确
- 不要在已有足够信息时过度提问，优先根据上下文推理
- 每次只问一个最关键的问题；选项设计要互斥、完整
- 技术细节选择应自行决策，只在影响业务逻辑的选择上才问用户`

type ClaudeClient interface {
	SendMessage(ctx context.Context, system string, messages []claude.Message, tools []claude.Tool) (*claude.APIResponse, error)
	MessagesStream(ctx context.Context, system string, messages []claude.Message, tools []claude.Tool) (<-chan claude.StreamEvent, func(), error)
}

type Agent struct {
	cfg      *config.Config
	client   ClaudeClient
	registry *plugin.Registry
	gate     *safety.Gate
	ui       ui.Sink
	conv     *Conversation
	history  []string
	log      observability.Logger
	// catalog 注入动态主机清单文本，每次 ProcessInput 重新取值并拼进
	// 系统提示词（主机清单可能随会话中途增删而变化）。
	catalog  func() string
	auditLog AuditLogger
	session  string
	// basePrompt 是 New 时拼好的静态部分（系统提示词 + 共识目录 + 记忆指南），
	// ProcessInput 每次以 basePrompt + catalog() 重建完整提示词。
	basePrompt string
}

// Option configures an Agent during construction.
type Option func(*Agent)

// WithLogger injects a structured event logger. Defaults to Noop.
func WithLogger(l observability.Logger) Option {
	return func(a *Agent) { a.log = l }
}

// WithCatalog 注入动态主机清单文本（每次 ProcessInput 重新取值）。
func WithCatalog(fn func() string) Option {
	return func(a *Agent) { a.catalog = fn }
}

// AuditLogger 是审计落盘的最小接口；webapi 用按用户盖 User 戳的包装器
// 注入，*audit.Logger 天然满足。
//
// 契约（禁用审计须传字面 nil）：WithAudit 的归一化只识别
// (*audit.Logger)(nil) 这一种 typed-nil；包装器实现不得传自身类型的 nil
// 指针（会以非 nil 接口落入 Agent，调用时 panic），内层 logger 为 nil 时
// 不得包装——调用方自行改传字面 nil。
type AuditLogger interface {
	Log(entry audit.Entry) error
}

// WithAudit 注入审计日志；nil 表示不审计。调用方在 audit.Open 失败时会把
// *audit.Logger 置 nil 后照常传参——typed-nil 装箱成接口后 != nil，这里
// 归一化为 nil，保证「审计未启用」语义不因接口化而破坏。
func WithAudit(l AuditLogger) Option {
	return func(a *Agent) {
		if al, ok := l.(*audit.Logger); ok && al == nil {
			l = nil
		}
		a.auditLog = l
	}
}

// WithSession 标记审计流里的会话标识。
func WithSession(id string) Option {
	return func(a *Agent) { a.session = id }
}

func New(cfg *config.Config, client ClaudeClient, registry *plugin.Registry, gate *safety.Gate, sink ui.Sink, c *consensus.Consensus, opts ...Option) *Agent {
	prompt := systemPrompt
	if c != nil && len(c.Guidelines) > 0 {
		prompt = prompt + "\n\n" + c.Catalog()
	}
	prompt = prompt + "\n\n" + memoryGuide()
	a := &Agent{
		cfg:        cfg,
		client:     client,
		registry:   registry,
		gate:       gate,
		ui:         sink,
		conv:       NewConversation(prompt),
		log:        observability.Noop(),
		basePrompt: prompt,
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

func memoryGuide() string {
	return `=== 服务器记忆（Server Memory）使用指南 ===
你可以使用 Memory 按主机持久化记住服务器的状态，避免重复探测。

可用分类：
- system_info: 操作系统、内核、CPU、内存、磁盘等基本信息
- history: Agent 执行过的关键操作记录

使用原则：
1. 所有 memory 工具调用必须带 host 参数（受管主机名），各主机记忆互相隔离
2. 首次需要服务器基本信息时，先 memory_read(host=<主机名>, category=system_info) 检查是否已有记录
3. 如果没有记录或 updated_at 超过 7 天，执行探测命令并 memory_write 保存
4. 执行会改变系统状态的重要操作后（安装软件、修改配置、启动服务），先 memory_read(history)、追加新记录、再 memory_write
5. history 每条记录建议包含：timestamp（ISO 8601）、action（简短摘要）、details（命令或变更内容）
6. history 只追加不删除，方便后续会话了解历史变更

过期判断参考：
- system_info: 基本不变（可信任 7 天）
- history: 不过期

Memory 文件：~/.gengar-pro/memory/<host>.json`
}

func (a *Agent) ProcessInput(ctx context.Context, input string) string {
	a.log.Log(observability.Event{
		Timestamp: time.Now().Format(time.RFC3339Nano),
		Type:      "user_input",
		Text:      input,
	})

	// 主机清单可能随会话中途增删而变化，每次输入开始时重建系统提示词。
	if a.catalog != nil {
		a.conv.SetSystemPrompt(a.basePrompt + "\n\n" + a.catalog())
	}

	a.conv.AddUserMessage(input)

	maxSteps := a.cfg.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 20
	}

	pluginTools := a.registry.Tools()
	claudeTools := make([]claude.Tool, len(pluginTools))
	for i, td := range pluginTools {
		claudeTools[i] = claude.Tool{Name: td.Name, Description: td.Description, InputSchema: td.InputSchema}
	}

	for step := 0; step < maxSteps; step++ {
		// 上下文压缩（评审 ②-1）：每步调 LLM 前检查估算值，超 0.8×上限时
		// 把最旧的 tool_result 替换为占位符（保留最近 6 条完整）。长会话从
		// 「必死」（超上下文 API 报错且不可恢复）变为可持续。
		if ShouldCompress(a.conv, 0) {
			if omitted := CompressConversation(a.conv); omitted > 0 {
				a.ui.Info(fmt.Sprintf("上下文接近上限：已省略较旧的工具输出（约 %d 字符），对话可继续。", omitted))
				a.log.Log(observability.Event{
					Timestamp: time.Now().Format(time.RFC3339Nano),
					Type:      "context_compressed",
					Step:      step + 1,
					Output:    fmt.Sprintf("%d chars elided", omitted),
				})
			}
		}
		a.log.Log(observability.Event{
			Timestamp: time.Now().Format(time.RFC3339Nano),
			Type:      "step_start",
			Step:      step + 1,
		})
		a.ui.Step(step+1, maxSteps)
		a.ui.Thinking(true)

		stream, cancel, err := a.client.MessagesStream(ctx, a.conv.SystemPrompt(), a.conv.Messages(), claudeTools)
		if err != nil {
			a.ui.Thinking(false)
			a.ui.Error(fmt.Sprintf("API 调用失败: %v", err))
			a.log.Log(observability.Event{
				Timestamp: time.Now().Format(time.RFC3339Nano),
				Type:      "api_error",
				Step:      step + 1,
				Error:     err.Error(),
			})
			return ""
		}

		thinkingDone := false
		stopReason := ""
		var assistantContent []claude.ContentBlock
		var textBuf strings.Builder
		textOpen := false

		type pendingTool struct {
			id, name string
			input    json.RawMessage
		}
		var toolCalls []pendingTool

		streamClosed := false
		for !streamClosed {
			select {
			case <-ctx.Done():
				cancel()
				a.ui.Thinking(false)
				if textOpen {
					a.ui.AssistantTextEnd()
					textOpen = false
				}
				if textBuf.Len() > 0 {
					assistantContent = append(assistantContent, claude.ContentBlock{Type: "text", Text: textBuf.String()})
				}
				// 取消中毒修复（评审 ②-3）：已收集的 tool_use 若没有对应
				// tool_result，API 的下一轮请求会被消息校验拒绝——特定时点
				// 取消会把整个会话永久报废。先落 assistant 消息（含
				// tool_use），再补写配对的 tool_result，保持消息序列完整。
				a.conv.AddAssistantMessage(assistantContent)
				for _, tc := range toolCalls {
					a.conv.AddToolResult(tc.id, "cancelled by user")
				}
				a.ui.Warning("[cancelled by user]")
				return ""
			case ev, ok := <-stream:
				if !ok {
					streamClosed = true
					continue
				}
				switch ev.Type {
				case claude.EventContentBlockStart:
					// 只在可见块（text/tool_use）开始时撤下 spinner。thinking 等
					// 未识别块（BlockUnknown）的 delta 不渲染，若提前撤下 spinner，
					// 长推理期间界面无任何反馈，观感如同卡死。
					if !thinkingDone && ev.Block != claude.BlockUnknown {
						a.ui.Thinking(false)
						thinkingDone = true
					}
					if ev.Block == claude.BlockText {
						textBuf.Reset()
						textOpen = true
					}
				case claude.EventContentBlockDelta:
					if ev.Block == claude.BlockText {
						a.ui.AssistantTextDelta(ev.Text)
						textBuf.WriteString(ev.Text)
					}
				case claude.EventContentBlockStop:
					if ev.Block == claude.BlockText && textOpen {
						a.ui.AssistantTextEnd()
						if textBuf.Len() > 0 {
							text := textBuf.String()
							assistantContent = append(assistantContent, claude.ContentBlock{Type: "text", Text: text})
							a.log.Log(observability.Event{
								Timestamp: time.Now().Format(time.RFC3339Nano),
								Type:      "response",
								Step:      step + 1,
								Text:      text,
							})
						}
						textBuf.Reset()
						textOpen = false
					}
					if ev.Block == claude.BlockToolUse {
						input := ev.ToolInput
						if len(input) == 0 {
							input = json.RawMessage(`{}`)
						}
						toolCalls = append(toolCalls, pendingTool{id: ev.ToolID, name: ev.ToolName, input: input})
						assistantContent = append(assistantContent, claude.ContentBlock{
							Type:  "tool_use",
							ID:    ev.ToolID,
							Name:  ev.ToolName,
							Input: input,
						})
						a.log.Log(observability.Event{
							Timestamp: time.Now().Format(time.RFC3339Nano),
							Type:      "tool_call",
							Step:      step + 1,
							ToolName:  ev.ToolName,
							ToolID:    ev.ToolID,
							Input:     input,
						})
					}
				case claude.EventMessageDelta:
					if ev.StopReason != "" {
						stopReason = ev.StopReason
					}
				case claude.EventMessageStop:
					// stream end; loop exits via channel close (ok == false)
				case claude.EventError:
					cancel()
					a.ui.Thinking(false)
					a.ui.Error(fmt.Sprintf("API 流式错误: %s", ev.Error))
					a.log.Log(observability.Event{
						Timestamp: time.Now().Format(time.RFC3339Nano),
						Type:      "api_error",
						Step:      step + 1,
						Error:     ev.Error,
					})
					return ""
				}
			}
		}
		cancel()
		if !thinkingDone {
			a.ui.Thinking(false)
		}

		a.conv.AddAssistantMessage(assistantContent)

		if stopReason == "end_turn" || len(toolCalls) == 0 {
			a.log.Log(observability.Event{
				Timestamp:  time.Now().Format(time.RFC3339Nano),
				Type:       "run_end",
				Step:       step + 1,
				StopReason: stopReason,
			})
			finalText := ""
			for _, b := range assistantContent {
				if b.Type != "text" {
					continue
				}
				if finalText != "" {
					finalText += "\n"
				}
				finalText += b.Text
			}
			if stopReason == "max_tokens" {
				// 不允许静默截断：巡检报告等长输出被上限掐断时留痕（曾以 4096 复现缺尾）
				a.ui.Warning("回复达到单次输出上限（max_tokens）被截断——发送「继续」可让模型补全剩余部分。")
			}
			return finalText
		}

		// 单次 API 响应内的 risky 命令一次性批量确认（一个弹窗），而非逐条弹。
		// ask_user 不参与（它是澄清提问，按原顺序在下方处理）。
		var batchReqs []ui.ConfirmRequest
		for _, tc := range toolCalls {
			if tc.name == "ask_user" {
				continue
			}
			if a.gate.Classify(tc.name, tc.input) != safety.LevelRisky {
				continue
			}
			highRisk := false
			if tc.name == "execute_command" {
				var params struct {
					Command string `json:"command"`
				}
				_ = json.Unmarshal(tc.input, &params)
				highRisk = a.gate.MatchHighRisk(params.Command)
			}
			batchReqs = append(batchReqs, ui.ConfirmRequest{
				ToolName: tc.name,
				Summary:  ui.FormatConfirmSummary(tc.name, tc.input),
				HighRisk: highRisk,
				Hosts:    a.registry.TargetNames(tc.name, tc.input),
			})
		}
		batchApproved := true
		if len(batchReqs) > 0 {
			batchApproved = a.ui.ConfirmBatch(batchReqs)
		}

		for _, tc := range toolCalls {
			// ask_user: interactive clarification, skip safety gate
			if tc.name == "ask_user" {
				answer, err := a.ui.AskUser(tc.input)
				if err != nil {
					a.conv.AddToolResult(tc.id, fmt.Sprintf("Error: %v", err))
					a.log.Log(observability.Event{
						Timestamp: time.Now().Format(time.RFC3339Nano),
						Type:      "tool_result",
						Step:      step + 1,
						ToolID:    tc.id,
						Error:     err.Error(),
					})
					// ask_user 同样落审计：本分支曾提前 continue 整体绕过
					// ——澄清提问是用户直接决议的工具调用，零记录不可接受。
					// 提问面向用户而非主机，Hosts 留空；Output 记错误摘要以
					// 区分于回答。
					if a.auditLog != nil {
						if aerr := a.auditLog.Log(audit.Entry{
							TS:       time.Now().Format(time.RFC3339),
							Session:  a.session,
							Tool:     tc.name,
							Input:    tc.input,
							Approved: true, // 澄清不走安全门，即视为放行
							IsError:  true,
							Output:   "ask error: " + err.Error(),
						}); aerr != nil {
							a.log.Log(observability.Event{
								Timestamp: time.Now().Format(time.RFC3339Nano),
								Type:      "audit_error",
								Error:     aerr.Error(),
							})
						}
					}
					continue
				}
				a.conv.AddToolResult(tc.id, answer)
				a.log.Log(observability.Event{
					Timestamp: time.Now().Format(time.RFC3339Nano),
					Type:      "tool_result",
					Step:      step + 1,
					ToolID:    tc.id,
					Output:    answer,
				})
				if a.auditLog != nil {
					if aerr := a.auditLog.Log(audit.Entry{
						TS:       time.Now().Format(time.RFC3339),
						Session:  a.session,
						Tool:     tc.name,
						Input:    tc.input,
						Approved: true,
						IsError:  false,
						Output:   answer,
					}); aerr != nil {
						a.log.Log(observability.Event{
							Timestamp: time.Now().Format(time.RFC3339Nano),
							Type:      "audit_error",
							Error:     aerr.Error(),
						})
					}
				}
				continue
			}

			level := a.gate.Classify(tc.name, tc.input)

			approved := true
			if level == safety.LevelRisky {
				approved = batchApproved // 复用上方一次性批量决议
			}
			if !approved {
				a.log.Log(observability.Event{
					Timestamp:   time.Now().Format(time.RFC3339Nano),
					Type:        "safety_block",
					Step:        step + 1,
					ToolName:    tc.name,
					ToolID:      tc.id,
					Input:       tc.input,
					SafetyLevel: "risky",
				})
				a.conv.AddToolResult(tc.id, "Error: User rejected this operation.")
				if a.auditLog != nil {
					if aerr := a.auditLog.Log(audit.Entry{
						TS:       time.Now().Format(time.RFC3339),
						Session:  a.session,
						Tool:     tc.name,
						Input:    tc.input,
						Hosts:    a.registry.TargetNames(tc.name, tc.input),
						Approved: level != safety.LevelRisky || batchApproved,
						IsError:  true,
						Output:   "user rejected",
					}); aerr != nil {
						a.log.Log(observability.Event{
							Timestamp: time.Now().Format(time.RFC3339Nano),
							Type:      "audit_error",
							Error:     aerr.Error(),
						})
					}
				}
				continue
			}

			a.ui.ToolCallStart(tc.id, ui.FormatToolDisplay(tc.name, tc.input))

			result, err := a.registry.Execute(ctx, tc.name, tc.input)
			if err != nil {
				a.ui.ToolCallResult(tc.id, fmt.Sprintf("Error: %v", err), false)
				a.log.Log(observability.Event{
					Timestamp: time.Now().Format(time.RFC3339Nano),
					Type:      "tool_result",
					Step:      step + 1,
					ToolID:    tc.id,
					Error:     err.Error(),
				})
				a.conv.AddToolResult(tc.id, fmt.Sprintf("Error: %v", err))
				// 分发失败也要落审计：能走到这里说明确认已通过（未知工具、
				// resolver 连接失败等），用户批准过的操作零记录不可接受。
				if a.auditLog != nil {
					if aerr := a.auditLog.Log(audit.Entry{
						TS:       time.Now().Format(time.RFC3339),
						Session:  a.session,
						Tool:     tc.name,
						Input:    tc.input,
						Hosts:    a.registry.TargetNames(tc.name, tc.input),
						Approved: true, // 已过确认才可能进入执行
						IsError:  true,
						Output:   fmt.Sprintf("dispatch error: %v", err),
					}); aerr != nil {
						a.log.Log(observability.Event{
							Timestamp: time.Now().Format(time.RFC3339Nano),
							Type:      "audit_error",
							Error:     aerr.Error(),
						})
					}
				}
				continue
			}

			// 多主机聚合已被 Registry 的 per-host 截断约束在预算内；这里
			// 再过 4000/1500 的 head/tail 截断会砍掉中间主机的分节头
			// （[FAILED] 标记随之静默消失），所以仅单主机/控制面结果走
			// agent 层截断。
			content := result.Content
			if len(result.Hosts) <= 1 {
				content = claude.TruncateOutput(result.Content, 4000, 1500)
			}
			if result.IsError {
				content = "Command failed:\n" + content
			}
			a.ui.ToolCallResult(tc.id, content, !result.IsError)
			a.log.Log(observability.Event{
				Timestamp: time.Now().Format(time.RFC3339Nano),
				Type:      "tool_result",
				Step:      step + 1,
				ToolID:    tc.id,
				Output:    content,
			})
			a.conv.AddToolResult(tc.id, content)
			if a.auditLog != nil {
				if aerr := a.auditLog.Log(audit.Entry{
					TS:       time.Now().Format(time.RFC3339),
					Session:  a.session,
					Tool:     tc.name,
					Input:    tc.input,
					Hosts:    result.Hosts,
					Approved: level != safety.LevelRisky || batchApproved,
					IsError:  result.IsError,
					Output:   content,
				}); aerr != nil {
					a.log.Log(observability.Event{
						Timestamp: time.Now().Format(time.RFC3339Nano),
						Type:      "audit_error",
						Error:     aerr.Error(),
					})
				}
			}
			a.history = append(a.history, fmt.Sprintf("%s: %s", tc.name, string(tc.input)))
		}
	}

	a.ui.Warning(fmt.Sprintf("已达到步骤上限 (%d)，任务可能未完成。", maxSteps))
	a.log.Log(observability.Event{
		Timestamp:  time.Now().Format(time.RFC3339Nano),
		Type:       "run_end",
		Step:       maxSteps,
		StopReason: "max_steps",
	})
	return ""
}

func (a *Agent) History() []string { return a.history }
func (a *Agent) Reset()            { a.conv.Clear(); a.history = nil }

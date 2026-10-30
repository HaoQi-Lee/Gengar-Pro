// gengar-headless 是 gengar-pro 的无 GUI 验证入口（测试设施，非产品）：
// stdin 驱动 Agent 循环，plain Sink 输出，斜杠命令与 Skill 全保留。
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"gengar-pro/internal/appcore"
	"gengar-pro/internal/audit"
	"gengar-pro/internal/command"
	"gengar-pro/internal/consensus"
	"gengar-pro/internal/executor"
	"gengar-pro/internal/host"
	"gengar-pro/internal/skill"
	"gengar-pro/internal/ui"
	"gengar-pro/internal/ui/plain"
)

func main() {
	home, _ := os.UserHomeDir()
	var (
		dataDir string
		autoYes bool
		pinAll  bool
		secrets string
	)
	flag.StringVar(&dataDir, "data", filepath.Join(home, ".gengar-pro"), "数据目录（清单/记忆/审计）")
	flag.BoolVar(&autoYes, "yes", false, "自动批准全部 Risky 确认（自动化测试用）")
	flag.BoolVar(&pinAll, "pin-all", false, "把清单内全部主机设为默认目标")
	flag.StringVar(&secrets, "secrets", os.Getenv("GENGAR_SECRETS"), "内联秘密 ref=pass,...（替代系统钥匙串；值不能含逗号，键值首尾空格会被修剪）")
	flag.Parse()

	// headless 是测试设施，不碰系统钥匙串（Web 化后产品路径是 EncryptedStore）：
	// 默认空内存实现，仅 --secrets / GENGAR_SECRETS 注入内联秘密。
	secretStore := host.ParseEnvSecrets("")
	if secrets != "" {
		secretStore = host.ParseEnvSecrets(secrets)
	}

	timeout := 30 * time.Second
	if d, err := time.ParseDuration(os.Getenv("GENGAR_COMMAND_TIMEOUT")); err == nil && d > 0 {
		timeout = d
	}

	// OpenAt 不建父目录；headless 面向一次性数据目录（CI / 集成测试），
	// 由入口负责创建，空目录直接可用。
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "错误: 创建数据目录 %s: %v\n", dataDir, err)
		os.Exit(1)
	}
	// known_hosts 随数据目录走（与 gengar-server 入口语义一致；TOFU 记录
	// 隔离进一次性数据目录）。须在任何 SSH 拨号前设置。
	executor.SetKnownHostsPath(filepath.Join(dataDir, "known_hosts"))
	core, err := appcore.OpenAt(dataDir, secretStore, timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 打开数据目录 %s: %v\n", dataDir, err)
		os.Exit(1)
	}
	defer core.Close()

	cfg := appcore.LoadConfig(core.Store(), core.Secrets())
	if cfg.APIKey == "" {
		fmt.Fprintln(os.Stderr, "错误: 未配置 API Key（设置页或 ANTHROPIC_API_KEY）。")
		os.Exit(1)
	}

	auditLog, err := audit.Open(filepath.Join(dataDir, "audit.jsonl"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "警告: 审计未启用: %v\n", err)
		auditLog = nil
	}
	if auditLog != nil {
		defer auditLog.Close()
	}

	// csns 必须非 nil（BuildAgent 前置条件；加载失败直接退出）
	csns, err := consensus.Load(os.Getenv("GENGAR_ENV"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 无法加载共识数据: %v\n", err)
		os.Exit(1)
	}

	if pinAll {
		ids := make([]string, 0)
		if hosts, err := core.Store().List(); err == nil {
			for _, h := range hosts {
				ids = append(ids, h.ID)
			}
		}
		core.SetPinned(ids)
	}
	if autoYes {
		ui.ConfirmAutoApprove = true
	}

	sink := plain.New(os.Stdout)
	session := time.Now().Format("20060102-150405")
	a, err := core.BuildAgent(cfg, sink, csns, auditLog, session)
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 组装 Agent 失败: %v\n", err)
		os.Exit(1)
	}

	skills, err := skill.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "错误: 无法加载 Skill 数据: %v\n", err)
		os.Exit(1)
	}
	skillCmds := make([]command.Command, 0, len(skills))
	for _, s := range skills {
		skillCmds = append(skillCmds, s.ToCommand())
	}
	reg := command.NewRegistry(skillCmds...)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	n := 0
	if hosts, err := core.Store().List(); err == nil {
		n = len(hosts)
	}
	fmt.Printf("🔮 gengar-headless · 会话 %s · 主机 %d 台 · 斜杠命令触发 Skill（/inspect /install）\n", session, n)
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "/") {
			cmdName, args := command.SplitArgs(line)
			if cmd := reg.Lookup(cmdName); cmd != nil {
				cmd.Run(command.Context{Agent: a, Out: ui.NewLineWriter(sink.Info), Ctx: ctx, Args: args})
				continue
			}
		}
		_ = a.ProcessInput(ctx, line)
	}
	// EOF 之外的读取失败（ErrTooLong 截断、IO 错误）必须非零退出，
	// 否则 CI 会把被截断的脚本静默当作全部通过。
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "错误: 读取 stdin: %v\n", err)
		os.Exit(1)
	}
}

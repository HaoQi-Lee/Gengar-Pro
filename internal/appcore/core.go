// Package appcore 是 gengar-pro 两条入口（headless / 桌面）共享的组装层：
// 主机清单 + 执行管理 + 钉住集合 + 目标解析 + Agent 构建。
package appcore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gengar-pro/internal/agent"
	"gengar-pro/internal/claude"
	"gengar-pro/internal/config"
	"gengar-pro/internal/consensus"
	"gengar-pro/internal/executor"
	"gengar-pro/internal/host"
	"gengar-pro/internal/memory"
	"gengar-pro/internal/plugin"
	"gengar-pro/internal/safety"
	"gengar-pro/internal/ui"
)

type Core struct {
	store   *host.Store
	secrets host.SecretStore
	manager *executor.Manager
	memDir  string
	dataDir string
	timeout time.Duration

	mu     sync.Mutex
	pinned []string // host IDs
}

// OpenAt 指定数据目录（测试与自定义安装位置）。目录结构：
// gengar.db · memory/ · audit.jsonl · known_hosts（executor 模块自管）。
func OpenAt(dataDir string, secrets host.SecretStore, timeout time.Duration) (*Core, error) {
	store, err := host.OpenStore(filepath.Join(dataDir, "gengar.db"))
	if err != nil {
		return nil, err
	}
	memDir := filepath.Join(dataDir, "memory")
	if _, err := memory.OpenStoreDir(memDir); err != nil {
		return nil, err
	}
	return &Core{
		store:   store,
		secrets: secrets,
		manager: executor.NewManager(secrets, timeout),
		memDir:  memDir,
		dataDir: dataDir,
		timeout: timeout,
	}, nil
}

func (c *Core) Store() *host.Store { return c.store }
func (c *Core) DataDir() string    { return c.dataDir }

// Secrets 透出秘密存储（桌面录入表单写密码用）。
func (c *Core) Secrets() host.SecretStore { return c.secrets }

// SetSecrets 替换秘密存储并重建连接管理器。供 server 入口在 OpenAt 之后
// 装配 EncryptedStore（OpenAt 先传占位实现）；必须在任何 SSH 连接
// 建立前调用（启动期单线程，天然满足）。
func (c *Core) SetSecrets(secrets host.SecretStore) {
	c.secrets = secrets
	c.manager = executor.NewManager(secrets, c.timeout)
}

// ExecutorFor 透出连接管理（桌面连通性测试用）。
func (c *Core) ExecutorFor(h *host.Host) (executor.Executor, error) {
	return c.manager.ExecutorFor(h)
}

func (c *Core) Close() error {
	c.manager.CloseAll()
	return c.store.Close()
}

// ---- 钉住集合（UI 的默认目标） ----

func (c *Core) SetPinned(ids []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pinned = ids
}

func (c *Core) PinnedIDs() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.pinned...)
}

// ---- plugin.TargetResolver 实现 ----

// inventory 读清单并建 ID 索引。
func (c *Core) inventory() ([]*host.Host, map[string]*host.Host) {
	hosts, err := c.store.List()
	if err != nil {
		return nil, nil
	}
	byID := make(map[string]*host.Host, len(hosts))
	for _, h := range hosts {
		byID[h.ID] = h
	}
	return hosts, byID
}

// pinnedHosts 返回钉住集合对应的主机（按钉住顺序，跳过已删除项）。
func (c *Core) pinnedHosts() []*host.Host {
	_, byID := c.inventory()
	ids := c.PinnedIDs()
	out := make([]*host.Host, 0, len(ids))
	for _, id := range ids {
		if h, ok := byID[id]; ok {
			out = append(out, h)
		}
	}
	return out
}

// matchedHosts 是 Names 与 Resolve 共用的匹配阶段——结构性保证两者
// 对相同 refs 产出相同主机集合（plugin.TargetResolver 的承重不变量：
// 确认弹窗显示的主机 ≡ 实际执行的主机）。
func (c *Core) matchedHosts(refs []string) (matched []*host.Host, unresolved []string) {
	return matchHosts(c.inventory, c.pinnedHosts, refs)
}

// matchHosts 是 Core/Session 共用的匹配逻辑：refs 为空用 pinned，
// 否则全清单解析。
func matchHosts(inventory func() ([]*host.Host, map[string]*host.Host),
	pinnedHosts func() []*host.Host, refs []string) (matched []*host.Host, unresolved []string) {
	if len(refs) == 0 {
		return pinnedHosts(), nil
	}
	hosts, _ := inventory()
	return host.Resolve(hosts, refs)
}

// Names 展开引用为主机名；refs 为空时返回钉住集合的主机名。不建立连接。
// 与 Resolve 构成 plugin.TargetResolver 的对称契约（Names 是免连接投影）。
func (c *Core) Names(refs []string) (names []string, unresolved []string) {
	matched, unresolved := c.matchedHosts(refs)
	for _, h := range matched {
		names = append(names, h.Name)
	}
	return names, unresolved
}

// Resolve 展开引用并绑定执行器（复用/建立 SSH 连接）。refs 为空时用钉住集合。
func (c *Core) Resolve(refs []string) (targets []plugin.Target, unresolved []string, err error) {
	matched, unresolved := c.matchedHosts(refs)
	for _, h := range matched {
		ex, err := c.manager.ExecutorFor(h)
		if err != nil {
			return nil, nil, fmt.Errorf("connect %s (%s): %w", h.Name, h.Endpoint(), err)
		}
		targets = append(targets, plugin.Target{Name: h.Name, Exec: ex})
	}
	return targets, unresolved, nil
}

// CatalogText 生成注入系统提示词的主机清单目录。确定性输出（ORDER BY name），
// 附加在 basePrompt 尾部——为将来加 prompt cache 断点保留正确形态。
func (c *Core) CatalogText() string {
	// 单次快照：hosts 与 pinned 交集自同一份清单，避免 pinnedHosts
	// 二次读表引入两份快照不一致的窗口（钉住标记错标已删主机等）。
	hosts, byID := c.inventory()
	pinned := pinnedSet(c.PinnedIDs(), byID)
	return buildCatalog(hosts, pinned)
}

// pinnedSet 取与当前清单交集的钉住 ID 集（跳过已删除主机）。
func pinnedSet(ids []string, byID map[string]*host.Host) map[string]bool {
	m := map[string]bool{}
	for _, id := range ids {
		if _, ok := byID[id]; ok {
			m[id] = true
		}
	}
	return m
}

// buildCatalog 生成注入系统提示词的主机清单目录（Core 与 Session 共用，
// 输出与原 CatalogText 字节一致：ORDER BY name、🎯 标注、空清单提示）。
// pinned 集即目标范围：Web 每对话会话经 ScopePicker 选定；headless 经
// --pin-all 全选。
func buildCatalog(hosts []*host.Host, pinned map[string]bool) string {
	var b strings.Builder
	b.WriteString("=== 受管主机清单 ===\n")
	if len(hosts) == 0 {
		b.WriteString("（清单为空。请提示用户先在服务器管理里添加主机。）\n")
	}
	for _, h := range hosts {
		pin := ""
		if pinned[h.ID] {
			pin = " 🎯对话目标"
		}
		if h.OS != "" {
			fmt.Fprintf(&b, "- %s · 标签: %s · %s%s\n", h.Name, strings.Join(h.Tags, ", "), h.OS, pin)
		} else {
			fmt.Fprintf(&b, "- %s · 标签: %s%s\n", h.Name, strings.Join(h.Tags, ", "), pin)
		}
	}
	if len(pinned) == 0 {
		b.WriteString("\n注意：本次会话未设置目标主机（无 🎯 主机）。省略 hosts 前先向用户确认目标。\n")
	} else {
		// 历史对话（如多机对比线程）会诱导模型把目标扩大到 all——明确
		// 边界：范围只能由用户当轮指令改变，模型不得自行扩散。
		b.WriteString("\n注意：用户未指明目标时，工具调用应省略 hosts（作用于全部 🎯对话目标 主机）；" +
			"除非用户本轮明确点名其他主机、标签或 all，不要把范围扩大到目标之外（即使早前对话涉及过其他主机）。\n")
	}
	b.WriteString("\n目标引用规则：主机名 / 标签名（机组）/ all；省略 hosts = 全部 🎯对话目标 主机。\n")
	return b.String()
}

// LoadConfig 优先级链（APIKey/APIBaseURL/Model 三字段各自独立判断）：
// api_key：环境变量 > secrets 密文通道（AES-GCM，设置页保存）> 旧版
// settings 表明文（读出即迁入 secrets 并清明文，幂等）> 引导；
// base_url/model：SQLite settings > 环境变量 > 引导 > 内置默认。
func LoadConfig(store *host.Store, secrets host.SecretStore) *config.Config {
	cfg := config.LoadEnv()
	if cfg.APIKey == "" && secrets != nil {
		if v, err := secrets.Get("api_key"); err == nil && v != "" {
			cfg.APIKey = v
		}
	}
	if cfg.APIKey == "" {
		if v, err := store.GetSetting("api_key"); err == nil && v != "" {
			cfg.APIKey = v
			// 兼容迁移：老版本明文 key 迁入密文通道并清掉明文行（失败不
			// 阻断启动——明文仍在，下次启动重试）
			if secrets != nil {
				if err := secrets.Set("api_key", v); err == nil {
					_ = store.SetSetting("api_key", "")
				}
			}
		}
	}
	apply := func(key string, dst *string) {
		if v, err := store.GetSetting(key); err == nil && v != "" {
			*dst = v
		}
	}
	apply("base_url", &cfg.APIBaseURL)
	apply("model", &cfg.Model)
	bootstrapFromClaude(store, secrets, cfg, claudeSettingsPath())
	def := config.Default()
	if cfg.APIBaseURL == "" {
		cfg.APIBaseURL = def.APIBaseURL
	}
	if cfg.Model == "" {
		cfg.Model = def.Model
	}
	cfg.MaxSteps = def.MaxSteps
	cfg.CommandTimeout = def.CommandTimeout
	return cfg
}

// claudeSettingsPath 返回 ~/.claude/settings.json 路径；home 取不到时返回
// 空串（bootstrapFromClaude 对读不到的路径静默跳过）。
func claudeSettingsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "settings.json")
}

// bootstrapFromClaude 字段级引导：cfg 中仍为空的字段若能从 Claude Code 的
// settings.json（env 对象）补齐，则填入并落库（一次性初始化；下次启动
// SQLite 已有值，本函数自然短路）。文件缺失/损坏/落库失败均静默跳过——
// 引导是兜底而非关键路径，失败应保持现状而非阻断启动。api_key 落 secrets
// 密文通道（与设置页保存一致）；secrets 不可用时退回 settings 表。
func bootstrapFromClaude(store *host.Store, secrets host.SecretStore, cfg *config.Config, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	src := config.ParseClaudeSettings(data)
	fields := []struct {
		key string // settings 键（base_url/model；api_key 仅在 secrets 缺席时经此落库）
		val string // 引导候选值
		dst *string
	}{
		{"api_key", src.APIKey, &cfg.APIKey},
		{"base_url", src.APIBaseURL, &cfg.APIBaseURL},
		{"model", src.Model, &cfg.Model},
	}
	for _, f := range fields {
		if f.val == "" || *f.dst != "" {
			continue
		}
		*f.dst = f.val // 内存先生效
		if f.key == "api_key" && secrets != nil {
			_ = secrets.Set("api_key", f.val) // 落库失败：下次启动重试
			continue
		}
		_ = store.SetSetting(f.key, f.val)
	}
}

// BuildAgent 组装完整 Agent：客户端、安全门、插件、记忆、解析器、审计。
// headless 与桌面入口共用，保证两条路径行为一致。
// csns：KnowledgeBase 已做 nil 防护（返回 IsError 而非 panic），但入口仍应
// 尽量传非 nil——nil 时系统提示词缺共识目录、query_consensus 恒报错；
// consensus 加载失败的入口应直接退出。
// auditLog 接口化（webapi 传按用户盖戳的包装器；*audit.Logger 天然满足），
// 禁用审计须传字面 nil（见 agent.AuditLogger 契约）。归调用方所有，
// Core.Close 不会关闭它。
func (c *Core) BuildAgent(cfg *config.Config, sink ui.Sink, csns *consensus.Consensus,
	auditLog agent.AuditLogger, sessionID string, extra ...agent.Option) (*agent.Agent, error) {
	return c.buildAgent(c, c.CatalogText, cfg, sink, csns, auditLog, sessionID, extra)
}

// buildAgent 是 Core/Session 共用的组装体：resolver 与 catalog 由调用方
// 决定（Session 传自身，实现按用户的默认目标与目录）。
func (c *Core) buildAgent(resolver plugin.TargetResolver, catalog func() string,
	cfg *config.Config, sink ui.Sink, csns *consensus.Consensus,
	auditLog agent.AuditLogger, sessionID string, extra []agent.Option) (*agent.Agent, error) {
	clientCfg := claude.ClientConfig{APIKey: cfg.APIKey, BaseURL: cfg.APIBaseURL, Model: cfg.Model}
	client := claude.NewClient(clientCfg)
	gate := safety.NewGate()

	reg := plugin.NewRegistry()
	reg.Register(plugin.NewShellExecutor())
	reg.Register(plugin.NewFileManager())
	reg.Register(plugin.NewDockerManager())
	reg.Register(plugin.NewSystemdManager())
	reg.Register(plugin.NewPackageManager())
	reg.Register(plugin.NewDiagnosticKit())
	reg.Register(plugin.NewNetworkPlugin())
	reg.Register(plugin.NewRedisPlugin())
	reg.Register(plugin.NewKnowledgeBase(csns))
	reg.Register(plugin.NewClarifyPlugin())
	memStore, err := memory.OpenStoreDir(c.memDir)
	if err != nil {
		return nil, err
	}
	reg.Register(plugin.NewMemoryManager(memStore))
	reg.SetResolver(resolver)
	// 分级单源（评审 ①-4）：插件声明的 RiskLevel 灌进 gate，删除
	// safety 手工表——agent.Classify 走到的就是插件声明值。
	reg.SetGate(gate)

	opts := append([]agent.Option{
		agent.WithCatalog(catalog),
		agent.WithAudit(auditLog),
		agent.WithSession(sessionID),
	}, extra...)
	return agent.New(cfg, client, reg, gate, sink, csns, opts...), nil
}

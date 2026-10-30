// Package webapi 是 gengar-pro 的 Web 服务层：登录会话 + REST API +
// WS 事件流（对话/终端，后续任务加入），向下调 appcore。单实例进程内
// 状态（token 表、每用户会话），不依赖外部服务。
package webapi

import (
	"net/http"
	"os"
	"sync"
	"time"

	"gengar-pro/internal/agent"
	"gengar-pro/internal/appcore"
	"gengar-pro/internal/audit"
	"gengar-pro/internal/command"
	"gengar-pro/internal/config"
	"gengar-pro/internal/consensus"
	"gengar-pro/internal/skill"
	"gengar-pro/internal/term"
)

// Server 持全局共享状态；每用户状态在 userState。
type Server struct {
	core   *appcore.Core
	audit  *audit.Logger // 可为 nil（审计文件打开失败时降级）
	csns   *consensus.Consensus
	cfgMu  sync.Mutex
	cfg    *config.Config
	cmdReg *command.Registry

	statusMu sync.Mutex
	status   map[string]string // hostID → ok|fail（连通性，全局共享）

	tokenMu sync.Mutex
	tokens  map[string]tokenEntry // cookie token → 用户名 + 签发时间（TTL 见 auth.go）

	// loginGate 登录并发闸（bcrypt CPU 背压）；loginLimit 登录失败指数限速。
	loginGate chan struct{}
	loginLim  *loginLimit

	chatsMu sync.Mutex
	chats   map[string]*chatSession // 会话 ID → 对话会话（每对话 tab 一个）

	termMgr termConn // 终端管理器注入；nil 表示未启用
	termHub *termHub // 会话事件 fan-out（按 sessionID 多订阅者）

	keysDir string // 上传私钥落盘目录（main 注入；空 = 未启用，上传报错）

	static http.Handler // 内嵌前端产物；测试传 nil

	mux *http.ServeMux
}

// New 组装路由。static 为 nil 时不挂前端（单测场景）。
func New(core *appcore.Core, auditLog *audit.Logger, static http.Handler) *Server {
	cfg := appcore.LoadConfig(core.Store(), core.Secrets())
	s := &Server{
		core:      core,
		audit:     auditLog,
		cfg:       cfg,
		status:    map[string]string{},
		tokens:    map[string]tokenEntry{},
		loginGate: make(chan struct{}, 4),
		loginLim:  newLoginLimit(),
		chats:     map[string]*chatSession{},
		termHub:   newTermHub(),
		static:    static,
	}
	if reg, err := buildCommandRegistry(); err == nil {
		s.cmdReg = reg
	}
	// consensus 加载失败降级 nil 继续服务（区别于 headless/桌面直接退出：
	// Web 是长驻多用户进程，因一段嵌入 YAML 损坏而拒绝全部请求代价过高；
	// KnowledgeBase 已做 nil 防护，代价仅是共识目录缺失与 query_consensus 报错）。
	s.csns, _ = consensus.Load(os.Getenv("GENGAR_ENV"))
	s.mux = http.NewServeMux()
	s.routes()
	return s
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("POST /api/login", s.handleLogin)
	m.HandleFunc("POST /api/logout", s.handleLogout)
	m.HandleFunc("GET /api/me", s.authed(s.handleMe))
	m.HandleFunc("GET /api/health", s.handleHealth)
	m.HandleFunc("GET /api/hosts", s.authed(s.handleGetHosts))
	m.HandleFunc("POST /api/hosts", s.authed(s.handleSaveHost))
	m.HandleFunc("DELETE /api/hosts/{id}", s.authed(s.handleDeleteHost))
	m.HandleFunc("POST /api/hosts/{id}/test", s.authed(s.handleTestHost))
	m.HandleFunc("POST /api/hosts/test", s.authed(s.handleTestHostDraft))
	m.HandleFunc("GET /api/settings", s.admined(s.handleGetSettings))
	m.HandleFunc("PUT /api/settings", s.admined(s.handleSaveSettings))
	m.HandleFunc("PUT /api/chat/scope", s.authed(s.handleSetScope))
	// /api/* 未命中不得落进 SPA fallback（static 挂载时；ServeMux 最具体匹配优先）
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "接口不存在")
	})
	m.HandleFunc("POST /api/chat", s.authed(s.handleSubmitChat))
	m.HandleFunc("POST /api/chat/cancel", s.authed(s.handleCancelChat))
	m.HandleFunc("POST /api/confirm/{id}", s.authed(s.handleResolveConfirm))
	m.HandleFunc("POST /api/ask/{id}", s.authed(s.handleResolveAsk))
	m.HandleFunc("GET /api/ws/chat", s.handleChatWS)
	m.HandleFunc("POST /api/hosts/{id}/term", s.authed(s.handleTermOpen))
	m.HandleFunc("POST /api/term/{id}/close", s.authed(s.handleTermClose))
	m.HandleFunc("GET /api/ws/term", s.handleTermWS)
	m.HandleFunc("POST /api/xshell/import", s.authed(s.handleXshellPreview))
	m.HandleFunc("POST /api/xshell/commit", s.authed(s.handleXshellCommit))
	m.HandleFunc("GET /api/users", s.admined(s.handleListUsers))
	m.HandleFunc("POST /api/users", s.admined(s.handleCreateUser))
	m.HandleFunc("PUT /api/users/{username}/password", s.admined(s.handleResetPassword))
	m.HandleFunc("DELETE /api/users/{username}", s.admined(s.handleDeleteUser))
	if s.static != nil {
		m.HandleFunc("/", s.handleStatic)
	}
}

// rebuildAgent 用当前 cfg 快照重建该会话 Agent（设置变更 / 会话新建）。
// 锁序：cfgMu（快照）→ 释放 → cs.agentMu。任何路径不得同时持有
// chatsMu 与 cfgMu（chatOpen 在 chatsMu 内不拿 cfgMu；本方法不拿 chatsMu）。
func (s *Server) rebuildAgent(cs *chatSession) {
	s.cfgMu.Lock()
	cfgSnap := *s.cfg
	auditLog := s.audit
	s.cfgMu.Unlock()
	var al agent.AuditLogger
	if auditLog != nil {
		al = &userAudit{l: auditLog, user: cs.owner}
	}
	cs.agentMu.Lock()
	defer cs.agentMu.Unlock()
	// 经会话自有的 Session 组装：目标范围（原钉住集）与目录按会话隔离
	ag, err := cs.sess.BuildAgent(&cfgSnap, cs.sink, s.csns, al, cs.agentSID)
	if err != nil {
		cs.agent = nil // 组装失败禁用对话，下次设置保存重试
		return
	}
	cs.agent = ag
}

// rebuildAllAgents 设置保存后调用：锁外快照 chats 后逐个重建；重建后向
// 每个会话的 hub 发布 reset 帧并清空缓冲（评审 ②-5）——兑现 README
// 「设置变更会重置全部用户对话」的承诺，前端经 reset 帧统一响应。
func (s *Server) rebuildAllAgents() {
	s.chatsMu.Lock()
	chats := make([]*chatSession, 0, len(s.chats))
	for _, cs := range s.chats {
		chats = append(chats, cs)
	}
	s.chatsMu.Unlock()
	for _, cs := range chats {
		s.rebuildAgent(cs)
		cs.hub.reset()
	}
}

// ServeHTTP 让 Server 直接作为 http.Handler。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// handleHealth 健康检查（评审 ③-3）：无鉴权轻量探活，暴露审计写失败
// 计数（>0 = 磁盘/权限问题，运维告警信号）。终端管理器与时间戳一并给出。
func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	resp := map[string]any{
		"ok":       true,
		"time":     time.Now().Format(time.RFC3339),
		"sessions": len(s.chats),
	}
	if s.audit != nil {
		resp["audit_write_fails"] = s.audit.WriteFails()
		resp["audit_shard"] = s.audit.CurrentShard()
	} else {
		resp["audit_write_fails"] = -1 // 审计未启用
	}
	if s.termMgr != nil {
		resp["terminals"] = 0 // termConn 无计数面；仅标记启用
	}
	writeJSON(w, http.StatusOK, resp)
}

// SetTermManager 注入终端管理器（Emitter 已接 onTermEvent）。
func (s *Server) SetTermManager(m termConn) { s.termMgr = m }

// SetKeysDir 注入私钥上传目录（<data>/keys）。
func (s *Server) SetKeysDir(dir string) { s.keysDir = dir }

// CloseTerminals 进程退出前关闭全部 PTY。
func (s *Server) CloseTerminals() {
	if s.termMgr != nil {
		s.termMgr.CloseAll()
	}
}

// OnTermEvent 暴露给入口做 term.Manager 的 Emitter。
func (s *Server) OnTermEvent(ev term.Event) { s.onTermEvent(ev) }

func buildCommandRegistry() (*command.Registry, error) {
	skills, err := skill.Load()
	if err != nil {
		return nil, err
	}
	skillCmds := make([]command.Command, 0, len(skills))
	for _, sk := range skills {
		skillCmds = append(skillCmds, sk.ToCommand())
	}
	return command.NewRegistry(skillCmds...), nil
}

// handleStatic 前端 SPA 托管。
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	s.static.ServeHTTP(w, r)
}

package webapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"

	"gengar-pro/internal/agent"
	"gengar-pro/internal/appcore"
)

// chatSession 是一个对话会话：hub/sink/Agent/单任务锁按会话隔离——每个
// 对话 tab 一个，不同登录（哪怕同账号）互不可见、可并行跑任务。sess 是
// 会话自有的 appcore.Session，其钉住集即本次对话的目标范围（ScopePicker
// 选定；省略主机的指令作用于范围内全部主机）。
type chatSession struct {
	id        string // 会话 ID（8 字节随机 hex），前端经 WS hello 绑定
	owner     string // 创建者用户名（删除用户时清理其会话）
	viewAdmin bool   // 创建者是否 admin（清单视角快照，scopeNames 用）
	sess      *appcore.Session
	hub       *hub
	sink      *userSink
	agentSID  string // 注入 Agent/审计的会话标识

	agentMu sync.Mutex
	agent   *agent.Agent

	taskMu sync.Mutex
	busy   bool
	cancel context.CancelFunc

	// lastActive 最近一次有 WS 订阅者活动的时间（reaper 判定基准）。
	lastActive time.Time
}

// newChatSession 装配 hub↔sink（onIdle：该会话浏览器全关 → 取消其待决议，
// 阻塞在确认框的任务被放行）。接线须在 hub 可被其他 goroutine 访问前
// 完成（见 newUserSink 的锁序说明）。视角（owner/isAdmin）在创建时快照
// ——角色创建后不可变（ensureAdmin 只在种子时提权，Web 不改角色）。
func newChatSession(core *appcore.Core, owner string, isAdmin bool) *chatSession {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	id := hex.EncodeToString(b)
	cs := &chatSession{
		id:         id,
		owner:      owner,
		viewAdmin:  isAdmin,
		sess:       core.NewSessionFor(owner, isAdmin),
		hub:        newHub(),
		agentSID:   time.Now().Format("20060102-150405") + "-chat-" + id,
		lastActive: time.Now(),
	}
	cs.sink = newUserSink(cs.hub)
	return cs
}

// chatFor 按会话 ID 查找；不存在返回 nil（POST 路由据此 400）。
func (s *Server) chatFor(id string) *chatSession {
	s.chatsMu.Lock()
	defer s.chatsMu.Unlock()
	return s.chats[id]
}

// reapIdleSessions 回收闲置对话会话（评审 ③-2）：无 WS 订阅 30 分钟
// 且非 busy 的会话关闭并释放 hub/Agent（客户端刷新即复活为新会话，
// reset 帧通道已有提示语义）。busy 会话不回收（任务在跑）。
func (s *Server) reapIdleSessions() {
	const idleAfter = 30 * time.Minute
	s.chatsMu.Lock()
	var reap []*chatSession
	now := time.Now()
	for id, cs := range s.chats {
		cs.taskMu.Lock()
		busy := cs.busy
		cs.taskMu.Unlock()
		idle := now.Sub(cs.lastActive) > idleAfter && !cs.hub.hasSubs()
		if !busy && idle {
			reap = append(reap, cs)
			delete(s.chats, id)
		}
	}
	s.chatsMu.Unlock()
	for _, cs := range reap {
		cs.hub.reset() // 在线（迟到的重连已不可能——无订阅才回收）防御性清缓冲
	}
}

// chatForUser 属主过滤的会话查找：不存在或属主非该用户一律 nil（请求方
// 统一报「会话不存在」——不区分两者，避免会话 id 存在性探测）。会话 id
// 不可猜（8 字节随机），但一旦经日志/分享泄露，属主校验挡住替人发消息/
// 批准 Risky 命令/改目标范围。
func (s *Server) chatForUser(id, username string) *chatSession {
	s.chatsMu.Lock()
	defer s.chatsMu.Unlock()
	cs := s.chats[id]
	if cs == nil || cs.owner != username {
		return nil
	}
	return cs
}

// chatOpen 返回会话；id 为空或未知（含服务重启后的 stale id）时新建——
// WS 首连/复活路径，hello.session 告知客户端实际 id。Agent 组装在
// chatsMu 释放后进行（锁序 chatsMu → 释放 → cfgMu → agentMu，不嵌套）。
func (s *Server) chatOpen(id, owner string) *chatSession {
	s.chatsMu.Lock()
	if cs, ok := s.chats[id]; ok {
		s.chatsMu.Unlock()
		return cs
	}
	_, isAdmin := s.viewerOf(owner)
	cs := newChatSession(s.core, owner, isAdmin)
	s.chats[cs.id] = cs
	s.chatsMu.Unlock()
	s.rebuildAgent(cs)
	return cs
}

// Session 是 Core 之上的单用户视图：钉住集合（= 省略 hosts 时的默认目标）
// 与 Agent 会话按用户隔离，主机清单/连接管理/设置全局共享。webapi 每个
// 登录用户各持一个；Core 自身的方法保留给 headless（单用户语义）。
//
// 视角：NewSessionFor(owner, isAdmin) 建立按属主过滤的清单视图（Agent
// 目标解析/目录只含自己主机；admin 见全部）；NewSession() 为全局视图
// （headless 单用户语义与既有测试兼容）。
package appcore

import (
	"fmt"
	"sync"

	"gengar-pro/internal/agent"
	"gengar-pro/internal/config"
	"gengar-pro/internal/consensus"
	"gengar-pro/internal/host"
	"gengar-pro/internal/plugin"
	"gengar-pro/internal/ui"
)

type Session struct {
	core    *Core
	owner   string // 清单视角属主（global=true 时忽略）
	isAdmin bool   // admin 视角（ListFor 全表）
	global  bool   // NewSession() 全局视图：清单不按属主过滤（headless）

	mu     sync.Mutex
	pinned []string // host IDs
}

// NewSession 全局视图（headless 单用户语义：清单不按属主过滤）。
func (c *Core) NewSession() *Session { return &Session{core: c, global: true} }

// NewSessionFor 用户视角：普通用户只解析自己名下主机；admin 解析全表。
// 视角在创建时快照（角色创建后不可变，与 admined 每请求查库同一前提）。
func (c *Core) NewSessionFor(owner string, isAdmin bool) *Session {
	return &Session{core: c, owner: owner, isAdmin: isAdmin}
}

// SetPinned 防御性拷贝入参——调用方事后改底层数组不得波及本会话。
func (s *Session) SetPinned(ids []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pinned = append([]string(nil), ids...)
}

func (s *Session) PinnedIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.pinned...)
}

// inventory 读本视角的清单并建 ID 索引（全局视角 = 全表）。
func (s *Session) inventory() ([]*host.Host, map[string]*host.Host) {
	var hosts []*host.Host
	var err error
	if s.global {
		hosts, err = s.core.store.List()
	} else {
		hosts, err = s.core.store.ListFor(s.owner, s.isAdmin)
	}
	if err != nil {
		return nil, nil
	}
	byID := make(map[string]*host.Host, len(hosts))
	for _, h := range hosts {
		byID[h.ID] = h
	}
	return hosts, byID
}

func (s *Session) pinnedHosts() []*host.Host {
	_, byID := s.inventory()
	ids := s.PinnedIDs()
	out := make([]*host.Host, 0, len(ids))
	for _, id := range ids {
		if h, ok := byID[id]; ok {
			out = append(out, h)
		}
	}
	return out
}

func (s *Session) matchedHosts(refs []string) (matched []*host.Host, unresolved []string) {
	return matchHosts(s.inventory, s.pinnedHosts, refs)
}

// Names / Resolve 与 Core 对应方法同语义，钉住集合读本会话的。
func (s *Session) Names(refs []string) (names []string, unresolved []string) {
	matched, unresolved := s.matchedHosts(refs)
	for _, h := range matched {
		names = append(names, h.Name)
	}
	return names, unresolved
}

func (s *Session) Resolve(refs []string) (targets []plugin.Target, unresolved []string, err error) {
	matched, unresolved := s.matchedHosts(refs)
	for _, h := range matched {
		ex, err := s.core.manager.ExecutorFor(h)
		if err != nil {
			return nil, nil, fmt.Errorf("connect %s (%s): %w", h.Name, h.Endpoint(), err)
		}
		targets = append(targets, plugin.Target{Name: h.Name, Exec: ex})
	}
	return targets, unresolved, nil
}

func (s *Session) CatalogText() string {
	hosts, byID := s.inventory()
	return buildCatalog(hosts, pinnedSet(s.PinnedIDs(), byID))
}

// BuildAgent 组装绑定本会话（默认目标/目录按本用户钉住集合）的 Agent。
// auditLog 接口化（见 Core.BuildAgent；禁用审计须传字面 nil）。
func (s *Session) BuildAgent(cfg *config.Config, sink ui.Sink, csns *consensus.Consensus,
	auditLog agent.AuditLogger, sessionID string, extra ...agent.Option) (*agent.Agent, error) {
	return s.core.buildAgent(s, s.CatalogText, cfg, sink, csns, auditLog, sessionID, extra)
}

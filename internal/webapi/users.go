// users.go 用户管理端点（仅 admin）：列表/创建/重置密码/删除。密码只落
// bcrypt 哈希；重置与删除即时吊销目标用户会话（token 摘除）。
package webapi

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gengar-pro/internal/host"
)

// usernameRe 合法用户名：1-32 位字母数字与 _ . @ -（保证 URL path 与
// sessionID 拼接安全）。
var usernameRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,32}$`)

// admined 在 authed 之上再要求 admin 角色（每请求查库一次：角色创建后
// 不可变，内网流量下省掉 token 表结构复杂化）。
func (s *Server) admined(h func(w http.ResponseWriter, r *http.Request, username string)) http.HandlerFunc {
	return s.authed(func(w http.ResponseWriter, r *http.Request, username string) {
		u, err := s.core.Store().GetUser(username)
		if err != nil || u.Role != "admin" {
			writeErr(w, http.StatusForbidden, "需要管理员权限")
			return
		}
		h(w, r, username)
	})
}

// userView 列表行；不回显 PwHash。
type userView struct {
	Username  string `json:"username"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request, _ string) {
	users, err := s.core.Store().ListUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取用户失败")
		return
	}
	views := make([]userView, 0, len(users))
	for _, u := range users {
		views = append(views, userView{Username: u.Username, Role: u.Role, CreatedAt: u.CreatedAt})
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": views})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request, operator string) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	name := strings.TrimSpace(req.Username)
	if !usernameRe.MatchString(name) {
		writeErr(w, http.StatusBadRequest, "用户名须为 1-32 位字母数字或 _ . @ -")
		return
	}
	if len(req.Password) < 6 || len(req.Password) > 128 {
		writeErr(w, http.StatusBadRequest, "密码长度须为 6-128 位")
		return
	}
	// 先判重再插入（UNIQUE 约束兜底并发），区分 400 与 500
	if _, err := s.core.Store().GetUser(name); err == nil {
		writeErr(w, http.StatusBadRequest, "用户名已存在")
		return
	} else if !errors.Is(err, host.ErrNoSuchUser) {
		writeErr(w, http.StatusInternalServerError, "读取用户失败")
		return
	}
	h, err := bcrypt.GenerateFromPassword([]byte(req.Password), host.PwHashCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "密码处理失败")
		return
	}
	// 创建的用户一律普通角色（admin 只由种子产生，角色编辑非目标）
	if err := s.core.Store().CreateUser(host.User{
		Username: name, PwHash: string(h), CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, "创建用户失败")
		return
	}
	s.logUserOp(operator, "user_create", name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleResetPassword(w http.ResponseWriter, r *http.Request, operator string) {
	var req struct {
		Password string `json:"password"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if len(req.Password) < 6 || len(req.Password) > 128 {
		writeErr(w, http.StatusBadRequest, "密码长度须为 6-128 位")
		return
	}
	name := r.PathValue("username")
	h, err := bcrypt.GenerateFromPassword([]byte(req.Password), host.PwHashCost)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "密码处理失败")
		return
	}
	if err := s.core.Store().UpdateUserPassword(name, string(h)); err != nil {
		if errors.Is(err, host.ErrNoSuchUser) {
			writeErr(w, http.StatusNotFound, "用户不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "保存密码失败")
		return
	}
	s.revokeUserTokens(name)
	s.logUserOp(operator, "user_pw_reset", name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request, operator string) {
	name := r.PathValue("username")
	if name == operator {
		writeErr(w, http.StatusForbidden, "不能删除当前登录账号")
		return
	}
	u, err := s.core.Store().GetUser(name)
	if err != nil {
		if errors.Is(err, host.ErrNoSuchUser) {
			writeErr(w, http.StatusNotFound, "用户不存在")
			return
		}
		writeErr(w, http.StatusInternalServerError, "读取用户失败")
		return
	}
	if u.Role == "admin" {
		// 最后一个 admin 保护（防御性：当前操作者必是 admin 且≠目标，
		// 常规路径不可达；为将来角色编辑留的保险。并发对删有 TOCTOU 窗口，
		// alpha 接受）。
		users, err := s.core.Store().ListUsers()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "读取用户失败")
			return
		}
		admins := 0
		for _, x := range users {
			if x.Role == "admin" {
				admins++
			}
		}
		if admins <= 1 {
			writeErr(w, http.StatusForbidden, "不能删除最后一个管理员")
			return
		}
	}
	// 名下主机移交 admin（数据不丢，设计 §4.4）；失败中止删除可重试。
	owned, _ := s.core.Store().ListFor(name, false)
	if err := s.core.Store().ReassignHosts(name, "admin"); err != nil {
		writeErr(w, http.StatusInternalServerError, "移交主机失败: "+err.Error())
		return
	}
	if err := s.core.Store().DeleteUser(name); err != nil {
		writeErr(w, http.StatusInternalServerError, "删除用户失败")
		return
	}
	s.revokeUserTokens(name)
	s.dropUserSessions(name)
	s.logUserOp(operator, "user_delete", fmt.Sprintf("%s hosts=%d→admin", name, len(owned)))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// revokeUserTokens 摘除该用户全部会话令牌（重置密码/删除后强制重登录）。
func (s *Server) revokeUserTokens(username string) {
	s.tokenMu.Lock()
	for tok, te := range s.tokens {
		if te.user == username {
			delete(s.tokens, tok)
		}
	}
	s.tokenMu.Unlock()
}

// dropUserSessions 摘除并取消该用户的全部对话会话（删除后不再占用
// Agent/任务）。锁序：chatsMu 内取走 → 释放 → taskMu（不嵌套）。
func (s *Server) dropUserSessions(username string) {
	s.chatsMu.Lock()
	var mine []*chatSession
	for id, cs := range s.chats {
		if cs.owner == username {
			mine = append(mine, cs)
			delete(s.chats, id)
		}
	}
	s.chatsMu.Unlock()
	for _, cs := range mine {
		cs.taskMu.Lock()
		cancel := cs.cancel
		cs.taskMu.Unlock()
		if cancel != nil {
			cancel()
		}
		cs.sink.CancelPendings()
	}
}

// logUserOp 记用户管理审计：目标用户名放 Output（"user=<name>"，与
// logTerm 的 session=<id> 同一处理思路），Hosts 留空。统一委托 auditOp
// （nil logger 防护与 TS 填充不再各处手写）。
func (s *Server) logUserOp(operator, tool, target string) {
	s.auditOp(operator, tool, "user="+target, nil)
}

// viewerOf 返回该用户名的视角（属主名 + 是否 admin）。查库一次——与
// admined 的每请求查库同代价模式；GetUser 失败按普通用户降级（authed
// 已保证用户存在，此处失败仅剩库故障，降级让 404 而非 500）。
func (s *Server) viewerOf(username string) (owner string, isAdmin bool) {
	u, err := s.core.Store().GetUser(username)
	if err != nil || u.Role != "admin" {
		return username, false
	}
	return username, true
}

// 登录：用户名 + bcrypt 校验（users 表），cookie 会话令牌存内存
// （服务重启全员重新登录，alpha 可接受）。
// token 有 TTL（7 天，每小时清扫——评审 ③-1：旧 token 永不过期等于
// 泄漏的 cookie 永久有效）；登录失败有指数限速（防在线爆破）。
package webapi

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gengar-pro/internal/host"
)

const cookieName = "gengar_sess"

// tokenTTL 登录令牌存活期；cookie MaxAge 对齐。
const tokenTTL = 7 * 24 * time.Hour

// tokenEntry 是 tokens 表的值：用户名 + 签发时间（TTL 判定用）。
type tokenEntry struct {
	user   string
	issued time.Time
}

// loginLimit 登录失败指数限速（评审 ③-1）：同一用户名连续失败后，
// 允许下一次尝试的最小间隔翻倍增长（1s 起，上限 30s）；成功登录清零。
// 全局还有并发闸（loginGate）防打满 bcrypt CPU。内网小团队规模下，
// 按 IP 限速的精度收益不值引入 IP 追踪的复杂度。
type loginLimit struct {
	mu       sync.Mutex
	failures map[string]int // username → 连续失败次数
	nextTry  map[string]time.Time
}

func newLoginLimit() *loginLimit {
	return &loginLimit{
		failures: map[string]int{},
		nextTry:  map[string]time.Time{},
	}
}

// allow 报告该用户名现在是否允许尝试登录。
func (l *loginLimit) allow(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	next, ok := l.nextTry[name]
	if !ok {
		return true
	}
	return time.Now().After(next)
}

// fail 记一次失败：连续次数 +1，冷却 = min(1s<<n, 30s)。
func (l *loginLimit) fail(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[name]++
	cooldown := time.Duration(1<<l.failures[name]) * time.Second
	if cooldown > 30*time.Second {
		cooldown = 30 * time.Second
	}
	l.nextTry[name] = time.Now().Add(cooldown)
}

// success 清零该用户名的失败计数。
func (l *loginLimit) success(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, name)
	delete(l.nextTry, name)
}

// dummyHash 用户不存在时也跑一次同代价的 bcrypt 比较，两条失败路径
// 耗时一致，不泄漏用户名存在性。cost 与 --add-user 同源（host.PwHashCost）。
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("gengar-dummy"), host.PwHashCost)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	// 全局限速闸：并发 bcrypt 有 CPU 成本，闸外排队不拒绝（内网规模下
	// 排队即背压）；登录失败计数闸才是拒绝源。
	s.loginGate <- struct{}{}
	defer func() { <-s.loginGate }()
	if !s.loginLim.allow(req.Username) {
		writeErr(w, http.StatusTooManyRequests, "尝试过于频繁，请稍后再试")
		return
	}
	u, err := s.core.Store().GetUser(req.Username)
	if err != nil {
		if !errors.Is(err, host.ErrNoSuchUser) {
			writeErr(w, http.StatusInternalServerError, "读取用户失败")
			return
		}
		_ = bcrypt.CompareHashAndPassword(dummyHash, []byte(req.Password))
		s.loginLim.fail(req.Username)
		// 未认证 → User 置空；尝试的用户名放 Output（审计侧可追溯爆破目标）。
		s.auditOp("", "login_fail", fmt.Sprintf("user=%s ip=%s", req.Username, clientIP(r)), nil)
		writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PwHash), []byte(req.Password)) != nil {
		s.loginLim.fail(req.Username)
		s.auditOp("", "login_fail", fmt.Sprintf("user=%s ip=%s", req.Username, clientIP(r)), nil)
		writeErr(w, http.StatusUnauthorized, "用户名或密码错误")
		return
	}
	s.loginLim.success(req.Username)
	s.auditOp(u.Username, "login_ok", fmt.Sprintf("user=%s ip=%s", u.Username, clientIP(r)), nil)
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		writeErr(w, http.StatusInternalServerError, "生成会话失败")
		return
	}
	val := hex.EncodeToString(tok)
	s.tokenMu.Lock()
	s.tokens[val] = tokenEntry{user: u.Username, issued: time.Now()}
	s.tokenMu.Unlock()
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    val,
		Path:     "/",
		MaxAge:   int(tokenTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"username": u.Username, "role": u.Role})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// 删 token 前取用户名记 logout；未登录登出（无有效 token）不记。
	if name, ok := s.currentUser(r); ok {
		s.auditOp(name, "logout", "user="+name, nil)
	}
	if c, err := r.Cookie(cookieName); err == nil {
		s.tokenMu.Lock()
		delete(s.tokens, c.Value)
		s.tokenMu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, username string) {
	u, err := s.core.Store().GetUser(username)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "未登录")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"username": u.Username, "role": u.Role})
}

// currentUser 从 cookie 解析登录用户名；未登录或 token 过期返回 ("", false)。
func (s *Server) currentUser(r *http.Request) (string, bool) {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return "", false
	}
	s.tokenMu.Lock()
	te, ok := s.tokens[c.Value]
	name := te.user
	s.tokenMu.Unlock()
	if !ok {
		return "", false
	}
	// 惰性过期：超 TTL 的 token 视为未登录（清扫 goroutine 兜底回收）。
	if time.Since(te.issued) > tokenTTL {
		s.tokenMu.Lock()
		delete(s.tokens, c.Value)
		s.tokenMu.Unlock()
		return "", false
	}
	return name, true
}

// sweepTokens 每小时清除过期 token（惰性过期之外的内存回收）。
func (s *Server) sweepTokens() {
	s.tokenMu.Lock()
	for tok, te := range s.tokens {
		if time.Since(te.issued) > tokenTTL {
			delete(s.tokens, tok)
		}
	}
	s.tokenMu.Unlock()
}

// StartBackground 启动后台清扫（token 过期 / 会话与 PTY 回收——评审 ③）。
// 返回停止函数（main 退出时调用）。间隔 1 分钟扫描：
//   - token：每小时一次全量过期清扫（间隔倍数控制）；
//   - chatSession：无订阅 30 分钟且非 busy → 关闭并释放 hub/Agent；
//   - PTY：无 WS 订阅 1 小时 → CloseTerminal。
func (s *Server) StartBackground() func() {
	ticker := time.NewTicker(time.Minute)
	stop := make(chan struct{})
	done := make(chan struct{})
	tick := 0
	go func() {
		defer close(done)
		for {
			select {
			case <-ticker.C:
				tick++
				if tick%60 == 0 {
					s.sweepTokens()
				}
				s.reapIdleSessions()
				if s.termMgr != nil {
					s.termHub.reapIdle(s.termMgr)
				}
			case <-stop:
				ticker.Stop()
				return
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}

// authed 包裹需要登录的 handler，注入用户名。
func (s *Server) authed(h func(w http.ResponseWriter, r *http.Request, username string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name, ok := s.currentUser(r)
		if !ok {
			writeErr(w, http.StatusUnauthorized, "未登录")
			return
		}
		h(w, r, name)
	}
}

// Package term 提供内嵌交互终端的 SSH PTY 会话管理：每会话一条专属
// 连接（不复用 executor.Manager 的缓存——其 alive 探测/驱逐语义会砍掉
// 长挂的交互会话），输出经 Emitter 回调外送（webapi 终端端点 termws
// 桥接为 WS 下行，data 为 base64——终端流含任意二进制转义序列），
// 输入/尺寸/关闭经 Manager 方法进入。可单测。
package term

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"gengar-pro/internal/executor"
	"gengar-pro/internal/host"
)

type EventType int

const (
	EventData EventType = iota
	EventExit
)

// Event 是会话外送事件。
type Event struct {
	Type EventType
	ID   string
	Data string // EventData：base64 编码的输出块
	Err  string // EventExit：远端退出原因（可空）
}

// Emitter 接收会话事件。会被 x/crypto 的 stdout/stderr 拷贝 goroutine 与
// 退出看护 goroutine 并发调用（每会话最多三个并发方），实现必须并发安全
// 且不得无限期阻塞（阻塞会卡死输出泵并吞掉 EventExit）。同一会话的事件
// 有序：EventData 严格先于 EventExit。
type Emitter func(Event)

// dialTimeout 覆盖 TCP + 握手全程（DialClient 语义）。
const dialTimeout = 15 * time.Second

type Manager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	secrets  host.SecretStore
	emit     Emitter

	// hostMu 是按「主机+属主」的拨号串行化锁（惰性建，m.mu 保护其表）。
	// Open 全程持该锁并在锁内重查 sessions：并发 Open 同主机同属主的败者
	// 直接复用胜者刚注册的会话，消除「检查-拨号-注册」窗口内的双拨号
	// （TOCTOU）。锁序恒为 hostMu → m.mu，别处不得反向嵌套。
	hostMu map[string]*sync.Mutex
}

func NewManager(secrets host.SecretStore, emit Emitter) *Manager {
	return &Manager{sessions: map[string]*Session{}, secrets: secrets, emit: emit}
}

// writerFunc 把输出泵挂在 sess.Stdout/Stderr 上：x/crypto 为 stdout/stderr
// 各起一个拷贝 goroutine 调用 Write，同步 emit 即可。
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// replayBufCap 终端回放环形缓冲上限（字节，评审 ④-6）：attach 前的输出
// 存最近 64KB，attach 时补发——修「attach 与 open 之间的输出丢失窗口」
// 及已知限制「attach 前的输出不补发」。
const replayBufCap = 64 << 10

type Session struct {
	id        string
	hostID    string
	owner     string // 开启者用户名：webapi 据此做 attach/close 的属主校验
	client    *ssh.Client
	sess      *ssh.Session
	stdin     io.WriteCloser
	emit      Emitter
	writeMu   sync.Mutex // 串行化 stdin 写与 window-change：x/crypto 同流并发写不安全，webapi 终端端点（termws）的调用方各自独立 goroutine
	closeOnce sync.Once

	// replay 是输出回放环形缓冲（replayBufCap 字节）。输出泵写入；
	// webapi attach 时经 Replay 取走补发给新订阅者。mu 保护。
	replayMu sync.Mutex
	replay   []byte
}

// randomID 生成 16 字节随机 hex 会话 ID——不可枚举（顺序 t0,t1 会让任何
// 登录用户猜出他人会话 id 并附加其 PTY）。rand.Read 失败（极罕见）以
// panic 显式失败，不退化为可预测值。
func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("term: crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// Open 拨号并启动交互 shell（xterm-256color，初始 24 行 × 80 列——
// 注意 x/crypto 的 RequestPty 参数序是 (term, h, w)）。按「主机+属主」
// 单会话约定为 GetOrCreate：同一用户对同一主机已有在线会话则幂等返回
// 其 ID；不同用户各开各的 PTY（多用户隔离——共享他人终端 = 读其输出 +
// 注入击键）。
func (m *Manager) Open(h *host.Host, owner string) (string, error) {
	// 拨号串行化：全程持该「主机+属主」的锁，锁内重查——并发同键的第二次
	// Open 会看到胜者已注册的会话并直接复用，不再二次拨号。
	key := h.ID + "\x00" + owner
	m.mu.Lock()
	if m.hostMu == nil {
		m.hostMu = map[string]*sync.Mutex{}
	}
	hm := m.hostMu[key]
	if hm == nil {
		hm = &sync.Mutex{}
		m.hostMu[key] = hm
	}
	m.mu.Unlock()

	hm.Lock()
	defer hm.Unlock()

	if id, ok := m.FindByHost(h.ID, owner); ok {
		return id, nil
	}

	client, err := executor.DialClient(h, m.secrets, dialTimeout)
	if err != nil {
		return "", err
	}
	sess, err := client.NewSession()
	if err != nil {
		_ = client.Close()
		return "", fmt.Errorf("term: new session: %w", err)
	}
	if err := sess.RequestPty("xterm-256color", 24, 80, ssh.TerminalModes{}); err != nil {
		_ = sess.Close()
		_ = client.Close()
		return "", fmt.Errorf("term: request pty: %w", err)
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		_ = sess.Close()
		_ = client.Close()
		return "", fmt.Errorf("term: stdin pipe: %w", err)
	}

	m.mu.Lock()
	id := randomID()
	s := &Session{id: id, hostID: h.ID, owner: owner, client: client, sess: sess, stdin: stdin, emit: m.emit}
	m.sessions[id] = s
	m.mu.Unlock()

	out := writerFunc(func(p []byte) (int, error) {
		s.pushReplay(p)
		s.emit(Event{Type: EventData, ID: id, Data: base64.StdEncoding.EncodeToString(p)})
		return len(p), nil
	})
	sess.Stdout = out
	sess.Stderr = out
	if err := sess.Shell(); err != nil {
		m.Close(id) // 从表移除并关连接
		return "", fmt.Errorf("term: shell: %w", err)
	}
	go func() {
		werr := sess.Wait()
		msg := ""
		if werr != nil {
			msg = werr.Error()
		}
		s.emit(Event{Type: EventExit, ID: id, Err: msg})
		m.Close(id) // 远端退出即回收：从表移除、关连接
	}()
	return id, nil
}

func (m *Manager) Write(id, data string) error {
	s := m.get(id)
	if s == nil {
		return fmt.Errorf("term: unknown session %s", id)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.stdin.Write([]byte(data)); err != nil {
		return fmt.Errorf("term: write %s: %w", id, err)
	}
	return nil
}

// Resize 注意参数序转换：x/crypto 的 WindowChange 是 (h, w)。
func (m *Manager) Resize(id string, cols, rows int) error {
	s := m.get(id)
	if s == nil {
		return fmt.Errorf("term: unknown session %s", id)
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if err := s.sess.WindowChange(rows, cols); err != nil {
		return fmt.Errorf("term: resize %s: %w", id, err)
	}
	return nil
}

func (m *Manager) Close(id string) {
	m.mu.Lock()
	s := m.sessions[id]
	delete(m.sessions, id)
	m.mu.Unlock()
	if s != nil {
		s.close()
	}
}

// CloseHost 关闭某主机的全部会话（DeleteHost 时调用）。
func (m *Manager) CloseHost(hostID string) {
	m.mu.Lock()
	var ss []*Session
	for id, s := range m.sessions {
		if s.hostID == hostID {
			ss = append(ss, s)
			delete(m.sessions, id)
		}
	}
	m.mu.Unlock()
	for _, s := range ss {
		s.close()
	}
}

func (m *Manager) CloseAll() {
	m.mu.Lock()
	ss := make([]*Session, 0, len(m.sessions))
	for id, s := range m.sessions {
		ss = append(ss, s)
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	for _, s := range ss {
		s.close()
	}
}

// FindByHost 返回该「主机+属主」当前打开的会话 ID（单会话约定）。
// webapi 的 REST 开终端端点用它实现「同主机重复打开 = 附加既有会话」。
func (m *Manager) FindByHost(hostID, owner string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, s := range m.sessions {
		if s.hostID == hostID && s.owner == owner {
			return id, true
		}
	}
	return "", false
}

// Owner 返回会话属主（webapi 的 WS attach / REST close 属主校验用）。
// 会话不存在返回 ("", false)。
func (m *Manager) Owner(id string) (string, bool) {
	s := m.get(id)
	if s == nil {
		return "", false
	}
	return s.owner, true
}

// Exists 判断会话是否仍在线（webapi 的 WS attach 前校验）。
func (m *Manager) Exists(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[id]
	return ok
}

func (m *Manager) get(id string) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[id]
}

// pushReplay 追加输出进环形缓冲（超 cap 丢最旧——attach 只需「最近的
// 上下文」而非全量历史，64KB 约一屏滚动量的 3-4 倍）。
func (s *Session) pushReplay(p []byte) {
	s.replayMu.Lock()
	s.replay = append(s.replay, p...)
	if len(s.replay) > replayBufCap {
		s.replay = append([]byte{}, s.replay[len(s.replay)-replayBufCap:]...)
	}
	s.replayMu.Unlock()
}

// Replay 返回会话的回放缓冲快照（可为空；会话不存在返回 nil）。
// webapi 在 WS attach 后、进入实时流之前补发——补发先于 subscribe，
// 顺序即输出顺序（输出泵在 emit 的 fanout 返回前不会写下一块）。
func (m *Manager) Replay(id string) []byte {
	s := m.get(id)
	if s == nil {
		return nil
	}
	s.replayMu.Lock()
	defer s.replayMu.Unlock()
	if len(s.replay) == 0 {
		return nil
	}
	out := make([]byte, len(s.replay))
	copy(out, s.replay)
	return out
}

func (s *Session) close() {
	s.closeOnce.Do(func() {
		_ = s.stdin.Close()
		_ = s.sess.Close()
		_ = s.client.Close()
	})
}

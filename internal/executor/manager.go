package executor

import (
	"sync"
	"time"

	"gengar-pro/internal/host"
)

// probeDeadlineCap 是探活看门狗的期限上限。探活失败（连接黑洞化——
// TCP 未断但对端永不回复）时 SendRequest 会阻塞到 OS 层 TCP keepalive
// 超时（几十秒到几分钟），而探活全程持有 Manager 互斥锁：不设期限，
// 一台主机黑洞就会冻结所有主机的 ExecutorFor/Close/CloseAll。5s 足够
// 覆盖健康网络的一次 keepalive 往返（毫秒级），又远小于 TCP 栈默认的
// 黑洞检测期，是"尽快判死"与"慢网不误杀"的折中。
const probeDeadlineCap = 5 * time.Second

// Manager 按主机缓存 SSH 连接：同一台主机的连续工具调用复用一条
// 连接（每命令一个 session，连接级多路复用），关闭后重拨。
// 并发安全；拨号按主机串行化（hostMu，惰性建——term.Manager 同范式），
// 不再全局持锁拨号：一台慢主机的 30s 拨号不再冻结其他主机的
// ExecutorFor/Close/CloseAll（评审 ③-4）。
type Manager struct {
	secrets host.SecretStore
	timeout time.Duration

	mu      sync.Mutex
	clients map[string]Executor // host.ID → 绑定连接的 Executor

	// hostMu 按主机 ID 串行化拨号（惰性建，mu 保护其表）。锁序恒为
	// hostMu → mu，别处不得反向嵌套。
	hostMu map[string]*sync.Mutex
}

func NewManager(secrets host.SecretStore, timeout time.Duration) *Manager {
	return &Manager{secrets: secrets, timeout: timeout, clients: map[string]Executor{}}
}

// ExecutorFor 返回主机的 Executor：命中缓存且探活通过则复用，否则关闭
// 旧连接、清条目、重拨。探活带期限看门狗（见 alive），防黑洞连接持锁
// 冻结其他主机。拨号全程持该主机的 hostMu 并在锁内重查缓存——并发
// 同主机的败者复用胜者刚建的连接，消除双拨号 TOCTOU。
func (m *Manager) ExecutorFor(h *host.Host) (Executor, error) {
	hm := m.hostMutex(h.ID)
	hm.Lock()
	defer hm.Unlock()

	m.mu.Lock()
	if ex, ok := m.clients[h.ID]; ok {
		if alive(ex, m.probeDeadline()) {
			m.mu.Unlock()
			return ex, nil
		}
		// 死连接：先关 fd 再弃条目（否则泄漏到 GC/进程退出）；Close
		// 同时解除探活看门狗放弃的 goroutine 在 SendRequest 里的阻塞。
		closeExecutor(ex)
		delete(m.clients, h.ID)
	}
	m.mu.Unlock()

	ex, err := DialHost(h, m.secrets, m.timeout)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.clients[h.ID] = ex
	m.mu.Unlock()
	return ex, nil
}

// hostMutex 返回该主机的拨号串行化锁（惰性建）。
func (m *Manager) hostMutex(hostID string) *sync.Mutex {
	m.mu.Lock()
	if m.hostMu == nil {
		m.hostMu = map[string]*sync.Mutex{}
	}
	hm := m.hostMu[hostID]
	if hm == nil {
		hm = &sync.Mutex{}
		m.hostMu[hostID] = hm
	}
	m.mu.Unlock()
	return hm
}

// probeDeadline 返回探活期限：min(timeout, probeDeadlineCap)。
// timeout<=0（不设限）时取上限。
func (m *Manager) probeDeadline() time.Duration {
	if m.timeout <= 0 || m.timeout > probeDeadlineCap {
		return probeDeadlineCap
	}
	return m.timeout
}

// alive 在 deadline 内探测 SSH 连接是否仍可用：发送 openssh keepalive
// （wantReply=true 强制往返，死亡连接立即返回错误）。黑洞连接下
// SendRequest 永不返回，由看门狗判死；被放弃的 goroutine 会持续阻塞
// 在 SendRequest，靠调用方随后 closeExecutor 关闭连接解除——done 缓冲
// 为 1，解除后本 goroutine 发送完成即退出，不泄漏。
func alive(ex Executor, deadline time.Duration) bool {
	s, ok := ex.(*SSH)
	if !ok {
		return true // Local 恒可用
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := s.client.SendRequest("keepalive@openssh.com", true, nil)
		done <- err
	}()
	select {
	case err := <-done:
		return err == nil
	case <-time.After(deadline):
		return false
	}
}

// closeExecutor 关闭 Executor 背后的连接（Local 无连接可关）。
func closeExecutor(ex Executor) {
	if s, ok := ex.(*SSH); ok {
		_ = s.client.Close()
	}
}

// Close 关闭指定主机的缓存连接。
func (m *Manager) Close(hostID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ex, ok := m.clients[hostID]; ok {
		closeExecutor(ex)
		delete(m.clients, hostID)
	}
}

// CloseAll 关闭全部缓存连接。
func (m *Manager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, ex := range m.clients {
		closeExecutor(ex)
		delete(m.clients, id)
	}
}

// ActiveConns 返回当前缓存的连接数。
func (m *Manager) ActiveConns() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.clients)
}

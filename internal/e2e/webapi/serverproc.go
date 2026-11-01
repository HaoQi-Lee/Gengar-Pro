//go:build e2e

// Package webapi 是 e2e 的 webapi 档：驱动真实 gengar-server 子进程走
// HTTP/WS 全链路（登录/主机/对话）。与 headless 档（internal/e2e）同
// build tag、各自独立包：headless 测 CLI 二进制，本包测 server 二进制
// （子进程 + 真实端口，客户端是货真价实的 http.Client / websocket.Dialer）。
//
// 运行：go test -tags e2e ./internal/e2e/webapi/ -v
// 前置：冒烟/W01 登录流不碰主机、无 Docker 依赖；W02 主机 CRUD 需 Docker
// （缺失时统一 Skip）。需 go toolchain 现场编译。
package webapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// serverBin 构建缓存（进程级 sync.Once，同 buildHeadless 哲学）：全部
// 用例复用同一二进制，临时目录由 TestMain 在进程退出时统一清理——它
// 比任何单个用例活得久，不能挂在 t.Cleanup 上。
var (
	serverBuildOnce sync.Once
	serverBinDir    string
	serverBin       string
	serverBuildErr  error
)

// buildServer 现场编译 gengar-server（仓库根 main 包，go:embed 前端产物
// 已入库，无需先构建前端）。失败信息带编译输出。
func buildServer(t *testing.T) string {
	t.Helper()
	serverBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "gengar-e2e-server-bin")
		if err != nil {
			serverBuildErr = err
			return
		}
		bin := filepath.Join(dir, "gengar-server.exe")
		cmd := exec.Command("go", "build", "-o", bin, ".")
		cmd.Dir = repoRoot(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			serverBuildErr = fmt.Errorf("build gengar-server: %v\n%s", err, out)
			return
		}
		serverBinDir, serverBin = dir, bin
	})
	if serverBuildErr != nil {
		t.Fatalf("%v", serverBuildErr)
	}
	return serverBin
}

// adminUser/adminPass 是 startServer 引导的登录账号。角色语义（实测
// main.go）：runAddUser 建用户不写 Role，serve 路径的 ensureAdmin 把
// 已存在的 admin 用户提权为 role=admin——所以「--add-user admin + 正常
// 启动」两步后它就是管理员，无需第三步验证。
const (
	adminUser = "admin"
	adminPass = "e2e-admin-pass"
)

// lockedBuf 并发安全的输出缓冲。server 子进程的 stdout/stderr 由 exec
// 内部的拷贝 goroutine 持续写入，而 waitHealth 的超时/早退路径在进程
// 可能仍在写时就要读全部内容拼错误信息——bytes.Buffer 无锁，这个读写
// 并发是数据竞争（Task 3 审查 Important）。锁的是正确性而非性能：诊断
// 输出就几行，临界区开销无关紧要。写方（exec 拷贝 goroutine 的 Write）
// 与读方（测试进程的 String）由同一把 mu 串行化；本机 -race 检测器全局
// 损坏跑不了，正确性以本结构保证而非检测复核。
type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// String 返回当前全部内容的快照（锁内拷出，与后续写入互不影响）。
func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// serverProc 运行中的 gengar-server 子进程。out 收 stdout+stderr，
// 失败路径（health 超时/进程早退/用例失败）打印其内容诊断。
type serverProc struct {
	baseURL string // http://127.0.0.1:<port>
	dataDir string
	cmd     *exec.Cmd
	out     lockedBuf

	done    chan struct{} // Wait goroutine 返回后关闭（进程存活探测）
	waitErr error         // cmd.Wait 的返回值（done 关闭后才可读）
}

// startServer 引导式启动 gengar-server：
//  1. 一次性进程 --add-user admin（密码走 GENGAR_NEW_PASSWORD env）建首用户；
//  2. 正式进程 --listen 127.0.0.1:<空闲端口>，env 注入桩 LLM 地址/key；
//  3. 轮询 GET /api/health 至 ok（10s 上限；进程早退立即失败并附输出）；
//  4. t.Cleanup 杀进程 + 等待收尸；用例失败时顺带打印进程输出。
//
// 端口竞态（取到端口与服务器绑定间被他者抢占）重试一次（每次尝试取
// 新端口），再败即报错。GENGAR_MASTER_KEY 不注入：每用例独立 t.TempDir，
// 首启自动生成的 secret.key 已达成密码学隔离。
func startServer(t *testing.T, dataDir, stubURL, apiKey string) *serverProc {
	t.Helper()
	bin := buildServer(t)
	runAddAdmin(t, bin, dataDir)
	var errs []error
	for attempt := 1; ; attempt++ {
		p := launchServer(t, bin, dataDir, stubURL, apiKey, freePort(t))
		err := p.waitHealth(10 * time.Second)
		if err == nil {
			t.Cleanup(func() { p.stop(t) })
			return p
		}
		p.stop(t) // 回收这次尝试（可能已退出；stop 幂等）
		// 两次尝试的错误都留下：只报最后一次会吞掉首因——首次失败往往
		// 才是环境问题的原始信号，别让第二次的端口竞态报错掩盖它。
		errs = append(errs, fmt.Errorf("尝试%d: %w", attempt, err))
		if attempt >= 2 {
			t.Fatalf("gengar-server 两次启动均失败:\n%v", errors.Join(errs...))
		}
	}
}

// runAddAdmin 跑一次性 --add-user 进程（建用户后即退出），超时 30s。
func runAddAdmin(t *testing.T, bin, dataDir string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "--data", dataDir, "--add-user", adminUser)
	cmd.Dir = repoRoot(t)
	cmd.Env = serverEnv("GENGAR_NEW_PASSWORD=" + adminPass)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("add-user %s: %v\n--- output ---\n%s", adminUser, err, out.String())
	}
}

// launchServer 启动正式 server 进程并挂 Wait 收尸 goroutine（done 探活），
// 不等待就绪（health 轮询是调用方的事）。
func launchServer(t *testing.T, bin, dataDir, stubURL, apiKey string, port int) *serverProc {
	t.Helper()
	p := &serverProc{
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		dataDir: dataDir,
		done:    make(chan struct{}),
	}
	p.cmd = exec.Command(bin, "--data", dataDir, "--listen", fmt.Sprintf("127.0.0.1:%d", port))
	p.cmd.Dir = repoRoot(t)
	// env 语义同 runHeadless：先剔除继承的 ANTHROPIC_*/GENGAR_*（开发机
	// 的真实 key/网关地址不能漏进用例），再注入套件自管变量——
	// ANTHROPIC_BASE_URL/API_KEY 让 server 的 LLM 走桩（config 的 env 档
	// 优先级最高）；GENGAR_KNOWN_HOSTS 隔离 TOFU 库（main.go 启动期本来
	// 也会设 <data>/known_hosts，此处显式同值双保险）。
	p.cmd.Env = serverEnv(
		"ANTHROPIC_BASE_URL="+stubURL,
		"ANTHROPIC_API_KEY="+apiKey,
		knownHostsEnv(dataDir),
	)
	p.cmd.Stdout, p.cmd.Stderr = &p.out, &p.out
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start gengar-server: %v", err)
	}
	go func() {
		p.waitErr = p.cmd.Wait()
		close(p.done)
	}()
	return p
}

// serverEnv 构造子进程环境：继承环境剔除 ANTHROPIC_*/GENGAR_* 前缀后
// 追加 extra（后见者胜）。
func serverEnv(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "ANTHROPIC_") || strings.HasPrefix(kv, "GENGAR_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// knownHostsEnv 喂给 server 子进程的 TOFU 库隔离变量（同 internal/e2e
// harness 的 runHeadless；那边符号未导出，这三行不值得跨包导出）。
func knownHostsEnv(dataDir string) string {
	return "GENGAR_KNOWN_HOSTS=" + filepath.Join(dataDir, "known_hosts")
}

// waitHealth 轮询 GET /api/health 至 200 且 ok=true。两条失败路径都把
// 进程输出带进错误信息（启动崩溃 log.Fatalf 的原因只在输出里）：
//   - 进程早退（done 已关）：立即返回；
//   - timeout 上限：连接一直不通 / ok 不为 true。
//
// 返回 error 而非 t.Fatalf（供 startServer 重试），也让超时/早退两条
// 路径可用手工构造的 serverProc 无进程单测。
func (p *serverProc) waitHealth(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	var lastErr error
	for {
		resp, err := client.Get(p.baseURL + "/api/health")
		if err == nil {
			var body struct {
				OK bool `json:"ok"`
			}
			decErr := json.NewDecoder(resp.Body).Decode(&body)
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK && decErr == nil && body.OK {
				return nil
			}
			lastErr = fmt.Errorf("health = %d (ok=%v, decode=%v)", resp.StatusCode, body.OK, decErr)
		} else {
			lastErr = err
		}
		select {
		case <-p.done: // nil channel 恒阻塞 = 「进程还活着」的单测构造
			return fmt.Errorf("gengar-server 提前退出: %v\n--- server output ---\n%s", p.waitErr, p.out.String())
		default:
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("health 等待超时(%v)，最后错误: %v\n--- server output ---\n%s", timeout, lastErr, p.out.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// stop 收尾：杀进程（已退出则 Kill 报错忽略）+ 等 Wait 收尸恰好一次。
// 用例失败时打印进程输出（正常路径只有一两行监听日志，静默即可）。
func (p *serverProc) stop(t *testing.T) {
	t.Helper()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	<-p.done
	if t.Failed() {
		t.Logf("--- gengar-server output ---\n%s", p.out.String())
	}
}

// freePort 取一个空闲 TCP 端口（Listen 127.0.0.1:0 后读端口再关闭）。
// 关闭到服务器绑定之间的抢占窗口由 startServer 的重试兜底。
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// repoRoot 返回仓库根（测试进程 cwd = internal/e2e/webapi，上翻三级）。
func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join(".", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

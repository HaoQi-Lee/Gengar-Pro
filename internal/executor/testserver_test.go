package executor

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// newTestServer 启动一个进程内 SSH 服务器：密码认证 + exec 回显
// （输出 "RAN: <command>"）+ SFTP 子系统（服务本进程文件系统）。
// 返回监听地址。t.Cleanup 自动关闭。SSHExecutor 的单测全靠它，
// 无需 Docker 或真实 sshd。
func newTestServer(t *testing.T, user, password string) string {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)

	cfg := &ssh.ServerConfig{}
	cfg.PasswordCallback = func(meta ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
		if meta.User() == user && string(pass) == password {
			return nil, nil
		}
		return nil, fmt.Errorf("auth rejected for %s", meta.User())
	}
	cfg.AddHostKey(signer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleTestConn(conn, cfg)
		}
	}()
	return listener.Addr().String()
}

func handleTestConn(conn net.Conn, cfg *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			_ = newCh.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			// Accept 失败（客户端断开等）时 ch 为 nil，直接进 channel
			// 处理会 panic 并拖垮整个测试二进制
			continue
		}
		go handleTestSession(ch, chReqs)
	}
}

func handleTestSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "exec":
			var cmd struct{ Command string }
			_ = ssh.Unmarshal(req.Payload, &cmd)
			if cmd.Command == "__block__" {
				// 魔法命令：模拟长跑远端命令（取消/超时测试用），
				// 睡够久之后仍按普通 exec 流程回显
				time.Sleep(30 * time.Second)
			}
			_, _ = fmt.Fprintf(ch, "RAN: %s", cmd.Command)
			_ = req.Reply(true, nil)
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
			return
		case "subsystem":
			var sub struct{ Name string }
			_ = ssh.Unmarshal(req.Payload, &sub)
			if sub.Name == "sftp" {
				_ = req.Reply(true, nil)
				server, err := sftp.NewServer(ch)
				if err != nil {
					return
				}
				_ = server.Serve()
				return
			}
			_ = req.Reply(false, nil)
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// TestFixtureServerSmoke 冒烟：直接用 x/crypto/ssh 客户端拨号假服务器，
// 验证密码认证、exec 回显与 SFTP 子系统确实可用（提前暴露 fixture
// 自身的问题；02-T3 的 SSHExecutor 单测复用本 fixture）。
func TestFixtureServerSmoke(t *testing.T) {
	const user, pass = "gengar", "hunter2"
	addr := newTestServer(t, user, pass)

	cli, err := ssh.Dial("tcp", addr, &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(pass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()

	// exec 回显
	sess, err := cli.NewSession()
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer sess.Close()
	out, err := sess.Output("echo hi")
	if err != nil {
		t.Fatalf("exec output: %v", err)
	}
	if want := "RAN: echo hi"; string(out) != want {
		t.Fatalf("exec echo = %q, want %q", out, want)
	}

	// SFTP 子系统写入本进程文件系统
	sc, err := sftp.NewClient(cli)
	if err != nil {
		t.Fatalf("sftp client: %v", err)
	}
	defer sc.Close()
	path := t.TempDir() + "/fixture.txt"
	f, err := sc.Create(path)
	if err != nil {
		t.Fatalf("sftp create: %v", err)
	}
	if _, err := f.Write([]byte("sftp-ok")); err != nil {
		t.Fatalf("sftp write: %v", err)
	}
	_ = f.Close()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "sftp-ok" {
		t.Fatalf("sftp readback = %q err=%v", got, err)
	}
}

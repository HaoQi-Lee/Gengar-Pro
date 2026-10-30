// gengar-server 是 gengar-pro 的 Web 服务入口：单二进制自托管服务器
// （go:embed 内嵌前端产物 + SQLite），内网小团队部署，浏览器访问。
package main

import (
	"bufio"
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/bcrypt"

	"gengar-pro/internal/appcore"
	"gengar-pro/internal/audit"
	"gengar-pro/internal/executor"
	"gengar-pro/internal/host"
	"gengar-pro/internal/term"
	"gengar-pro/internal/webapi"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	home, _ := os.UserHomeDir()
	defaultData := filepath.Join(home, ".gengar-pro")
	var dataDir, listen, addUser, keyFile string
	flag.StringVar(&dataDir, "data", defaultData, "数据目录（gengar.db/memory/audit/secret.key）")
	flag.StringVar(&listen, "listen", ":8080", "监听地址")
	flag.StringVar(&addUser, "add-user", "", "创建登录用户后退出（密码读 GENGAR_NEW_PASSWORD，否则交互输入两遍）")
	flag.StringVar(&keyFile, "master-key-file", "", "主密钥文件（默认 <data>/secret.key，首次自动生成 0600；设 GENGAR_MASTER_KEY hex 时优先）")
	flag.Parse()

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Fatalf("创建数据目录 %s: %v", dataDir, err)
	}
	// known_hosts 随数据目录走（TOFU 记录在安全边界内；systemd 服务用户
	// 可能无 HOME，UserHomeDir 会失败）。须在任何 SSH 拨号前设置。
	executor.SetKnownHostsPath(filepath.Join(dataDir, "known_hosts"))
	if keyFile == "" {
		keyFile = filepath.Join(dataDir, "secret.key")
	}
	key, err := host.LoadOrCreateMasterKey(keyFile, os.Getenv("GENGAR_MASTER_KEY"))
	if err != nil {
		log.Fatalf("主密钥: %v", err)
	}
	// OpenAt 先用内存占位（启动期无任何拨号），随后装配 EncryptedStore。
	core, err := appcore.OpenAt(dataDir, host.MemoryStore{}, 30*time.Second)
	if err != nil {
		log.Fatalf("打开数据目录 %s: %v", dataDir, err)
	}
	enc, err := host.OpenEncrypted(core.Store(), key)
	if err != nil {
		log.Fatalf("加密存储: %v", err)
	}
	core.SetSecrets(enc)

	if addUser != "" {
		runAddUser(core, addUser) // 失败分支 log.Fatalf 直接退出
		_ = core.Close()          // 成功分支与 serve 路径对称收尾
		return
	}

	if err := ensureAdmin(core.Store()); err != nil {
		log.Fatalf("内置管理员: %v", err)
	}

	auditLog, err := audit.Open(filepath.Join(dataDir, "audit.jsonl"))
	if err != nil {
		log.Printf("警告: 审计未启用: %v", err)
		auditLog = nil
	}

	sub, err := fs.Sub(assets, "frontend/dist")
	if err != nil {
		log.Fatalf("内嵌前端: %v", err)
	}
	srv := webapi.New(core, auditLog, webapi.StaticHandler(sub))
	srv.SetTermManager(term.NewManager(core.Secrets(), srv.OnTermEvent))
	srv.SetKeysDir(filepath.Join(dataDir, "keys"))
	stopBackground := srv.StartBackground() // token TTL 清扫 / 闲置会话与 PTY 回收
	httpSrv := &http.Server{Addr: listen, Handler: srv}

	// shutdownDone 由信号 goroutine 在排空完成后关闭：ListenAndServe 在
	// Shutdown 关 listener 时即返回（早于排空完成），main 须等它再收尾。
	shutdownDone := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		signal.Stop(sig) // 第二个 Ctrl-C 走默认处置：优雅退出卡死时仍可强退
		log.Println("正在退出…")
		// 混合式优雅退出：先 Shutdown 排空在途请求（超时 3s）；将来 WS
		// 连接不被 Shutdown 关闭，超时/出错时 Close 兜底强杀全部连接。
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := httpSrv.Shutdown(ctx); err != nil {
			_ = httpSrv.Close()
		}
		close(shutdownDone)
	}()
	log.Printf("🔮 gengar-server 监听 %s · 数据目录 %s", listen, dataDir)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("服务: %v", err)
	}
	// 等排空完成再关 core/audit：3s 排空窗口内仍在跑的在途请求（如长
	// 连通测试）否则会撞上已关闭的 SQLite/audit。非优雅路径已 Fatalf 退出，
	// 必然收到 close（Shutdown 被 ctx 界定、Close 即时，goroutine 必终止）。
	<-shutdownDone
	stopBackground() // 停后台清扫（reaper/ sweeps）
	srv.CloseTerminals() // 先关全部 PTY，再收 audit/core
	if auditLog != nil {
		_ = auditLog.Close()
	}
	_ = core.Close()
}

// runAddUser 创建登录用户：密码优先 GENGAR_NEW_PASSWORD，否则交互输入两遍。
// 交互输入有回显是 alpha 已知限制（在服务器本地控制台执行）。
func runAddUser(core *appcore.Core, name string) {
	if _, err := core.Store().GetUser(name); err == nil {
		log.Fatalf("用户 %s 已存在", name)
	}
	pw := strings.TrimSpace(os.Getenv("GENGAR_NEW_PASSWORD"))
	if pw == "" {
		rd := bufio.NewReader(os.Stdin)
		fmt.Printf("为 %s 设置密码: ", name)
		l1, _ := rd.ReadString('\n')
		fmt.Printf("再输一遍: ")
		l2, _ := rd.ReadString('\n')
		l1, l2 = strings.TrimSpace(l1), strings.TrimSpace(l2)
		if l1 == "" || l1 != l2 {
			log.Fatalf("两次输入不一致或为空")
		}
		pw = l1
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), host.PwHashCost)
	if err != nil {
		log.Fatalf("bcrypt: %v", err)
	}
	if err := core.Store().CreateUser(host.User{
		Username: name, PwHash: string(h), CreatedAt: time.Now().Format(time.RFC3339),
	}); err != nil {
		log.Fatalf("创建用户: %v", err)
	}
	log.Printf("用户 %s 已创建", name)
}

// ensureAdmin 保证内置管理员存在：不存在则以 admin/admin 创建（role=admin）；
// 已存在则补齐角色（旧库 --add-user 建的 admin 提权，不动密码）；密码仍是
// 默认值时打警告。无管理员则系统不可管理，失败由 main Fatalf 拒启。
func ensureAdmin(store *host.Store) error {
	u, err := store.GetUser("admin")
	if errors.Is(err, host.ErrNoSuchUser) {
		h, err := bcrypt.GenerateFromPassword([]byte("admin"), host.PwHashCost)
		if err != nil {
			return fmt.Errorf("bcrypt: %w", err)
		}
		if err := store.CreateUser(host.User{
			Username: "admin", PwHash: string(h), Role: "admin",
			CreatedAt: time.Now().Format(time.RFC3339),
		}); err != nil {
			return fmt.Errorf("创建 admin: %w", err)
		}
		log.Printf("已创建内置管理员 admin/admin，请尽快修改密码")
		return nil
	}
	if err != nil {
		return err
	}
	if u.Role != "admin" {
		if err := store.SetUserRole("admin", "admin"); err != nil {
			return fmt.Errorf("提权 admin: %w", err)
		}
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PwHash), []byte("admin")) == nil {
		log.Printf("警告: admin 仍在使用默认密码，请尽快修改")
	}
	return nil
}

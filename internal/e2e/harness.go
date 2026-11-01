//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gengar-pro/internal/audit"
	"gengar-pro/internal/host"
)

// harness：用例脚手架。播种数据目录（主机清单 + 标签 + 密码引用）→
// 启动 headless 子进程（ANTHROPIC_BASE_URL 指桩 / 真实 API）→ 喂输入 →
// 收 stdout 与 audit.jsonl。断言数据源 = stdout（plain sink 全量文本）
// + audit 行（工具名 / Hosts / IsError）。

// SeedHost 描述一台待播种主机。密码统一走 e2ePassRef 引用，
// 实际值经 headless 的 --secrets 注入（不碰系统钥匙串）。
type SeedHost struct {
	Name string
	Addr string
	Port int
	User string
	Tags []string
}

// SeedHosts 用 host.OpenStore（同模块直写）把主机写入 dataDir 的 gengar.db，
// 与 headless/server 的 appcore.OpenAt 打开同一文件。须在启动被测进程前
// 完成。（导出：internal/e2e/webapi 档的 W 用例预置主机复用，StartStubLLM
// 先例。）
func SeedHosts(t *testing.T, dataDir string, hosts []SeedHost) {
	t.Helper()
	store, err := host.OpenStore(filepath.Join(dataDir, "gengar.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	auth := host.AuthConfig{Method: host.AuthPassword, PasswordRef: e2ePassRef}
	for _, sh := range hosts {
		user := sh.User
		if user == "" {
			user = e2eUser
		}
		h := &host.Host{
			ID: host.NewID(), Name: sh.Name, Addr: sh.Addr, Port: sh.Port,
			User: user, Auth: auth, Tags: sh.Tags,
		}
		if err := store.Save(h); err != nil {
			t.Fatalf("seed host %s: %v", sh.Name, err)
		}
	}
}

// knownHostsEnv 是喂给 headless 子进程的 TOFU 库隔离环境变量。容器主机
// 密钥每次运行都变，必须隔离；而 SSH 拨号发生在子进程——测试进程的
// executor.SetKnownHostsPath 够不着它，只能经 GENGAR_KNOWN_HOSTS 注入
// （executor.knownhostsPath 的 env 档，见该函数注释）。
func knownHostsEnv(dataDir string) string {
	return "GENGAR_KNOWN_HOSTS=" + filepath.Join(dataDir, "known_hosts")
}

var (
	headlessBuildOnce sync.Once
	headlessBinDir    string
	headlessBin       string
	headlessBuildErr  error
)

// buildHeadless 编译 ./cmd/gengar-headless，进程内只编一次（sync.Once
// 缓存到系统临时目录；每个用例复用同一二进制）。临时目录的清理由
// TestMain 在进程退出时统一执行——它比任何单个用例活得久，不能挂在
// 某个用例的 t.Cleanup 上（首用例结束就会删掉后续用例仍要复用的文件）。
func buildHeadless(t *testing.T) string {
	t.Helper()
	headlessBuildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "gengar-e2e-bin")
		if err != nil {
			headlessBuildErr = err
			return
		}
		bin := filepath.Join(dir, "gengar-headless.exe")
		cmd := exec.Command("go", "build", "-o", bin, "./cmd/gengar-headless")
		cmd.Dir = repoRoot(t)
		if out, err := cmd.CombinedOutput(); err != nil {
			headlessBuildErr = fmt.Errorf("build headless: %v\n%s", err, out)
			return
		}
		headlessBinDir, headlessBin = dir, bin
	})
	if headlessBuildErr != nil {
		t.Fatalf("%v", headlessBuildErr)
	}
	return headlessBin
}

// runHeadless 启动 headless 子进程跑完一轮输入：
//   - env：ANTHROPIC_BASE_URL（空 = 用真实 API 默认地址）/ ANTHROPIC_API_KEY
//     / GENGAR_COMMAND_TIMEOUT（单条 SSH 命令上限）/ GENGAR_KNOWN_HOSTS
//     （TOFU 库隔离进 dataDir）
//   - args：--data dataDir --pin-all --secrets e2ePassRef=e2ePass；
//     autoYes=true 加 --yes（自动批准全部 Risky 确认；false 时 plain sink
//     的 ConfirmBatch 恒返 false——E03 据此走人工确认拒绝路径）
//   - stdin：inputs 逐行喂入后关流，进程处理完 EOF 自然退出
//
// 返回合并的 stdout+stderr（断言与排障共用）。超时/非零退出 t.Fatalf。
func runHeadless(t *testing.T, dataDir, baseURL, apiKey string, inputs []string, timeout time.Duration, autoYes bool) string {
	t.Helper()
	bin := buildHeadless(t)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := []string{
		"--data", dataDir, "--pin-all",
		"--secrets", e2ePassRef + "=" + e2ePass,
	}
	if autoYes {
		args = append(args, "--yes")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = repoRoot(t)
	// 先剔除继承的 ANTHROPIC_* 与 GENGAR_*（开发机的真实 key / 网关地址 /
	// GENGAR_ENV 共识环境等不能漏进用例——用例行为只由下方显式注入决定），
	// 再显式注入桩地址或空值（空 = config.Default 的官方 API）与套件自管
	// 的 GENGAR_* 两个变量。
	env := make([]string, 0, len(os.Environ())+4)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "ANTHROPIC_") || strings.HasPrefix(kv, "GENGAR_") {
			continue
		}
		env = append(env, kv)
	}
	cmd.Env = append(env,
		"ANTHROPIC_BASE_URL="+baseURL,
		"ANTHROPIC_API_KEY="+apiKey,
		"GENGAR_COMMAND_TIMEOUT=20s",
		knownHostsEnv(dataDir),
	)
	cmd.Stdin = strings.NewReader(strings.Join(inputs, "\n") + "\n")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("headless exited: %v\n--- output ---\n%s", err, out.String())
	}
	return out.String()
}

// ReadAudit 解析 dataDir 下的审计分片（轮转后为 audit-YYYY-MM-DD.jsonl；
// 旧固定名 audit.jsonl 兼容读取）为 audit.Entry 列表（文件缺失 = 空）。
// （导出：webapi 档 W 用例的审计断言复用。）
func ReadAudit(t *testing.T, dataDir string) []audit.Entry {
	t.Helper()
	var raw []byte
	if shards, _ := filepath.Glob(filepath.Join(dataDir, "audit-*.jsonl")); len(shards) > 0 {
		for _, sh := range shards {
			b, err := os.ReadFile(sh)
			if err != nil {
				continue
			}
			raw = append(raw, b...)
			if len(raw) > 0 && raw[len(raw)-1] != '\n' {
				raw = append(raw, '\n')
			}
		}
	} else {
		raw, _ = os.ReadFile(filepath.Join(dataDir, "audit.jsonl"))
	}
	var entries []audit.Entry
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var e audit.Entry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("bad audit line %q: %v", line, err)
		}
		entries = append(entries, e)
	}
	return entries
}

// repoRoot 返回仓库根：从测试进程 cwd 逐级上翻找 go.mod。不能按包深
// 度硬编码上翻层数——本包的夹具（StartHosts 等）被 internal/e2e/webapi
// 档跨包复用，那边测试二进制的 cwd 深一级（internal/e2e/webapi），固定
// 上翻两级会错位到不存在的 internal\internal。
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("从 %s 上翻未找到 go.mod（仓库根定位失败）", dir)
		}
		dir = parent
	}
}

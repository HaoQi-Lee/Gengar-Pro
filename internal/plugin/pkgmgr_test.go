package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestPackageManager_Tools(t *testing.T) {
	pm := NewPackageManager()
	tools := pm.Tools()
	if len(tools) != 4 {
		t.Fatalf("Tools() count = %d, want 4", len(tools))
	}
	for _, td := range tools {
		if !td.Targeting {
			t.Errorf("%s Targeting = false, want true", td.Name)
		}
	}
}

// detectStub answers detection probes on a fake executor's RunShell. It
// serves /etc/os-release content for the `cat` probe and answers
// `command -v <bin>` from a known-binary set. Any `which` call errors —
// so if a test passes, detection provably did NOT rely on the external
// `which` binary (absent on minimal images — the original bug).
type detectStub struct {
	osrelease string            // content returned for `cat /etc/os-release`
	binaries  map[string]string // binary name -> path; presence means "installed"
}

func (d detectStub) run(cmd string) ([]byte, error) {
	switch {
	case cmd == "cat /etc/os-release":
		return []byte(d.osrelease), nil
	case strings.HasPrefix(cmd, "command -v "):
		name := strings.TrimPrefix(cmd, "command -v ")
		if p, ok := d.binaries[name]; ok {
			return []byte(p + "\n"), nil
		}
		return nil, fmt.Errorf("exit status 1") // binary not installed
	case strings.HasPrefix(cmd, "which "):
		return nil, fmt.Errorf("bash: which: command not found") // must never be used
	default:
		return nil, fmt.Errorf("unexpected probe: %q", cmd)
	}
}

// newPkgFake 组装一个 fakeExec：探测命令（cat /etc/os-release、command -v、
// which）由 detectStub 经 RunShell 回答；执行类子命令（argv 形式）记录到
// gotName/gotArgs 并返回空输出。
func newPkgFake(stub detectStub, got *string, gotArgs *[]string) *fakeExec {
	return &fakeExec{
		shellFn: func(_ context.Context, cmd string) ([]byte, error) {
			switch {
			case cmd == "cat /etc/os-release",
				strings.HasPrefix(cmd, "command -v "),
				strings.HasPrefix(cmd, "which "):
				return stub.run(cmd)
			default:
				return nil, fmt.Errorf("unexpected shell probe: %q", cmd)
			}
		},
		argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if got != nil {
				*got = name + " " + strings.Join(args, " ")
			}
			if gotArgs != nil {
				*gotArgs = args
			}
			return []byte(""), nil
		},
	}
}

func newPkgFakeShellOnly(stub detectStub, got *string) *fakeExec {
	return &fakeExec{shellFn: func(_ context.Context, cmd string) ([]byte, error) {
		return stub.run(cmd)
	}}
}

func TestPackageManager_DetectApt(t *testing.T) {
	pm := NewPackageManager()
	fake := newPkgFakeShellOnly(detectStub{
		osrelease: "ID=ubuntu\nID_LIKE=debian\n",
		binaries:  map[string]string{"apt": "/usr/bin/apt"},
	}, nil)
	mgr, err := pm.detectPackageManager(context.Background(), fake)
	if err != nil {
		t.Fatalf("detectPackageManager error: %v", err)
	}
	if mgr != "apt" {
		t.Errorf("detectPackageManager() = %q, want apt", mgr)
	}
}

func TestPackageManager_Install(t *testing.T) {
	pm := NewPackageManager()
	var got string
	fake := newPkgFake(detectStub{
		osrelease: "ID=ubuntu\nID_LIKE=debian\n",
		binaries:  map[string]string{"apt": "/usr/bin/apt"},
	}, &got, nil)
	result, err := pm.Execute(context.Background(), "package_install", json.RawMessage(`{"name":"nginx"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got != "apt install -y -- nginx" {
		t.Errorf("cmd = %q, want 'apt install -y -- nginx'", got)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
}

func TestPackageManager_Search(t *testing.T) {
	pm := NewPackageManager()
	stub := detectStub{
		osrelease: "ID=ubuntu\n",
		binaries:  map[string]string{"apt": "/usr/bin/apt"},
	}
	fake := &fakeExec{
		shellFn: func(_ context.Context, cmd string) ([]byte, error) {
			return stub.run(cmd)
		},
		argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name == "apt-cache" && strings.Join(args, " ") == "search -- nginx" {
				return []byte("nginx - small, powerful, scalable web server"), nil
			}
			return nil, fmt.Errorf("unexpected argv: %s %v", name, args)
		},
	}
	result, err := pm.Execute(context.Background(), "package_search", json.RawMessage(`{"query":"nginx"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if result.IsError {
		t.Errorf("unexpected error: %s", result.Content)
	}
}

func TestPackageManager_Remove(t *testing.T) {
	pm := NewPackageManager()
	var got string
	fake := newPkgFake(detectStub{
		osrelease: "ID=ubuntu\n",
		binaries:  map[string]string{"apt": "/usr/bin/apt"},
	}, &got, nil)
	_, err := pm.Execute(context.Background(), "package_remove", json.RawMessage(`{"name":"nginx"}`), fake)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if got != "apt remove -y -- nginx" {
		t.Errorf("cmd = %q, want 'apt remove -y -- nginx'", got)
	}
}

// TestPackageManager_DetectsYumOnCentOS is the regression for the reported
// T02/T19 bug: on a yum host whose image lacks `which`, the old detector probed
// `which apt|yum|dnf` (all failed) and silently fell back to apt. It must
// instead read os-release and pick yum.
func TestPackageManager_DetectsYumOnCentOS(t *testing.T) {
	pm := NewPackageManager()
	var got string
	fake := newPkgFake(detectStub{
		osrelease: "NAME=\"CentOS Linux\"\nVERSION=\"7 (Core)\"\nID=\"centos\"\nID_LIKE=\"rhel fedora\"\n",
		binaries:  map[string]string{"yum": "/usr/bin/yum"},
	}, &got, nil)

	if _, err := pm.Execute(context.Background(), "package_install", json.RawMessage(`{"name":"httpd"}`), fake); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if want := "yum install -y -- httpd"; got != want {
		t.Errorf("install cmd = %q, want %q (apt fallback regression)", got, want)
	}
}

func TestPackageManager_DetectsAptOnUbuntu(t *testing.T) {
	pm := NewPackageManager()
	var got string
	fake := newPkgFake(detectStub{
		osrelease: "NAME=\"Ubuntu\"\nID=ubuntu\nID_LIKE=debian\n",
		binaries:  map[string]string{"apt": "/usr/bin/apt"},
	}, &got, nil)

	if _, err := pm.Execute(context.Background(), "package_install", json.RawMessage(`{"name":"nginx"}`), fake); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if want := "apt install -y -- nginx"; got != want {
		t.Errorf("install cmd = %q, want %q", got, want)
	}
}

func TestPackageManager_PrefersDNFWhenPresent(t *testing.T) {
	pm := NewPackageManager()
	var got string
	fake := newPkgFake(detectStub{
		osrelease: "NAME=\"Rocky Linux\"\nID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\n",
		binaries:  map[string]string{"dnf": "/usr/bin/dnf", "yum": "/usr/bin/yum"},
	}, &got, nil)

	if _, err := pm.Execute(context.Background(), "package_remove", json.RawMessage(`{"name":"foo"}`), fake); err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if want := "dnf remove -y -- foo"; got != want {
		t.Errorf("remove cmd = %q, want %q (dnf preferred over yum)", got, want)
	}
}

// TestPackageManager_NoSilentAptFallback: when nothing is detectable, the tool
// must surface an error rather than guessing apt (the old default) and running
// an `apt install` that is doomed to fail with "command not found".
func TestPackageManager_NoSilentAptFallback(t *testing.T) {
	pm := NewPackageManager()
	var got string
	fake := newPkgFake(detectStub{
		osrelease: "NAME=\"Unknown Distro\"\n", // no ID=/ID_LIKE= → unknown family
		binaries:  map[string]string{},         // nothing installed
	}, &got, nil)

	res, err := pm.Execute(context.Background(), "package_install", json.RawMessage(`{"name":"httpd"}`), fake)
	if err != nil {
		t.Fatalf("Execute returned go-error %v; want a ToolResult with IsError", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError when no manager is detectable, got Content=%q", res.Content)
	}
	if got != "" {
		t.Errorf("no package command may run with a guessed manager, got %q", got)
	}
}

// TestPackageManager_InjectionBecomesArgvError: 评审 ①-4 注入回归——
// 包名夹带 `; reboot` 旧实现 Sprintf 拼 shell 免确认执行（package_install
// Risky 但 search/list 是 Safe 免确认）；argv 化后注入串只是字面参数。
func TestPackageManager_InjectionBecomesArgvError(t *testing.T) {
	pm := NewPackageManager()
	var gotArgs []string
	var shellProbes []string
	fake := &fakeExec{
		shellFn: func(_ context.Context, cmd string) ([]byte, error) {
			// 探测固定命令可以走 shell；执行面必须全部 argv
			shellProbes = append(shellProbes, cmd)
			return nil, fmt.Errorf("exit status 1")
		},
		argvFn: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != "dnf" {
				return nil, fmt.Errorf("unexpected argv name %q", name)
			}
			gotArgs = args
			return []byte(""), nil
		},
	}
	// os-release 解析失败 + 探测全失败会走「nothing detectable」错误路径，
	// 与注入无关；改用 rpm 家族确保探测成功。为省探测复杂度：直接验证
	// search（Safe 级）在 yum/dnf 下走 argv。
	stub := detectStub{
		osrelease: "ID=\"rocky\"\nID_LIKE=\"rhel\"\n",
		binaries:  map[string]string{"dnf": "/usr/bin/dnf"},
	}
	fake.shellFn = func(_ context.Context, cmd string) ([]byte, error) {
		return stub.run(cmd)
	}
	_, _ = pm.Execute(context.Background(), "package_search", json.RawMessage(`{"query":"nginx; reboot"}`), fake)
	for _, p := range shellProbes {
		if strings.Contains(p, ";") {
			t.Fatalf("injected payload must never reach a shell probe: %q", p)
		}
	}
	found := false
	for _, a := range gotArgs {
		if a == "nginx; reboot" {
			found = true
		}
	}
	if !found {
		t.Fatalf("injected query must appear verbatim as one argv: %v", gotArgs)
	}
}

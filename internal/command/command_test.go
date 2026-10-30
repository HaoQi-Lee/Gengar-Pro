package command

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"testing"
)

func TestRegistryAllReturnsCommands(t *testing.T) {
	c := Command{Name: "/foo", Description: "foo cmd"}
	r := NewRegistry(c)
	got := r.All()
	if len(got) != 1 {
		t.Fatalf("All() len = %d, want 1", len(got))
	}
	if got[0].Name != "/foo" {
		t.Errorf("All()[0].Name = %q, want %q", got[0].Name, "/foo")
	}
}

func TestRegistryMatch(t *testing.T) {
	r := NewRegistry(
		Command{Name: "/install"},
		Command{Name: "/inspect"},
	)

	cases := []struct {
		prefix string
		want   []string
	}{
		{"/", []string{"/inspect", "/install"}},
		{"/ins", []string{"/inspect", "/install"}},
		{"/insta", []string{"/install"}},
		{"/x", nil},
		{"hello", nil}, // doesn't start with "/", no matches
	}

	for _, tc := range cases {
		got := r.Match(tc.prefix)
		gotNames := make([]string, len(got))
		for i, c := range got {
			gotNames[i] = c.Name
		}
		if len(gotNames) != len(tc.want) {
			t.Errorf("Match(%q) len = %d, want %d (%v)", tc.prefix, len(gotNames), len(tc.want), gotNames)
			continue
		}
		for i, name := range gotNames {
			if name != tc.want[i] {
				t.Errorf("Match(%q)[%d] = %q, want %q", tc.prefix, i, name, tc.want[i])
			}
		}
	}
}

func TestRegistryLookup(t *testing.T) {
	r := NewRegistry(
		Command{Name: "/inspect", Aliases: []string{"/inspection", "/healthcheck"}},
		Command{Name: "/install", Aliases: []string{"/inst"}},
	)

	cases := []struct {
		input   string
		wantNil bool
		wantNm  string
	}{
		{"/inspect", false, "/inspect"},
		{"/healthcheck", false, "/inspect"},
		{"/install", false, "/install"},
		{"/inst", false, "/install"},
		{"/unknown", true, ""},
		{"", true, ""},
	}

	for _, tc := range cases {
		got := r.Lookup(tc.input)
		if tc.wantNil {
			if got != nil {
				t.Errorf("Lookup(%q) = %v, want nil", tc.input, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("Lookup(%q) = nil, want %q", tc.input, tc.wantNm)
			continue
		}
		if got.Name != tc.wantNm {
			t.Errorf("Lookup(%q).Name = %q, want %q", tc.input, got.Name, tc.wantNm)
		}
	}
}

type fakeAgent struct {
	processInputs []string
}

func (f *fakeAgent) ProcessInput(_ context.Context, input string) string {
	f.processInputs = append(f.processInputs, input)
	return ""
}

func TestSplitArgs(t *testing.T) {
	cases := []struct {
		line     string
		wantCmd  string
		wantArgs []string
	}{
		{"/install nginx", "/install", []string{"nginx"}},
		{"/install   nginx  mysql", "/install", []string{"nginx", "mysql"}},
		{"/inspect", "/inspect", nil},
		{"   /inst  ", "/inst", nil},
		{"", "", nil},
		{"    ", "", nil},
	}
	for _, tc := range cases {
		gotCmd, gotArgs := SplitArgs(tc.line)
		if gotCmd != tc.wantCmd || !reflect.DeepEqual(gotArgs, tc.wantArgs) {
			t.Errorf("SplitArgs(%q) = (%q, %v), want (%q, %v)",
				tc.line, gotCmd, gotArgs, tc.wantCmd, tc.wantArgs)
		}
	}
}

func TestContextArgsForwardedToRun(t *testing.T) {
	var got []string
	cmd := Command{
		Name: "/capture",
		Run: func(ctx Context) {
			got = ctx.Args
		},
	}
	r := NewRegistry(cmd)
	c := r.Lookup("/capture")
	if c == nil {
		t.Fatal("/capture not registered")
	}
	var buf bytes.Buffer
	c.Run(Context{Agent: &fakeAgent{}, Out: &buf, Args: []string{"nginx", "mysql"}})
	if len(got) != 2 || got[0] != "nginx" || got[1] != "mysql" {
		t.Errorf("ctx.Args = %v, want [nginx mysql]", got)
	}
}

// 命令执行透传 Agent/Out：Skill 命令的注入路径依赖这两个字段。
func TestRunReceivesAgentAndOut(t *testing.T) {
	a := &fakeAgent{}
	var buf bytes.Buffer
	cmd := Command{
		Name: "/inject",
		Run: func(ctx Context) {
			fmt.Fprintln(ctx.Out, "提示")
			ctx.Agent.ProcessInput(ctx.Ctx, "PLAYBOOK")
		},
	}
	NewRegistry(cmd).Lookup("/inject").Run(Context{Agent: a, Out: &buf})
	if !bytes.Contains(buf.Bytes(), []byte("提示")) {
		t.Errorf("Out 未收到输出: %q", buf.String())
	}
	if len(a.processInputs) != 1 || a.processInputs[0] != "PLAYBOOK" {
		t.Errorf("Agent.ProcessInput 收到 %v", a.processInputs)
	}
}

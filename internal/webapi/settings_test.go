// settings_test.go 全局设置保存：审计只记变更字段名清单，绝不记值
// （api_key 是全团队共享凭据，明文落审计即泄密——红线）。
package webapi

import (
	"net/http"
	"strings"
	"testing"
)

// TestSettingsSaveAuditFields settings_save 审计：Output 记
// fields=<变更字段名逗号清单>；掩码提交的 api_key 与未变化的字段不算
// 变更；反向断言明文 key 绝不出现在任何条目的 Input/Output。登录后再
// 开审计，排除 login_ok 干扰条数。
func TestSettingsSaveAuditFields(t *testing.T) {
	s := newAdminServer(t)
	ck := login(t, s, "admin", "adminpw")
	l := openTestAudit(t, s)

	// 首次保存：三字段全变（测试值不可能与默认/环境值撞车）→ 三名齐现
	w, _ := doJSON(t, s, "PUT", "/api/settings", `{"apiKey":"sk-testkey123","baseUrl":"https://x.example","model":"m1"}`, ck)
	if w.Code != http.StatusOK {
		t.Fatalf("save all = %d; want 200", w.Code)
	}
	es := readAuditEntries(t, l)
	if len(es) != 1 || es[0].Tool != "settings_save" || es[0].User != "admin" {
		t.Fatalf("settings_save 事件 = %+v; want 1 条 tool=settings_save user=admin", es)
	}
	if out := es[0].Output; !strings.Contains(out, "fields=") ||
		!strings.Contains(out, "api_key") || !strings.Contains(out, "base_url") || !strings.Contains(out, "model") {
		t.Fatalf("Output = %q; want fields= 含 api_key/base_url/model", out)
	}

	// 再保存：仅改 model；api_key 传 GET 回显的掩码形式（= 未修改），
	// baseUrl 原样 → 清单只有 model
	w, _ = doJSON(t, s, "PUT", "/api/settings", `{"apiKey":"sk-****y123","baseUrl":"https://x.example","model":"m2"}`, ck)
	if w.Code != http.StatusOK {
		t.Fatalf("save model only = %d; want 200", w.Code)
	}
	es = readAuditEntries(t, l)
	if len(es) != 2 || es[1].Tool != "settings_save" {
		t.Fatalf("第二次保存事件 = %+v; want 第 2 条 settings_save", es)
	}
	if out := es[1].Output; !strings.Contains(out, "model") || strings.Contains(out, "api_key") || strings.Contains(out, "base_url") {
		t.Fatalf("Output = %q; want 仅含 model（api_key 掩码、base_url 未变不入清单）", out)
	}

	// 红线反向断言：明文 key 不得出现在任何条目的 Input/Output 全文
	for _, e := range es {
		if strings.Contains(e.Output, "sk-testkey123") || strings.Contains(string(e.Input), "sk-testkey123") {
			t.Fatalf("审计条目泄值: %+v", e)
		}
	}
}

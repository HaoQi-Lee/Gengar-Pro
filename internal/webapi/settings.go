// 全局设置（API key / base URL / 模型）：全团队共享一份，仅 admin 可读写。
package webapi

import (
	"net/http"
	"strings"
)

type settingsView struct {
	APIKey  string `json:"apiKey"`
	BaseURL string `json:"baseUrl"`
	Model   string `json:"model"`
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request, username string) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	writeJSON(w, http.StatusOK, settingsView{
		APIKey: maskKey(s.cfg.APIKey), BaseURL: s.cfg.APIBaseURL, Model: s.cfg.Model,
	})
}

// handleSaveSettings 全局设置（全团队共享一份，仅 admin——key 是全团队
// 共享的账面凭据，非 admin 不应接触）：落库 + 更新内存 cfg，成功后重建
// 全部已在线用户的 Agent（新配置下次任务生效）。API Key 走 secrets 密文
// 通道（AES-GCM，与 SSH 凭据同标准），base_url/model 走 settings 表；
// 任一落库失败即回滚内存并 500（不留内存/DB 分叉）。
func (s *Server) handleSaveSettings(w http.ResponseWriter, r *http.Request, username string) {
	var v settingsView
	if err := readJSON(w, r, &v); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	secrets := s.core.Secrets()
	// changed 只收成功落库的字段名（在 rollback append 旁成对维护）；
	// 审计只报名清单，绝不报值——api_key 是凭据。
	changed := []string{}
	rollback := []func(){}
	s.cfgMu.Lock()
	fail := func(err error) {
		for _, undo := range rollback {
			undo()
		}
		s.cfgMu.Unlock()
		writeErr(w, http.StatusInternalServerError, err.Error())
	}
	if !strings.HasPrefix(v.APIKey, "sk-****") && v.APIKey != "" { // APIKey 为掩码时表示未修改
		old := s.cfg.APIKey
		if err := secrets.Set("api_key", v.APIKey); err != nil {
			fail(err)
			return
		}
		rollback = append(rollback, func() { s.cfg.APIKey = old })
		changed = append(changed, "api_key")
		s.cfg.APIKey = v.APIKey
	}
	if v.BaseURL != "" && v.BaseURL != s.cfg.APIBaseURL {
		old := s.cfg.APIBaseURL
		if err := s.core.Store().SetSetting("base_url", v.BaseURL); err != nil {
			fail(err)
			return
		}
		rollback = append(rollback, func() { s.cfg.APIBaseURL = old })
		changed = append(changed, "base_url")
		s.cfg.APIBaseURL = v.BaseURL
	}
	if v.Model != "" && v.Model != s.cfg.Model {
		old := s.cfg.Model
		if err := s.core.Store().SetSetting("model", v.Model); err != nil {
			fail(err)
			return
		}
		rollback = append(rollback, func() { s.cfg.Model = old })
		changed = append(changed, "model")
		s.cfg.Model = v.Model
	}
	s.cfgMu.Unlock()
	s.rebuildAllAgents() // cfgMu 释放后锁外重建（锁序见 rebuildAgent）
	// 全掩码/无变更的保存也落一条 fields= 空清单事件（记录保存动作本身）
	s.auditOp(username, "settings_save", "fields="+strings.Join(changed, ","), nil)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func maskKey(k string) string {
	if len(k) < 4 { // 含空串；过短时全掩码，避免负索引切片
		if k == "" {
			return ""
		}
		return "sk-****"
	}
	return "sk-****" + k[len(k)-4:]
}

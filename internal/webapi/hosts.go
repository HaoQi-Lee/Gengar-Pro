// 主机管理 REST：app.go 同名绑定方法的移植（单用户 App → Server + username）。
package webapi

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"gengar-pro/internal/executor"
	"gengar-pro/internal/host"
)

// badReqError 标记客户端可修正的业务错误（必填缺失/已被删除），
// handleSaveHost 映射 400；其余错误（Secrets/Store 故障）映射 500。
// 用类型而非哨兵 wrap 是为了让对外消息保持纯业务文案。
type badReqError struct{ msg string }

func (e *badReqError) Error() string { return e.msg }

func badReq(format string, a ...any) error {
	return &badReqError{msg: fmt.Sprintf(format, a...)}
}

// keyPathWithinDir 判定 p（Clean 后）是否位于 dir 内（含相等——
// 上传模式落盘的 <keysDir>/<id>.key 形态）。相对路径与 ../ 逃逸均判否。
func keyPathWithinDir(p, dir string) bool {
	cleanP := filepath.Clean(p)
	cleanDir := filepath.Clean(dir)
	// 相对路径不接受；p 与 dir 的绝对性必须一致（Windows 盘符差异走
	// filepath.Rel 的 err 判否）。上传模式服务端自定绝对路径，客户端
	// 声明的合法值只可能是同一 keysDir 下的绝对路径。
	if !filepath.IsAbs(cleanP) || filepath.IsAbs(cleanDir) != filepath.IsAbs(cleanP) {
		return false
	}
	rel, err := filepath.Rel(cleanDir, cleanP)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// hostView 与前端 types.ts HostView 对应（与 app.go HostView 同构）。
type hostView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Addr        string   `json:"addr"`
	Port        int      `json:"port"`
	User        string   `json:"user"`
	Tags        []string `json:"tags"`
	OS          string   `json:"os"`
	Folder      string   `json:"folder"`
	Status      string   `json:"status"`
	AuthMethod  string   `json:"authMethod"`
	KeyPath     string   `json:"keyPath"`
	PasswordRef string   `json:"passwordRef"`
}

type hostReq struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Addr          string   `json:"addr"`
	Port          int      `json:"port"`
	User          string   `json:"user"`
	Tags          []string `json:"tags"`
	Folder        string   `json:"folder"`
	AuthMethod    string   `json:"authMethod"`
	KeyPath       string   `json:"keyPath"`
	KeyData       string   `json:"keyData"` // base64 私钥内容（上传模式；非空时服务端落盘并自定 KeyPath）
	Password      string   `json:"password"`
	KeyPassphrase string   `json:"keyPassphrase"`
}

func (s *Server) handleGetHosts(w http.ResponseWriter, r *http.Request, username string) {
	_, isAdmin := s.viewerOf(username)
	hosts, err := s.core.Store().ListFor(username, isAdmin)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "读取清单失败: "+err.Error())
		return
	}
	out := make([]hostView, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, hostView{
			ID: h.ID, Name: h.Name, Addr: h.Addr, Port: h.Port, User: h.User,
			Tags: h.Tags, OS: h.OS, Folder: h.Folder,
			Status:     s.getStatus(h.ID),
			AuthMethod: string(h.Auth.Method), KeyPath: h.Auth.KeyPath, PasswordRef: h.Auth.PasswordRef,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": out})
}

func (s *Server) handleSaveHost(w http.ResponseWriter, r *http.Request, username string) {
	var req hostReq
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	_, isAdmin := s.viewerOf(username)
	h, err := s.saveHost(req, username, isAdmin)
	if err != nil {
		var br *badReqError
		if errors.As(err, &br) {
			writeErr(w, http.StatusBadRequest, err.Error())
		} else {
			writeErr(w, http.StatusInternalServerError, "保存失败: "+err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": h.ID})
}

// saveHost 是 app.go SaveHost 的移植：校验 → 新 ID/编辑保留原引用 →
// 秘密写 SecretStore → 防复活复查 → upsert。视角（username/isAdmin）：
// 新建归属操作者；编辑/防复活经 findHostFor 属主过滤（他人主机等价
// 不存在，防存在性探测）。
func (s *Server) saveHost(req hostReq, username string, isAdmin bool) (*host.Host, error) {
	if req.Name == "" || req.Addr == "" || req.User == "" {
		return nil, badReq("名称、地址、用户为必填")
	}
	if req.Port == 0 {
		req.Port = 22
	}
	// KeyPath 白名单（评审 ①-5）：客户端声明的 keyPath 只接受服务端密钥
	// 目录内的路径（上传模式落盘的自定路径）。任意路径会让登录用户把
	// 服务器本地文件（如 /root/.ssh/id_rsa）当私钥连任意主机。
	if req.KeyData == "" && req.KeyPath != "" {
		if s.keysDir == "" || !keyPathWithinDir(req.KeyPath, s.keysDir) {
			return nil, badReq("私钥请上传，不支持引用服务器本地路径")
		}
	}
	h := &host.Host{
		ID: req.ID, Name: req.Name, Addr: req.Addr, Port: req.Port, User: req.User,
		Tags: req.Tags, Folder: req.Folder,
		Auth: host.AuthConfig{Method: host.AuthMethod(req.AuthMethod), KeyPath: req.KeyPath},
	}
	if h.ID == "" {
		h.ID = host.NewID()
		h.Owner = username // 新建归属操作者（admin 替谁建的场景不存在——无划转功能）
	} else {
		old := s.findHostFor(h.ID, username, isAdmin)
		if old == nil {
			return nil, badReq("主机已被删除") // 编辑目标不存在（或非属主）：报错，不按旧 ID 复活条目
		}
		h.Owner = old.Owner // 编辑不改归属（owner upsert 覆盖，防丢失）
		// 编辑路径：表单不回传秘密与 OS（密码只存钥匙串、OS 靠 TestHost 探测），
		// 保留原引用与探测值，否则一次改名就会打坏密码认证、清掉 OS。
		// KeyPath 同语义：不上传新私钥就沿用已落盘的 key 文件。
		h.Auth.PasswordRef = old.Auth.PasswordRef
		h.Auth.KeyPassphraseRef = old.Auth.KeyPassphraseRef
		h.Auth.KeyPath = old.Auth.KeyPath
		h.OS = old.OS
	}
	if req.Password != "" {
		ref := "gengar-host-" + h.ID + "-password"
		if err := s.core.Secrets().Set(ref, req.Password); err != nil {
			return nil, fmt.Errorf("写入凭据存储失败: %w", err)
		}
		h.Auth.PasswordRef = ref
	}
	if req.KeyPassphrase != "" {
		ref := "gengar-host-" + h.ID + "-passphrase"
		if err := s.core.Secrets().Set(ref, req.KeyPassphrase); err != nil {
			return nil, fmt.Errorf("写入凭据存储失败: %w", err)
		}
		h.Auth.KeyPassphraseRef = ref
	}
	if req.KeyData != "" {
		if s.keysDir == "" {
			return nil, badReq("服务端未配置密钥目录")
		}
		raw, err := base64.StdEncoding.DecodeString(req.KeyData)
		if err != nil {
			return nil, badReq("私钥内容不是有效的 base64")
		}
		// 口令依次尝试：表单新填 > 旧 ref > 无口令；首个解析成功者胜，
		// 胜者同时决定 KeyPassphraseRef 设/留/清。
		var tries []string
		if req.KeyPassphrase != "" {
			tries = append(tries, req.KeyPassphrase)
		}
		if h.Auth.KeyPassphraseRef != "" {
			if old, err := s.core.Secrets().Get(h.Auth.KeyPassphraseRef); err == nil && old != "" {
				tries = append(tries, old)
			}
		}
		tries = append(tries, "")
		winner := -1
		for i, pass := range tries {
			if pass == "" {
				if _, err = ssh.ParsePrivateKey(raw); err == nil {
					winner = i
					break
				}
			} else if _, err = ssh.ParsePrivateKeyWithPassphrase(raw, []byte(pass)); err == nil {
				winner = i
				break
			}
		}
		if winner < 0 {
			return nil, badReq("私钥无法解析：口令错误或格式不支持")
		}
		switch {
		case winner == 0: // 表单口令胜 → 已由上方 passphrase 段写 ref
		case winner == 1: // 旧口令胜 → ref 已保留
		default: // 无口令胜且旧 ref 存在 → 新 key 无口令，清掉旧引用
			h.Auth.KeyPassphraseRef = ""
		}
		if err := os.MkdirAll(s.keysDir, 0o700); err != nil {
			return nil, fmt.Errorf("创建密钥目录失败: %w", err)
		}
		keyPath := filepath.Join(s.keysDir, h.ID+".key")
		// 先写文件后 Save 主机：Save 失败留下的孤儿文件按 hostID 命名、
		// 永不被引用，无害（同 ID 重建时覆盖）。
		if err := os.WriteFile(keyPath, raw, 0o600); err != nil {
			return nil, fmt.Errorf("写入私钥文件失败: %w", err)
		}
		h.Auth.KeyPath = keyPath
	}
	if h.Auth.Method == host.AuthKey && h.Auth.KeyPath == "" {
		return nil, badReq("请上传私钥文件")
	}
	// 编辑场景的 delete-resurrect 防护：写钥匙串期间主机可能已被删除，
	// 最终 Save 前再确认旧 ID 仍存在（新主机无此顾虑）。
	if req.ID != "" && s.findHostFor(req.ID, username, isAdmin) == nil {
		return nil, badReq("主机已被删除")
	}
	if err := s.core.Store().Save(h); err != nil {
		// hosts.name 表级 UNIQUE（全局唯一，跨用户）：同名冲突给出可操作
		// 文案而非裸 500（重名后缀预览按视角算，视角外同名要到这里才暴露）。
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			return nil, badReq("主机名 %s 已被使用（全局唯一），请换名", h.Name)
		}
		return nil, err
	}
	return h, nil
}

func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request, username string) {
	id := r.PathValue("id")
	_, isAdmin := s.viewerOf(username)
	if s.findHostFor(id, username, isAdmin) == nil {
		writeErr(w, http.StatusNotFound, "主机不存在")
		return
	}
	if err := s.deleteHost(id); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) deleteHost(id string) error {
	if err := s.core.Store().Delete(id); err != nil {
		return err
	}
	// 顺带清钥匙串引用（幂等；密钥认证的主机可能没有密码条目）
	_ = s.core.Secrets().Delete("gengar-host-" + id + "-password")
	_ = s.core.Secrets().Delete("gengar-host-" + id + "-passphrase")
	if s.keysDir != "" {
		_ = os.Remove(filepath.Join(s.keysDir, id+".key")) // 幂等；上传的私钥随主机删除
	}
	if s.termMgr != nil { // 终端未启用（nil，测试场景）时跳过
		s.termMgr.CloseHost(id) // 在线终端随主机删除断开
	}
	s.statusMu.Lock()
	delete(s.status, id)
	s.statusMu.Unlock()
	return nil
}

// handleTestHost 拨号 + echo 探测（app.go TestHost 移植）；成功采集 OS 回写。
func (s *Server) handleTestHost(w http.ResponseWriter, r *http.Request, username string) {
	id := r.PathValue("id")
	_, isAdmin := s.viewerOf(username)
	h := s.findHostFor(id, username, isAdmin)
	if h == nil {
		s.setStatus(id, "fail")
		writeErr(w, http.StatusNotFound, "主机不存在")
		return
	}
	ex, err := s.core.ExecutorFor(h)
	if err != nil {
		s.setStatus(id, "fail")
		writeErr(w, http.StatusBadGateway, "连接失败: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, err := ex.RunShell(ctx, "echo gengar-ok"); err != nil {
		s.setStatus(id, "fail")
		writeErr(w, http.StatusBadGateway, "执行失败: "+err.Error())
		return
	}
	osInfo, _ := ex.RunShell(ctx, `. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME" || uname -s`)
	if v := strings.TrimSpace(string(osInfo)); v != "" {
		h.OS = v // 探测失败时保留清单里已有的 OS，不用空串覆盖
	}
	if s.findHostFor(id, username, isAdmin) == nil { // 探测窗口内被删：不回写（Save 会复活已删条目）
		s.setStatus(id, "ok")
		writeJSON(w, http.StatusOK, map[string]string{"message": "连通正常 · " + h.OS})
		return
	}
	_ = s.core.Store().Save(h)
	s.setStatus(id, "ok")
	writeJSON(w, http.StatusOK, map[string]string{"message": "连通正常 · " + h.OS})
}

// overlayStore 表单内测试的明文秘密覆盖层：Get 先查 plain（未保存的
// 密码/口令），未命中落 inner（已存主机的秘密引用）；Set/Delete 透传。
type overlayStore struct {
	plain map[string]string
	inner host.SecretStore
}

func (o overlayStore) Set(ref, secret string) error { return o.inner.Set(ref, secret) }

func (o overlayStore) Get(ref string) (string, error) {
	if v, ok := o.plain[ref]; ok {
		return v, nil
	}
	return o.inner.Get(ref)
}

func (o overlayStore) Delete(ref string) error { return o.inner.Delete(ref) }

// handleTestHostDraft 表单内测试：用未保存的表单参数拨号（不写库、不写
// 密钥目录）——明文密码/口令经 overlay 覆盖层、keyData 落临时文件用后
// 即删；req.ID 给出时继承已存主机的 KeyPath 与秘密引用。仅 req.ID 非空
// 时回写侧栏连通状态。
func (s *Server) handleTestHostDraft(w http.ResponseWriter, r *http.Request, username string) {
	var req hostReq
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	if req.Addr == "" || req.User == "" {
		writeErr(w, http.StatusBadRequest, "地址与用户为必填")
		return
	}
	if req.Port == 0 {
		req.Port = 22
	}
	_, isAdmin := s.viewerOf(username)
	h := &host.Host{
		Name: req.Name, Addr: req.Addr, Port: req.Port, User: req.User,
		Auth: host.AuthConfig{Method: host.AuthMethod(req.AuthMethod), KeyPath: req.KeyPath},
	}
	plain := map[string]string{}
	if req.ID != "" {
		if old := s.findHostFor(req.ID, username, isAdmin); old != nil {
			h.ID = old.ID
			h.Auth.PasswordRef = old.Auth.PasswordRef
			h.Auth.KeyPassphraseRef = old.Auth.KeyPassphraseRef
			h.Auth.KeyPath = old.Auth.KeyPath
		} else {
			// 他人主机的秘密引用不可继承（凭据探测面）：等价不存在
			writeErr(w, http.StatusNotFound, "主机不存在")
			return
		}
	}
	if req.Password != "" {
		plain["draft-password"] = req.Password
		h.Auth.PasswordRef = "draft-password"
	}
	if req.KeyPassphrase != "" {
		plain["draft-passphrase"] = req.KeyPassphrase
		h.Auth.KeyPassphraseRef = "draft-passphrase"
	}
	cleanup := func() {}
	if req.KeyData != "" {
		raw, err := base64.StdEncoding.DecodeString(req.KeyData)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "私钥内容不是有效的 base64")
			return
		}
		tmp, err := os.CreateTemp("", "gengar-keytest-")
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "创建临时文件失败")
			return
		}
		if _, err := tmp.Write(raw); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
			writeErr(w, http.StatusInternalServerError, "写入临时文件失败")
			return
		}
		_ = tmp.Close()
		_ = os.Chmod(tmp.Name(), 0o600)
		cleanup = func() { _ = os.Remove(tmp.Name()) }
		h.Auth.KeyPath = tmp.Name()
	}
	defer cleanup()
	ex, closeEx, err := executor.DialHostOnce(h, overlayStore{plain: plain, inner: s.core.Secrets()}, 20*time.Second)
	if err != nil {
		if req.ID != "" {
			s.setStatus(req.ID, "fail")
		}
		writeErr(w, http.StatusBadGateway, "连接失败: "+err.Error())
		return
	}
	defer closeEx()
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if _, err := ex.RunShell(ctx, "echo gengar-ok"); err != nil {
		if req.ID != "" {
			s.setStatus(req.ID, "fail")
		}
		writeErr(w, http.StatusBadGateway, "执行失败: "+err.Error())
		return
	}
	osInfo, _ := ex.RunShell(ctx, `. /etc/os-release 2>/dev/null && echo "$PRETTY_NAME" || uname -s`)
	if req.ID != "" {
		s.setStatus(req.ID, "ok")
	}
	writeJSON(w, http.StatusOK, map[string]string{"message": "连通正常 · " + strings.TrimSpace(string(osInfo))})
}

// findHostFor 按 ID 查主机（视角过滤）：不存在或非属主且非 admin 一律
// nil——对外统一「不存在」，与 chatForUser 同语义防存在性探测（设计 §4.1）。
// 清单很小，视角内全量扫足够。
func (s *Server) findHostFor(id, username string, isAdmin bool) *host.Host {
	hosts, err := s.core.Store().ListFor(username, isAdmin)
	if err != nil {
		return nil
	}
	for _, h := range hosts {
		if h.ID == id {
			return h
		}
	}
	return nil
}

func (s *Server) setStatus(id, st string) {
	s.statusMu.Lock()
	s.status[id] = st
	s.statusMu.Unlock()
}

func (s *Server) getStatus(id string) string {
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	if v, ok := s.status[id]; ok {
		return v
	}
	return "unknown"
}

// handleSetScope 设置/更改该会话的对话目标（ScopePicker 选定）。hostIds
// 过滤为「该用户视角内」的现存主机（他人主机不可钉住）；空集 = 清空目标
// （省略主机的指令会被模型反问）。
func (s *Server) handleSetScope(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Session string   `json:"session"`
		HostIDs []string `json:"hostIds"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	cs := s.chatForUser(req.Session, username)
	if cs == nil {
		writeErr(w, http.StatusBadRequest, "会话不存在（服务可能已重启，请刷新页面）")
		return
	}
	_, isAdmin := s.viewerOf(username)
	var ids []string
	if hosts, err := s.core.Store().ListFor(username, isAdmin); err == nil {
		exist := make(map[string]bool, len(hosts))
		for _, h := range hosts {
			exist[h.ID] = true
		}
		for _, id := range req.HostIDs {
			if exist[id] {
				ids = append(ids, id)
			}
		}
	}
	cs.sess.SetPinned(ids)
	msg := "🎯 已清空对话目标（省略主机的指令会被询问目标）"
	if names := s.scopeNames(cs); names != "" {
		msg = "🎯 本次对话目标已设定：" + strings.ReplaceAll(names, "、", " · ") + "（下一条消息起生效）"
	}
	cs.sink.Info(msg)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// scopeNames 返回该会话当前目标的主机名（顿号连接）；无目标返回空。
// 目标解析按创建者视角（会话内主机清单隔离，设计 §4.2）。
func (s *Server) scopeNames(cs *chatSession) string {
	ids := cs.sess.PinnedIDs()
	if len(ids) == 0 {
		return ""
	}
	var names []string
	if hosts, err := s.core.Store().ListFor(cs.owner, cs.viewAdmin); err == nil {
		byID := make(map[string]string, len(hosts))
		for _, h := range hosts {
			byID[h.ID] = h.Name
		}
		for _, id := range ids {
			if n, ok := byID[id]; ok {
				names = append(names, n)
			}
		}
	}
	return strings.Join(names, "、")
}

// XShell 上传导入：浏览器读文件 → POST 内容（base64）→ 服务端解析预览
// （重复标记 + 重名后缀）→ 勾选提交。替代桌面端的目录扫描三步流。
package webapi

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"gengar-pro/internal/xshell"
)

type xshellFileReq struct {
	Name string `json:"name"`
	Data string `json:"data"` // base64 原始字节（UTF-16LE BOM 等）
}

type xshellRowView struct {
	Name       string   `json:"name"`
	Host       string   `json:"host"`
	Port       int      `json:"port"`
	User       string   `json:"user"`
	AuthMethod string   `json:"authMethod"`
	KeyPath    string   `json:"keyPath"`
	Tags       []string `json:"tags"`
	Folder     string   `json:"folder"`
	Source     string   `json:"source"`
	Duplicate  bool     `json:"duplicate"`
}

// handleXshellPreview 是 app.go ScanXshell 的移植（559-594 行）：判重键
// addr|port|user，重名 UniqueName 加后缀（预览行显示的即最终名），folder
// 取 Source 目录段；区别仅在输入为上传内容而非目录扫描。
func (s *Server) handleXshellPreview(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Files []xshellFileReq `json:"files"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	files := make([]xshell.File, 0, len(req.Files))
	for _, f := range req.Files {
		// 服务端扩展名防御（与 Scan 的 WalkDir 过滤对齐；前端也有一层）
		if !strings.EqualFold(filepath.Ext(f.Name), ".xsh") {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(f.Data)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "文件 "+f.Name+" base64 解码失败")
			return
		}
		files = append(files, xshell.File{Name: f.Name, Content: raw})
	}
	res, err := xshell.ParseFiles(files)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	existing := map[string]bool{}
	usedNames := map[string]bool{} // name UNIQUE：预览行显示的即最终名
	_, isAdmin := s.viewerOf(username)
	if hosts, lerr := s.core.Store().ListFor(username, isAdmin); lerr == nil {
		for _, h := range hosts {
			existing[fmt.Sprintf("%s|%d|%s", h.Addr, h.Port, h.User)] = true
			usedNames[h.Name] = true
		}
	}
	rows := make([]xshellRowView, 0, len(res.Sessions))
	for _, sess := range res.Sessions {
		name := xshell.UniqueName(sess.Name, usedNames)
		usedNames[name] = true
		folder := ""
		if i := strings.LastIndex(sess.Source, "/"); i > 0 {
			folder = sess.Source[:i] // "web/a.xsh" → "web"；根会话留空
		}
		rows = append(rows, xshellRowView{
			Name: name, Host: sess.Host, Port: sess.Port, User: sess.User,
			AuthMethod: sess.AuthMethod, KeyPath: sess.KeyPath, Tags: sess.Tags,
			Folder: folder, Source: sess.Source,
			Duplicate: existing[fmt.Sprintf("%s|%d|%s", sess.Host, sess.Port, sess.User)],
		})
	}
	// 审计只记解析计数（parsed= 可导入行数）；.xsh 内容与主机数据不入审计。
	s.auditOp(username, "xshell_import_preview", fmt.Sprintf("parsed=%d hosts", len(rows)), nil)
	writeJSON(w, http.StatusOK, map[string]any{
		"rows": rows, "skipped": res.Skipped, "failed": res.Failed,
	})
}

// handleXshellCommit 是 app.go ImportXshell 的移植：逐台复用 saveHost
// 语义，首错中止（已入库保留）；badReqError→400、存储故障→500 与
// handleSaveHost 同分流（T1.5 审查注意点）。导入归属导入者（saveHost
// 新建分支的视角语义）。
func (s *Server) handleXshellCommit(w http.ResponseWriter, r *http.Request, username string) {
	var req struct {
		Hosts []hostReq `json:"hosts"`
	}
	if err := readJSON(w, r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "请求格式错误")
		return
	}
	_, isAdmin := s.viewerOf(username)
	saved := make([]string, 0, len(req.Hosts)) // 已成功入库的主机名（部分失败时的溯源清单）
	for _, hr := range req.Hosts {
		if _, err := s.saveHost(hr, username, isAdmin); err != nil {
			var br *badReqError
			if errors.As(err, &br) {
				writeErr(w, http.StatusBadRequest, fmt.Sprintf("导入 %s 失败: %s", hr.Name, err.Error()))
			} else {
				writeErr(w, http.StatusInternalServerError, fmt.Sprintf("导入 %s 失败: %s", hr.Name, err.Error()))
			}
			// 部分失败审计：已入库主机必须有痕迹——落汇总条目并带名单，
			// Output 指明入库数与首个失败名；首台即失败（k=0）无任何
			// 入库，不记（响应已报错，与全失败现状一致）。
			if len(saved) > 0 {
				s.auditOp(username, "xshell_import_commit",
					fmt.Sprintf("imported=%d/failed_at=%s", len(saved), hr.Name),
					map[string]any{"names": saved})
			}
			return
		}
		saved = append(saved, hr.Name)
	}
	s.auditOp(username, "xshell_import_commit", fmt.Sprintf("imported=%d hosts", len(req.Hosts)),
		map[string]any{"names": saved})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

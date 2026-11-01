// auditop.go webapi 合成审计事件的统一注入点：Tool=事件名约定
// （settings_save/term_open/login_ok/...），input 为结构化载荷（可为 nil）。
// 审计写失败不阻塞业务（与既有 logUserOp/settings_save 同语义）。
package webapi

import (
	"encoding/json"
	"net"
	"net/http"
	"time"

	"gengar-pro/internal/audit"
)

// auditOp 记一条 webapi 合成事件。user=操作者，tool=事件名，
// output=人读详情，input=结构化载荷（nil 则不记）。
func (s *Server) auditOp(user, tool, output string, input any) {
	if s.audit == nil {
		return
	}
	var in json.RawMessage
	if input != nil {
		if b, err := json.Marshal(input); err == nil {
			in = b
		}
	}
	_ = s.audit.Log(audit.Entry{
		TS:     time.Now().Format(time.RFC3339),
		User:   user,
		Tool:   tool,
		Input:  in,
		Output: output,
	})
}

// clientIP 取请求来源 IP（RemoteAddr 去端口；解析失败返回原串）。
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

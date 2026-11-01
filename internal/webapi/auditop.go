// auditop.go webapi 合成审计事件的统一注入点：Tool=事件名约定
// （settings_save/term_open/login_ok/...），input 为结构化载荷（可为 nil）。
// 审计写失败不阻塞业务（与既有 logUserOp/settings_save 同语义）。
package webapi

import (
	"encoding/json"
	"fmt"
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

// truncRunes 超长时按 rune 截断 s 至 n 字符并以省略号收尾（按 rune 截
// 不切开多字节字符）。webapi 审计载荷/详情截断共用（原居 hosts.go，
// chat 等处也要用故挪此统一）。
func truncRunes(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

// truncAuditText chat 原文入审计的截断：8KB（rune 数）上限，超限保留
// 前缀并以「…[+N chars]」显式收尾——正文自然出现的省略号不足以辨认
// 截断，N 为被省略的字符数（超限可辨识）。
func truncAuditText(s string) string {
	const max = 8 << 10
	rs := []rune(s)
	if len(rs) <= max {
		return s
	}
	return truncRunes(s, max) + fmt.Sprintf("[+%d chars]", len(rs)-max)
}

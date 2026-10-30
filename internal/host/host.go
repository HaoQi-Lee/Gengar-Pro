// Package host 提供 gengar-pro 的受管服务器清单：数据模型、目标解析、
// 持久化存储与凭据引用。清单只存在于控制端，远端服务器零部署。
package host

import (
	"crypto/rand"
	"fmt"
)

// AuthMethod 枚举认证方式。v1 支持密钥与密码两种（设计 D4）。
type AuthMethod string

const (
	AuthKey      AuthMethod = "key"      // 私钥文件（可选 passphrase）
	AuthPassword AuthMethod = "password" // 密码，存系统钥匙串
)

// AuthConfig 描述如何登录一台主机。秘密本身永不入库：
// 密码/口令以 PasswordRef/PassphraseRef 引用系统钥匙串条目（见 secrets.go）。
type AuthConfig struct {
	Method           AuthMethod `json:"method"`
	KeyPath          string     `json:"key_path,omitempty"`           // AuthKey：私钥路径
	KeyPassphraseRef string     `json:"key_passphrase_ref,omitempty"` // AuthKey：私钥口令的钥匙串引用（可选）
	PasswordRef      string     `json:"password_ref,omitempty"`       // AuthPassword：密码的钥匙串引用
}

// Host 是一台受管服务器。ID 创建后不变，记忆按主机名分域
// （memory/<name>.json，重命名视为新机器），Name 可改。机组 = 标签（Tags）。
type Host struct {
	ID   string     `json:"id"`
	Name string     `json:"name"`
	Addr string     `json:"addr"`
	Port int        `json:"port"`
	User string     `json:"user"`
	Auth AuthConfig `json:"auth"`
	Tags []string   `json:"tags"`
	OS   string     `json:"os,omitempty"` // 保存时探测写入（如 ubuntu 22.04）
	// Folder 是斜杠路径目录（"生产/web"），空=根。仅侧栏 UI 组织用，
	// 不参与 Agent 目标解析（那是 Tags 的职责）。
	Folder string `json:"folder,omitempty"`
	// Owner 是主机属主（web 登录用户名）；空 = 归 admin（存量迁移行）。
	// 属主决定 web 端视角（ListFor）：普通用户只见自己的，admin 见全部。
	Owner string `json:"owner,omitempty"`
}

// HasTag 报告主机是否带某标签。
func (h *Host) HasTag(tag string) bool {
	for _, t := range h.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Endpoint 返回 "user@addr:port" 形式的连接摘要。
func (h *Host) Endpoint() string {
	return fmt.Sprintf("%s@%s:%d", h.User, h.Addr, h.Port)
}

// NewID 生成主机 ID。8 字节随机数的 hex 编码，创建时调用一次。
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return fmt.Sprintf("h%x", b)
}

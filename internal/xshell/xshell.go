// Package xshell 解析本机 XShell 会话目录（.xsh 文件）为 gengar-pro 主机清单
// 的导入数据。只读连接结构（Host/Port/UserName/UserKey），不触碰 Password
// 键——XShell 密码为专有加密，导入策略为结构导入、密码留空（设计文档 X1）。
package xshell

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Session 是一条可导入的 SSH 会话。
type Session struct {
	Name       string // 文件名去 .xsh
	Host, User string
	Port       int // INI 缺省 22
	AuthMethod string
	// KeyPath 不再从 UserKey 带入（评审 ①-5）：NetSarang 自有路径指向
	// 导入者本机文件，服务器侧不存在且构成「引用服务器本地私钥」面；
	// 保留字段仅为预览行结构兼容，恒为空串。导入后密钥认证的主机需
	// 上传私钥方可连通（与「密码不导入」同一提示位）。
	KeyPath string
	Tags    []string
	Source  string // 相对路径（预览展示与排错）
}

// Result 是一次目录扫描的汇总（容错：单文件失败不中断）。
type Result struct {
	Sessions []Session
	Skipped  int // 非 SSH 协议或 Host 为空
	Failed   int // 解析失败（编码损坏等）
}

// errSkip 表示该文件被有意跳过（区别于解析失败）。
var errSkip = errors.New("xshell: skip session")

// UniqueName 返回不与 used 冲突的名字：重名时追加 -2、-3…（导入改名用，
// XShell 子文件夹同名会话很常见，store 的 name 有 UNIQUE 约束）。
func UniqueName(base string, used map[string]bool) string {
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		c := fmt.Sprintf("%s-%d", base, i)
		if !used[c] {
			return c
		}
	}
}

// Scan 递归扫描 dir 下全部 .xsh（扩展名不区分大小写）。
func Scan(dir string) (*Result, error) {
	res := &Result{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".xsh") {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			rel = path
		}
		s, perr := parseFile(path)
		switch {
		case perr == nil:
			s.Source = filepath.ToSlash(rel)
			s.Tags = dirTags(rel)
			res.Sessions = append(res.Sessions, *s)
		case errors.Is(perr, errSkip):
			res.Skipped++
		default:
			res.Failed++ // 容错继续：单个坏文件不打断整批
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("xshell: scan %s: %w", dir, err)
	}
	sort.Slice(res.Sessions, func(i, j int) bool { return res.Sessions[i].Source < res.Sessions[j].Source })
	return res, nil
}

// dirTags 从相对路径取文件夹段做标签（根目录文件无标签）。
func dirTags(rel string) []string {
	parts := strings.Split(filepath.ToSlash(filepath.Dir(rel)), "/")
	tags := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "." && p != "" && !strings.ContainsAny(p, `/\`) {
			tags = append(tags, p)
		}
	}
	return tags
}

// File 是一次上传的单个 .xsh（Name 为浏览器侧的相对路径，Content 为原始字节）。
type File struct {
	Name    string
	Content []byte
}

// ParseFiles 解析上传文件列表（web 上传导入路径；目录扫描走 Scan）。
// 容错语义与 Scan 一致：单文件跳过/失败不打断整批。
func ParseFiles(files []File) (*Result, error) {
	res := &Result{}
	for _, f := range files {
		name := filepath.Base(f.Name)
		s, perr := parseSession(name, f.Content)
		switch {
		case perr == nil:
			s.Source = filepath.ToSlash(f.Name)
			s.Tags = dirTags(filepath.ToSlash(f.Name))
			res.Sessions = append(res.Sessions, *s)
		case errors.Is(perr, errSkip):
			res.Skipped++
		default:
			res.Failed++ // 容错继续：单个坏文件不打断整批
		}
	}
	sort.Slice(res.Sessions, func(i, j int) bool { return res.Sessions[i].Source < res.Sessions[j].Source })
	return res, nil
}

// parseFile 读取并解析单个 .xsh；被跳过返回 errSkip。
func parseFile(path string) (*Session, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseSession(filepath.Base(path), raw)
}

// parseSession 解析单个 .xsh 内容（name 为文件名，含扩展名）；被跳过返回 errSkip。
func parseSession(name string, raw []byte) (*Session, error) {
	ini := parseINI(decode(raw))
	conn := ini["CONNECTION"]
	auth := ini["CONNECTION:AUTHORIZATION"]
	proto, ok := conn["Protocol"]
	if !ok {
		// 连 Protocol 键都没有：不是有效的 XShell 会话文件（二进制/损坏内容），
		// 属解析失败而非有意跳过——两类计数在前端分别提示（设计 §3.3）。
		return nil, fmt.Errorf("xshell: %s: no [CONNECTION] Protocol key", name)
	}
	if !strings.EqualFold(strings.TrimSpace(proto), "SSH") {
		return nil, errSkip // Telnet/Serial 等与 gengar-pro 无关
	}
	host := strings.TrimSpace(conn["Host"])
	if host == "" {
		return nil, errSkip
	}
	if name == ".xsh" {
		return nil, errSkip // 空名会话：SaveHost 必拒且中断整批导入
	}
	port := 22
	if p, err := strconv.Atoi(strings.TrimSpace(conn["Port"])); err == nil && p > 0 && p <= 65535 {
		port = p
	}
	user := strings.TrimSpace(auth["UserName"])
	if user == "" {
		user = "root" // 与 HostForm 默认一致
	}
	s := &Session{
		Name: strings.TrimSuffix(name, filepath.Ext(name)),
		Host: host, Port: port, User: user,
		AuthMethod: "password",
	}
	if key := strings.TrimSpace(auth["UserKey"]); key != "" {
		// 标记 key 认证，但不带路径（见 Session.KeyPath 注释，评审 ①-5）
		s.AuthMethod = "key"
	}
	return s, nil
}

// decode 处理编码：BOM FF FE → UTF-16LE（unicode/utf16 标准库，无新依赖）；
// 无 BOM 按 UTF-8 容错（个别第三方工具生成的文件）。
func decode(b []byte) string {
	if len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE {
		u := make([]uint16, 0, len(b)/2)
		for i := 2; i+1 < len(b); i += 2 {
			u = append(u, uint16(b[i])|uint16(b[i+1])<<8)
		}
		return string(utf16.Decode(u))
	}
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		b = b[3:] // UTF-8 BOM：Windows 第三方工具常见形态
	}
	return string(b)
}

// parseINI 极简 INI 解析：节名大小写不敏感（XShell 实际为全大写），
// 键保留原样，忽略注释与空行。坏行静默忽略（会话文件字段容错优先）。
func parseINI(text string) map[string]map[string]string {
	out := map[string]map[string]string{}
	section := ""
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, ";") || strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			section = strings.ToUpper(strings.TrimSpace(t[1 : len(t)-1]))
			if out[section] == nil {
				out[section] = map[string]string{}
			}
			continue
		}
		if k, v, ok := strings.Cut(t, "="); ok && section != "" {
			out[section][strings.TrimSpace(k)] = v
		}
	}
	return out
}

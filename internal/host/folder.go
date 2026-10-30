package host

import "strings"

// NormalizeFolder 归一化目录路径：段内 trim、空段丢弃、斜杠统一单个、
// 首尾不留斜杠；空串表示根目录。Save 时统一调用（绑定层无需重复）。
func NormalizeFolder(s string) string {
	s = strings.ReplaceAll(s, "\\", "/") // Windows 习惯的反斜杠分隔符归一
	parts := strings.Split(s, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "/")
}

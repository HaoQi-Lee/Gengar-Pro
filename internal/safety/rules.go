package safety

import "regexp"

// cmdStart matches a command at the beginning or after shell operators (;, |, ||, &&).
// Also handles sudo/doas prefixes.
const cmdStart = `(?:^|;\s*|\|\s*|\|\|\s*|&&\s*)(?:(?:sudo|doas)\s+)?`

var (
	blacklistRegexps = []*regexp.Regexp{
		// 权限破坏：chmod 777/666 等危险模式到根目录或系统目录
		regexp.MustCompile(cmdStart + `chmod\s+(?:777|666|000)\s+/(?:\s|$)`),
		regexp.MustCompile(cmdStart + `chmod\s+-R\s+(?:777|666|000)\s+`),
		// 根目录删除：rm -rf / 或 rm -rf /* 或 rm -rf / --no-preserve-root
		regexp.MustCompile(cmdStart + `rm\s+-rf\s+/(?:\s|$|\*|--no-preserve)`),
		// 磁盘/分区破坏
		regexp.MustCompile(cmdStart + `dd\s+if=/dev/zero`),
		regexp.MustCompile(cmdStart + `mkfs\.\w+`),
		// fork bomb
		regexp.MustCompile(`:\(\)\s*\{.*:.*\|.*:.*&.*\}\s*;`),
		// 系统控制
		regexp.MustCompile(cmdStart + `reboot\b`),
		regexp.MustCompile(cmdStart + `shutdown\b`),
		regexp.MustCompile(cmdStart + `init\s+[06]\b`),
		regexp.MustCompile(cmdStart + `passwd\b`),
	}

	// dangerRegexps 保留供 UI/未来分级参考：破坏性但可恢复的操作（rm、mv、
	// kill -9、chmod、iptables -F）。分段判定下这些段必然不过白名单，
	// 无需再独立驱动 Risky——但 MatchDangerous 语义保留给确认弹窗摘要。
	dangerousRegexps = []*regexp.Regexp{
		regexp.MustCompile(cmdStart + `rm\s+-[rf]`),
		regexp.MustCompile(cmdStart + `rm\s+-r\b`),
		regexp.MustCompile(cmdStart + `mv\s+\S`),
		regexp.MustCompile(cmdStart + `kill\s+-9\b`),
		regexp.MustCompile(cmdStart + `chmod\s+\S`),
		regexp.MustCompile(cmdStart + `iptables\s+-F\b`),
	}

	// 白名单有意不含：env（全量环境变量泄密）、curl/wget（外带数据/管道
	// 执行面）——这三类移出免确认名单（评审 ①-3）；cat 也只读，但重定向
	// `echo p > /etc/passwd` 的写段 `p` 不在白名单，分段判定已覆盖。
	whitelistRegexps = []*regexp.Regexp{
		regexp.MustCompile(`^ls\b`),
		regexp.MustCompile(`^ps\b`),
		regexp.MustCompile(`^df\b`),
		regexp.MustCompile(`^free\b`),
		regexp.MustCompile(`^cat\b`),
		regexp.MustCompile(`^systemctl\s+status\b`),
		regexp.MustCompile(`^docker\s+ps\b`),
		regexp.MustCompile(`^netstat\b`),
		regexp.MustCompile(`^journalctl\b`),
		regexp.MustCompile(`^ss\b`),
		regexp.MustCompile(`^grep\b`),
		regexp.MustCompile(`^find\b`),
		regexp.MustCompile(`^tail\b`),
		regexp.MustCompile(`^head\b`),
		regexp.MustCompile(`^wc\b`),
		regexp.MustCompile(`^du\b`),
		regexp.MustCompile(`^uname\b`),
		regexp.MustCompile(`^uptime\b`),
		regexp.MustCompile(`^whoami\b`),
		regexp.MustCompile(`^id\b`),
		regexp.MustCompile(`^pwd\b`),
		regexp.MustCompile(`^which\b`),
		regexp.MustCompile(`^echo\b`),
		regexp.MustCompile(`^stat\b`),
		regexp.MustCompile(`^file\b`),
		regexp.MustCompile(`^tree\b`),
	}
)

var protectedPaths = []string{
	"/etc/shadow",
	"/etc/passwd",
	"/etc/ssh/",
	"/root/.ssh/",
}
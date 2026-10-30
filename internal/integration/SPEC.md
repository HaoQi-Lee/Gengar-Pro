---
psy_kind: factual
psy_version: 1
package: internal/integration
---

# internal/integration

## 概述

`internal/integration` 是测试设施包（`go:build integration`，默认构建不含）：在 Docker 里起真实 sshd 容器，端到端验证 SSHExecutor 与 Registry fan-out 的行为——不经 LLM，确定性断言。

## 文件

- `integration_test.go` — 三个集成测试：SSHExecutor 三方法与 0644 权限断言、Registry 标签 fan-out 聚合、Core Names/Resolve 真连接对称性；含容器构建/启动/清理辅助
- `sshd.Dockerfile` — 最小 sshd 目标机镜像（alpine:3.20 + openssh，root 密码认证）

## API

无导出符号（仅测试）。

运行：`go test -tags integration ./internal/integration/ -v`；Docker 不可用（CLI 缺失或守护进程未运行）时 Skip 而非 Fail。

## 依赖

**内部包：** `appcore`、`executor`、`host`、`plugin`

**外部工具：** docker CLI（构建/运行容器）

**标准库：** `context`、`net`、`os/exec`、`path/filepath`、`strconv`、`strings`、`testing`、`time`

## 设计重点

### 隔离 TOFU 库

容器主机密钥每次运行都变，测试经 `executor.SetKnownHostsPath` 把 known_hosts 重定向到 `t.TempDir()`，不污染用户真实 `~/.gengar-pro/known_hosts`。

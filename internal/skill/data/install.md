---
name: install
command: /install
aliases:
  - /inst
  - /pkg
description: 软件安装 — 按发行版选包管理器、适配源与代理、安装后验证并按需起服务
---
# 软件安装 Playbook

安装指定软件：按发行版选对包管理器、按网络环境配好源与 HTTP 代理、处理异常、装完验证、必要时起服务并确认。所有写操作（改源、设代理、安装、enable/start）落地前必须用 ask_user 确认；命令级风险另由安全门批量确认。apt/yum/dnf 优先用 package_install，pacman/apk/zypper 用 execute_command 回退。探测类只读可自由执行。汇总仅对话输出（Markdown 格式），不写文件、不 memory_write。若注入末尾有「用户通过命令传入的参数」即为目标包；否则第一步用 ask_user 询问要安装什么。

## 1. 前置探测
- system_info 取发行版 ID / 版本 / 内核。
- 判定包管理器家族：apt（debian/ubuntu/mint/kali…）、dnf/yum（rhel/centos/rocky/almalinux/fedora/kylin/openeuler/uos…）、pacman（arch）、apk（alpine）、zypper（suse）。apt/yum/dnf 家族安装时委托 package_install；pacman/apk/zypper 标记「需 execute_command 回退」。
- 读当前源配置：apt 查 /etc/apt/sources.list 与 sources.list.d；yum/dnf 查 /etc/yum.repos.d/*.repo；pacman 查 /etc/pacman.d/mirrorlist；apk 查 /etc/apk/repositories；zypper 执行 zypper lr。
- 读代理环境：http_proxy/https_proxy/HTTP_PROXY/HTTPS_PROXY/no_proxy；yum 的 /etc/yum.conf 的 proxy=；apt 的 /etc/apt/apt.conf.d/。
- 探默认源连通性：http_check 默认镜像 URL + ping + dns_lookup，判定「直连可用 / 慢 / 被墙 / 不可达」。

## 2. 环境适配
- 直连可用 → 不动源，进步骤 3。
- 慢 / 被墙 / 不可达 → 按发行版选国内镜像（默认阿里云；Arch/Alpine/openSUSE 用 TUNA）：apt→mirrors.aliyun.com/{debian,ubuntu}；CentOS 7(EOL)→vault.centos.org（注释 mirrorlist、启用 baseurl，覆盖 base/extras/updates，已知阻塞点重点处理）；Rocky/Alma→mirrors.aliyun.com/{rockylinux,almalinux}；Fedora→mirrors.aliyun.com/fedora；pacman→mirrors.tuna.tsinghua.edu.cn/archlinux；apk→mirrors.tuna.tsinghua.edu.cn/alpine；zypper→mirrors.tuna.tsinghua.edu.cn/opensuse。
- 代理：检测到代理环境/配置则确认包管理器走代理；无代理但被墙，提示用户提供代理 URL，不擅自编造。
- 汇总「选定的源 + 代理」方案，与安装计划一并确认。

## 3. 确认安装计划
- package_search 校验包名存在（不存在先告警）。
- ask_user 展示：待装包、检测到的包管理器、将要落地的换源/代理改动、是否预期起服务。等用户 OK；拒绝则终止，仅输出已探测信息。

## 4. 执行安装
- 先落地已确认的换源/代理：apt 用 write_file 改 sources 或 execute_command sed；yum/dnf 改 .repo；pacman 改 mirrorlist；apk/zypper 改对应配置。改完刷新索引：apt update / makecache / pacman -Sy。
- 安装：apt/yum/dnf→package_install；pacman→execute_command "pacman -Sy --noconfirm --needed <pkg>"；apk→execute_command "apk add <pkg>"；zypper→execute_command "zypper --non-interactive install <pkg>"。
- 异常处置：dpkg/rpm 锁占用→等待 + 有限重试(≤3)，不删锁；repomd 404 / 源找不到→回退备用镜像重试，CentOS 7 直切 vault；NOKEY / GPG 失败→execute_command 导入 key 后重试；非 root→ask_user 是否 sudo；磁盘满→停止并 check_disk 报告；包名错→package_search 校正；依赖冲突→报告并 ask_user，默认不动；非交互→一律带 -y/--noconfirm/--non-interactive。回退重试一次仍失败则停止报错。

## 5. 安装后测试
- execute_command "command -v <bin>" 验证二进制在 PATH（精简镜像无 which，用 command -v）。
- execute_command "<bin> --version" 取版本。
- 库类（无二进制）：package_list 确认已装 + 版本，或 grep_files 找 .so。
- 服务类：execute_command "systemctl cat <svc>" 或 "systemctl list-unit-files | grep <svc>" 查 unit 是否存在。

## 6. 服务启动
- 仅当装的是 systemd 服务且用户在步骤 3 同意起服务：
  - systemctl_start <svc>（已在跑则 systemctl_restart）。
  - 开机自启：execute_command "systemctl enable <svc>"（systemd 插件无 systemctl_enable 工具）。
  - systemctl_status <svc> 验证 active(running)；失败则 journalctl_logs 取最近 50 行定位。
  - 该监听端口的（httpd→80、nginx→80、docker→2375）用 check_port 验证。

## 7. 汇总
- Markdown 表格列：包名 / 版本 / 包管理器 / 来源镜像 / 代理 / 安装状态 / 服务状态(端口)；要点与后续项用列表，命令片段放 ``` 代码块。
- 列出遇到的异常及处置。
- 后续项：需 reboot、需 systemctl daemon-reload、需手动放行防火墙端口等。
- 仅对话输出，不写文件、不 memory_write。

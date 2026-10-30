---
name: inspect
command: /inspect
aliases:
  - /inspection
  - /healthcheck
description: 服务器巡检 — 系统性健康检查
---
# 服务器巡检 Playbook

对本机做一次系统性健康巡检。所有操作只读，禁止任何修改。逐项检查后，用 Markdown 汇总：## 标题分节、要点用列表、多主机/多维度对比用 Markdown 表格（| 列 | 列 |）。

## 1. 资源水位
- system_info：采集 OS、内核版本、CPU 核数、运行时长。
- check_disk：列出各挂载点使用率，超过 85% 的分区标记「警告」。
- check_memory：查内存与 Swap 使用率，超过 90% 标记「警告」。
- 用 execute_command 执行 uptime，读取 load average；持续超过 CPU 核数视为「警告」。

## 2. 服务健康
- 确定关键服务清单：默认包含 sshd、cron；若探测到已安装 nginx 或 docker，也纳入。
- 对每个服务调用 systemctl_status，非 active(running) 标记「异常」。
- 调用 journalctl_logs 取最近 50 行，提取含 ERROR、Failed、panic 的条目并汇总出现次数。

## 3. 网络连通
- check_port：检查关键端口（如 22、80、443），应监听却未监听标记「异常」。
- check_network：检查默认网关连通性。
- 用 execute_command 查看网络接口（ip -s link 或 ifconfig），统计 error/drop 计数不为 0 的接口。

## 4. 安全审计
- 用 execute_command 统计近期失败登录（lastb 或 journalctl 中 sshd 的 Failed password），次数过高标记「警告」。
- 防火墙状态：执行 firewall-cmd --state 或 iptables -L 或 ufw status；完全未启用标记「警告」。
- 待重启更新：检查 /var/run/reboot-required 或执行 needs-restarting -r；存在待重启项标记「警告」。
- 时间同步：执行 timedatectl 或 chronyc tracking；未同步标记「警告」。

## 输出格式
- 用 Markdown：## 标题分节、"- " 列表列要点、对比数据用 Markdown 表格；命令片段放 ``` 代码块。
- 每项标注「正常」「警告」或「异常」，并附关键数值。
- 末尾给出总体结论，并列出建议优先处理的 Top 问题（按严重度排序，有序列表）。
- 仅在对话输出，不要写文件、不要调用 memory_write。

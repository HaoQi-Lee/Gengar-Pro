package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"gengar-pro/internal/executor"
)

// DockerManager 提供容器运维工具。全部工具（docker_exec 的 command 除外）
// 用 argv 形式执行：模型可控的 container/path 参数不经 shell 重新解释，
// `nginx; reboot` 只会得到 cli 参数错误而非执行注入（评审 ①-4）。
// docker_exec 的 command 本就是任意命令语义且 RiskLevel=1 需确认，
// 保留 shell 通道；container 名仍 argv 化。
type DockerManager struct{}

func NewDockerManager() *DockerManager { return &DockerManager{} }

func (dm *DockerManager) Tools() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        "docker_ps",
			Description: "List Docker containers (running by default, all with all=true).\nUse when: checking which services are up; finding a container ID/name; verifying after docker_compose_up.\nReturns: docker ps table with container IDs, images, status, ports. Prefer over execute_command + docker ps.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"all":{"type":"boolean","description":"Show all containers including stopped"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}}}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "docker_logs",
			Description: "View the last N lines of a Docker container's logs.\nUse when: debugging why a container crashed; checking service output; verifying a deploy produced expected logs.\nReturns: the container's stdout+stderr (last N lines). Prefer over execute_command + docker logs.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"container":{"type":"string","description":"Container name or ID"},"lines":{"type":"integer","description":"Number of lines to show","default":100},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["container"]}`),
			RiskLevel:   0,
			Targeting:   true,
		},
		{
			Name:        "docker_compose_up",
			Description: "Start services defined in docker-compose.yml in detached mode.\nUse when: bringing up a stack for the first time; restarting a stack after config changes.\nReturns: docker compose up output. Prefer over execute_command + docker compose up -d.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path to docker-compose.yml file or its directory"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}}}`),
			RiskLevel:   1,
			Targeting:   true,
		},
		{
			Name:        "docker_compose_down",
			Description: "Stop and remove services started by docker compose, including networks.\nUse when: tearing down a stack; freeing resources; before a clean re-deploy.\nReturns: docker compose down output. Prefer over execute_command + docker compose down.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"path":{"type":"string","description":"Path to docker-compose.yml file or its directory"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}}}`),
			RiskLevel:   1,
			Targeting:   true,
		},
		{
			Name:        "docker_exec",
			Description: "Run a command inside a running Docker container (like docker exec).\nUse when: inspecting state inside a container; running migrations; quick debug commands.\nReturns: combined stdout+stderr from the in-container command. Prefer over execute_command + docker exec.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"container":{"type":"string","description":"Container name or ID"},"command":{"type":"string","description":"Command to execute"},"hosts":{"type":"array","items":{"type":"string"},"description":"Target hosts: host names, tag/group names, or 'all'. Omit to use the default (pinned) targets."}},"required":["container","command"]}`),
			RiskLevel:   1,
			Targeting:   true,
		},
	}
}

func (dm *DockerManager) Execute(ctx context.Context, toolName string, input json.RawMessage, exec executor.Executor) (ToolResult, error) {
	switch toolName {
	case "docker_ps":
		var p struct {
			All bool `json:"all"`
		}
		json.Unmarshal(input, &p)
		args := []string{"ps"}
		if p.All {
			args = append(args, "-a")
		}
		return dm.exec(ctx, exec, "docker", args...)
	case "docker_logs":
		var p struct {
			Container string `json:"container"`
			Lines     int    `json:"lines"`
		}
		json.Unmarshal(input, &p)
		if p.Lines <= 0 {
			p.Lines = 100 // 负数/零一律回落默认（评审 ①-4 参数健壮性）
		}
		return dm.exec(ctx, exec, "docker", "logs", "--tail", strconv.Itoa(p.Lines), "--", p.Container)
	case "docker_compose_up":
		var p struct {
			Path string `json:"path"`
		}
		json.Unmarshal(input, &p)
		args := composeFileArgs(p.Path)
		args = append(args, "up", "-d")
		return dm.exec(ctx, exec, "docker", append([]string{"compose"}, args...)...)
	case "docker_compose_down":
		var p struct {
			Path string `json:"path"`
		}
		json.Unmarshal(input, &p)
		args := composeFileArgs(p.Path)
		args = append(args, "down")
		return dm.exec(ctx, exec, "docker", append([]string{"compose"}, args...)...)
	case "docker_exec":
		var p struct {
			Container string `json:"container"`
			Command   string `json:"command"`
		}
		json.Unmarshal(input, &p)
		// command 是任意 shell 语义（本就 Risky=1 需确认）；container 名仍
		// argv 化防「container 注入 + 确认弹窗被批准」的组合面。
		return dm.exec(ctx, exec, "docker", "exec", "--", p.Container, "sh", "-c", p.Command)
	default:
		return ToolResult{}, fmt.Errorf("DockerManager: unknown tool %q", toolName)
	}
}

// composeFileArgs 把 path 变成 compose 的 -f 参数序列：空 path 返回空
// （compose 在 cwd 找 docker-compose.yml，与旧行为一致）；非空时直接
// 传原值——`docker compose -f` 同时接受文件与目录（目录内找 compose.yaml/
// docker-compose.yml），取代旧的 `cd path && docker-compose` shell 拼接。
func composeFileArgs(path string) []string {
	if path == "" {
		return nil
	}
	return []string{"-f", path}
}

func (dm *DockerManager) exec(ctx context.Context, exec executor.Executor, name string, args ...string) (ToolResult, error) {
	output, err := exec.RunArgv(ctx, name, args...)
	if err != nil {
		return ToolResult{Content: fmt.Sprintf("Error: %v\n%s", err, output), IsError: true}, nil
	}
	return ToolResult{Content: string(output)}, nil
}

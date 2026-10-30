// Package memory 提供 Gengar 的服务器记忆持久化能力（按主机分域）。
//
// 数据以 JSON 格式存储在 ~/.gengar-pro/memory/<host>.json，每个受管主机
// 一个文件，包含两个分类：
//   - system_info: 服务器基本信息（OS、内核、CPU、内存、磁盘）
//   - history: Agent 执行过的关键操作记录
package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Memory 表示单个主机的完整记忆数据。
type Memory struct {
	SystemInfo *Category `json:"system_info,omitempty"`
	History    *Category `json:"history,omitempty"`
	path       string    // 由 Store 注入；Save 写回该文件
}

// Category 表示一个分类的数据 + 最后更新时间戳。
// Data 用 interface{} 保留 JSON 原始结构（map 或 slice）。
type Category struct {
	UpdatedAt time.Time   `json:"updated_at"`
	Data      interface{} `json:"data"`
}

// Store 按主机分域管理记忆文件：<dir>/<host>.json。
// host 键为主机名（清单里 Name 唯一）；重命名主机视为新机器从零记忆，
// 旧文件保留不迁移（alpha 明确接受，见设计文档第 8 节）。
//
// Get 每次返回从磁盘新读出的独立实例：同主机连续两轮写入不共享内存态，
// 每轮 Get→Write→Save 是一次完整的读改写，并发下 last-write-wins。
// 对 alpha 的 read→append→write 用法够用。
type Store struct {
	dir string
	mu  sync.Mutex
}

// OpenStore 打开默认记忆目录 ~/.gengar-pro/memory。
func OpenStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}
	return OpenStoreDir(filepath.Join(home, ".gengar-pro", "memory"))
}

// OpenStoreDir 供测试与 appcore 指定数据目录。
func OpenStoreDir(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir 返回记忆数据目录。
func (s *Store) Dir() string { return s.dir }

// Get 返回主机记忆；文件不存在或损坏时返回空 Memory（不视为错误，
// 与旧顶层 Load 的降级语义一致）。同主机多次 Get 返回独立实例，
// 调用方负责 Save 落盘。
func (s *Store) Get(host string) *Memory {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.hostPath(host)
	raw, err := os.ReadFile(path)
	if err != nil {
		return &Memory{path: path}
	}
	var m Memory
	if json.Unmarshal(raw, &m) != nil {
		return &Memory{path: path}
	}
	m.path = path
	return &m
}

// OpenStoreForTest 供跨包测试构造临时 Store（忽略错误：dir 已由调用方创建）。
func OpenStoreForTest(dir string) *Store {
	s, _ := OpenStoreDir(dir)
	return s
}

// windowsDeviceNames 是 Windows 设备别名清单。带扩展名的形式（如 CON.json）
// 在 Windows 10 及更早仍解析到设备：实测 WriteFile 报成功但文件不落盘，
// 记忆会静默丢失（Win11 22H2+ 才放宽）。别名按首个 '.' 前的词、不区分
// 大小写匹配，CON.x 同样命中。
var windowsDeviceNames = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true,
	"COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true,
	"LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// hostPath 清洗主机名：仅允许字母数字、- _ .，其余替换为 _；
// 清洗结果命中 Windows 设备别名时加前缀 _（跨平台统一规则，文件名可预期）。
// 主机名来自受控清单，这层是防御性兜底（防目录穿越与设备别名）。
func (s *Store) hostPath(host string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '_'
		}
	}, host)
	stem := strings.ToUpper(strings.SplitN(safe, ".", 2)[0])
	if windowsDeviceNames[stem] {
		safe = "_" + safe
	}
	return filepath.Join(s.dir, safe+".json")
}

// Save 写回 Store 注入的路径。path 为空（直接构造的 Memory）时返回错误。
func (m *Memory) Save() error {
	if m.path == "" {
		return fmt.Errorf("memory has no backing path; obtain it via Store.Get")
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal memory: %w", err)
	}
	if err := os.WriteFile(m.path, raw, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", m.path, err)
	}
	return nil
}

// Write 写入或更新指定分类的数据，自动刷新 UpdatedAt。
// data 可以是 map[string]interface{}（如 system_info）
// 或 []interface{}（如 history 的记录列表）。
// 未知分类返回错误。
func (m *Memory) Write(category string, data interface{}) error {
	cat := &Category{
		UpdatedAt: time.Now().UTC(),
		Data:      data,
	}
	switch category {
	case "system_info":
		m.SystemInfo = cat
	case "history":
		m.History = cat
	default:
		return fmt.Errorf("unknown category %q (available: system_info, history)", category)
	}
	return nil
}

// Read 读取指定分类。category 取值：
//   - "system_info" / "history": 返回 {category, updated_at, data}，
//     未写入过则返回错误。
//   - "all": 返回 {<cat>: {updated_at, data}, ...}，空 Memory 时返回空 map。
func (m *Memory) Read(category string) (interface{}, error) {
	switch category {
	case "system_info":
		if m.SystemInfo == nil {
			return nil, fmt.Errorf("system_info has no data yet")
		}
		return categoryView("system_info", m.SystemInfo), nil
	case "history":
		if m.History == nil {
			return nil, fmt.Errorf("history has no data yet")
		}
		return categoryView("history", m.History), nil
	case "all":
		out := map[string]interface{}{}
		if m.SystemInfo != nil {
			out["system_info"] = innerView(m.SystemInfo)
		}
		if m.History != nil {
			out["history"] = innerView(m.History)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unknown category %q (available: system_info, history, all)", category)
	}
}

func categoryView(name string, c *Category) map[string]interface{} {
	return map[string]interface{}{
		"category":   name,
		"updated_at": c.UpdatedAt.Format(time.RFC3339),
		"data":       c.Data,
	}
}

func innerView(c *Category) map[string]interface{} {
	return map[string]interface{}{
		"updated_at": c.UpdatedAt.Format(time.RFC3339),
		"data":       c.Data,
	}
}

// Delete 删除指定分类的数据。
//   - "system_info" / "history": 仅清空对应字段，调用方负责 Save()。
//   - "all": 同时清空 Memory 字段并删除磁盘上的 <host>.json；
//     文件不存在时不报错。
func (m *Memory) Delete(category string) error {
	switch category {
	case "system_info":
		m.SystemInfo = nil
	case "history":
		m.History = nil
	case "all":
		m.SystemInfo = nil
		m.History = nil
		if err := os.Remove(m.path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", m.path, err)
		}
	default:
		return fmt.Errorf("unknown category %q (available: system_info, history, all)", category)
	}
	return nil
}

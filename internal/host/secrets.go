package host

import (
	"errors"
	"fmt"
	"strings"
)

// ErrSecretNotFound 表示引用在 SecretStore 中不存在（密码/口令尚未保存）。
var ErrSecretNotFound = errors.New("secret not found")

// SecretStore 保管登录秘密（密码、私钥口令）。引用（ref）是清单里
// AuthConfig 存的字符串键；秘密本体由实现决定去处（产品路径是
// EncryptedStore 加密入库，测试/自动化用 MemoryStore）。
type SecretStore interface {
	Set(ref, secret string) error
	Get(ref string) (string, error)
	Delete(ref string) error
}

// MemoryStore 是 SecretStore 的内存实现，供测试注入与 headless 内联秘密
// （05 模块 ParseEnvSecrets 在运行时使用）；非并发安全，注入须在使用前完成。
// 零值不可用（值接收者 Set 无法自初始化 nil map），须以 MemoryStore{} 或
// 字面量初始化后使用。
type MemoryStore map[string]string

func (m MemoryStore) Set(ref, secret string) error { m[ref] = secret; return nil }

func (m MemoryStore) Get(ref string) (string, error) {
	v, ok := m[ref]
	if !ok {
		return "", fmt.Errorf("get secret %q: %w", ref, ErrSecretNotFound)
	}
	return v, nil
}

func (m MemoryStore) Delete(ref string) error { delete(m, ref); return nil }

// ParseEnvSecrets 把 "ref1=pass1,ref2=pass2" 形式的字符串解析为内存
// SecretStore。headless 的 --secrets / GENGAR_SECRETS 使用，让集成测试
// 与自动化场景不碰真实系统钥匙串。
//
// 语法限制：按逗号分隔、首个 "=" 分键值，因此值不能含逗号（会在此被
// 拆断）；键与值的首尾空格会被修剪；首个 "=" 之后的 "=" 原样保留在
// 值中。无 "=" 的片段整段跳过。
func ParseEnvSecrets(s string) SecretStore {
	m := MemoryStore{}
	for _, pair := range strings.Split(s, ",") {
		kv := strings.SplitN(pair, "=", 2)
		if len(kv) == 2 {
			m[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		}
	}
	return m
}

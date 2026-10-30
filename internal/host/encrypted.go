// EncryptedStore 是 SecretStore 的服务端加密实现（web 部署没有系统钥匙串）：
// SSH 密码/私钥口令用 AES-256-GCM 加密后存 Store 的 secrets 表，主密钥
// 来自 keyfile（0600，首次自动生成）或 hex 环境变量。ref 本身不加密
// （hosts 表本来就持有它，非秘密）。
package host

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
)

type EncryptedStore struct {
	store *Store
	aead  cipher.AEAD
}

// OpenEncrypted 用 32 字节主密钥构建。密文布局：nonce(12) || ciphertext+tag，
// AAD=ref（密文行被复制到别的 ref 后认证标签不匹配，防错位重放）。
func OpenEncrypted(store *Store, masterKey []byte) (*EncryptedStore, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes, got %d", len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &EncryptedStore{store: store, aead: aead}, nil
}

// LoadOrCreateMasterKey 解析主密钥：envHex 非空时优先（hex 编码 32 字节，
// 容器部署友好）；否则读 path，不存在则生成随机 32 字节并以 0600 落盘。
func LoadOrCreateMasterKey(path, envHex string) ([]byte, error) {
	if envHex != "" {
		k, err := hex.DecodeString(envHex)
		if err != nil {
			return nil, fmt.Errorf("decode master key env: %w", err)
		}
		if len(k) != 32 {
			return nil, fmt.Errorf("master key env must be 32 bytes hex, got %d bytes", len(k))
		}
		return k, nil
	}
	if b, err := os.ReadFile(path); err != nil {
		// 读错误必须精确区分：只有"文件不存在"才允许走生成新钥匙分支。
		// 若 keyfile 存在但不可读（权限拒绝/路径是目录等）却误当不存在，
		// 会重生成钥匙覆盖旧文件，既有密文将永久无法解密。
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("read master key %s: %w", path, err)
		}
	} else if len(b) != 32 {
		return nil, fmt.Errorf("master key file %s: want 32 bytes, got %d", path, len(b))
	} else {
		return b, nil
	}
	k := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, k); err != nil {
		return nil, err
	}
	// O_CREATE|O_EXCL 原子创建：两个并发首跑不互相覆盖；写一半崩溃留下的
	// 部分文件会因长度校验在下次启动报错，不会被当有效钥匙读。
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("create master key %s: already exists (concurrent first run?)", path)
		}
		return nil, fmt.Errorf("create master key %s: %w", path, err)
	}
	if _, err := f.Write(k); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("write master key %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close master key %s: %w", path, err)
	}
	return k, nil
}

func (e *EncryptedStore) Set(ref, secret string) error {
	nonce := make([]byte, e.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	ct := e.aead.Seal(nonce, nonce, []byte(secret), []byte(ref))
	return e.store.SetSecretBlob(ref, ct)
}

// Get 归一不存在与解密失败：前者 ErrSecretNotFound（上层提示补录密码），
// 后者原样上抛（换钥匙/库损坏要显性暴露，不能伪装成"没存过"）。
// 截断密文（< nonce 长度）同样按解密失败处理，不 panic。
func (e *EncryptedStore) Get(ref string) (string, error) {
	ct, err := e.store.GetSecretBlob(ref)
	if err != nil {
		return "", err
	}
	if ct == nil {
		return "", fmt.Errorf("get secret %q: %w", ref, ErrSecretNotFound)
	}
	ns := e.aead.NonceSize()
	if len(ct) <= ns {
		return "", fmt.Errorf("decrypt secret %q: ciphertext too short (%d bytes)", ref, len(ct))
	}
	pt, err := e.aead.Open(nil, ct[:ns], ct[ns:], []byte(ref))
	if err != nil {
		return "", fmt.Errorf("decrypt secret %q: %w", ref, err)
	}
	return string(pt), nil
}

func (e *EncryptedStore) Delete(ref string) error {
	return e.store.DeleteSecretBlob(ref)
}

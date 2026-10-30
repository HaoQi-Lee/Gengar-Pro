---
psy_kind: factual
psy_version: 1
package: internal/host
---

# internal/host

## 概述

`internal/host` 提供 gengar-pro 的受管服务器清单：数据模型（`Host`/`AuthConfig`）、目标引用解析（主机名/标签/all）、SQLite 持久化存储（清单/设置/用户/秘密密文四张表）、登录凭据的服务端加密存储（`EncryptedStore`）与 web 登录账号（users 表）。清单只存在于控制端，远端服务器零部署。主机按属主（`hosts.owner`，空=admin）隔离：`ListFor` 给出按用户视角的清单投影。

## 文件

- `host.go` — `Host`/`AuthConfig`/`AuthMethod` 数据模型、`NewID`、`HasTag`/`Endpoint` 辅助方法
- `folder.go` — `NormalizeFolder`：目录路径规范化（段 trim、空段丢弃、`\` 归一为 `/`、单斜杠、首尾无斜杠；空=根）
- `resolve.go` — `Resolve`：把目标引用列表（主机名 / `all` / 标签）解析为具体主机集合
- `secrets.go` — `SecretStore` 接口与内存实现 `MemoryStore` 及 `ParseEnvSecrets`
- `encrypted.go` — `EncryptedStore`（AES-256-GCM 加密入库）与 `LoadOrCreateMasterKey`（keyfile/env 双来源主密钥）
- `store.go` — `Store`：主机清单、设置 KV、web 用户、秘密密文的 SQLite 持久化（纯 Go 驱动，无 CGO；含 folder/users.role/owner 列的幂等 ALTER 迁移；`ListFor` 视角查询与 `ReassignHosts` 属主划转）
- `folder_test.go` / `resolve_test.go` / `secrets_test.go` / `store_test.go` / `store_users_test.go` / `encrypted_test.go` — 对应单元测试（加密含往返/错钥匙/截断密文/AAD 绑定/主密钥文件）

## API

- `type AuthMethod string` — 认证方式枚举：`AuthKey`（私钥文件）、`AuthPassword`（密码）
- `type AuthConfig struct` — 登录配置；秘密本体不直接入库，只存引用（`PasswordRef`/`KeyPassphraseRef`，本体由 SecretStore 实现保管）
- `type Host struct` — 一台受管服务器（ID 创建后不变；Name 全局唯一可改，记忆按主机名分域；Tags 即机组；Folder 为斜杠路径目录、空=根，仅侧栏 UI 组织用、不参与目标解析；Owner 为属主用户名、空=admin 归属——web 端 `ListFor` 视角的过滤键）
- `NormalizeFolder(s string) string` — 目录路径规范化（见文件说明）；`Store.Save` 前统一调用，绑定层无需重复
- `(h *Host) HasTag(tag string) bool` — 报告主机是否带某标签
- `(h *Host) Endpoint() string` — 返回 `user@addr:port` 连接摘要
- `NewID() string` — 生成主机 ID（8 字节随机数 hex，`h` 前缀）
- `Resolve(hosts []*Host, refs []string) (matched []*Host, unresolved []string)` — 按引用匹配主机（精确名优先 → `all` → 标签），按命中顺序去重；无法解析的引用收集在第二返回值，不报错中断
- `ErrSecretNotFound` — 哨兵错误：引用在 SecretStore 中不存在
- `type SecretStore interface` — `Set(ref, secret)` / `Get(ref)` / `Delete(ref)` 三方法的秘密存取接口
- `type MemoryStore map[string]string` — 内存实现（测试注入与 headless 内联秘密）；非并发安全，零值不可用，须以字面量初始化
- `ParseEnvSecrets(s string) SecretStore` — 把 `ref1=pass1,ref2=pass2` 解析为 `MemoryStore`（值不能含逗号，首个 `=` 分键值，无 `=` 的片段跳过）
- `type EncryptedStore struct` — SecretStore 的服务端加密实现（Web 部署无系统钥匙串）：秘密 AES-256-GCM 加密后存 `secrets` 表；密文布局 `nonce(12) || ciphertext+tag`，AAD=ref
- `OpenEncrypted(store *Store, masterKey []byte) (*EncryptedStore, error)` — 用 32 字节主密钥构建；`Get` 把「不存在」归一为 `ErrSecretNotFound`、解密失败原样上抛（换钥匙/库损坏要显性暴露）
- `LoadOrCreateMasterKey(path, envHex string) ([]byte, error)` — 主密钥解析：envHex 非空优先（hex 32 字节，容器部署友好）；否则读 path，不存在则生成随机 32 字节以 0600 落盘（O_EXCL 原子创建）
- `type Store struct` — SQLite 持久化（`hosts` 清单表 + `settings` KV 表 + `users` 用户表 + `secrets` 密文表）
- `OpenStore(path string) (*Store, error)` — 打开/初始化数据库；单连接池（见设计重点）
- `(s *Store) Close() error` — 关闭数据库
- `(s *Store) List() ([]*Host, error)` — 全量主机（不按属主过滤——Core/headless 全局语义），按 Name 排序
- `(s *Store) ListFor(owner string, isAdmin bool) ([]*Host, error)` — 视角查询：普通用户只返回 `owner = username` 的行；admin 返回全表。空 owner 行归一为 admin 归属（仅 admin 视角可见，存量迁行语义）
- `(s *Store) ReassignHosts(from, to string) error` — 把 from 名下全部主机划给 to（删除用户时移交 admin 用）
- `(s *Store) Save(h *Host) error` — 按 ID upsert（owner 一并 upsert——编辑须带原值防归属漂移）；Name 有表级 UNIQUE 约束，重名报错
- `(s *Store) Delete(id string) error` — 按 ID 删除
- `(s *Store) SetSetting(key, value string) error` — 设置 KV upsert
- `(s *Store) GetSetting(key string) (string, error)` — 读设置；键不存在返回 `("", nil)`
- `ErrNoSuchUser` — 哨兵错误：用户名不存在（登录失败判定用）
- `PwHashCost` — 登录密码 bcrypt 成本（=12；webapi 的 dummy-hash 均衡与 `gengar-server --add-user` 同源引用，改动其一即破坏时序均衡）
- `type User struct` — web 登录账号（Username/PwHash bcrypt 散列/CreatedAt/Role——`"admin"` 或空=普通用户；明文密码不落库）
- `(s *Store) CreateUser(u User) error` — 创建用户（重复用户名报错）
- `(s *Store) GetUser(username string) (*User, error)` — 查用户；不存在返回 `ErrNoSuchUser`（`errors.Is` 判定）
- `(s *Store) ListUsers() ([]*User, error)` — 全部用户，按用户名排序（用户管理列表用）
- `(s *Store) UpdateUserPassword(username, pwHash string) error` — 重置密码哈希；不存在返回 `ErrNoSuchUser`
- `(s *Store) SetUserRole(username, role string) error` — 设置角色；不存在返回 `ErrNoSuchUser`
- `(s *Store) DeleteUser(username string) error` — 删除用户；不存在返回 `ErrNoSuchUser`
- `(s *Store) CountUsers() (int, error)` — 用户数（首用户引导用）
- `(s *Store) SetSecretBlob(ref string, ct []byte) error` — 密文 upsert（EncryptedStore 的落盘行，本层不感知语义）
- `(s *Store) GetSecretBlob(ref string) ([]byte, error)` — 读密文；ref 不存在返回 `(nil, nil)`
- `(s *Store) DeleteSecretBlob(ref string) error` — 幂等删除密文

## 依赖

**内部包：** 无（被 `executor`/`appcore`/`webapi`/两条入口消费）

**第三方库：** `modernc.org/sqlite`（纯 Go SQLite 驱动）

**标准库：** `crypto/aes`、`crypto/cipher`、`crypto/rand`、`database/sql`、`encoding/hex`、`encoding/json`、`errors`、`fmt`、`io`、`os`、`strings`

## 设计重点

### 秘密加密入库（AES-GCM，AAD=ref）

密码与私钥口令在清单的 `AuthConfig` 里只存字符串引用（ref），本体由 `EncryptedStore` 以 AES-256-GCM 加密后存 `secrets` 表（ref 本身不加密——hosts 表本来就持有它，非秘密）。AAD 绑定 ref：密文行被复制到别的 ref 后认证标签不匹配，防错位重放。删除主机时由调用方（webapi deleteHost）顺带清理密文行。headless/测试场景用 `MemoryStore`/`ParseEnvSecrets` 整体替换后端，不碰真实密文库。

### 主密钥生命周期

`LoadOrCreateMasterKey` 的读错误精确区分：只有 `os.ErrNotExist` 才允许走生成新钥匙分支——keyfile 存在但不可读（权限拒绝/路径是目录）却误当不存在，会重生成钥匙覆盖旧文件，既有密文永久无法解密。首次生成用 `O_CREATE|O_EXCL` 原子创建：两个并发首跑不互相覆盖；写一半崩溃留下的部分文件会因长度校验在下次启动报错，不会被当有效钥匙读。

### 补列迁移（folder / users.role / hosts.owner）

schema 是 `CREATE TABLE IF NOT EXISTS`，不会给旧库加列：`OpenStore` 在建表后执行 `ALTER TABLE ... ADD COLUMN`（hosts.folder、users.role、hosts.owner），报 `duplicate column`（列已存在）则忽略——幂等；其他错误致命（走 Startup 的降级路径）。单连接池保证 ALTER 与后续写天然串行。hosts.owner 空值不在迁移时刷数据——读侧 `ListFor` 归一为 admin 归属（存量库平滑升级零风险）。

### 单连接 SQLite

`modernc.org/sqlite` 无默认 busy_timeout，而 `database/sql` 默认开无界连接池，多连接并发写会立刻报 `database is locked`。`OpenStore` 里 `SetMaxOpenConns(1)` 把单进程桌面场景的所有读写串行化，一行消除锁竞争。

### 解析不中断

`Resolve` 不因个别引用无法解析而报错——未解析项收集后返回，由调用方决定是回传模型（Registry 聚合末尾提示）还是直接失败。

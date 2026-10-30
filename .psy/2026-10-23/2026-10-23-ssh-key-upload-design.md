# SSH 私钥上传（替代路径填写） 设计

日期：2026-10-23 · 分支：feature/web-system

## 背景与目标

密钥认证目前靠手填服务器本地路径（`~/.ssh/id_ed25519`），要求用户先把私钥放到 gengar-server 所在机器。目标：表单改为**上传私钥文件**，后端落到固定目录 `<data>/keys/<hostID>.key`，`KeyPath` 自动指向它——executor 只认路径，零改动。

非目标：共享密钥库（一主机一 key，共用 key 传两遍）、公钥管理、KeyPath 相对路径/data 搬迁兼容（KeyPath 存绝对路径，data 是整体搬迁单位）。

## 后端

### hostReq 扩展（webapi/hosts.go）

- 加 `KeyData string`（base64 私钥内容，可选）。
- `hostReq.KeyPath` 保留（XShell 导入仍携带原始路径做参考；上传的 key 服务端自定路径，客户端传的 KeyPath 仅在无 keyData 时生效）。

### saveHost 密钥处理

1. **有 keyData**：
   - base64 解码 → 确定口令：依次尝试 `[req.KeyPassphrase（非空）, 编辑场景旧 KeyPassphraseRef 解出的口令, 无口令]` 解析（`ssh.ParsePrivateKey[WithPassphrase]`），首个成功者胜——胜者同时决定 KeyPassphraseRef 的设/留/清（req 胜 → 写新 ref；旧 ref 胜 → 保留；无口令胜且旧 ref 存在 → 清除）；全部失败 → 400「私钥无法解析：口令错误或格式不支持」。
   - 落盘 `os.MkdirAll(keysDir, 0o700)` + `os.WriteFile(keysDir/<hostID>.key, raw, 0o600)`（先写文件后 Save 主机；Save 失败留下的孤儿文件按 hostID 命名、永不被引用，无害）。
   - `h.Auth.KeyPath = filepath.Join(keysDir, h.ID+".key")`。
2. **无 keyData**：编辑路径保留 `old.Auth.KeyPath`（与 PasswordRef「留空保持不变」同语义）。
3. **兜底校验**：`Auth.Method == key && h.Auth.KeyPath == ""`（新建未上传、或编辑从密码切密钥未上传）→ 400「请上传私钥文件」。

### 删除清理（deleteHost）

顺带 `os.Remove(keysDir/<id>.key)`（幂等；keysDir 未设置时跳过）。

### keysDir 注入

`Server.keysDir` 字段 + `SetKeysDir(dir string)`（`SetTermManager` 同款注入模式）；main.go 在 `New` 之后调用 `srv.SetKeysDir(filepath.Join(dataDir, "keys"))`。

## 前端（HostForm）

- 密钥认证分支：路径文本框 → 文件选择（hidden input + 按钮触发，ImportDialog 模式）；选中后按钮显示文件名。
- 编辑已有密钥的主机：hint 显示「✓ 已上传密钥（重新选择可替换）」；口令留空 = 沿用原口令。
- 前端校验：新建密钥认证未选文件 → 报错（后端同样 400 兜底）。
- `HostReq` 加 `keyData?: string`；FileReader `readAsDataURL` 取逗号后 base64 段；私钥内容不落 console。

## 测试

- `hosts_test.go`：上传落盘（路径/内容一致）、坏私钥 400、加密私钥（错误口令 400 / 正确口令 200）、新建密钥认证缺 keyData 与 keyPath 400、编辑不重传保留原 KeyPath、重传覆盖文件、删除主机清 key 文件。
- 前端：既有 client/Hook 层测试保持全绿（HostForm 组件无自动化测试为既定边界）；`tsc` + build 验证。

## 环境注意

本机 `go test -race` 全局损坏，禁用。

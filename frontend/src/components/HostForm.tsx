// HostForm.tsx 主机录入/编辑：私钥走文件上传（base64 随保存提交，服务端
// 落盘 <data>/keys 并自定路径）；秘密不回填，留空 = 保持原值；底部「测试」
// 按钮用当前表单参数直连探测（POST /api/hosts/test，不落库）。保存成功后
// fire-and-forget TestHost 再探一次并回写侧栏状态。严禁 console.log——
// 请求体含私钥与口令字段。
import { useRef, useState } from "react";
import { SaveHost, TestHost, TestHostDraft } from "../api/client";
import { parseTags } from "../form";
import type { HostReq, HostView } from "../types";

interface Props {
  edit?: HostView | null; // 非空 = 编辑该主机
  presetFolder?: string; // 新建时预填的目录（侧栏右键「添加主机到此目录」）
  onClose: () => void;
  onSaved: () => Promise<HostView[]>; // 保存成功后刷新清单并返回新列表
  onToast: (text: string) => void;
}

export default function HostForm({ edit, presetFolder, onClose, onSaved, onToast }: Props) {
  const [name, setName] = useState(edit?.name ?? "");
  const [addr, setAddr] = useState(edit?.addr ?? "");
  const [port, setPort] = useState(String(edit?.port ?? 22));
  const [user, setUser] = useState(edit?.user ?? "root");
  const [authMethod, setAuthMethod] = useState<"key" | "password">(
    edit?.authMethod === "password" ? "password" : "key"
  );
  const [password, setPassword] = useState("");
  const [keyPassphrase, setKeyPassphrase] = useState("");
  const keyInputRef = useRef<HTMLInputElement>(null);
  const [keyFile, setKeyFile] = useState<File | null>(null); // 选中待上传的私钥
  const [keyData, setKeyData] = useState(""); // 私钥 base64（readAsDataURL 取逗号后段）
  const [tags, setTags] = useState(edit ? edit.tags.join(", ") : "");
  const [folder, setFolder] = useState(edit ? edit.folder : (presetFolder ?? ""));
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);

  // 选私钥文件：读为 base64 暂存，保存/测试时随请求提交
  const pickKey = (f: File | null) => {
    setKeyFile(f);
    setKeyData("");
    if (!f) {
      return;
    }
    const rd = new FileReader();
    rd.onload = () => {
      // data URL 形如 data:application/octet-stream;base64,AAAA → 取逗号后段
      const s = String(rd.result ?? "");
      setKeyData(s.slice(s.indexOf(",") + 1));
    };
    rd.readAsDataURL(f);
  };

  const buildReq = (): HostReq => {
    const portNum = parseInt(port, 10);
    return {
      id: edit?.id ?? "",
      name: name.trim(),
      addr: addr.trim(),
      port: Number.isFinite(portNum) && portNum > 0 ? portNum : 22,
      user: user.trim(),
      tags: parseTags(tags), // 去重：重复标签只会让侧栏/目标解析更乱
      folder: folder.trim(),
      authMethod,
      keyData: authMethod === "key" ? keyData : "",
      password,
      keyPassphrase,
    };
  };

  const validate = (): string => {
    if (name.trim() === "" || addr.trim() === "" || user.trim() === "") {
      return "名称、地址、用户为必填";
    }
    if (authMethod === "key" && keyData === "" && (edit?.keyPath ?? "") === "") {
      return "密钥认证请选择私钥文件";
    }
    return "";
  };

  const submit = async () => {
    setError("");
    const v = validate();
    if (v !== "") {
      setError(v);
      return;
    }
    const req = buildReq();
    setSaving(true);
    try {
      // HostReq 全字段提交；不 log：请求体含口令字段
      await SaveHost(req);
    } catch (e) {
      setError(String(e));
      return;
    } finally {
      setSaving(false);
      setPassword(""); // 安全约定：提交后清空秘密字段
      setKeyPassphrase("");
    }
    const hosts = await onSaved();
    onClose();
    // 探测连通性：结果或错误直接进 toast（简单 div）
    const saved =
      req.id !== ""
        ? hosts.find((h) => h.id === req.id)
        : hosts.find((h) => h.name === req.name && h.addr === req.addr && h.port === req.port);
    if (saved) {
      TestHost(saved.id)
        .then((msg) => onToast(msg))
        .catch((e) => onToast(saved.name + ": " + String(e)));
    }
  };

  // 表单内测试：用当前输入直连探测（未保存；结果不落库），成功失败均 toast
  const testDraft = async () => {
    setError("");
    if (addr.trim() === "" || user.trim() === "") {
      setError("地址与用户为必填");
      return;
    }
    setTesting(true);
    try {
      const msg = await TestHostDraft(buildReq());
      onToast(msg);
    } catch (e) {
      onToast("测试失败：" + String(e));
    } finally {
      setTesting(false);
    }
  };

  const keepHint = edit ? "留空保持不变" : undefined;

  return (
    <div className="overlay">
      <div className="dialog host-form">
        <div className="dialog-title">{edit ? "编辑主机" : "添加主机"}</div>
        <div className="form-grid">
          <label>
            名称 *
            <input autoFocus value={name} onChange={(e) => setName(e.target.value)} placeholder="web-01" />
          </label>
          <label>
            地址 *
            <input value={addr} onChange={(e) => setAddr(e.target.value)} placeholder="192.168.1.10" />
          </label>
          <label>
            端口
            <input value={port} onChange={(e) => setPort(e.target.value)} inputMode="numeric" />
          </label>
          <label>
            用户 *
            <input value={user} onChange={(e) => setUser(e.target.value)} placeholder="root" />
          </label>
          <label>
            认证方式
            <select
              value={authMethod}
              onChange={(e) => setAuthMethod(e.target.value as "key" | "password")}
            >
              <option value="key">SSH 密钥</option>
              <option value="password">密码</option>
            </select>
          </label>
          {authMethod === "key" ? (
            <label>
              私钥文件 *
              <input
                ref={keyInputRef}
                type="file"
                className="hidden-input"
                onChange={(e) => pickKey(e.target.files?.[0] ?? null)}
              />
              <button
                type="button"
                className="btn ghost small"
                onClick={() => keyInputRef.current?.click()}
              >
                {keyFile ? "已选：" + keyFile.name : "选择私钥文件…"}
              </button>
              {keyFile ? (
                <span className="hint">保存时上传到服务器密钥目录</span>
              ) : edit?.keyPath ? (
                <span className="hint">✓ 已上传密钥（重新选择可替换）</span>
              ) : undefined}
            </label>
          ) : (
            <label>
              密码
              <input
                type="password"
                value={password}
                autoComplete="new-password"
                placeholder={keepHint}
                onChange={(e) => setPassword(e.target.value)}
              />
            </label>
          )}
          <label>
            密钥口令（可选）
            <input
              type="password"
              value={keyPassphrase}
              autoComplete="new-password"
              placeholder={keepHint}
              onChange={(e) => setKeyPassphrase(e.target.value)}
            />
          </label>
          <label>
            目录
            <input
              value={folder}
              onChange={(e) => setFolder(e.target.value)}
              placeholder="生产/web · 留空为根目录"
            />
          </label>
          <label>
            标签（逗号分隔）
            <input value={tags} onChange={(e) => setTags(e.target.value)} placeholder="web, prod" />
          </label>
        </div>
        {error && <div className="form-error">{error}</div>}
        <div className="dialog-footer">
          <span className="hint">{edit ? "保存后重新探测连通性" : "保存后自动探测连通性"}</span>
          <button className="btn ghost" disabled={testing || saving} onClick={() => void testDraft()}>
            {testing ? "测试中…" : "测试"}
          </button>
          <button className="btn ghost" disabled={saving} onClick={onClose}>
            取消
          </button>
          <button className="btn primary" disabled={saving} onClick={() => void submit()}>
            保存
          </button>
        </div>
      </div>
    </div>
  );
}

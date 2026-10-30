// Settings.tsx 模型设置：GetSettings 加载（apiKey 返回掩码），SaveSettings
// 保存——后端掩码=未修改。保存会重建 Agent（新会话），成功后由父组件清空
// 消息列表再关闭。
import { useEffect, useState } from "react";
import { GetSettings, SaveSettings } from "../api/client";
import type { SettingsView } from "../types";

interface Props {
  onClose: () => void;
  onSaved: () => void; // 清空消息（Agent 已重建，旧对话不可续）
}

export default function Settings({ onClose, onSaved }: Props) {
  const [apiKey, setApiKey] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [model, setModel] = useState("");
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    GetSettings()
      .then((s: SettingsView) => {
        setApiKey(s?.apiKey ?? "");
        setBaseUrl(s?.baseUrl ?? "");
        setModel(s?.model ?? "");
        setLoaded(true);
      })
      .catch((e) => {
        setError(String(e));
        setLoaded(true);
      });
  }, []);

  const submit = async () => {
    setError("");
    setSaving(true);
    try {
      await SaveSettings({ apiKey: apiKey.trim(), baseUrl: baseUrl.trim(), model: model.trim() });
    } catch (e) {
      setError(String(e));
      return;
    } finally {
      setSaving(false);
    }
    onSaved();
    onClose();
  };

  return (
    <div className="overlay">
      <div className="dialog settings">
        <div className="dialog-title">设置</div>
        <div className="form-grid">
          <label>
            API Key
            <input
              type="password"
              value={apiKey}
              placeholder={loaded ? "sk-****（掩码 = 保持不变）" : "加载中…"}
              autoComplete="off"
              onChange={(e) => setApiKey(e.target.value)}
            />
          </label>
          <label>
            模型
            <input
              value={model}
              placeholder="claude-sonnet-4-20250514"
              onChange={(e) => setModel(e.target.value)}
            />
          </label>
          <label>
            Base URL（可选）
            <input
              value={baseUrl}
              placeholder="https://api.anthropic.com"
              onChange={(e) => setBaseUrl(e.target.value)}
            />
          </label>
        </div>
        {error && <div className="form-error">{error}</div>}
        <div className="dialog-footer">
          <span className="hint">保存将重建 Agent 并清空当前对话</span>
          <button className="btn ghost" onClick={onClose}>
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

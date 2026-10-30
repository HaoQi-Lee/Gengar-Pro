// Login.tsx 登录页：用户名 + 密码，成功后 onDone 回调进入主界面。
// 登录走 raw401 路径：401 不触发全局未登录回调，透传服务端文案（如
//「用户名或密码错误」）。
import { useState, type FormEvent } from "react";
import { Login as loginApi } from "../api/client";

export default function Login({ onDone }: { onDone: (me: { username: string; role: string }) => void }) {
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (busy || username === "" || password === "") {
      return;
    }
    setBusy(true);
    setError("");
    loginApi(username, password)
      .then((me) => onDone(me))
      .catch((err) => setError(String(err)))
      .finally(() => setBusy(false));
  };

  return (
    <div className="overlay">
      <form className="dialog" onSubmit={submit}>
        <div className="dialog-title">🔮 gengar-pro</div>
        <label className="form-row">
          <span>用户名</span>
          <input value={username} onChange={(e) => setUsername(e.target.value)} autoFocus />
        </label>
        <label className="form-row">
          <span>密码</span>
          <input type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </label>
        {error && <div className="form-error">{error}</div>}
        <div className="dialog-footer">
          <button
            className="btn primary"
            type="submit"
            disabled={busy || username === "" || password === ""}
          >
            {busy ? "登录中…" : "登录"}
          </button>
        </div>
      </form>
    </div>
  );
}

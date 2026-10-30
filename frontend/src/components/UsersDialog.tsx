// UsersDialog.tsx 用户管理（仅 admin 打开）：列表 + 创建 + 行内重置密码 +
// 两次点击删除（同主机删除交互）。当前登录账号删除按钮禁用；admin 角色
// 徽标标注内置管理员。
import { useEffect, useState } from "react";
import { CreateUser, DeleteUser, GetUsers, ResetUserPassword } from "../api/client";
import type { UserView } from "../types";

interface Props {
  me: string; // 当前登录用户名（其删除按钮禁用）
  onClose: () => void;
  onToast: (text: string) => void;
}

export default function UsersDialog({ me, onClose, onToast }: Props) {
  const [users, setUsers] = useState<UserView[] | null>(null);
  const [error, setError] = useState("");
  // 创建表单
  const [name, setName] = useState("");
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [creating, setCreating] = useState(false);
  // 行内操作：pwFor=重置密码输入框展开的目标用户；confirming=两次点击删除确认
  const [pwFor, setPwFor] = useState("");
  const [pwInput, setPwInput] = useState("");
  const [confirming, setConfirming] = useState("");

  useEffect(() => {
    GetUsers()
      .then((us) => setUsers(us))
      .catch((e) => {
        setError(String(e));
        setUsers([]);
      });
  }, []);

  const refresh = () => {
    GetUsers()
      .then((us) => setUsers(us))
      .catch((e) => setError(String(e)));
  };

  const submitCreate = async () => {
    setError("");
    if (name.trim() === "" || pw.length < 6) {
      setError("用户名必填，密码至少 6 位");
      return;
    }
    if (pw !== pw2) {
      setError("两次输入的密码不一致");
      return;
    }
    setCreating(true);
    try {
      await CreateUser(name.trim(), pw);
    } catch (e) {
      setError(String(e));
      return;
    } finally {
      setCreating(false);
    }
    setName("");
    setPw("");
    setPw2("");
    onToast("用户已创建");
    refresh();
  };

  const submitReset = async (u: string) => {
    setError("");
    if (pwInput.length < 6) {
      setError("密码至少 6 位");
      return;
    }
    try {
      await ResetUserPassword(u, pwInput);
    } catch (e) {
      setError(String(e));
      return;
    }
    setPwFor("");
    setPwInput("");
    onToast("已重置 " + u + " 的密码（其会话已吊销）");
  };

  const del = async (u: string) => {
    try {
      await DeleteUser(u);
    } catch (e) {
      setError(String(e));
      return;
    }
    onToast("已删除用户 " + u);
    refresh();
  };

  return (
    <div className="overlay">
      <div className="dialog users">
        <div className="dialog-title">用户管理</div>
        <div className="users-list">
          {users === null && <div className="hint">加载中…</div>}
          {users?.map((u) => (
            <div key={u.username} className="user-row">
              <span className="user-name">{u.username}</span>
              {u.role === "admin" && <span className="role-badge">admin</span>}
              <span className="user-created">{u.createdAt}</span>
              {pwFor === u.username ? (
                <span className="user-reset">
                  <input
                    type="password"
                    placeholder="新密码（≥6 位）"
                    value={pwInput}
                    autoFocus
                    onChange={(e) => setPwInput(e.target.value)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") {
                        void submitReset(u.username);
                      }
                      if (e.key === "Escape") {
                        setPwFor("");
                        setPwInput("");
                      }
                    }}
                  />
                  <button className="btn ghost small" onClick={() => void submitReset(u.username)}>
                    确定
                  </button>
                  <button
                    className="btn ghost small"
                    onClick={() => {
                      setPwFor("");
                      setPwInput("");
                    }}
                  >
                    取消
                  </button>
                </span>
              ) : (
                <span className="user-ops">
                  <button
                    className="btn ghost small"
                    onClick={() => {
                      setPwFor(u.username);
                      setPwInput("");
                      setConfirming("");
                    }}
                  >
                    重置密码
                  </button>
                  <button
                    className={confirming === u.username ? "btn danger small" : "btn ghost small"}
                    disabled={u.username === me}
                    title={
                      u.username === me
                        ? "不能删除当前登录账号"
                        : confirming === u.username
                          ? "再点一次确认删除（其名下主机将移交 admin）"
                          : "删除用户（其名下主机将移交 admin）"
                    }
                    onClick={() => {
                      if (confirming === u.username) {
                        setConfirming("");
                        void del(u.username);
                      } else {
                        setConfirming(u.username);
                      }
                    }}
                  >
                    {confirming === u.username ? "确认?" : "删除"}
                  </button>
                </span>
              )}
            </div>
          ))}
        </div>
        <div className="form-grid">
          <label>
            新用户名
            <input
              value={name}
              placeholder="bob（1-32 位字母数字 _ . @ -）"
              onChange={(e) => setName(e.target.value)}
            />
          </label>
          <label>
            初始密码
            <input type="password" value={pw} placeholder="至少 6 位" onChange={(e) => setPw(e.target.value)} />
          </label>
          <label>
            确认密码
            <input type="password" value={pw2} onChange={(e) => setPw2(e.target.value)} />
          </label>
        </div>
        {error && <div className="form-error">{error}</div>}
        <div className="dialog-footer">
          <span className="hint">重置密码后该用户需重新登录 · 删除用户时其名下主机移交 admin</span>
          <button className="btn ghost" onClick={onClose}>
            关闭
          </button>
          <button className="btn primary" disabled={creating} onClick={() => void submitCreate()}>
            创建用户
          </button>
        </div>
      </div>
    </div>
  );
}

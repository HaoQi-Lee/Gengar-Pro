// ScopePicker.tsx 对话目标选择器（仅对话开始、目标为空时出现——一经选定
// 不允许修改，换范围=开新对话 tab）。按目录分组复选 + 目录整组勾选 +
// 全部/清空快捷项；至少选一台才能开始。样式自包含，只复用主题变量。
import { useMemo, useState } from "react";
import type { HostView } from "../types";

interface Props {
  hosts: HostView[];
  sessionReady: boolean; // WS hello 已绑定会话 id（未就绪时开始钮禁用）
  onConfirm: (hostIds: string[]) => void;
}

export default function ScopePicker({ hosts, sessionReady, onConfirm }: Props) {
  // 目录路径 → 主机列表（码点序稳定排列）
  const groups = useMemo(() => {
    const byFolder = new Map<string, HostView[]>();
    for (const h of hosts) {
      const key = h.folder ?? "";
      const list = byFolder.get(key);
      if (list) {
        list.push(h);
      } else {
        byFolder.set(key, [h]);
      }
    }
    return [...byFolder.entries()].sort((a, b) => (a[0] < b[0] ? -1 : a[0] > b[0] ? 1 : 0));
  }, [hosts]);

  const [selected, setSelected] = useState<Set<string>>(new Set());

  const toggle = (id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      return next;
    });
  };
  const toggleGroup = (list: HostView[]) => {
    setSelected((prev) => {
      const ids = list.map((h) => h.id);
      const all = ids.every((id) => prev.has(id));
      const next = new Set(prev);
      for (const id of ids) {
        if (all) {
          next.delete(id);
        } else {
          next.add(id);
        }
      }
      return next;
    });
  };

  return (
    <div className="overlay scope-picker-overlay">
      <div className="dialog scope-picker">
        <div className="dialog-title">选择本次对话的目标主机</div>
        {hosts.length === 0 ? (
          <div className="host-empty">还没有主机——先在侧栏「+ 添加」录入。</div>
        ) : (
          <div className="scope-groups">
            {groups.map(([folder, list]) => {
              const all = list.every((h) => selected.has(h.id));
              return (
                <div key={folder} className="scope-group">
                  <label className="scope-folder">
                    <input type="checkbox" checked={all} onChange={() => toggleGroup(list)} />
                    <span className="scope-folder-name">📁 {folder === "" ? "未分组" : folder}</span>
                    <span className="scope-n">{list.length} 台</span>
                  </label>
                  {list.map((h) => (
                    <label key={h.id} className="scope-host">
                      <input type="checkbox" checked={selected.has(h.id)} onChange={() => toggle(h.id)} />
                      <span className={"dot dot-" + h.status} title={h.status} />
                      <span className="scope-host-name">{h.name}</span>
                      <span
                        className="scope-host-meta"
                        title={`${h.addr}:${h.port} · ${h.user}${h.os ? " · " + h.os : ""}`}
                      >
                        {h.addr}:{h.port} · {h.user}
                        {h.os ? " · " + h.os : ""}
                      </span>
                    </label>
                  ))}
                </div>
              );
            })}
          </div>
        )}
        <div className="scope-hint">
          {selected.size > 0 ? `已选 ${selected.size} 台——省略主机名的指令将作用于全部目标` : "至少选择一台主机（目录复选可整组勾选）"}
        </div>
        <div className="dialog-footer">
          <div className="scope-footer-ops">
            <button className="btn ghost small" onClick={() => setSelected(new Set(hosts.map((h) => h.id)))}>
              全部
            </button>
            <button className="btn ghost small" onClick={() => setSelected(new Set())}>
              清空
            </button>
          </div>
          <button
            className="btn primary"
            disabled={!sessionReady || selected.size === 0}
            title={!sessionReady ? "会话建立中…" : selected.size === 0 ? "至少选择一台主机" : ""}
            onClick={() => onConfirm([...selected])}
          >
            开始对话
          </button>
        </div>
      </div>
    </div>
  );
}

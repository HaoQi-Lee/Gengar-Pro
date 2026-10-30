// ConfirmModal.tsx 批量确认弹窗：reqs 为 PascalCase（后端无 json tag）。
// Esc 拒绝；非高风险批次 Enter 一键批准。任一 HighRisk → 严格门（设计 4.5）：
// 标题警示 + 必须在输入框键入 yes/是 才能点批准，Enter 不再触发批准。
// 仅当 active（最上层弹窗）时响应键盘；成为顶层时把焦点收进弹窗（严格模式
// 直接进输入框），避免焦点遗留在底层输入框造成 Enter 双路径触发。
import { useEffect, useRef, useState } from "react";
import type { ConfirmRequestView } from "../types";

interface Props {
  data: { id: string; reqs: ConfirmRequestView[] };
  active: boolean; // 是否最上层弹窗：被表单等盖住时不响应 Enter/Esc
  onResolve: (approved: boolean) => void;
}

export default function ConfirmModal({ data, active, onResolve }: Props) {
  const dialogRef = useRef<HTMLDivElement>(null);
  const gateInputRef = useRef<HTMLInputElement>(null);
  const [gateText, setGateText] = useState("");
  const highRisk = data.reqs.some((r) => r.HighRisk);
  const hostCount = new Set(data.reqs.flatMap((r) => r.Hosts ?? [])).size;
  // 严格门通过条件：非高风险，或已键入 yes / 是
  const gatePassed =
    !highRisk || gateText.trim() === "yes" || gateText.trim() === "是";

  useEffect(() => {
    if (!active) {
      return;
    }
    (highRisk ? gateInputRef.current : dialogRef.current)?.focus();
  }, [active, highRisk]);

  useEffect(() => {
    if (!active) {
      return;
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onResolve(false);
      } else if (e.key === "Enter" && !highRisk) {
        // 高风险批次：Enter 不触发批准，只能键入 yes 后点击按钮
        e.preventDefault();
        onResolve(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [active, highRisk, onResolve]);

  return (
    <div className="overlay">
      <div
        className={highRisk ? "dialog confirm high-risk" : "dialog confirm"}
        ref={dialogRef}
        tabIndex={-1}
      >
        <div className="dialog-title">{highRisk ? "⚠️ 高风险操作" : "确认操作"}</div>
        {hostCount > 0 && <div className="confirm-hostcount">将影响 {hostCount} 台机器</div>}
        <div className="confirm-list">
          {data.reqs.map((r, i) => (
            <div key={i} className="confirm-item">
              <div className="confirm-tool">
                {r.ToolName}
                {r.HighRisk ? " · 高风险" : ""}
              </div>
              <pre className="confirm-summary">{r.Summary}</pre>
              {r.Hosts && r.Hosts.length > 0 && (
                <div className="badge-row">
                  {r.Hosts.map((h) => (
                    <span key={h} className="host-badge">
                      {h}
                    </span>
                  ))}
                </div>
              )}
            </div>
          ))}
        </div>
        {highRisk && (
          <div className="gate">
            <input
              ref={gateInputRef}
              value={gateText}
              placeholder="yes"
              autoComplete="off"
              onChange={(e) => setGateText(e.target.value)}
            />
            <div className="gate-hint">输入 yes 以确认执行</div>
          </div>
        )}
        <div className="dialog-footer">
          <span className="hint">
            {highRisk ? "Esc 拒绝 · 键入 yes 后点击批准" : "Esc 拒绝 · Enter 批准"}
          </span>
          <button className="btn ghost" onClick={() => onResolve(false)}>
            拒绝
          </button>
          <button
            className={highRisk ? "btn danger" : "btn primary"}
            disabled={!gatePassed}
            onClick={() => onResolve(true)}
          >
            批准
          </button>
        </div>
      </div>
    </div>
  );
}

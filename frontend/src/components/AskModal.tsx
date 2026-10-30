// AskModal.tsx ask_user 提问弹窗：选项按钮回填 label（与 tea 端一致），
// 自定义输入 Enter 提交，Esc 以「（用户取消了回答）」回填让模型拿到明确信号。
// 仅当 active（最上层弹窗）时响应 Esc；挂载即收走焦点（见 ConfirmModal）。
import { useEffect, useRef, useState } from "react";

export interface AskOption {
  label: string;
  description: string;
}

export interface AskData {
  id: string;
  question: string;
  header?: string;
  options: AskOption[];
}

interface Props {
  data: AskData;
  active: boolean; // 是否最上层弹窗：被表单等盖住时不响应 Esc
  onAnswer: (answer: string) => void;
}

export default function AskModal({ data, active, onAnswer }: Props) {
  const [custom, setCustom] = useState("");
  const dialogRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    dialogRef.current?.focus();
  }, []);

  useEffect(() => {
    if (!active) {
      return;
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        onAnswer("（用户取消了回答）");
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [active, onAnswer]);

  const submitCustom = () => {
    const v = custom.trim();
    if (v !== "") {
      onAnswer(v);
    }
  };

  return (
    <div className="overlay">
      <div className="dialog ask" ref={dialogRef} tabIndex={-1}>
        <div className="dialog-title">{data.header ? "提问 · " + data.header : "Agent 提问"}</div>
        <div className="ask-question">{data.question}</div>
        <div className="ask-options">
          {data.options.map((o, i) => (
            <button key={i} className="ask-option" onClick={() => onAnswer(o.label)}>
              <span className="ask-option-label">
                {i + 1}. {o.label}
              </span>
              {o.description && <span className="ask-option-desc">{o.description}</span>}
            </button>
          ))}
        </div>
        <div className="ask-custom">
          <input
            value={custom}
            placeholder="或输入自定义回答，Enter 提交"
            onChange={(e) => setCustom(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                submitCustom();
              }
            }}
          />
        </div>
        <div className="dialog-footer">
          <span className="hint">Esc 取消回答</span>
          <button className="btn primary" disabled={custom.trim() === ""} onClick={submitCustom}>
            提交自定义
          </button>
        </div>
      </div>
    </div>
  );
}

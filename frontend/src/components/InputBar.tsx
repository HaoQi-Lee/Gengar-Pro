// InputBar.tsx 输入区：Enter 提交、Shift+Enter 换行、busy 时 Esc 取消任务。
// 任何弹窗打开（modalOpen）时 Esc 归弹窗处理，这里忽略，避免误取消任务。
import { useState } from "react";
import type { KeyboardEvent } from "react";

interface Props {
  busy: boolean;
  disabled?: boolean; // 未选对话目标时禁发（目标选择器在上方）
  modalOpen: boolean;
  onSubmit: (text: string) => void;
  onCancel: () => void;
}

export default function InputBar({ busy, disabled = false, modalOpen, onSubmit, onCancel }: Props) {
  const [text, setText] = useState("");

  const send = () => {
    const line = text.trim();
    if (line === "" || busy) {
      return;
    }
    onSubmit(line);
    setText("");
  };

  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      send();
    } else if (e.key === "Escape" && busy && !modalOpen) {
      onCancel();
    }
  };

  return (
    <div className="inputbar">
      <textarea
        value={text}
        rows={3}
        disabled={disabled}
        placeholder={
          disabled
            ? "请先选择本次对话的目标主机…"
            : busy
              ? "任务执行中…（Esc 取消）"
              : "输入消息，Enter 发送 · Shift+Enter 换行 · /inspect 巡检 · /install 安装"
        }
        onChange={(e) => setText(e.target.value)}
        onKeyDown={onKeyDown}
      />
      <button className="btn primary" disabled={disabled || busy || text.trim() === ""} onClick={send}>
        发送
      </button>
    </div>
  );
}

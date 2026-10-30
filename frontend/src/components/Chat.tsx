// Chat.tsx 渲染消息流：user 右侧气泡、assistant 块序列、system 居中灰字；
// 工具行折叠 body，多主机 body 按 "===== host =====" 分节渲染徽标。
// 02 模块：思考指示器/残留行/工具耗时的渲染层。空状态展示耿鬼 logo。
import { memo, useEffect, useRef, useState } from "react";
import gengarLogo from "../assets/gengar-logo.png";
import MarkdownView from "./Markdown";
import type { Block, Message, ThinkingState, ToolBlock } from "../types";
import { SPINNER_FRAMES, THINK_RESIDUE_MS, TOOL_DUR_MS, fmtDur } from "../timing";

interface BodySection {
  host: string | null; // null = 无分节头的普通内容
  failed: boolean;
  content: string;
}

// 后端 fan-out 聚合格式："===== web-01 =====\n…" / "===== web-02 [FAILED] =====\n…"
function splitHostSections(body: string): BodySection[] {
  const re = /===== ([^\s=]+)( \[FAILED\])? =====\n?/g;
  const out: BodySection[] = [];
  let last = 0;
  let m: RegExpExecArray | null;
  while ((m = re.exec(body)) !== null) {
    const before = body.slice(last, m.index);
    if (out.length > 0) {
      out[out.length - 1].content += before; // 头之前的内容属于上一节
    } else if (before.trim() !== "") {
      out.push({ host: null, failed: false, content: before });
    }
    out.push({ host: m[1], failed: m[2] !== undefined, content: "" });
    last = re.lastIndex;
  }
  const tail = body.slice(last);
  if (out.length > 0) {
    out[out.length - 1].content += tail;
  } else if (tail.trim() !== "") {
    out.push({ host: null, failed: false, content: tail });
  }
  return out;
}

function ToolBody({ body }: { body: string }) {
  const sections = splitHostSections(body);
  return (
    <div className="tool-body">
      {sections.map((s, i) =>
        s.host === null ? (
          <pre key={i} className="tool-pre">
            {s.content.replace(/\s+$/, "")}
          </pre>
        ) : (
          <div key={i} className="tool-section">
            <span className={s.failed ? "host-badge failed" : "host-badge"}>
              {s.host}
              {s.failed ? " · 失败" : ""}
            </span>
            <pre className="tool-pre">{s.content.replace(/\s+$/, "")}</pre>
          </div>
        )
      )}
    </div>
  );
}

/** 运行中计时：自持 100ms interval 本地重渲染（不重渲染整个消息列表）。
 *  只在工具未完成时挂载，完成态由 ToolLine 渲染定格值。 */
function Elapsed({ startAt }: { startAt: number }) {
  const [, tick] = useState(0);
  useEffect(() => {
    const t = window.setInterval(() => tick((n) => n + 1), 100);
    return () => window.clearInterval(t);
  }, []);
  return <span className="tool-dur live">{fmtDur(performance.now() - startAt)}</span>;
}

/** 思考指示器：⠋ Brewing… (2s)，动词与起点来自 ThinkingState。 */
function ThinkingIndicator({ state }: { state: ThinkingState }) {
  const [frame, setFrame] = useState(0);
  useEffect(() => {
    const t = window.setInterval(() => setFrame((f) => f + 1), 100);
    return () => window.clearInterval(t);
  }, []);
  const secs = Math.floor((performance.now() - state.startAt) / 1000);
  return (
    <div className="think-live">
      <span className="spin-frame" aria-hidden="true">{SPINNER_FRAMES[frame % SPINNER_FRAMES.length]}</span>{" "}
      {state.verb}… ({secs}s)
    </div>
  );
}

/** 思考残留行：✻ 思考 12.4s。 */
function ThinkingResidue({ durationMs }: { durationMs: number }) {
  return <div className="think-res">✻ 思考 {fmtDur(durationMs)}</div>;
}

function ToolLine({ block }: { block: ToolBlock }) {
  // ok 是 omitempty 布尔：未完成 → run，完成后按真值分 ok/fail
  const status = !block.done ? "run" : block.ok ? "ok" : "fail";
  const mark = !block.done ? "…" : block.ok ? "✓" : "✗";
  return (
    <details className={"tool-line " + status}>
      <summary>
        <span className={"tool-mark tool-mark-" + status}>{mark}</span>
        <span className="tool-display">{block.display}</span>
        {!block.done && block.startedAt !== undefined ? (
          <Elapsed startAt={block.startedAt} />
        ) : null}
        {block.done && block.durationMs !== undefined && block.durationMs >= TOOL_DUR_MS ? (
          <span className="tool-dur">· {fmtDur(block.durationMs)}</span>
        ) : null}
      </summary>
      {block.done && block.body ? <ToolBody body={block.body} /> : null}
    </details>
  );
}

function blocksText(blocks: Block[]): string {
  return blocks
    .map((b) => (b.kind === "text" ? b.text : ""))
    .join("\n");
}

// MessageView memo 化（评审 ④-3）：流式 delta 只更新末条消息，历史消息
// 引用不变即跳过重渲染（Markdown 不再全列表重解析）。
const MessageView = memo(function MessageView({ message }: { message: Message }) {
  if (message.role === "user") {
    return (
      <div className="msg msg-user">
        <div className="bubble">{blocksText(message.blocks)}</div>
      </div>
    );
  }
  if (message.role === "system") {
    return (
      <div className="msg msg-system">
        <pre className="system-text">{blocksText(message.blocks)}</pre>
      </div>
    );
  }
  return (
    <div className="msg msg-assistant">
      {message.blocks.map((b, i) =>
        b.kind === "tool" ? (
          <ToolLine key={i} block={b} />
        ) : b.kind === "thinking" ? (
          b.durationMs >= THINK_RESIDUE_MS ? <ThinkingResidue key={i} durationMs={b.durationMs} /> : null
        ) : (
          <div key={i} className="text">
            <MarkdownView text={b.text} />
          </div>
        )
      )}
    </div>
  );
});

export default function Chat({
  messages,
  thinking,
}: {
  messages: Message[];
  thinking: ThinkingState | null;
}) {
  const chatRef = useRef<HTMLDivElement>(null);
  // 温和自动滚底：仅在用户停在底部附近（80px 阈值）时跟随新内容；
  // 上翻阅读时 delta 不再把视图拽回底部。scroll 事件先于内容增长的
  // effect 更新该标记，因此判断的是"更新前"的位置。
  const stickRef = useRef(true);

  const onScroll = () => {
    const el = chatRef.current;
    if (el) {
      stickRef.current = el.scrollTop + el.clientHeight >= el.scrollHeight - 80;
    }
  };

  useEffect(() => {
    const el = chatRef.current;
    if (el && stickRef.current) {
      el.scrollTop = el.scrollHeight;
    }
  }, [messages, thinking]);

  return (
    <div className="chat" ref={chatRef} onScroll={onScroll}>
      {messages.length === 0 && (
        <div className="chat-empty">
          <img src={gengarLogo} alt="" className="chat-empty-logo" />
          <div className="chat-empty-title">gengar-pro</div>
          <div>在侧栏添加主机、开对话时选定目标，即可对多台机器发号施令。</div>
          <div className="chat-empty-hint">输入 /inspect 开始服务器巡检 · /install 安装软件</div>
        </div>
      )}
      {messages.map((m, i) => (
        // 稳定 key：消息身份按索引在会话内恒定（消息只追加不插入），
        // memo 的引用比较即可全量跳过历史渲染
        <MessageView key={i} message={m} />
      ))}
      {thinking && <ThinkingIndicator state={thinking} />}
    </div>
  );
}

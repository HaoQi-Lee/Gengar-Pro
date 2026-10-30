// timing.ts 对话流计时的纯函数层：所有函数接收注入的 now
// （performance.now() 毫秒），便于单测；UI 组件只负责打点与渲染。
// 块操作沿用 App.tsx 既有 reducer 的不可变更新风格。
import type { Block, ChatEvent, Message, ThinkingState } from "./types";

export const THINKING_VERBS = [
  "Thinking", "Pondering", "Reasoning", "Brewing",
  "Cogitating", "Musing", "Deliberating", "Reflecting",
] as const; // 同原 gengar CLI 的动词表

export const SPINNER_FRAMES = ["⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"] as const;

/** 思考残留行阈值：不足 1s 的思考不留痕（快速响应是噪音）。 */
export const THINK_RESIDUE_MS = 1000;
/** 工具耗时显示阈值：不足 1s 不显示。 */
export const TOOL_DUR_MS = 1000;

export function startThinking(now: number): ThinkingState {
  return {
    startAt: now,
    verb: THINKING_VERBS[Math.floor(Math.random() * THINKING_VERBS.length)],
  };
}

function appendBlock(messages: Message[], block: Block): Message[] {
  const last = messages[messages.length - 1];
  if (last && last.role === "assistant") {
    const next = [...messages];
    next[next.length - 1] = { ...last, blocks: [...last.blocks, block] };
    return next;
  }
  return [...messages, { role: "assistant", blocks: [block] }];
}

/** 结束思考：≥ 阈值则向（必要时新建的）assistant 消息插入残留块。 */
export function endThinking(messages: Message[], st: ThinkingState, now: number): Message[] {
  const dur = now - st.startAt;
  if (dur < THINK_RESIDUE_MS) {
    return messages;
  }
  return appendBlock(messages, { kind: "thinking", durationMs: dur });
}

export function beginTool(messages: Message[], id: string, display: string, now: number): Message[] {
  return appendBlock(messages, { kind: "tool", id, display, done: false, startedAt: now });
}

/** 从后往前找 id 匹配且未完成的 tool block 回填结果与耗时。 */
export function finishTool(
  messages: Message[],
  id: string,
  body: string,
  ok: boolean,
  now: number
): Message[] {
  for (let mi = messages.length - 1; mi >= 0; mi--) {
    const msg = messages[mi];
    if (msg.role === "user") {
      break; // 防御：事件重放/乱序下也不跨轮回填（正常流 id 唯一，不可达）
    }
    if (msg.role !== "assistant") {
      continue;
    }
    for (let bi = msg.blocks.length - 1; bi >= 0; bi--) {
      const b = msg.blocks[bi];
      if (b.kind === "tool" && b.id === id && !b.done) {
        const durationMs = b.startedAt !== undefined ? now - b.startedAt : undefined;
        const next = [...messages];
        const blocks = [...msg.blocks];
        blocks[bi] = { ...b, body, ok, done: true, durationMs };
        next[mi] = { ...msg, blocks };
        return next;
      }
    }
  }
  return messages; // 无匹配（不应发生）：原样返回
}

/** done 事件（含 Esc 取消）：冻结当前轮未完成的工具块——agent 取消路径
 *  不发 tool_result，不冻结则运行中的计时组件会永久滴答。ok:false 表
 *  示"未完成"而非执行失败；轮尾统计此前已把悬挂工具计到 now，语义一致。
 *  从后往前扫到本轮 user 边界为止，绝不跨轮；数组拷贝惰性进行。 */
export function settleHangingTools(messages: Message[], now: number): Message[] {
  let changed = false;
  let next = messages; // 惰性拷贝：出现首个冻结项才复制数组
  for (let mi = messages.length - 1; mi >= 0; mi--) {
    const m = messages[mi];
    if (m.role === "user") {
      break;
    }
    if (m.role !== "assistant") {
      continue;
    }
    const blocks = m.blocks.map((b) => {
      if (b.kind === "tool" && !b.done) {
        changed = true;
        return {
          ...b,
          done: true,
          ok: false,
          durationMs: b.startedAt !== undefined ? now - b.startedAt : undefined,
        };
      }
      return b;
    });
    if (changed) {
      if (next === messages) {
        next = [...messages];
      }
      next[mi] = { ...m, blocks };
    }
  }
  return next; // 无冻结项：同引用——省数组拷贝与一次自动滚动 effect 触发
}

/** done 事件：追加轮尾统计 system 消息。只统计当前轮（最后一条 user
 *  之后）：思考块求和；已定格工具取 durationMs，悬挂工具计到 now。
 *  本轮无任何 assistant 输出（如斜杠命令只回 system 行）时不加 footer。 */
export function appendTurnFooter(messages: Message[], turnStartAt: number, now: number): Message[] {
  let thinkMs = 0;
  let toolMs = 0;
  let sawAssistant = false;
  for (let mi = messages.length - 1; mi >= 0; mi--) {
    const m = messages[mi];
    if (m.role === "user") {
      break;
    }
    if (m.role !== "assistant") {
      continue;
    }
    sawAssistant = true;
    for (const b of m.blocks) {
      if (b.kind === "thinking") {
        thinkMs += b.durationMs;
      } else if (b.kind === "tool") {
        if (b.durationMs !== undefined) {
          toolMs += b.durationMs;
        } else if (b.startedAt !== undefined) {
          toolMs += now - b.startedAt;
        }
      }
    }
  }
  const total = now - turnStartAt;
  if (!sawAssistant) {
    return messages; // 斜杠命令轮：无 assistant 输出，不加 footer
  }
  const parts = [fmtDur(total)];
  if (thinkMs >= THINK_RESIDUE_MS || toolMs >= TOOL_DUR_MS) { // 思考块构造即 ≥ 阈值：前半 ≈ 存在任何思考块
    parts.push(`思考 ${fmtDur(thinkMs)}`, `工具 ${fmtDur(toolMs)}`);
  }
  const text =
    parts.length > 1
      ? `✓ 本轮完成 · ${parts[0]}（${parts.slice(1).join(" · ")}）`
      : `✓ 本轮完成 · ${parts[0]}`;
  return [...messages, { role: "system", blocks: [{ kind: "text", text }] }];
}

// ---- 事件归约辅助（自 App.tsx 迁入，仅 applyChatEvent 消费，不导出） ----

function appendDelta(messages: Message[], delta: string): Message[] {
  if (delta === "") {
    return messages;
  }
  const last = messages[messages.length - 1];
  if (last && last.role === "assistant") {
    const blocks = [...last.blocks];
    const lastBlock = blocks[blocks.length - 1];
    if (lastBlock && lastBlock.kind === "text") {
      blocks[blocks.length - 1] = { kind: "text", text: lastBlock.text + delta };
    } else {
      blocks.push({ kind: "text", text: delta });
    }
    const next = [...messages];
    next[next.length - 1] = { ...last, blocks };
    return next;
  }
  return [...messages, { role: "assistant", blocks: [{ kind: "text", text: delta }] }];
}

// 连续的 system 事件（如 Skill 加载提示）合并进同一条消息
function appendSystem(messages: Message[], text: string): Message[] {
  if (text === "") {
    return messages;
  }
  const last = messages[messages.length - 1];
  if (last && last.role === "system") {
    const prev = last.blocks.map((b) => (b.kind === "text" ? b.text : "")).join("\n");
    const next = [...messages];
    next[next.length - 1] = {
      role: "system",
      blocks: [{ kind: "text", text: prev === "" ? text : prev + "\n" + text }],
    };
    return next;
  }
  return [...messages, { role: "system", blocks: [{ kind: "text", text }] }];
}

/** 归约器状态：App 的 chat state 形状。 */
export interface ChatState {
  messages: Message[];
  thinking: ThinkingState | null;
}

/** 单事件归约：App 的 EventsOn 回调整体委托此处，保持纯函数可测
 *  （事件序列 → 块状态/时长/轮尾统计，spec §7 的测试策略）。
 *  now 与 turnStartAt 由调用方在 setChat 之外捕获，归约器自身无时钟。
 *  busy/refreshHosts 等副作用仍留在 App 侧。 */
export function applyChatEvent(chat: ChatState, ev: ChatEvent, now: number, turnStartAt: number): ChatState {
  let { messages, thinking } = chat;
  // 首个可见输出（或流终止）结束思考指示器，≥1s 留残留行
  const clearThinking = () => {
    if (thinking) {
      messages = endThinking(messages, thinking, now);
      thinking = null;
    }
  };
  switch (ev.type) {
    case "delta":
      clearThinking();
      messages = appendDelta(messages, ev.text ?? "");
      break;
    case "tool_start":
      clearThinking();
      messages = beginTool(messages, ev.id ?? "", ev.display ?? "", now);
      break;
    case "tool_result":
      // ok 是 omitempty：false 不出现在 JSON，真值判断而非 === true
      messages = finishTool(messages, ev.id ?? "", ev.body ?? "", Boolean(ev.ok), now);
      break;
    case "thinking":
      if (ev.start) {
        clearThinking(); // 异常重入：先留残留再起新思考（正常流 start/stop 成对）
        thinking = startThinking(now);
      } else {
        clearThinking(); // 流结束/出错/取消且尚无可见输出
      }
      break;
    case "text_end":
      clearThinking();
      break;
    case "error":
    case "warning":
    case "info":
      clearThinking();
      messages = appendSystem(messages, ev.text ?? "");
      break;
    case "done":
      clearThinking();
      messages = settleHangingTools(messages, now); // Esc 取消：冻结未完成工具的计时
      messages = appendTurnFooter(messages, turnStartAt, now);
      break;
    default:
      return chat; // step 等无操作事件：同引用，免一次重渲染
  }
  return { messages, thinking };
}

/** 8.2s / 1m03s；负值钳到 0。 */
export function fmtDur(ms: number): string {
  if (ms < 0) {
    ms = 0;
  }
  if (ms < 60_000) {
    return (ms / 1000).toFixed(1) + "s";
  }
  const s = Math.floor(ms / 1000);
  return `${Math.floor(s / 60)}m${String(s % 60).padStart(2, "0")}s`;
}

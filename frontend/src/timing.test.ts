// timing.test.ts 计时纯函数测试：全部注入 now，断言块结构与阈值行为。
import { describe, expect, it } from "vitest";
import {
  TOOL_DUR_MS,
  applyChatEvent,
  appendTurnFooter,
  beginTool,
  endThinking,
  finishTool,
  fmtDur,
  settleHangingTools,
  startThinking,
} from "./timing";
import type { ChatState } from "./timing";
import type { ChatEvent, Message } from "./types";

describe("endThinking", () => {
  it("不足阈值不留残留行", () => {
    const ms: Message[] = [{ role: "user", blocks: [{ kind: "text", text: "hi" }] }];
    const st = startThinking(1000);
    expect(endThinking(ms, st, 1500)).toBe(ms); // 原引用返回：未变更
  });
  it("达到阈值插入 ThinkingBlock（无 assistant 消息则新建）", () => {
    const st = startThinking(1000);
    const out = endThinking([{ role: "user", blocks: [] }], st, 2500);
    expect(out).toHaveLength(2);
    expect(out[1].role).toBe("assistant");
    expect(out[1].blocks[0]).toEqual({ kind: "thinking", durationMs: 1500 });
  });
});

describe("beginTool / finishTool", () => {
  it("beginTool 记 startedAt；finishTool 算 durationMs 并回填", () => {
    let ms = beginTool([{ role: "user", blocks: [] }], "t1", "Bash(ls)", 2000);
    ms = finishTool(ms, "t1", "out", true, 5800);
    const b = ms[1].blocks[0];
    expect(b).toMatchObject({ kind: "tool", id: "t1", done: true, ok: true, durationMs: 3800 });
  });
  it("无匹配 id 原样返回", () => {
    const ms: Message[] = [];
    expect(finishTool(ms, "nope", "x", true, 1)).toBe(ms);
  });
});

describe("appendTurnFooter", () => {
  it("只统计当前轮（最后一条 user 之后），悬挂工具计到 now", () => {
    let ms: Message[] = [{ role: "user", blocks: [] }];
    // 上一轮的残留（不应计入）
    ms = [...ms, { role: "assistant", blocks: [{ kind: "thinking", durationMs: 99_000 }] }];
    // 本轮
    ms = [...ms, { role: "user", blocks: [] }];
    const st = startThinking(10_000);
    ms = endThinking(ms, st, 12_000);
    ms = beginTool(ms, "t1", "run", 12_000);
    ms = finishTool(ms, "t1", "done", true, 15_000);
    ms = beginTool(ms, "t2", "hang", 15_000); // 未完成
    const out = appendTurnFooter(ms, 10_000, 20_000);
    const footer = out[out.length - 1];
    expect(footer.role).toBe("system");
    const text = footer.blocks[0].kind === "text" ? footer.blocks[0].text : "";
    expect(text).toContain("10.0s");
    expect(text).toContain("思考 2.0s");
    expect(text).toContain("工具 8.0s"); // 3s 完成 + 5s 悬挂
    expect(text).not.toContain("101.0s"); // 上一轮 99s 思考不得泄漏进本轮
  });
  it("本轮无 assistant 输出（斜杠命令）不加 footer", () => {
    const ms: Message[] = [
      { role: "user", blocks: [{ kind: "text", text: "/inspect" }] },
      { role: "system", blocks: [{ kind: "text", text: "可用命令…" }] },
    ];
    expect(appendTurnFooter(ms, 1000, 2000)).toBe(ms);
  });
});

describe("fmtDur", () => {
  it("秒级一位小数，分钟级 m:ss", () => {
    expect(fmtDur(8_200)).toBe("8.2s");
    expect(fmtDur(63_000)).toBe("1m03s");
    expect(fmtDur(0)).toBe("0.0s");
  });
});

describe("TOOL_DUR_MS 阈值语义", () => {
  it("阈值常量为 1000（渲染层据此决定是否显示）", () => {
    expect(TOOL_DUR_MS).toBe(1000);
  });
});

describe("settleHangingTools", () => {
  it("done 时冻结当前轮未完成工具（done+ok:false+耗时定格）", () => {
    let ms = beginTool([{ role: "user", blocks: [] }], "t1", "run", 1000);
    ms = settleHangingTools(ms, 4000);
    expect(ms[1].blocks[0]).toMatchObject({ kind: "tool", id: "t1", done: true, ok: false, durationMs: 3000 });
  });
  it("已完成工具与既往轮不受影响", () => {
    let ms: Message[] = [{ role: "user", blocks: [] }];
    ms = [...ms, { role: "assistant", blocks: [{ kind: "tool", id: "old", display: "x", done: true, ok: true, durationMs: 5 }] }];
    ms = [...ms, { role: "user", blocks: [] }];
    ms = beginTool(ms, "t1", "run", 2000);
    const out = settleHangingTools(ms, 3000);
    expect(out[1].blocks[0]).toMatchObject({ id: "old", durationMs: 5 }); // 既往轮原样
    expect(out[3].blocks[0]).toMatchObject({ id: "t1", done: true, durationMs: 1000 });
  });
});

describe("阈值边界", () => {
  it("恰好 1000ms 保留思考残留（< 才忽略）", () => {
    const out = endThinking([{ role: "user", blocks: [] }], startThinking(1000), 2000);
    expect(out[out.length - 1].blocks[0]).toEqual({ kind: "thinking", durationMs: 1000 });
  });
  it("残留块落在同消息既有工具块之后（多步轮的真实形态）", () => {
    let ms = beginTool([{ role: "user", blocks: [] }], "t1", "run", 1000);
    ms = finishTool(ms, "t1", "out", true, 2000);
    ms = endThinking(ms, startThinking(2000), 3500);
    const blocks = ms[1].blocks;
    expect(blocks).toHaveLength(2);
    expect(blocks[0].kind).toBe("tool");
    expect(blocks[1]).toEqual({ kind: "thinking", durationMs: 1500 });
  });
});

describe("applyChatEvent 事件序列", () => {
  const ev = (partial: Partial<ChatEvent> & { type: ChatEvent["type"] }): ChatEvent =>
    partial as ChatEvent;

  it("text_end 后 done 双重清除不重复插残留", () => {
    let c: ChatState = { messages: [{ role: "user", blocks: [] }], thinking: startThinking(1000) };
    c = applyChatEvent(c, ev({ type: "text_end" }), 3000, 1000); // 清 thinking + 残留 2s
    c = applyChatEvent(c, ev({ type: "done" }), 3100, 1000);
    const blocks = c.messages[1].blocks;
    expect(blocks.filter((b) => b.kind === "thinking")).toHaveLength(1);
  });

  it("思考中取消（无任何输出直接 done）：残留 + footer", () => {
    let c: ChatState = { messages: [{ role: "user", blocks: [] }], thinking: startThinking(5000) };
    c = applyChatEvent(c, ev({ type: "done" }), 9000, 5000);
    expect(c.thinking).toBeNull();
    const blocks = c.messages[1].blocks;
    expect(blocks[0]).toEqual({ kind: "thinking", durationMs: 4000 });
    const footer = c.messages[c.messages.length - 1];
    const text = footer.blocks[0].kind === "text" ? footer.blocks[0].text : "";
    expect(text).toContain("思考 4.0s");
  });

  it("工具执行中取消（done）：工具冻结 ok:false + footer 计入", () => {
    let c: ChatState = { messages: [{ role: "user", blocks: [] }], thinking: null };
    c = applyChatEvent(c, ev({ type: "tool_start", id: "t1", display: "run" }), 1000, 500);
    c = applyChatEvent(c, ev({ type: "done" }), 4000, 500);
    expect(c.messages[1].blocks[0]).toMatchObject({ kind: "tool", id: "t1", done: true, ok: false, durationMs: 3000 });
    const text = c.messages[c.messages.length - 1].blocks[0];
    expect(text.kind === "text" ? text.text : "").toContain("工具 3.0s");
  });

  it("thinking:start 重入（异常流）：先留残留再起新思考", () => {
    let c: ChatState = { messages: [{ role: "user", blocks: [] }], thinking: startThinking(1000) };
    c = applyChatEvent(c, ev({ type: "thinking", start: true }), 2500, 1000);
    expect(c.messages[1].blocks[0]).toEqual({ kind: "thinking", durationMs: 1500 });
    expect(c.thinking).not.toBeNull();
    expect(c.thinking!.startAt).toBe(2500);
  });

  it("思考→delta：残留块在文本块之前（同消息内顺序）", () => {
    let c: ChatState = { messages: [{ role: "user", blocks: [] }], thinking: startThinking(1000) };
    c = applyChatEvent(c, ev({ type: "delta", text: "hi" }), 3000, 1000);
    const blocks = c.messages[1].blocks;
    expect(blocks).toHaveLength(2);
    expect(blocks[0]).toEqual({ kind: "thinking", durationMs: 2000 });
    expect(blocks[1]).toEqual({ kind: "text", text: "hi" });
  });

  it("finishTool 不跨轮回填（防御断言）", () => {
    let ms = beginTool([{ role: "user", blocks: [] }], "t1", "run", 1000);
    ms = [...ms, { role: "user", blocks: [] }]; // 新一轮开始，旧工具未完成
    expect(finishTool(ms, "t1", "late", true, 5000)).toBe(ms); // 同引用：不跨轮
  });

  it("settleHangingTools 无未完成块时同引用返回", () => {
    const ms: Message[] = [{ role: "assistant", blocks: [{ kind: "text", text: "hi" }] }];
    expect(settleHangingTools(ms, 1000)).toBe(ms);
  });
});

// types.ts 是前端侧的契约类型：与 internal/webapi（hosts/settings/xshell
// 与 hub.go 的 chatEvent）的 JSON 形状一一对应。注意后端布尔字段
// omitempty（ok:false / start:false 不出现在 JSON），判断一律用真值而非
// === true。

/** agent:event 的统一事件包（webapi chatEvent）。 */
export interface ChatEvent {
  type:
    | "delta"
    | "text_end"
    | "tool_start"
    | "tool_result"
    | "step"
    | "thinking"
    | "error"
    | "warning"
    | "info"
    | "done";
  id?: string;
  text?: string;
  display?: string;
  body?: string;
  ok?: boolean; // omitempty：false 不出现
  start?: boolean; // omitempty：false 不出现
  current?: number;
  total?: number;
  hosts?: string[];
}

/** GET /api/hosts 返回的侧栏行数据（webapi hostView）。 */
export interface HostView {
  id: string;
  name: string;
  addr: string;
  port: number;
  user: string;
  tags: string[];
  os: string;
  folder: string; // 斜杠路径目录（空=根）；侧栏树分组用
  status: "unknown" | "ok" | "fail";
  authMethod: string;
  keyPath: string;
  passwordRef: string;
}

/** HostForm 提交（webapi hostReq）。含密码字段——严禁 console.log。 */
export interface HostReq {
  id?: string;
  name: string;
  addr: string;
  port?: number;
  user: string;
  tags: string[];
  folder?: string;
  authMethod: "key" | "password";
  /** 仅供服务端内部沿用（keysDir 内白名单校验）；前端表单不再直填，私钥一律 keyData 上传 */
  keyPath?: string;
  keyData?: string; // base64 私钥内容（上传模式；keyPath 由服务端自定）
  password?: string;
  keyPassphrase?: string;
}

/** agent:confirm 的 reqs 元素——后端 ui.ConfirmRequest 无 json tag，PascalCase。 */
export interface ConfirmRequestView {
  ToolName: string;
  Summary: string;
  HighRisk: boolean;
  Hosts?: string[];
}

/** GET/PUT /api/settings（webapi settingsView）。apiKey 为掩码时表示未修改。 */
export interface SettingsView {
  apiKey: string;
  baseUrl: string;
  model: string;
}

/** 一次工具调用：tool_start 追加，tool_result 回填 body/ok/done。
 *  startedAt/durationMs 为 performance.now() 域的计时点/耗时（02 模块）。 */
export interface ToolBlock {
  kind: "tool";
  id: string;
  display: string;
  body?: string;
  ok?: boolean;
  done: boolean;
  startedAt?: number;
  durationMs?: number;
}

export interface TextBlock {
  kind: "text";
  text: string;
}

/** 思考阶段残留行：✻ 思考 N s（<1s 的思考不生成）。 */
export interface ThinkingBlock {
  kind: "thinking";
  durationMs: number;
}

export type Block = ToolBlock | ThinkingBlock | TextBlock;

/** 思考指示器活动态（App 内部，不落消息流）。 */
export interface ThinkingState {
  startAt: number;
  verb: string;
}

export interface Message {
  role: "user" | "assistant" | "system";
  blocks: Block[];
}

/** Xshell 导入预览行（webapi xshellRowView）。name 已是重名自动加
 *  后缀后的最终名；duplicate 行勾选框禁用（无覆盖语义）。 */
export interface XshellRowView {
  name: string;
  host: string;
  port: number;
  user: string;
  authMethod: string;
  keyPath: string;
  tags: string[];
  folder: string; // Source 的目录部分（斜杠路径）；根会话为空——导入即归位
  source: string;
  duplicate: boolean;
}

/** GET /api/users 行（webapi userView）。无 PwHash——后端不回显哈希。 */
export interface UserView {
  username: string;
  role: string;
  createdAt: string;
}

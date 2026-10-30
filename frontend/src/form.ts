// form.ts 主机表单的纯函数（从 HostForm 提取以便单测）。
/** 逗号（中英文）分隔的标签串 → 去空白、去重、保序的标签数组。 */
export function parseTags(raw: string): string[] {
  return [
    ...new Set(
      raw
        .split(/[,，]/)
        .map((t) => t.trim())
        .filter((t) => t !== "")
    ),
  ];
}

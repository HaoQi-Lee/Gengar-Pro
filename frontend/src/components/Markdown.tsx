// Markdown.tsx assistant 文本块的 Markdown 渲染（react-markdown + GFM）：
// 输出 React 元素而非 innerHTML，且默认忽略原始 HTML——模型输出可夹带
// 任意文本，无 XSS 面。链接新标签打开；代码块不做语法高亮（样式见 .md）。
// memo（评审 ④-3）：流式 delta 只重渲染末块，历史块的 Markdown 解析
// 结果按 text 引用缓存。
import { memo } from "react";
import Markdown from "react-markdown";
import remarkGfm from "remark-gfm";

const MarkdownView = memo(function MarkdownView({ text }: { text: string }) {
  return (
    <div className="md">
      <Markdown
        remarkPlugins={[remarkGfm]}
        components={{
          a: (props) => <a {...props} target="_blank" rel="noopener noreferrer" />,
        }}
      >
        {text}
      </Markdown>
    </div>
  );
});

export default MarkdownView;

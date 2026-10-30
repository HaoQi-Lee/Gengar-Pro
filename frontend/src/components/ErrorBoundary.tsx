// ErrorBoundary.tsx 渲染异常降级（评审 ④-2）：任何组件树的渲染异常
// 显示错误卡片而非整页白屏。reset 借 key 重挂载语义（error 状态在
// key 变化时由 React 丢弃重建）。
import { Component, type ErrorInfo, type ReactNode } from "react";

interface Props {
  children: ReactNode;
  /** 降级卡片标题（区分对话区/终端区等挂载位） */
  label: string;
}

interface State {
  error: Error | null;
}

export default class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // 开发期可见；生产内网无采集通道，console 足够定位
    console.error("ErrorBoundary:", this.props.label, error, info.componentStack);
  }

  render() {
    if (this.state.error !== null) {
      return (
        <div className="eb-fallback">
          <div className="eb-title">⚠ {this.props.label}渲染出错</div>
          <div className="eb-msg">{String(this.state.error.message || this.state.error)}</div>
          <div className="eb-ops">
            <button className="btn ghost small" onClick={() => this.setState({ error: null })}>
              重试渲染
            </button>
          </div>
        </div>
      );
    }
    return this.props.children;
  }
}

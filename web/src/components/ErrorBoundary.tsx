import { Component, type ErrorInfo, type ReactNode } from 'react';

interface State {
  failed: boolean;
}

/** Last-resort UI for render failures; Owlwatch keeps diagnostics local. */
export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { failed: false };

  static getDerivedStateFromError(): State {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo): void {
    console.error('owlwatch UI crashed', error, info.componentStack);
  }

  render(): ReactNode {
    if (!this.state.failed) return this.props.children;
    return (
      <main className="boot-state" role="alert">
        <h1>owlwatch</h1>
        <p>The dashboard encountered an unexpected error.</p>
        <button type="button" onClick={() => window.location.reload()}>
          Reload dashboard
        </button>
      </main>
    );
  }
}

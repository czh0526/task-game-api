import type { ButtonHTMLAttributes, ReactNode } from "react";
import type { TaskKind, TaskStatus } from "taskgame-client";
import { KIND_LABEL, STATUS_LABEL } from "./format";

export function cx(...parts: Array<string | false | null | undefined>): string {
  return parts.filter(Boolean).join(" ");
}

export function Screen({ title, lede, children }: { title: string; lede?: string; children: ReactNode }) {
  return (
    <section className="screen">
      <header className="screen-head">
        <h1>{title}</h1>
        {lede ? <p>{lede}</p> : null}
      </header>
      {children}
    </section>
  );
}

export function ErrorNote({ message }: { message: string | null | undefined }) {
  if (!message) return null;
  return (
    <p className="banner error" role="alert">
      {message}
    </p>
  );
}

export function OkNote({ message }: { message: string | null | undefined }) {
  if (!message) return null;
  return <p className="banner ok">{message}</p>;
}

export function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="field">
      <span>{label}</span>
      {children}
      {hint ? <small>{hint}</small> : null}
    </label>
  );
}

export function Button({
  kind = "secondary",
  pending = false,
  className,
  children,
  disabled,
  ...rest
}: ButtonHTMLAttributes<HTMLButtonElement> & {
  kind?: "primary" | "secondary" | "danger" | "ghost";
  pending?: boolean;
}) {
  return (
    <button {...rest} className={cx("btn", kind, className)} disabled={disabled || pending}>
      {pending ? "处理中…" : children}
    </button>
  );
}

export function StatusPill({ status }: { status: TaskStatus }) {
  return (
    <span className="pill" data-status={status}>
      {STATUS_LABEL[status]} <span className="en">{status}</span>
    </span>
  );
}

export function KindPill({ kind }: { kind: TaskKind }) {
  return (
    <span className="pill kind">
      {KIND_LABEL[kind]} <span className="en">{kind}</span>
    </span>
  );
}

export function IdText({ value }: { value: string | null | undefined }) {
  if (!value) return <span className="muted">—</span>;
  return (
    <code className="mono" title={value}>
      {value}
    </code>
  );
}

export function ActionRow({
  title,
  reason,
  children,
}: {
  title: string;
  reason: string | null;
  children: ReactNode;
}) {
  return (
    <div className="action">
      <div className="action-top">
        <strong>{title}</strong>
        {children}
      </div>
      {reason ? <p className="hint">{reason}</p> : null}
    </div>
  );
}

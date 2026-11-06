import { useEffect, useRef, useState } from "react";
import type { ListTasksQuery, Task, TaskKind, TaskStatus } from "taskgame-client";
import { useApp } from "../context";
import { KIND_LABEL, STATUS_LABEL, formatCredits, formatError, isAbort } from "../format";
import { href } from "../route";
import { ErrorNote, KindPill, Screen, StatusPill } from "../ui";

const STATUSES: TaskStatus[] = ["draft", "open", "active", "in_review", "disputed", "settled", "cancelled"];
const KINDS: TaskKind[] = ["exclusive", "competitive", "collaborative"];

type Tab = "all" | "published" | "open" | "mine";

export function TaskList() {
  const { client, clientError, settings } = useApp();
  const [items, setItems] = useState<Task[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  const [status, setStatus] = useState<"" | TaskStatus>("");
  const [kind, setKind] = useState<"" | TaskKind>("");
  const [tab, setTab] = useState<Tab>("all");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reloadToken, setReloadToken] = useState(0);
  const requestSeq = useRef(0);

  useEffect(() => {
    if (!client) return;
    const seq = ++requestSeq.current;
    const ac = new AbortController();
    setLoading(true);
    setError(null);
    const query: ListTasksQuery = { limit: 50 };
    if (status) query.status = status;
    if (kind) query.kind = kind;
    client
      .listTasks(query, { signal: ac.signal })
      .then((page) => {
        if (ac.signal.aborted || seq !== requestSeq.current) return;
        setItems(page.items);
        setNextCursor(page.next_cursor || null);
      })
      .catch((err: unknown) => {
        if (isAbort(err) || seq !== requestSeq.current) return;
        setError(formatError(err));
      })
      .finally(() => {
        if (!ac.signal.aborted && seq === requestSeq.current) setLoading(false);
      });
    return () => ac.abort();
  }, [client, status, kind, reloadToken]);

  async function loadMore() {
    if (!client || !nextCursor) return;
    const seq = requestSeq.current;
    const cursor = nextCursor;
    setLoading(true);
    setError(null);
    try {
      const query: ListTasksQuery = { limit: 50, cursor };
      if (status) query.status = status;
      if (kind) query.kind = kind;
      const page = await client.listTasks(query);
      if (seq !== requestSeq.current) return;
      setItems((prev) => [...prev, ...page.items]);
      setNextCursor(page.next_cursor || null);
    } catch (err) {
      if (seq !== requestSeq.current) return;
      setError(formatError(err));
    } finally {
      if (seq === requestSeq.current) setLoading(false);
    }
  }

  const visible = items.filter((task) => matchesTab(task, tab, settings.userId));

  return (
    <Screen
      title="任务列表"
      lede="服务端返回整个市场。下面的页签只过滤已经加载的任务。状态和类型会传给 listTasks。"
    >
      <ErrorNote message={client ? error : clientError} />
      <div className="toolbar">
        <div className="tabs" role="tablist">
          {(
            [
              ["all", "全部"],
              ["published", "我发布的"],
              ["open", "可接的"],
              ["mine", "我参与的"],
            ] as const
          ).map(([value, label]) => (
            <button key={value} type="button" className={tab === value ? "tab on" : "tab"} onClick={() => setTab(value)}>
              {label}
            </button>
          ))}
        </div>
        <label className="inline">
          状态
          <select
            value={status}
            onChange={(event) => {
              const value = event.target.value;
              setStatus(STATUSES.find((item) => item === value) ?? "");
            }}
          >
            <option value="">全部</option>
            {STATUSES.map((item) => (
              <option key={item} value={item}>
                {STATUS_LABEL[item]} {item}
              </option>
            ))}
          </select>
        </label>
        <label className="inline">
          类型
          <select
            value={kind}
            onChange={(event) => {
              const value = event.target.value;
              setKind(KINDS.find((item) => item === value) ?? "");
            }}
          >
            <option value="">全部</option>
            {KINDS.map((item) => (
              <option key={item} value={item}>
                {KIND_LABEL[item]} {item}
              </option>
            ))}
          </select>
        </label>
        <button type="button" className="btn ghost" onClick={() => setReloadToken((value) => value + 1)}>
          刷新
        </button>
      </div>

      {visible.length === 0 && !loading ? <p className="empty">这一页没有符合的任务。</p> : null}
      <ul className="task-list">
        {visible.map((task) => (
          <li key={task.id}>
            <a href={href.task(task.id)}>
              <div className="task-top">
                <StatusPill status={task.status} />
                <KindPill kind={task.kind} />
                <span className="relation">{relationLabel(task, settings.userId)}</span>
              </div>
              <h2>{task.title}</h2>
              <p className="meta">
                <span>{formatCredits(task.payment.amount)}</span>
                <span className="en">{task.payment.mode}</span>
                <code className="mono">{task.id}</code>
              </p>
            </a>
          </li>
        ))}
      </ul>
      <div className="toolbar">
        <button type="button" className="btn secondary" disabled={!nextCursor || loading} onClick={() => void loadMore()}>
          {loading ? "加载中…" : nextCursor ? "加载更多" : "没有更多"}
        </button>
        <a className="btn primary" href={href.newTask}>
          创建任务
        </a>
      </div>
    </Screen>
  );
}

function matchesTab(task: Task, tab: Tab, userId: string): boolean {
  if (tab === "all") return true;
  if (tab === "published") return task.publisher_id === userId;
  if (tab === "mine") {
    if ((task.executor_ids ?? []).includes(userId)) return true;
    return (task.collaborative?.roles ?? []).some((role) => role.assignee_user_id === userId);
  }
  if (task.publisher_id === userId) return false;
  if ((task.executor_ids ?? []).includes(userId)) return false;
  if (task.kind === "collaborative") {
    return (
      (task.status === "open" || task.status === "active") &&
      (task.collaborative?.roles ?? []).some((role) => (role.status ?? "open") === "open")
    );
  }
  if (task.kind === "competitive") {
    const slots = task.competitive?.slots ?? 0;
    const taken = task.executor_ids?.length ?? 0;
    return (task.status === "open" || task.status === "active") && taken < slots;
  }
  return task.status === "open";
}

function relationLabel(task: Task, userId: string): string {
  if (task.publisher_id === userId) return "我发布";
  if ((task.executor_ids ?? []).includes(userId)) return "我参与";
  if ((task.collaborative?.roles ?? []).some((role) => role.assignee_user_id === userId)) return "我参与";
  return "";
}

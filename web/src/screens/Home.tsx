import { useEffect, useMemo, useRef, useState } from "react";
import type { ListTasksQuery, Task, TaskKind } from "taskgame-client";
import { useApp } from "../context";
import { formatCredits, formatError, formatTime, isAbort, PAYMENT_MODE_LABEL } from "../format";
import { href } from "../route";
import { ErrorNote, Screen, StatusPill } from "../ui";

const CATEGORIES: Array<{
  kind: TaskKind;
  short: string;
  title: string;
  en: string;
  description: string;
}> = [
  {
    kind: "exclusive",
    short: "独",
    title: "独占任务",
    en: "exclusive",
    description: "一位执行者独立承接，提交后由发布者确认。也可以按阶段交付。",
  },
  {
    kind: "competitive",
    short: "竞",
    title: "竞争任务",
    en: "competitive",
    description: "多人同时提交，发布者指定中标方案，赢家通吃全部托管。",
  },
  {
    kind: "collaborative",
    short: "协",
    title: "协作任务",
    en: "collaborative",
    description: "多个席位按角色分工，支持分别结算或全员到齐后统一结算。",
  },
];

const PREVIEW_LIMIT = 3;
type Scope = "all" | "available";

export function Home() {
  const { client, clientError, settings } = useApp();
  const [scope, setScope] = useState<Scope>("all");
  const [items, setItems] = useState<Task[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reloadToken, setReloadToken] = useState(0);
  const requestSeq = useRef(0);

  useEffect(() => {
    if (!client) return;
    const seq = ++requestSeq.current;
    const ac = new AbortController();
    const query: ListTasksQuery = { limit: 100 };
    if (scope === "available") query.assignable = "me";
    setLoading(true);
    setError(null);
    client
      .listTasks(query, { signal: ac.signal })
      .then((page) => {
        if (ac.signal.aborted || seq !== requestSeq.current) return;
        setItems(page.items);
      })
      .catch((err: unknown) => {
        if (isAbort(err) || seq !== requestSeq.current) return;
        setError(formatError(err));
      })
      .finally(() => {
        if (!ac.signal.aborted && seq === requestSeq.current) setLoading(false);
      });
    return () => ac.abort();
  }, [client, scope, reloadToken]);

  const grouped = useMemo(() => {
    const groups: Record<TaskKind, Task[]> = {
      exclusive: [],
      competitive: [],
      collaborative: [],
    };
    for (const task of items) groups[task.kind]?.push(task);
    for (const category of CATEGORIES) {
      groups[category.kind].sort((a, b) => b.created_at.localeCompare(a.created_at));
    }
    return groups;
  }, [items]);

  const stats = useMemo(
    () => ({
      total: items.length,
      open: items.filter((task) => task.status === "open").length,
      mine: items.filter((task) => isMine(task, settings.userId)).length,
    }),
    [items, settings.userId],
  );

  return (
    <Screen
      title="首页"
      lede="任务按玩法分成三类。这里展示最近的任务，点开卡片进入详情；「查看全部」会带着类型筛选进入任务列表。"
    >
      <ErrorNote message={client ? error : clientError} />

      <section className="home-hero">
        <div>
          <p className="eyebrow">任务市场</p>
          <h2>找到适合你的任务</h2>
          <p className="home-hero-copy">
            独占、竞争、协作三种玩法，托管与结算都是模拟积分。注册后即可承接，也可以发布自己的任务。
          </p>
          <div className="home-hero-actions">
            <a className="btn primary" href={href.tasks}>
              浏览任务
            </a>
            <a className="btn secondary" href={href.newTask}>
              创建任务
            </a>
          </div>
        </div>
        <div className="home-stats" aria-label="任务概览">
          <div className="stat">
            <strong>{stats.total}</strong>
            <span>任务总数</span>
          </div>
          <div className="stat">
            <strong>{stats.open}</strong>
            <span>招募中</span>
          </div>
          <div className="stat">
            <strong>{stats.mine}</strong>
            <span>与我相关</span>
          </div>
        </div>
      </section>

      <div className="toolbar home-toolbar">
        <div className="tabs" role="tablist" aria-label="任务范围">
          <button type="button" className={scope === "all" ? "tab on" : "tab"} onClick={() => setScope("all")}>
            全部任务
          </button>
          <button type="button" className={scope === "available" ? "tab on" : "tab"} onClick={() => setScope("available")}>
            只看可接
          </button>
        </div>
        <button type="button" className="btn ghost" onClick={() => setReloadToken((value) => value + 1)}>
          刷新
        </button>
        {loading ? <span className="hint">正在加载任务…</span> : null}
      </div>

      <div className="home-categories">
        {CATEGORIES.map((category) => {
          const tasks = grouped[category.kind] ?? [];
          return (
            <section className="category-card" key={category.kind} aria-labelledby={`category-${category.kind}`}>
              <header className="category-head">
                <span className="category-mark" aria-hidden="true">
                  {category.short}
                </span>
                <div>
                  <h2 id={`category-${category.kind}`}>
                    {category.title} <span className="en">{category.en}</span>
                  </h2>
                  <p>{category.description}</p>
                </div>
              </header>
              <div className="category-meta">
                <span>{tasks.length} 个任务</span>
                <a href={href.tasksByKind(category.kind)}>查看全部</a>
              </div>
              <ul className="task-list home-task-list">
                {tasks.length === 0 && !loading ? (
                  <li className="empty">当前范围内暂无{category.title}。</li>
                ) : null}
                {tasks.slice(0, PREVIEW_LIMIT).map((task) => (
                  <li key={task.id}>
                    <a href={href.task(task.id)}>
                      <div className="task-top">
                        <StatusPill status={task.status} />
                        {relationLabel(task, settings.userId) ? (
                          <span className="relation">{relationLabel(task, settings.userId)}</span>
                        ) : null}
                      </div>
                      <h3>{task.title}</h3>
                      <p className="meta">
                        <strong>{formatCredits(task.payment.amount)}</strong>
                        <span>{PAYMENT_MODE_LABEL[task.payment.mode] ?? task.payment.mode}</span>
                      </p>
                      <p className="home-task-deadline">接受截止 {formatTime(task.acceptance.deadline)}</p>
                    </a>
                  </li>
                ))}
              </ul>
              {tasks.length > PREVIEW_LIMIT ? (
                <a className="btn ghost home-more" href={href.tasksByKind(category.kind)}>
                  还有 {tasks.length - PREVIEW_LIMIT} 个任务
                </a>
              ) : null}
            </section>
          );
        })}
      </div>
    </Screen>
  );
}

function isMine(task: Task, userId: string): boolean {
  if (task.publisher_id === userId) return true;
  if ((task.executor_ids ?? []).includes(userId)) return true;
  return (task.collaborative?.roles ?? []).some((role) => role.assignee_user_id === userId);
}

function relationLabel(task: Task, userId: string): string {
  if (task.publisher_id === userId) return "我发布";
  if (isMine(task, userId)) return "我参与";
  return "";
}

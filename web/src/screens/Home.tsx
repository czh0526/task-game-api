import { useEffect, useMemo, useRef, useState } from "react";
import type { ListTasksQuery, Task, TaskKind } from "taskgame-client";
import { isSignedIn } from "../client";
import { useApp } from "../context";
import {
  executorRequirementLabel,
  formatCredits,
  formatError,
  formatTime,
  isAbort,
  PAYMENT_MODE_LABEL,
  RECURRENCE_LABEL,
  URGENCY_LABEL,
} from "../format";
import { href } from "../route";
import { ErrorNote, cx, Screen, StatusPill } from "../ui";

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
  const signedIn = isSignedIn(settings);
  const [scope, setScope] = useState<Scope>("all");
  const [items, setItems] = useState<Task[]>([]);
  const [activeKeyword, setActiveKeyword] = useState<string | null>(null);
  const [activeUrgency, setActiveUrgency] = useState<string | null>(null);
  const [activeRecurrence, setActiveRecurrence] = useState<string | null>(null);
  const [activeRequirement, setActiveRequirement] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reloadToken, setReloadToken] = useState(0);
  const requestSeq = useRef(0);

  useEffect(() => {
    if (!signedIn && scope === "available") setScope("all");
  }, [signedIn, scope]);

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

  const keywordCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const task of items) {
      for (const keyword of task.keywords ?? []) {
        counts.set(keyword, (counts.get(keyword) ?? 0) + 1);
      }
    }
    return Array.from(counts.entries())
      .map(([name, count]) => ({ name, count }))
      .sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
  }, [items]);

  const requirementCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const task of items) {
      for (const requirement of task.attributes?.executor_requirements ?? []) {
        counts.set(requirement, (counts.get(requirement) ?? 0) + 1);
      }
    }
    return Array.from(counts.entries())
      .map(([name, count]) => ({ name, count }))
      .sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
  }, [items]);

  // Drop selections that no longer exist after a refresh.
  useEffect(() => {
    if (activeKeyword && !keywordCounts.some((item) => item.name === activeKeyword)) setActiveKeyword(null);
    if (activeRequirement && !requirementCounts.some((item) => item.name === activeRequirement)) setActiveRequirement(null);
  }, [activeKeyword, keywordCounts, activeRequirement, requirementCounts]);

  const visibleItems = useMemo(
    () =>
      items.filter((task) => {
        if (activeKeyword && !(task.keywords ?? []).includes(activeKeyword)) return false;
        if (activeRequirement && !(task.attributes?.executor_requirements ?? []).includes(activeRequirement)) return false;
        if (activeUrgency && (task.attributes?.urgency ?? "other") !== activeUrgency) return false;
        if (activeRecurrence && (task.attributes?.recurrence ?? "one_time") !== activeRecurrence) return false;
        return true;
      }),
    [items, activeKeyword, activeRequirement, activeUrgency, activeRecurrence],
  );

  const grouped = useMemo(() => {
    const groups: Record<TaskKind, Task[]> = {
      exclusive: [],
      competitive: [],
      collaborative: [],
    };
    for (const task of visibleItems) groups[task.kind]?.push(task);
    for (const category of CATEGORIES) {
      groups[category.kind].sort((a, b) => b.created_at.localeCompare(a.created_at));
    }
    return groups;
  }, [visibleItems]);

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
      lede="不用登录也能浏览任务市场。任务按玩法分成三类；登录后可查看「可接任务」并参与承接。"
    >
      <ErrorNote message={client ? error : clientError} />

      <section className="home-hero">
        <div>
          <p className="eyebrow">任务市场</p>
          <h2>找到适合你的任务</h2>
          <p className="home-hero-copy">
            独占、竞争、协作三种玩法，托管与结算都是模拟积分。未登录可浏览，登录后即可承接或发布任务。
          </p>
          <div className="home-hero-actions">
            {signedIn ? (
              <>
                <a className="btn primary" href={href.tasks}>
                  浏览任务
                </a>
                <a className="btn secondary" href={href.newTask}>
                  创建任务
                </a>
              </>
            ) : (
              <>
                <a className="btn primary" href={href.login}>
                  登录 / 注册
                </a>
                <a className="btn secondary" href={href.tasks}>
                  浏览任务列表
                </a>
              </>
            )}
          </div>
        </div>
        <div className={cx("home-stats", !signedIn && "two")} aria-label="任务概览">
          <div className="stat">
            <strong>{stats.total}</strong>
            <span>任务总数</span>
          </div>
          <div className="stat">
            <strong>{stats.open}</strong>
            <span>招募中</span>
          </div>
          {signedIn ? (
            <div className="stat">
              <strong>{stats.mine}</strong>
              <span>与我相关</span>
            </div>
          ) : null}
        </div>
      </section>

      <div className="toolbar home-toolbar">
        <div className="tabs" role="tablist" aria-label="任务范围">
          <button type="button" className={scope === "all" ? "tab on" : "tab"} onClick={() => setScope("all")}>
            全部任务
          </button>
          {signedIn ? (
            <button type="button" className={scope === "available" ? "tab on" : "tab"} onClick={() => setScope("available")}>
              只看可接
            </button>
          ) : (
            <a className="tab" href={href.login}>
              登录后看可接任务
            </a>
          )}
        </div>
        <button type="button" className="btn ghost" onClick={() => setReloadToken((value) => value + 1)}>
          刷新
        </button>
        {loading ? <span className="hint">正在加载任务…</span> : null}
      </div>

      {keywordCounts.length > 0 ? (
        <section className="keyword-bar" aria-label="按关键字分类">
          <div className="keyword-bar-head">
            <h2>按关键字分类</h2>
            {activeKeyword ? (
              <button type="button" className="btn ghost" onClick={() => setActiveKeyword(null)}>
                清除筛选
              </button>
            ) : null}
          </div>
          <div className="keyword-chips">
            <button
              type="button"
              className={activeKeyword === null ? "keyword-chip on" : "keyword-chip"}
              onClick={() => setActiveKeyword(null)}
            >
              全部 <span>{items.length}</span>
            </button>
            {keywordCounts.map((item) => (
              <button
                type="button"
                key={item.name}
                className={activeKeyword === item.name ? "keyword-chip on" : "keyword-chip"}
                onClick={() => setActiveKeyword(item.name)}
              >
                {item.name} <span>{item.count}</span>
              </button>
            ))}
          </div>
          {activeKeyword ? <p className="hint">当前按「{activeKeyword}」筛选，下面三类任务只显示带这个关键字的任务。</p> : null}
        </section>
      ) : null}

      <section className="keyword-bar" aria-label="按任务属性分类">
        <div className="keyword-bar-head">
          <h2>按任务属性分类</h2>
          {activeUrgency || activeRecurrence || activeRequirement ? (
            <button
              type="button"
              className="btn ghost"
              onClick={() => {
                setActiveUrgency(null);
                setActiveRecurrence(null);
                setActiveRequirement(null);
              }}
            >
              清除属性筛选
            </button>
          ) : null}
        </div>
        <div className="attr-filter-row">
          <span className="attr-filter-label">时限</span>
          <button
            type="button"
            className={activeUrgency === null ? "keyword-chip on" : "keyword-chip"}
            onClick={() => setActiveUrgency(null)}
          >
            全部
          </button>
          {Object.entries(URGENCY_LABEL).map(([value, label]) => (
            <button
              key={value}
              type="button"
              className={activeUrgency === value ? "keyword-chip on" : "keyword-chip"}
              onClick={() => setActiveUrgency(value)}
            >
              {label}
            </button>
          ))}
        </div>
        <div className="attr-filter-row">
          <span className="attr-filter-label">周期</span>
          <button
            type="button"
            className={activeRecurrence === null ? "keyword-chip on" : "keyword-chip"}
            onClick={() => setActiveRecurrence(null)}
          >
            全部
          </button>
          {Object.entries(RECURRENCE_LABEL).map(([value, label]) => (
            <button
              key={value}
              type="button"
              className={activeRecurrence === value ? "keyword-chip on" : "keyword-chip"}
              onClick={() => setActiveRecurrence(value)}
            >
              {label}
            </button>
          ))}
        </div>
        {requirementCounts.length > 0 ? (
          <div className="attr-filter-row">
            <span className="attr-filter-label">完成者</span>
            <button
              type="button"
              className={activeRequirement === null ? "keyword-chip on" : "keyword-chip"}
              onClick={() => setActiveRequirement(null)}
            >
              全部
            </button>
            {requirementCounts.map((item) => (
              <button
                key={item.name}
                type="button"
                className={activeRequirement === item.name ? "keyword-chip on" : "keyword-chip"}
                onClick={() => setActiveRequirement(item.name)}
              >
                {executorRequirementLabel(item.name)} <span>{item.count}</span>
              </button>
            ))}
          </div>
        ) : null}
      </section>

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
                      {(task.keywords ?? []).length > 0 ? (
                        <span className="card-keywords">
                          {(task.keywords ?? []).map((keyword) => (
                            <span
                              key={keyword}
                              className={activeKeyword === keyword ? "card-keyword on" : "card-keyword"}
                              onClick={(event) => {
                                event.preventDefault();
                                setActiveKeyword(keyword);
                              }}
                            >
                              #{keyword}
                            </span>
                          ))}
                        </span>
                      ) : null}
                      <p className="meta">
                        <strong>{formatCredits(task.payment.amount)}</strong>
                        <span>{PAYMENT_MODE_LABEL[task.payment.mode] ?? task.payment.mode}</span>
                      </p>
                      {task.attributes ? (
                        <p className="home-task-attrs">
                          {task.attributes.location ? <span>位置 {task.attributes.location}</span> : null}
                          <span>时限 {URGENCY_LABEL[task.attributes.urgency ?? "other"] ?? task.attributes.urgency}</span>
                          <span>周期 {RECURRENCE_LABEL[task.attributes.recurrence ?? "one_time"] ?? task.attributes.recurrence}</span>
                        </p>
                      ) : null}
                      {(task.attributes?.executor_requirements ?? []).length > 0 ? (
                        <p className="home-task-requires">
                          需要：{(task.attributes?.executor_requirements ?? []).map(executorRequirementLabel).join("、")}
                        </p>
                      ) : null}
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

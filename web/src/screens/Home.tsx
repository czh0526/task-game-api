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

// Filters live in the URL hash after "#/home?" so a refresh or a shared link
// restores the exact same search. parseRoute ignores the query for /home.
type FilterParams = {
  q: string;
  scope: Scope;
  keyword: string | null;
  urgency: string | null;
  recurrence: string | null;
  requirement: string | null;
  lat: string;
  lng: string;
  radiusKm: string;
  sort: "recent" | "amount_desc" | "deadline_asc" | "distance_asc";
};

const SORTS = ["recent", "amount_desc", "deadline_asc", "distance_asc"] as const;

function readFilters(): FilterParams {
  const raw = window.location.hash.startsWith("#") ? window.location.hash.slice(1) : window.location.hash;
  const question = raw.indexOf("?");
  const params = question >= 0 ? new URLSearchParams(raw.slice(question + 1)) : new URLSearchParams();
  const sort = params.get("sort");
  return {
    q: params.get("q") ?? "",
    scope: params.get("scope") === "available" ? "available" : "all",
    keyword: params.get("keyword"),
    urgency: params.get("urgency"),
    recurrence: params.get("recurrence"),
    requirement: params.get("requirement"),
    lat: params.get("lat") ?? "",
    lng: params.get("lng") ?? "",
    radiusKm: params.get("radius") ?? "10",
    sort: SORTS.find((item) => item === sort) ?? "recent",
  };
}

function writeFilters(f: FilterParams): void {
  const params = new URLSearchParams();
  if (f.q.trim()) params.set("q", f.q.trim());
  if (f.scope === "available") params.set("scope", "available");
  if (f.keyword) params.set("keyword", f.keyword);
  if (f.urgency) params.set("urgency", f.urgency);
  if (f.recurrence) params.set("recurrence", f.recurrence);
  if (f.requirement) params.set("requirement", f.requirement);
  if (f.lat.trim()) params.set("lat", f.lat.trim());
  if (f.lng.trim()) params.set("lng", f.lng.trim());
  if (f.lat.trim() && f.lng.trim() && f.radiusKm.trim()) params.set("radius", f.radiusKm.trim());
  if (f.sort !== "recent") params.set("sort", f.sort);
  const text = params.toString();
  const next = `#/home${text ? `?${text}` : ""}`;
  if (window.location.hash !== next) window.history.replaceState(null, "", next);
}

export function Home() {
  const { client, clientError, settings } = useApp();
  const signedIn = isSignedIn(settings);
  const [initial] = useState(readFilters);
  const [searchText, setSearchText] = useState(initial.q);
  const [searchDraft, setSearchDraft] = useState(initial.q);
  const [scope, setScope] = useState<Scope>(initial.scope);
  const [items, setItems] = useState<Task[]>([]);
  const [facetItems, setFacetItems] = useState<Task[]>([]);
  const [activeKeyword, setActiveKeyword] = useState<string | null>(initial.keyword);
  const [activeUrgency, setActiveUrgency] = useState<string | null>(initial.urgency);
  const [activeRecurrence, setActiveRecurrence] = useState<string | null>(initial.recurrence);
  const [activeRequirement, setActiveRequirement] = useState<string | null>(initial.requirement);
  const [lat, setLat] = useState(initial.lat);
  const [lng, setLng] = useState(initial.lng);
  const [radiusKm, setRadiusKm] = useState(initial.radiusKm);
  const [sort, setSort] = useState<typeof initial.sort>(initial.sort);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reloadToken, setReloadToken] = useState(0);
  const requestSeq = useRef(0);

  useEffect(() => {
    if (!signedIn && scope === "available") setScope("all");
  }, [signedIn, scope]);

  // Build the query from all active filters. Filtering happens in SQL across
  // every task, not in the browser over a single 100-row page.
  const query = useMemo<ListTasksQuery>(() => {
    const q: ListTasksQuery = { limit: 100, sort };
    if (searchText.trim()) q.q = searchText.trim();
    if (scope === "available") q.assignable = "me";
    if (activeKeyword) q.keyword = activeKeyword;
    if (activeUrgency) q.urgency = activeUrgency as ListTasksQuery["urgency"];
    if (activeRecurrence) q.recurrence = activeRecurrence as ListTasksQuery["recurrence"];
    if (activeRequirement) q.executor_requirement = activeRequirement;
    const latitude = Number(lat);
    const longitude = Number(lng);
    const radius = Number(radiusKm);
    if (lat.trim() && lng.trim() && Number.isFinite(latitude) && Number.isFinite(longitude) && latitude >= -90 && latitude <= 90 && longitude >= -180 && longitude <= 180 && radius > 0) {
      q.latitude = latitude;
      q.longitude = longitude;
      q.radius_km = radius;
      if (sort === "recent") q.sort = "distance_asc";
    }
    return q;
  }, [searchText, scope, activeKeyword, activeUrgency, activeRecurrence, activeRequirement, lat, lng, radiusKm, sort]);

  // Persist the active filters to the URL so refresh/back and shared links work.
  useEffect(() => {
    writeFilters({
      q: searchText,
      scope,
      keyword: activeKeyword,
      urgency: activeUrgency,
      recurrence: activeRecurrence,
      requirement: activeRequirement,
      lat,
      lng,
      radiusKm,
      sort,
    });
  }, [searchText, scope, activeKeyword, activeUrgency, activeRecurrence, activeRequirement, lat, lng, radiusKm, sort]);

  // Unfiltered baseline used only to build the filter option lists, so options
  // don't disappear while a filter is active.
  useEffect(() => {
    if (!client) return;
    const ac = new AbortController();
    client
      .listTasks({ limit: 100 }, { signal: ac.signal })
      .then((page) => {
        if (!ac.signal.aborted) setFacetItems(page.items);
      })
      .catch(() => {});
    return () => ac.abort();
  }, [client, reloadToken]);

  useEffect(() => {
    if (!client) return;
    const seq = ++requestSeq.current;
    const ac = new AbortController();
    setLoading(true);
    setError(null);
    client
      .listTasks(query, { signal: ac.signal })
      .then((page) => {
        if (ac.signal.aborted || seq !== requestSeq.current) return;
        setItems(page.items);
        setTotal(page.total ?? page.items.length);
      })
      .catch((err: unknown) => {
        if (isAbort(err) || seq !== requestSeq.current) return;
        setError(formatError(err));
      })
      .finally(() => {
        if (!ac.signal.aborted && seq === requestSeq.current) setLoading(false);
      });
    return () => ac.abort();
  }, [client, query, reloadToken]);

  const keywordCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const task of facetItems) {
      for (const keyword of task.keywords ?? []) {
        counts.set(keyword, (counts.get(keyword) ?? 0) + 1);
      }
    }
    return Array.from(counts.entries())
      .map(([name, count]) => ({ name, count }))
      .sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
  }, [facetItems]);

  const requirementCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const task of facetItems) {
      for (const requirement of task.attributes?.executor_requirements ?? []) {
        counts.set(requirement, (counts.get(requirement) ?? 0) + 1);
      }
    }
    return Array.from(counts.entries())
      .map(([name, count]) => ({ name, count }))
      .sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
  }, [facetItems]);

  // The server returns only tasks matching the active filters.
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
      total: total,
      open: items.filter((task) => task.status === "open").length,
      mine: items.filter((task) => isMine(task, settings.userId)).length,
    }),
    [total, items, settings.userId],
  );

  const anyFilter = Boolean(searchText.trim() || activeKeyword || activeUrgency || activeRecurrence || activeRequirement || (lat.trim() && lng.trim()));

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

      <form
        className="home-search"
        role="search"
        onSubmit={(event) => {
          event.preventDefault();
          setSearchText(searchDraft);
        }}
      >
        <input
          type="search"
          value={searchDraft}
          onChange={(event) => setSearchDraft(event.target.value)}
          placeholder="搜索任务的标题、描述或关键字，回车搜索"
          aria-label="全文搜索任务"
        />
        <button type="submit" className="btn primary">
          搜索
        </button>
        {searchText.trim() ? (
          <button
            type="button"
            className="btn ghost"
            onClick={() => {
              setSearchText("");
              setSearchDraft("");
            }}
          >
            清除搜索
          </button>
        ) : null}
      </form>
      {searchText.trim() ? <p className="hint">正在全文搜索「{searchText.trim()}」，下面的筛选会在结果中继续收窄。</p> : null}

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
        <label className="home-sort">
          排序
          <select value={sort} onChange={(event) => setSort(event.target.value as typeof sort)}>
            <option value="recent">最新发布</option>
            <option value="amount_desc">报酬最高</option>
            <option value="deadline_asc">截止最近</option>
            <option value="distance_asc">距离最近</option>
          </select>
        </label>
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
              全部 <span>{total}</span>
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

      <section className="keyword-bar" aria-label="按距离筛选">
        <div className="keyword-bar-head">
          <h2>按距离筛选</h2>
          {(lat.trim() && lng.trim()) || anyFilter ? (
            <button
              type="button"
              className="btn ghost"
              onClick={() => {
                setLat("");
                setLng("");
                setRadiusKm("10");
              }}
            >
              清除位置
            </button>
          ) : null}
        </div>
        <p className="hint">填入中心点经纬度与半径，只显示该范围内的任务；任务需在创建时标记坐标。</p>
        <div className="distance-row">
          <label>
            纬度
            <input
              type="number"
              step="any"
              min={-90}
              max={90}
              placeholder="39.9042"
              value={lat}
              onChange={(event) => setLat(event.target.value)}
            />
          </label>
          <label>
            经度
            <input
              type="number"
              step="any"
              min={-180}
              max={180}
              placeholder="116.4074"
              value={lng}
              onChange={(event) => setLng(event.target.value)}
            />
          </label>
          <label>
            半径（公里）
            <input
              type="number"
              step="any"
              min={0}
              placeholder="10"
              value={radiusKm}
              onChange={(event) => setRadiusKm(event.target.value)}
            />
          </label>
          <button type="button" className="btn secondary" onClick={() => setReloadToken((value) => value + 1)}>
            按此位置查找
          </button>
        </div>
        <button
          type="button"
          className="btn ghost"
          onClick={() => {
            if (!navigator.geolocation) return;
            navigator.geolocation.getCurrentPosition(
              (pos) => {
                setLat(pos.coords.latitude.toFixed(4));
                setLng(pos.coords.longitude.toFixed(4));
              },
              () => {},
            );
          }}
        >
          使用我的当前位置
        </button>
        {lat.trim() && lng.trim() ? (
          <p className="hint">正在按「{lat}, {lng}」周围 {radiusKm || "10"} 公里筛选，共 {total} 个任务匹配。</p>
        ) : null}
      </section>

      {anyFilter ? (
        <button
          type="button"
          className="btn ghost home-clear-all"
          onClick={() => {
            setSearchText("");
            setSearchDraft("");
            setActiveKeyword(null);
            setActiveUrgency(null);
            setActiveRecurrence(null);
            setActiveRequirement(null);
            setLat("");
            setLng("");
          }}
        >
          清除全部筛选
        </button>
      ) : null}

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
                          {task.attributes.latitude != null && task.attributes.longitude != null ? (
                            <span>坐标 {task.attributes.latitude.toFixed(2)}, {task.attributes.longitude.toFixed(2)}</span>
                          ) : task.attributes.location ? (
                            <span>位置 {task.attributes.location}</span>
                          ) : null}
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

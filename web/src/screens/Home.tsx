import { useEffect, useMemo, useRef, useState } from "react";
import type { ListTasksQuery, Task, TaskKind } from "taskgame-client";
import { isSignedIn } from "../client";
import { useApp } from "../context";
import {
  executorRequirementLabel,
  formatCredits,
  formatError,
  isAbort,
  RECURRENCE_LABEL,
  URGENCY_LABEL,
} from "../format";
import { href } from "../route";
import { ErrorNote, Screen } from "../ui";

const CATEGORIES: Array<{
  kind: TaskKind;
  short: string;
  title: string;
  en: string;
  description: string;
  hot: string[];
}> = [
  {
    kind: "exclusive",
    short: "单",
    title: "单人任务",
    en: "exclusive",
    description: "一位执行者独立承接，提交后由发布者确认，也可按阶段交付。",
    hot: ["设计", "写作", "翻译", "开发", "running", "urgent"],
  },
  {
    kind: "competitive",
    short: "竞",
    title: "竞争任务",
    en: "competitive",
    description: "多人同时提交，发布者指定中标方案，赢家通吃全部托管。",
    hot: ["设计", "方案", "创意", "logo", "命名"],
  },
  {
    kind: "collaborative",
    short: "协",
    title: "协作任务",
    en: "collaborative",
    description: "多个席位按角色分工，支持分别结算或全员到齐后统一结算。",
    hot: ["开发", "写作", "推广", "策划", "design"],
  },
];

const PREVIEW_LIMIT = 6;
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

  // Keyword entries grouped by task kind, used by the "all categories" matrix.
  const keywordsByKind = useMemo(() => {
    const counts: Record<TaskKind, Map<string, number>> = {
      exclusive: new Map(),
      competitive: new Map(),
      collaborative: new Map(),
    };
    for (const task of facetItems) {
      for (const keyword of task.keywords ?? []) {
        counts[task.kind]?.set(keyword, (counts[task.kind]?.get(keyword) ?? 0) + 1);
      }
    }
    const out: Record<TaskKind, string[]> = { exclusive: [], competitive: [], collaborative: [] };
    for (const kind of Object.keys(counts) as TaskKind[]) {
      out[kind] = Array.from((counts[kind] ?? new Map()).entries())
        .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
        .slice(0, 14)
        .map(([name]) => name);
    }
    return out;
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

  const submitSearch = (event: React.FormEvent) => {
    event.preventDefault();
    setSearchText(searchDraft);
  };

  const clearAll = () => {
    setSearchText("");
    setSearchDraft("");
    setActiveKeyword(null);
    setActiveUrgency(null);
    setActiveRecurrence(null);
    setActiveRequirement(null);
    setLat("");
    setLng("");
  };

  return (
    <Screen title="任务市场" lede="不用登录也能逛任务市场。按玩法、关键字和任务属性快速找到适合你的任务。">
      <ErrorNote message={client ? error : clientError} />

      <div className="portal">
        {/* 1. 顶部：自有品牌 logo + 城市站位 + 大搜索框 + 快捷入口 */}
        <header className="portal-topbar">
          <a className="portal-logo" href={href.home} aria-label="任务游戏首页">
            <span className="portal-logo-mark" aria-hidden="true">任</span>
            <span className="portal-logo-text">
              <strong>任务游戏</strong>
              <small>TASK GAME</small>
            </span>
          </a>
          <div className="portal-site-tag" title="本地任务市场">
            <span className="portal-pin" aria-hidden="true">●</span> 在线任务站
          </div>
          <form className="portal-search" role="search" onSubmit={submitSearch}>
            <input
              type="search"
              value={searchDraft}
              onChange={(event) => setSearchDraft(event.target.value)}
              placeholder="搜任务：标题、描述、关键字"
              aria-label="全文搜索任务"
            />
            <button type="submit">搜索</button>
          </form>
          <div className="portal-top-actions">
            {signedIn ? (
              <>
                <a href={href.newTask}>免费发布任务</a>
                <a href={href.tasks}>任务大厅</a>
              </>
            ) : (
              <>
                <a href={href.login}>登录</a>
                <a href={href.login}>注册</a>
              </>
            )}
          </div>
        </header>

        {/* 2. 频道导航带：首页 / 单人任务 / 协作任务 / 竞争任务 / 代理专区 */}
        <nav className="portal-nav" aria-label="任务频道">
          <a className="on" href={href.home}>首页</a>
          {(["exclusive", "collaborative", "competitive"] as TaskKind[]).map((kind) => {
            const category = CATEGORIES.find((item) => item.kind === kind);
            return category ? <a key={kind} href={href.tasksByKind(kind)}>{category.title}</a> : null;
          })}
          <a href={href.agentZone}>代理专区</a>
          <span className="portal-nav-spacer" />
          <span className="portal-nav-count">共 {stats.total} 个任务</span>
        </nav>

        {/* 3. 热门搜索词（密集文字链，参考分类门户的热词区） */}
        {keywordCounts.length > 0 ? (
          <div className="portal-hotwords">
            <strong>热门搜索：</strong>
            {keywordCounts.slice(0, 12).map((item) => (
              <button
                type="button"
                key={item.name}
                className={activeKeyword === item.name ? "on" : undefined}
                onClick={() => setActiveKeyword(activeKeyword === item.name ? null : item.name)}
              >
                {item.name}
              </button>
            ))}
          </div>
        ) : null}

        {/* 4. 全部分类入口矩阵：每个玩法一列，下面是该玩法的密集关键字入口 */}
        <section className="portal-catalog" aria-label="全部分类">
          <div className="portal-section-head">
            <h2>全部分类</h2>
            <span>按玩法与关键字进入任务</span>
          </div>
          <div className="portal-catalog-grid">
            {CATEGORIES.map((category) => {
              const dynamic = keywordsByKind[category.kind] ?? [];
              const keywords = Array.from(new Set([...category.hot, ...dynamic])).slice(0, 14);
              return (
                <div className="portal-catalog-col" key={category.kind}>
                  <h3>
                    <a href={href.tasksByKind(category.kind)}>{category.title}</a>
                  </h3>
                  <div className="portal-catalog-links">
                    {keywords.map((keyword) => (
                      <button
                        type="button"
                        key={keyword}
                        className={activeKeyword === keyword ? "on" : undefined}
                        onClick={() => setActiveKeyword(activeKeyword === keyword ? null : keyword)}
                      >
                        {keyword}
                      </button>
                    ))}
                  </div>
                  <a className="portal-catalog-more" href={href.tasksByKind(category.kind)}>更多{category.title} &gt;</a>
                </div>
              );
            })}
          </div>
        </section>

        {/* 5. 快捷筛选入口：时限 / 周期 / 完成者，密集文字链 */}
        <section className="portal-quick" aria-label="快捷筛选">
          <div className="portal-quick-row">
            <span className="portal-quick-label">按时限</span>
            <button type="button" className={activeUrgency === null ? "on" : undefined} onClick={() => setActiveUrgency(null)}>全部</button>
            {Object.entries(URGENCY_LABEL).map(([value, label]) => (
              <button type="button" key={value} className={activeUrgency === value ? "on" : undefined} onClick={() => setActiveUrgency(value)}>{label}</button>
            ))}
          </div>
          <div className="portal-quick-row">
            <span className="portal-quick-label">按周期</span>
            <button type="button" className={activeRecurrence === null ? "on" : undefined} onClick={() => setActiveRecurrence(null)}>全部</button>
            {Object.entries(RECURRENCE_LABEL).map(([value, label]) => (
              <button type="button" key={value} className={activeRecurrence === value ? "on" : undefined} onClick={() => setActiveRecurrence(value)}>{label}</button>
            ))}
          </div>
          {requirementCounts.length > 0 ? (
            <div className="portal-quick-row">
              <span className="portal-quick-label">按完成者</span>
              <button type="button" className={activeRequirement === null ? "on" : undefined} onClick={() => setActiveRequirement(null)}>全部</button>
              {requirementCounts.map((item) => (
                <button type="button" key={item.name} className={activeRequirement === item.name ? "on" : undefined} onClick={() => setActiveRequirement(item.name)}>
                  {executorRequirementLabel(item.name)}
                </button>
              ))}
            </div>
          ) : null}
        </section>

        {/* 6. 主体：左侧结果表 + 右侧工具（距离、排序、统计） */}
        <div className="portal-body">
          <div className="portal-main">
            <div className="portal-filterbar">
              <div className="portal-scope">
                <button type="button" className={scope === "all" ? "on" : undefined} onClick={() => setScope("all")}>全部任务</button>
                {signedIn ? (
                  <button type="button" className={scope === "available" ? "on" : undefined} onClick={() => setScope("available")}>只看可接</button>
                ) : (
                  <a href={href.login}>登录后看可接任务</a>
                )}
              </div>
              <div className="portal-sort">
                <label htmlFor="portal-sort-select">排序</label>
                <select id="portal-sort-select" value={sort} onChange={(event) => setSort(event.target.value as typeof sort)}>
                  <option value="recent">最新发布</option>
                  <option value="amount_desc">报酬最高</option>
                  <option value="deadline_asc">截止最近</option>
                  <option value="distance_asc">距离最近</option>
                </select>
              </div>
              <button type="button" className="portal-refresh" onClick={() => setReloadToken((value) => value + 1)}>刷新</button>
              {loading ? <span className="portal-loading">正在加载…</span> : null}
              {anyFilter ? <button type="button" className="portal-clear" onClick={clearAll}>清除全部筛选</button> : null}
            </div>

            {anyFilter ? (
              <p className="portal-filter-note">
                当前结果：{searchText.trim() ? <>全文「{searchText.trim()}」</> : null}
                {activeKeyword ? <> · 关键字「{activeKeyword}」</> : null}
                {activeUrgency ? <> · 时限「{URGENCY_LABEL[activeUrgency as keyof typeof URGENCY_LABEL]}」</> : null}
                {activeRecurrence ? <> · 周期「{RECURRENCE_LABEL[activeRecurrence as keyof typeof RECURRENCE_LABEL]}」</> : null}
                {activeRequirement ? <> · 完成者「{executorRequirementLabel(activeRequirement)}」</> : null}
                {lat.trim() && lng.trim() ? <> · 距 {lat}, {lng} {radiusKm || "10"} 公里内</> : null}
                <span className="portal-filter-total"> · 共 {total} 个匹配</span>
              </p>
            ) : null}

            {CATEGORIES.map((category) => {
              const tasks = grouped[category.kind] ?? [];
              return (
                <section className="portal-block" key={category.kind}>
                  <div className="portal-block-head">
                    <h2>{category.title}<span>{category.en}</span></h2>
                    <span className="portal-block-desc">{category.description}</span>
                    <a href={href.tasksByKind(category.kind)}>更多 &gt;&gt;</a>
                  </div>
                  <ul className="portal-task-links">
                    {tasks.length === 0 && !loading ? (
                      <li className="portal-empty">当前范围内暂无{category.title}。</li>
                    ) : null}
                    {tasks.slice(0, PREVIEW_LIMIT).map((task) => (
                      <li key={task.id}>
                        <a href={href.task(task.id)} title={task.title}>
                          <span className="portal-task-title">{task.title}</span>
                          {relationLabel(task, settings.userId) ? <span className="portal-relation">{relationLabel(task, settings.userId)}</span> : null}
                          <span className="portal-task-pay">{formatCredits(task.payment.amount)}</span>
                        </a>
                      </li>
                    ))}
                  </ul>
                  {tasks.length > PREVIEW_LIMIT ? (
                    <a className="portal-block-more" href={href.tasksByKind(category.kind)}>查看全部 {tasks.length} 个{category.title} &gt;</a>
                  ) : null}
                </section>
              );
            })}
          </div>

          <aside className="portal-side">
            <section className="portal-side-card">
              <h3>任务概览</h3>
              <div className="portal-stats">
                <div><strong>{stats.total}</strong><span>匹配任务</span></div>
                <div><strong>{stats.open}</strong><span>招募中</span></div>
                {signedIn ? <div><strong>{stats.mine}</strong><span>与我相关</span></div> : null}
              </div>
            </section>

            <section className="portal-side-card">
              <h3>按距离找任务</h3>
              <p className="portal-side-hint">填中心点经纬度与半径，只看范围内的任务。</p>
              <div className="portal-distance">
                <label>纬度<input type="number" step="any" min={-90} max={90} placeholder="39.9042" value={lat} onChange={(event) => setLat(event.target.value)} /></label>
                <label>经度<input type="number" step="any" min={-180} max={180} placeholder="116.4074" value={lng} onChange={(event) => setLng(event.target.value)} /></label>
                <label>半径 km<input type="number" step="any" min={0} placeholder="10" value={radiusKm} onChange={(event) => setRadiusKm(event.target.value)} /></label>
                <button type="button" onClick={() => setReloadToken((value) => value + 1)}>查找</button>
              </div>
              <button
                type="button"
                className="portal-geo"
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
              {(lat.trim() && lng.trim()) || anyFilter ? (
                <button type="button" className="portal-clear-pos" onClick={() => { setLat(""); setLng(""); setRadiusKm("10"); }}>清除位置</button>
              ) : null}
            </section>

            <section className="portal-side-card">
              <h3>玩法说明</h3>
              <ul className="portal-guide">
                {CATEGORIES.map((category) => (
                  <li key={category.kind}><strong>{category.short} {category.title}</strong><br />{category.description}</li>
                ))}
              </ul>
              <p className="portal-side-hint">托管与结算均为模拟积分，不涉及真实资金。</p>
            </section>

            <section className="portal-side-card portal-publish">
              <h3>想发布任务？</h3>
              <p className="portal-side-hint">登录后即可发布单人、竞争或协作任务，并设置关键字与任务属性。</p>
              <a className="portal-publish-btn" href={signedIn ? href.newTask : href.login}>{signedIn ? "立即发布" : "登录 / 注册"}</a>
            </section>
          </aside>
        </div>
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

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

// Distance options use a browser-geolocated center point. Empty = no filter.
const DISTANCE_OPTIONS = [
  { value: "", label: "不限" },
  { value: "0.5", label: "500m" },
  { value: "1", label: "1km" },
  { value: "3", label: "3km" },
  { value: "5", label: "5km" },
  { value: "10", label: "10km" },
] as const;

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
    radiusKm: params.get("radius") ?? "",
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
  const scope = initial.scope;
  const sort = initial.sort;
  const [items, setItems] = useState<Task[]>([]);
  const [facetItems, setFacetItems] = useState<Task[]>([]);
  const [activeKeyword, setActiveKeyword] = useState<string | null>(initial.keyword);
  const [activeUrgency, setActiveUrgency] = useState<string | null>(initial.urgency);
  const [activeRecurrence, setActiveRecurrence] = useState<string | null>(initial.recurrence);
  const [activeRequirement, setActiveRequirement] = useState<string | null>(initial.requirement);
  const [lat, setLat] = useState(initial.lat);
  const [lng, setLng] = useState(initial.lng);
  const [radiusKm, setRadiusKm] = useState(initial.radiusKm);
  const [geoMessage, setGeoMessage] = useState<string | null>(null);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const requestSeq = useRef(0);

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
  }, [client]);

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
  }, [client, query]);

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

  // "推荐任务": three curated columns over the unfiltered baseline, so the
  // recommendations stay stable while a filter is active.
  const recommendations = useMemo(() => {
    const urgencyRank: Record<string, number> = { within_3_days: 0, within_week: 1, other: 2 };
    const byUrgency = [...facetItems].sort((a, b) => {
      const ra = urgencyRank[a.attributes?.urgency ?? "other"] ?? 2;
      const rb = urgencyRank[b.attributes?.urgency ?? "other"] ?? 2;
      return ra - rb || b.created_at.localeCompare(a.created_at);
    });
    const byNewest = [...facetItems].sort((a, b) => b.created_at.localeCompare(a.created_at));
    const byPay = [...facetItems].sort((a, b) => b.payment.amount - a.payment.amount);
    return [
      { name: "紧急任务", tasks: byUrgency.slice(0, 5) },
      { name: "最新任务", tasks: byNewest.slice(0, 5) },
      { name: "丰厚回报", tasks: byPay.slice(0, 5) },
    ];
  }, [facetItems]);

  // "全部任务": group the current (filtered) results by keyword. A task may
  // carry several keywords and appear in more than one category. Tasks without
  // any keyword go into a single "其它" category, always shown last.
  const keywordGroups = useMemo(() => {
    const groups = new Map<string, Task[]>();
    const others: Task[] = [];
    for (const task of items) {
      const keywords = task.keywords ?? [];
      if (keywords.length === 0) {
        others.push(task);
        continue;
      }
      for (const keyword of keywords) {
        const bucket = groups.get(keyword);
        if (bucket) bucket.push(task);
        else groups.set(keyword, [task]);
      }
    }
    const result = Array.from(groups.entries())
      .map(([name, tasks]) => ({ name, tasks }))
      .sort((a, b) => b.tasks.length - a.tasks.length || a.name.localeCompare(b.name));
    if (others.length > 0) result.push({ name: "其它", tasks: others });
    return result;
  }, [items]);

  const stats = useMemo(
    () => ({
      total: total,
      open: items.filter((task) => task.status === "open").length,
      mine: items.filter((task) => isMine(task, settings.userId)).length,
    }),
    [total, items, settings.userId],
  );

  const anyFilter = Boolean(searchText.trim() || activeKeyword || activeUrgency || activeRecurrence || activeRequirement || radiusKm);
  const distanceLabel = DISTANCE_OPTIONS.find((option) => option.value === radiusKm)?.label;

  const submitSearch = (event: React.FormEvent) => {
    event.preventDefault();
    setSearchText(searchDraft);
  };

  // Choosing a distance needs a center point. Read the browser location once,
  // then filter by radius on the server. "不限" clears the location and radius.
  const chooseDistance = (value: string) => {
    setRadiusKm(value);
    if (!value) {
      setLat("");
      setLng("");
      setGeoMessage(null);
      return;
    }
    if (lat.trim() && lng.trim()) {
      setGeoMessage(null);
      return;
    }
    if (!navigator.geolocation) {
      setGeoMessage("当前浏览器不支持定位，无法按距离筛选。");
      return;
    }
    setGeoMessage("正在获取你的位置…");
    navigator.geolocation.getCurrentPosition(
      (pos) => {
        setLat(pos.coords.latitude.toFixed(4));
        setLng(pos.coords.longitude.toFixed(4));
        setGeoMessage(null);
      },
      () => {
        setRadiusKm("");
        setGeoMessage("无法获取位置，请检查浏览器的定位权限后重试。");
      },
      { timeout: 8000 },
    );
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
    setRadiusKm("");
    setGeoMessage(null);
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

        {/* 4. 推荐任务：固定三列——紧急 / 最新 / 丰厚回报 */}
        <section className="portal-catalog" aria-label="推荐任务">
          <div className="portal-section-head">
            <h2>推荐任务</h2>
          </div>
          <div className="portal-catalog-grid">
            {recommendations.map((group, index) => (
              <div className={`portal-catalog-col portal-rec portal-rec-${index}`} key={group.name}>
                <h3>
                  <span className="portal-rec-mark" aria-hidden="true">{["急", "新", "奖"][index]}</span>
                  {group.name}
                  <span className="portal-cat-count">{group.tasks.length}</span>
                </h3>
                <ul className="portal-cat-tasks">
                  {group.tasks.length > 0 ? group.tasks.map((task) => (
                    <li key={task.id}>
                      <a href={href.task(task.id)} title={task.title}>
                        <span className="portal-cat-task-title">{task.title}</span>
                        <span className="portal-cat-task-pay">{formatCredits(task.payment.amount)}</span>
                      </a>
                    </li>
                  )) : (
                    <li className="portal-cat-empty">暂无任务</li>
                  )}
                </ul>
              </div>
            ))}
          </div>
        </section>

        {/* 5. 快捷筛选：时限 / 周期 / 距离用下拉框，完成者保持文字链 */}
        <section className="portal-quick" aria-label="快捷筛选">
          <div className="portal-quick-row portal-quick-selects">
            <label className="portal-quick-field">
              <span className="portal-quick-label">按时限</span>
              <select value={activeUrgency ?? ""} onChange={(event) => setActiveUrgency(event.target.value || null)}>
                <option value="">全部</option>
                {Object.entries(URGENCY_LABEL).map(([value, label]) => (
                  <option key={value} value={value}>{label}</option>
                ))}
              </select>
            </label>
            <label className="portal-quick-field">
              <span className="portal-quick-label">按周期</span>
              <select value={activeRecurrence ?? ""} onChange={(event) => setActiveRecurrence(event.target.value || null)}>
                <option value="">全部</option>
                {Object.entries(RECURRENCE_LABEL).map(([value, label]) => (
                  <option key={value} value={value}>{label}</option>
                ))}
              </select>
            </label>
            <label className="portal-quick-field">
              <span className="portal-quick-label">按距离</span>
              <select value={radiusKm} onChange={(event) => chooseDistance(event.target.value)}>
                {DISTANCE_OPTIONS.map((option) => (
                  <option key={option.value} value={option.value}>{option.label}</option>
                ))}
              </select>
            </label>
          </div>
          {geoMessage ? <p className="portal-geo-message">{geoMessage}</p> : null}
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
            {anyFilter ? (
              <p className="portal-filter-note">
                当前结果：{searchText.trim() ? <>全文「{searchText.trim()}」</> : null}
                {activeKeyword ? <> · 关键字「{activeKeyword}」</> : null}
                {activeUrgency ? <> · 时限「{URGENCY_LABEL[activeUrgency as keyof typeof URGENCY_LABEL]}」</> : null}
                {activeRecurrence ? <> · 周期「{RECURRENCE_LABEL[activeRecurrence as keyof typeof RECURRENCE_LABEL]}」</> : null}
                {activeRequirement ? <> · 完成者「{executorRequirementLabel(activeRequirement)}」</> : null}
                {radiusKm && distanceLabel ? <> · 距当前位置 {distanceLabel} 内</> : null}
                <span className="portal-filter-total"> · 共 {total} 个匹配</span>
                <button type="button" className="portal-clear" onClick={clearAll}>清除全部筛选</button>
              </p>
            ) : null}

            <section className="portal-alltasks" aria-label="全部任务">
              <div className="portal-section-head">
                <h2>全部任务</h2>
              </div>
              {keywordGroups.length > 0 ? (
                <div className="portal-catalog-grid portal-cat-grid portal-cat-grid-2">
                  {keywordGroups.map((group) => {
                    const isOther = group.name === "其它";
                    const active = !isOther && activeKeyword === group.name;
                    return (
                      <div className={`portal-catalog-col portal-cat${active ? " on" : ""}${isOther ? " other" : ""}`} key={group.name}>
                        <h3>
                          {isOther ? (
                            <span className="portal-cat-name">{group.name}</span>
                          ) : (
                            <button
                              type="button"
                              className="portal-cat-name"
                              onClick={() => setActiveKeyword(active ? null : group.name)}
                            >
                              {group.name}
                            </button>
                          )}
                          <span className="portal-cat-count">{group.tasks.length}</span>
                        </h3>
                        <ul className="portal-cat-tasks">
                          {group.tasks.slice(0, 6).map((task) => (
                            <li key={task.id}>
                              <a href={href.task(task.id)} title={task.title}>
                                <span className="portal-cat-task-title">{task.title}</span>
                                <span className="portal-cat-task-pay">{formatCredits(task.payment.amount)}</span>
                              </a>
                            </li>
                          ))}
                        </ul>
                      </div>
                    );
                  })}
                </div>
              ) : (
                <p className="portal-cat-empty">{loading ? "正在加载…" : "当前范围内暂无任务。"}</p>
              )}
            </section>
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


import { useEffect } from "react";
import { useApp } from "./context";
import { href, useRoute } from "./route";
import { ChatPane } from "./screens/ChatPane";
import { CreateTask } from "./screens/CreateTask";
import { Scores } from "./screens/Scores";
import { TaskDetail } from "./screens/TaskDetail";
import { TaskList } from "./screens/TaskList";
import { PRESETS, activePreset, settingsForRole, type Role } from "./settings";
import { cx } from "./ui";

const TITLES: Record<string, string> = {
  tasks: "任务列表",
  new: "创建任务",
  task: "任务详情",
  scores: "积分",
  chat: "会话",
};

export function App() {
  const route = useRoute();
  const { settings, setSettings, patchSettings, clientError, health } = useApp();
  const preset = activePreset(settings);

  useEffect(() => {
    document.title = `${TITLES[route.name] ?? "任务游戏"} · 任务游戏`;
  }, [route]);

  return (
    <div className="app">
      <header className="top">
        <div className="brand">
          <span className="mark" aria-hidden="true">
            任
          </span>
          <div>
            <strong>任务游戏</strong>
            <small>Open Protocol v0.1</small>
          </div>
        </div>
        <p className={cx("health", health === "ok" && "ok", health === "down" && "down")}>
          {health === "checking" ? "正在连接…" : health === "ok" ? "服务已连接" : "服务未连接"}
        </p>
      </header>

      <aside className="side">
        <div className="role-switch" role="group" aria-label="角色">
          {(Object.keys(PRESETS) as Role[]).map((role) => (
            <button
              key={role}
              type="button"
              className={preset === role ? "tab on" : "tab"}
              onClick={() => setSettings(settingsForRole(settings, role))}
            >
              {PRESETS[role].label}
              <span className="en">{PRESETS[role].userId}</span>
            </button>
          ))}
        </div>
        <p className="hint">点角色会改回该角色的开发密钥，并使用 API Key。已经粘贴的 Bearer token 还留在输入框里。</p>

        <form
          className="settings"
          onSubmit={(event) => {
            event.preventDefault();
            setSettings(settings);
          }}
        >
          <label className="field">
            <span>Base URL</span>
            <input
              value={settings.baseUrl}
              spellCheck={false}
              onChange={(event) => patchSettings({ baseUrl: event.target.value })}
            />
          </label>
          <label className="field">
            <span>鉴权</span>
            <select
              value={settings.authMode}
              onChange={(event) => patchSettings({ authMode: event.target.value === "bearer" ? "bearer" : "apiKey" })}
            >
              <option value="apiKey">API Key · X-Api-Key</option>
              <option value="bearer">Bearer · JWT</option>
            </select>
          </label>
          {settings.authMode === "bearer" ? (
            <label className="field">
              <span>Bearer token</span>
              <textarea
                rows={4}
                spellCheck={false}
                value={settings.bearerToken}
                onChange={(event) => patchSettings({ bearerToken: event.target.value })}
              />
              <small>贴 make token USER_ID=usr_extra 打出的 JWT。命令回显可以一起贴，页面会取其中的 token。</small>
            </label>
          ) : (
            <label className="field">
              <span>API Key</span>
              <input
                value={settings.apiKey}
                spellCheck={false}
                onChange={(event) => patchSettings({ apiKey: event.target.value })}
              />
            </label>
          )}
          <label className="field">
            <span>当前用户 id</span>
            <input
              value={settings.userId}
              spellCheck={false}
              onChange={(event) => patchSettings({ userId: event.target.value })}
            />
            <small>界面用它判断按钮和聊天对象。请求身份以密钥或 JWT 的 sub 为准，两边需要一致。</small>
          </label>
          {clientError ? <p className="banner error">{clientError}</p> : null}
          <button type="submit" className="btn secondary">
            立即使用这组设置
          </button>
        </form>

        <nav className="nav">
          <a href={href.tasks} aria-current={route.name === "tasks" || route.name === "task" ? "page" : undefined}>
            任务
          </a>
          <a href={href.newTask} aria-current={route.name === "new" ? "page" : undefined}>
            创建
          </a>
          <a href={href.scores} aria-current={route.name === "scores" ? "page" : undefined}>
            积分
          </a>
          <a href={href.chat} aria-current={route.name === "chat" ? "page" : undefined}>
            会话
          </a>
        </nav>
      </aside>

      <main className="main">
        {route.name === "tasks" ? <TaskList /> : null}
        {route.name === "new" ? <CreateTask /> : null}
        {route.name === "task" ? <TaskDetail key={route.id} id={route.id} /> : null}
        {route.name === "scores" ? <Scores /> : null}
        {route.name === "chat" ? <ChatPane /> : null}
      </main>
    </div>
  );
}

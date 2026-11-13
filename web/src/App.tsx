import { useEffect, useState } from "react";
import { isSignedIn } from "./client";
import { useApp } from "./context";
import { CHAT_ERROR_EVENT } from "./openChat";
import { DevIdentity, SessionCard } from "./identity";
import { href, useRoute } from "./route";
import { AuthScreen } from "./screens/AuthScreen";
import { Conversation } from "./screens/Conversation";
import { CreateTask } from "./screens/CreateTask";
import { Friends } from "./screens/Friends";
import { Invites } from "./screens/Invites";
import { Scores } from "./screens/Scores";
import { Settings } from "./screens/Settings";
import { TaskDetail } from "./screens/TaskDetail";
import { TaskList } from "./screens/TaskList";
import { cx, ErrorNote } from "./ui";

const TITLES: Record<string, string> = {
  tasks: "任务列表",
  new: "创建任务",
  task: "任务详情",
  scores: "积分",
  conversation: "会话",
  invites: "邀请",
  friends: "好友",
  settings: "用户信息",
};

export function App() {
  const route = useRoute();
  const { settings, health } = useApp();
  const signedIn = isSignedIn(settings);
  const [chatError, setChatError] = useState<string | null>(null);

  useEffect(() => {
    const onError = (event: Event) => {
      const detail = (event as CustomEvent<string>).detail;
      if (typeof detail === "string" && detail) setChatError(detail);
    };
    window.addEventListener(CHAT_ERROR_EVENT, onError);
    return () => window.removeEventListener(CHAT_ERROR_EVENT, onError);
  }, []);

  useEffect(() => {
    setChatError(null);
  }, [route]);

  useEffect(() => {
    const page = signedIn ? (TITLES[route.name] ?? "任务游戏") : "登录";
    document.title = `${page} · 任务游戏`;
  }, [route, signedIn]);

  return (
    <div className={cx("app", !signedIn && "auth-app")}>
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

      {signedIn ? (
        <aside className="side">
          <SessionCard />
          <DevIdentity />
          <nav className="nav">
          <a href={href.tasks} aria-current={route.name === "tasks" || route.name === "task" ? "page" : undefined}>
            任务
          </a>
          <a href={href.newTask} aria-current={route.name === "new" ? "page" : undefined}>
            创建
          </a>
          <a href={href.invites} aria-current={route.name === "invites" ? "page" : undefined}>
            邀请
          </a>
          <a href={href.friends} aria-current={route.name === "friends" ? "page" : undefined}>
            好友
          </a>
          <a href={href.scores} aria-current={route.name === "scores" ? "page" : undefined}>
            积分
          </a>
          <a href={href.settings} aria-current={route.name === "settings" ? "page" : undefined}>
            设置
          </a>
        </nav>
        </aside>
      ) : null}

      <main className={cx("main", !signedIn && "auth-main")}>
        {signedIn ? (
          <>
            <ErrorNote message={chatError} />
            {route.name === "tasks" ? <TaskList /> : null}
            {route.name === "new" ? <CreateTask /> : null}
            {route.name === "task" ? <TaskDetail key={route.id} id={route.id} /> : null}
            {route.name === "scores" ? <Scores /> : null}
            {route.name === "invites" ? <Invites /> : null}
            {route.name === "friends" ? <Friends /> : null}
            {route.name === "settings" ? <Settings /> : null}
            {route.name === "conversation" ? <Conversation key={route.id} id={route.id} /> : null}
          </>
        ) : (
          <AuthScreen />
        )}
      </main>
    </div>
  );
}

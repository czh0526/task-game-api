import { useEffect } from "react";
import { isSignedIn } from "./client";
import { useApp } from "./context";
import { DevIdentity, SessionCard } from "./identity";
import { href, useRoute } from "./route";
import { AuthScreen } from "./screens/AuthScreen";
import { ChatPane } from "./screens/ChatPane";
import { CreateTask } from "./screens/CreateTask";
import { Scores } from "./screens/Scores";
import { TaskDetail } from "./screens/TaskDetail";
import { TaskList } from "./screens/TaskList";
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
  const { settings, health } = useApp();
  const signedIn = isSignedIn(settings);

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
          <a href={href.scores} aria-current={route.name === "scores" ? "page" : undefined}>
            积分
          </a>
          <a href={href.chat} aria-current={route.name === "chat" ? "page" : undefined}>
            会话
          </a>
        </nav>
        </aside>
      ) : null}

      <main className={cx("main", !signedIn && "auth-main")}>
        {signedIn ? (
          <>
            {route.name === "tasks" ? <TaskList /> : null}
            {route.name === "new" ? <CreateTask /> : null}
            {route.name === "task" ? <TaskDetail key={route.id} id={route.id} /> : null}
            {route.name === "scores" ? <Scores /> : null}
            {route.name === "chat" ? <ChatPane /> : null}
          </>
        ) : (
          <AuthScreen />
        )}
      </main>
    </div>
  );
}

import { useEffect, useRef, useState } from "react";
import { isSignedIn } from "./client";
import { useApp } from "./context";
import { openEventStream } from "./eventStream";
import { LoginGate } from "./LoginGate";
import { startDesktopNotifications } from "./notifications";
import { CHAT_ERROR_EVENT } from "./openChat";
import { useUnreadTotal } from "./useUnread";
import { TopBar } from "./TopBar";
import { href, isProtectedRoute, useRoute, type Route } from "./route";
import { AuthScreen } from "./screens/AuthScreen";
import { Conversation } from "./screens/Conversation";
import { CreateTask } from "./screens/CreateTask";
import { Friends } from "./screens/Friends";
import { Home } from "./screens/Home";
import { Invites } from "./screens/Invites";
import { Scores } from "./screens/Scores";
import { Settings } from "./screens/Settings";
import { TaskDetail } from "./screens/TaskDetail";
import { TaskList } from "./screens/TaskList";
import { cx, ErrorNote } from "./ui";
import { UserCardHost } from "./userCard";

const TITLES: Record<string, string> = {
  home: "首页",
  auth: "登录",
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
  const { settings, health, client } = useApp();
  const signedIn = isSignedIn(settings);
  const route = useRoute();
  // Navigation follows the URL only. A protected page opened while signed out
  // keeps its URL and shows a login modal on top; signing in reveals the page.
  const gateOpen = !signedIn && isProtectedRoute(route);
  const [chatError, setChatError] = useState<string | null>(null);
  const unreadTotal = useUnreadTotal(client, settings.userId, signedIn);
  const routeRef = useRef(route);
  routeRef.current = route;

  // One stream per signed-in session. Screens subscribe to its signals and
  // keep a slow fallback poll for the case a frame is lost.
  useEffect(() => {
    if (!signedIn) return;
    return openEventStream(settings);
  }, [signedIn, settings.baseUrl, settings.authMode, settings.apiKey, settings.bearerToken, settings.userId]);

  // Desktop notifications are a global concern: they must also fire while the
  // user is on the home page or a task detail, where no screen owns the thread.
  useEffect(() => {
    if (!signedIn) return;
    return startDesktopNotifications(() => ({
      myUserId: settings.userId,
      visible: document.visibilityState === "visible" && document.hasFocus(),
      activeChatId: routeRef.current.name === "conversation" ? routeRef.current.id : null,
    }));
  }, [signedIn, settings.userId]);

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
    const page = route.name === "auth" && route.mode === "register" ? "注册" : (TITLES[route.name] ?? "任务游戏");
    document.title = `${page} · 任务游戏`;
  }, [route]);

  return (
    <div className="app">
      <TopBar route={route} health={health} unreadTotal={unreadTotal} />

      <main className={cx("main", route.name === "auth" && "auth-main", route.name === "home" && "home-main")}>
        <ErrorNote message={chatError} />
        {renderRoute(route, gateOpen)}
      </main>
      {gateOpen ? <LoginGate onClose={() => window.location.replace(href.home)} /> : null}
      <UserCardHost />
    </div>
  );
}

/** Resolve the URL to a screen, independent of sign-in state. */
function renderRoute(route: Route, gateOpen: boolean) {
  switch (route.name) {
    case "home":
      return <Home />;
    case "task":
      return <TaskDetail key={route.id} id={route.id} />;
    case "auth":
      return <AuthScreen mode={route.mode} />;
    case "tasks":
      return gateOpen ? <GatePlaceholder /> : <TaskList />;
    case "new":
      return gateOpen ? <GatePlaceholder /> : <CreateTask />;
    case "scores":
      return gateOpen ? <GatePlaceholder /> : <Scores />;
    case "invites":
      return gateOpen ? <GatePlaceholder /> : <Invites />;
    case "friends":
      return gateOpen ? <GatePlaceholder /> : <Friends />;
    case "settings":
      return gateOpen ? <GatePlaceholder /> : <Settings />;
    case "conversation":
      return gateOpen ? <GatePlaceholder /> : <Conversation key={route.id} id={route.id} />;
    default:
      return <Home />;
  }
}

function GatePlaceholder() {
  return (
    <div className="gate-placeholder">
      <p>这个页面需要登录后才能查看。请在弹出的窗口中登录。</p>
    </div>
  );
}

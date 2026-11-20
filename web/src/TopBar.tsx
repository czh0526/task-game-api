import { useEffect, useRef, useState } from "react";
import { Avatar } from "./Avatar";
import { isSignedIn } from "./client";
import { useApp } from "./context";
import {
  notificationPermission,
  notificationStatus,
  notificationsEnabled,
  subscribeNotificationPref,
  toggleDesktopNotifications,
  type NotifyPermission,
} from "./notifications";
import { badgeLabel } from "./notificationView";
import { PROFILE_EVENT, rememberNickname } from "./names";
import { personLabel } from "./person";
import { href, type Route } from "./route";
import { loggedOut } from "./settings";
import { cx } from "./ui";

/**
 * The single global navigation bar, shared by signed-out visitors and signed-in
 * members. Brand on the left, primary navigation in the middle, account state
 * on the right. Signed-out visitors get login/register; members get a menu with
 * their profile,常用入口 and sign-out. The URL stays the only source of truth
 * for which page is shown.
 */
export function TopBar({
  route,
  health,
  unreadTotal,
}: {
  route: Route;
  health: "checking" | "ok" | "down";
  /** Global unread direct-chat count, shown on every page. */
  unreadTotal: number;
}) {
  const { client, settings, setSettings } = useApp();
  const signedIn = isSignedIn(settings);
  const [nickname, setNickname] = useState("");
  const [menuOpen, setMenuOpen] = useState(false);
  const [notifyEnabled, setNotifyEnabled] = useState(notificationsEnabled);
  const [notifyPermission, setNotifyPermission] = useState<NotifyPermission>(notificationPermission);
  const [notifyNote, setNotifyNote] = useState<string | null>(null);
  const [notifyBusy, setNotifyBusy] = useState(false);
  const menuRef = useRef<HTMLDivElement>(null);

  useEffect(
    () =>
      subscribeNotificationPref((enabled) => {
        setNotifyEnabled(enabled);
        setNotifyPermission(notificationPermission());
      }),
    [],
  );

  // Load the current nickname for the account chip, and keep it in sync when
  // the settings screen (or any other screen) updates the profile.
  useEffect(() => {
    if (!signedIn || !client) return;
    const ac = new AbortController();
    client
      .me({ signal: ac.signal })
      .then((me) => {
        if (ac.signal.aborted) return;
        setNickname(me.nickname ?? "");
        rememberNickname(me.id, me.nickname);
      })
      .catch(() => {});
    const onProfile = (event: Event) => {
      const detail = (event as CustomEvent<{ userId?: string; nickname?: string }>).detail;
      if (detail && detail.userId === settings.userId) setNickname(detail.nickname ?? "");
    };
    window.addEventListener(PROFILE_EVENT, onProfile);
    return () => {
      ac.abort();
      window.removeEventListener(PROFILE_EVENT, onProfile);
    };
  }, [client, settings.userId, signedIn]);

  useEffect(() => {
    if (!menuOpen) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") setMenuOpen(false);
    };
    const onClick = (event: MouseEvent) => {
      if (menuRef.current && !menuRef.current.contains(event.target as Node)) setMenuOpen(false);
    };
    window.addEventListener("keydown", onKey);
    window.addEventListener("mousedown", onClick);
    return () => {
      window.removeEventListener("keydown", onKey);
      window.removeEventListener("mousedown", onClick);
    };
  }, [menuOpen]);

  useEffect(() => setMenuOpen(false), [route.name, signedIn]);

  const primary = [
    { label: "首页", href: href.home, active: route.name === "home", public: true },
    { label: "任务", href: href.tasks, active: route.name === "tasks" || route.name === "task", public: false },
    { label: "发布", href: href.newTask, active: route.name === "new", public: false },
    { label: "邀请", href: href.invites, active: route.name === "invites", public: false },
    { label: "好友", href: href.friends, active: route.name === "friends", public: false },
    { label: "积分", href: href.scores, active: route.name === "scores", public: false },
  ];

  const label = personLabel(nickname, settings.userId);
  const badge = badgeLabel(unreadTotal);
  const notifyState = notificationStatus(notifyPermission, notifyEnabled);

  async function onToggleNotifications() {
    setNotifyBusy(true);
    setNotifyNote(null);
    try {
      setNotifyNote(await toggleDesktopNotifications());
      setNotifyEnabled(notificationsEnabled());
      setNotifyPermission(notificationPermission());
    } finally {
      setNotifyBusy(false);
    }
  }

  return (
    <header className="topbar">
      <div className="topbar-inner">
        <a className="topbar-brand" href={href.home} aria-label="任务游戏首页">
          <span className="topbar-logo" aria-hidden="true">任</span>
          <span className="topbar-brand-text">
            <strong>任务游戏</strong>
            <small>TASK GAME</small>
          </span>
        </a>

        <nav className="topbar-nav" aria-label="主导航">
          {primary.map((item) => (
            <a
              key={item.label}
              href={item.href}
              className={cx(item.active && "on")}
              aria-current={item.active ? "page" : undefined}
              title={signedIn || item.public ? undefined : "登录后访问"}
            >
              {item.label}
            </a>
          ))}
        </nav>

        <div className="topbar-right">
          <span className={cx("topbar-health", health === "ok" && "ok", health === "down" && "down")} title="服务连接状态">
            <i aria-hidden="true" />
            {health === "checking" ? "连接中" : health === "ok" ? "已连接" : "未连接"}
          </span>
          {signedIn ? (
            <a
              className="topbar-bell"
              href={href.friends}
              title={badge ? `${unreadTotal} 条未读消息 · ${notifyState}` : `暂无未读消息 · ${notifyState}`}
              aria-label={badge ? `消息，${unreadTotal} 条未读` : "消息，暂无未读"}
            >
              <svg viewBox="0 0 24 24" width="20" height="20" aria-hidden="true" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
                <path d="M18 8a6 6 0 0 0-12 0c0 7-3 7-3 9h18c0-2-3-2-3-9" />
                <path d="M13.7 21a2 2 0 0 1-3.4 0" />
              </svg>
              {badge ? <span className="topbar-bell-badge">{badge}</span> : null}
            </a>
          ) : null}
          {signedIn ? (
            <div className="topbar-account" ref={menuRef}>
              <button
                type="button"
                className="topbar-account-btn"
                aria-haspopup="menu"
                aria-expanded={menuOpen}
                onClick={() => setMenuOpen((open) => !open)}
              >
                <Avatar userId={settings.userId} name={nickname || settings.userId} className="topbar-avatar" />
                <span className="topbar-account-name">{label}</span>
                <span className="topbar-caret" aria-hidden="true">▾</span>
              </button>
              {menuOpen ? (
                <div className="topbar-menu" role="menu">
                  <div className="topbar-menu-id">
                    <strong title={settings.userId}>{label}</strong>
                    <span className="mono" title={settings.userId}>{settings.userId}</span>
                  </div>
                  <a role="menuitem" href={href.settings}>用户信息</a>
                  <a role="menuitem" href={href.friends}>好友</a>
                  <a role="menuitem" href={href.invites}>邀请</a>
                  <a role="menuitem" href={href.scores}>积分</a>
                  <button
                    type="button"
                    role="menuitem"
                    className="topbar-menu-notify"
                    disabled={notifyBusy}
                    title={notifyState}
                    onClick={() => void onToggleNotifications()}
                  >
                    {notifyEnabled ? "关闭桌面通知" : "开启桌面通知"}
                  </button>
                  {notifyNote ? <p className="topbar-menu-note">{notifyNote}</p> : null}
                  <button
                    type="button"
                    role="menuitem"
                    className="topbar-menu-signout"
                    onClick={() => {
                      setMenuOpen(false);
                      setSettings(loggedOut(settings));
                      window.location.hash = href.home;
                    }}
                  >
                    退出登录
                  </button>
                </div>
              ) : null}
            </div>
          ) : (
            <div className="topbar-guest">
              <a href={href.login}>登录</a>
              <a className="topbar-register" href={href.register}>注册</a>
            </div>
          )}
        </div>
      </div>
    </header>
  );
}

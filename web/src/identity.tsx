import { useEffect, useState } from "react";
import { useApp } from "./context";
import { PROFILE_EVENT, rememberNickname } from "./names";
import { personLabel } from "./person";
import { href } from "./route";
import { PRESETS, activePreset, loggedOut, settingsForRole, type Role } from "./settings";
import { Button, ErrorNote, Field } from "./ui";

export function SessionCard() {
  const { settings, setSettings, client } = useApp();
  const [nickname, setNickname] = useState("");

  useEffect(() => {
    if (!client) return;
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
      if (!detail || detail.userId !== settings.userId) return;
      setNickname(detail.nickname ?? "");
    };
    window.addEventListener(PROFILE_EVENT, onProfile);
    return () => {
      ac.abort();
      window.removeEventListener(PROFILE_EVENT, onProfile);
    };
  }, [client, settings.userId]);

  const label = personLabel(nickname, settings.userId);
  return (
    <div className="who">
      <span className="fact-label">当前账号</span>
      <strong title={settings.userId}>{label}</strong>
      {settings.email ? <span className="who-email">{settings.email}</span> : <span className="who-email">开发密钥</span>}
      {label !== settings.userId ? <code className="mono">{settings.userId}</code> : null}
      <div className="who-actions">
        <a className="btn secondary" href={href.settings}>
          用户信息
        </a>
        <Button kind="secondary" onClick={() => setSettings(loggedOut(settings))}>
          退出登录
        </Button>
      </div>
    </div>
  );
}

export function DevIdentity() {
  const { settings, setSettings, patchSettings, clientError } = useApp();
  const preset = activePreset(settings);
  return (
    <details className="dev-id">
      <summary>开发快捷身份</summary>
      <p className="hint">发布者、执行者和裁定者的开发密钥仍可用于本地冒烟。裁定者是 usr_arbiter（dev-arbiter-key），也可以 make token USER_ID=usr_arbiter。点角色会改回该角色的 API Key。</p>
      <div className="role-switch" role="group" aria-label="开发角色">
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
      <form
        className="settings"
        onSubmit={(event) => {
          event.preventDefault();
          setSettings(settings);
        }}
      >
        <Field label="Base URL">
          <input
            value={settings.baseUrl}
            spellCheck={false}
            onChange={(event) => patchSettings({ baseUrl: event.target.value })}
          />
        </Field>
        <Field label="鉴权">
          <select
            value={settings.authMode}
            onChange={(event) => patchSettings({ authMode: event.target.value === "bearer" ? "bearer" : "apiKey" })}
          >
            <option value="apiKey">API Key · X-Api-Key</option>
            <option value="bearer">Bearer · JWT</option>
          </select>
        </Field>
        {settings.authMode === "bearer" ? (
          <Field label="Bearer token" hint="贴 make token 打出的 JWT。命令回显可以一起贴，页面会取出其中的 token。">
            <textarea
              rows={4}
              spellCheck={false}
              value={settings.bearerToken}
              onChange={(event) => patchSettings({ bearerToken: event.target.value })}
            />
          </Field>
        ) : (
          <Field label="API Key">
            <input
              value={settings.apiKey}
              spellCheck={false}
              onChange={(event) => patchSettings({ apiKey: event.target.value })}
            />
          </Field>
        )}
        <Field label="当前用户 id" hint="界面用它判断按钮和聊天对象。请求身份以密钥或 JWT 的 sub 为准，两边需要一致。">
          <input
            value={settings.userId}
            spellCheck={false}
            onChange={(event) => patchSettings({ userId: event.target.value })}
          />
        </Field>
        <ErrorNote message={clientError} />
        <Button type="submit" kind="secondary">
          立即使用这组设置
        </Button>
      </form>
    </details>
  );
}

import { useState, type FormEvent } from "react";
import { bearer, isTaskGameError } from "taskgame-client";
import { clientErrorText, createClient } from "../client";
import { useApp } from "../context";
import { formatError } from "../format";
import { DevIdentity } from "../identity";
import type { Settings } from "../settings";
import { Button, ErrorNote, Field, Screen } from "../ui";

type Mode = "login" | "register";

export function AuthScreen() {
  const { settings, setSettings, patchSettings, clientError } = useApp();
  const [mode, setMode] = useState<Mode>("login");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    setError(null);
    let anon;
    try {
      anon = createClient(loggedOutShape(settings));
    } catch (err) {
      setError(clientErrorText(err));
      return;
    }
    setPending(true);
    try {
      const body = { email: email.trim(), password };
      const session = mode === "register" ? await anon.register(body) : await anon.login(body);
      const me = await anon.withAuth(bearer(session.access_token)).me();
      if (me.id !== session.user.id) {
        throw new Error("登录返回的用户和令牌不一致");
      }
      setSettings({
        ...settings,
        authMode: "bearer",
        bearerToken: session.access_token,
        userId: me.id,
        email: me.email || session.user.email,
        apiKey: "",
      });
      if (!window.location.hash.startsWith("#/")) {
        window.location.hash = "#/tasks";
      }
    } catch (err) {
      setError(authErrorText(err));
    } finally {
      setPending(false);
    }
  }

  return (
    <Screen
      title="登录任务游戏"
      lede="用邮箱和密码进入。成功后签发的 JWT 与现有 Bearer 鉴权相同（HS256，sub 为用户 id），并保存在这台浏览器里。新用户带有起始模拟积分，可以直接创建并注资。"
    >
      <div className="tabs auth-switch" role="group" aria-label="登录或注册">
        <button type="button" className={mode === "login" ? "tab on" : "tab"} onClick={() => setMode("login")}>
          登录
        </button>
        <button type="button" className={mode === "register" ? "tab on" : "tab"} onClick={() => setMode("register")}>
          注册
        </button>
      </div>
      <form className="form card" onSubmit={onSubmit}>
        <Field label="服务地址" hint="本机 API 默认 http://127.0.0.1:8891">
          <input
            value={settings.baseUrl}
            spellCheck={false}
            autoComplete="off"
            onChange={(event) => patchSettings({ baseUrl: event.target.value })}
          />
        </Field>
        <Field label="邮箱">
          <input
            type="email"
            required
            autoComplete="email"
            value={email}
            onChange={(event) => setEmail(event.target.value)}
          />
        </Field>
        <Field label="密码" hint={mode === "register" ? "至少 8 个字符，最长 72 字节。服务只保存 bcrypt，不会存明文。" : undefined}>
          <input
            type="password"
            required
            minLength={mode === "register" ? 8 : undefined}
            autoComplete={mode === "register" ? "new-password" : "current-password"}
            value={password}
            onChange={(event) => setPassword(event.target.value)}
          />
        </Field>
        <ErrorNote message={error || clientError} />
        <Button type="submit" kind="primary" pending={pending}>
          {mode === "register" ? "注册并登录" : "登录"}
        </Button>
      </form>
      <DevIdentity />
    </Screen>
  );
}

function loggedOutShape(settings: Settings): Settings {
  return { ...settings, authMode: "apiKey", apiKey: "", bearerToken: "", userId: "", email: "" };
}

function authErrorText(error: unknown): string {
  if (isTaskGameError(error)) {
    if (error.code === "conflict") return "这个邮箱已经注册。";
    if (error.code === "unauthorized") return "邮箱或密码不正确。";
    if (error.code === "invalid_argument") {
      if (error.message.includes("email")) return "请填写有效的邮箱地址。";
      if (error.message.includes("at least")) return "密码至少 8 个字符。";
      if (error.message.includes("72")) return "密码不能超过 72 字节。";
      if (error.message.includes("required")) return "请填写密码。";
    }
  }
  return formatError(error);
}

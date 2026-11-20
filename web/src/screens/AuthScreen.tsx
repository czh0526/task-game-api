import { LoginForm } from "../LoginForm";
import { authHref, type AuthMode } from "../route";
import { Screen } from "../ui";

/** Standalone sign-in page. The gate modal reuses the same form via LoginForm. */
export function AuthScreen({ mode }: { mode: AuthMode }) {
  return (
    <Screen
      title={mode === "register" ? "注册任务游戏" : "登录任务游戏"}
      lede="用邮箱和密码进入。成功后签发的 JWT 与现有 Bearer 鉴权相同（HS256，sub 为用户 id），并保存在这台浏览器里。新用户带有起始模拟积分，可以直接创建并注资。"
    >
      <LoginForm
        mode={mode}
        onModeChange={(next) => {
          if (next === mode) return;
          window.location.hash = authHref(next, window.location.hash);
        }}
      />
    </Screen>
  );
}

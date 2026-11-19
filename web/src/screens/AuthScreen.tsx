import { LoginForm } from "../LoginForm";
import { Screen } from "../ui";

/** Standalone sign-in page. The gate modal reuses the same form via LoginForm. */
export function AuthScreen() {
  return (
    <Screen
      title="登录任务游戏"
      lede="用邮箱和密码进入。成功后签发的 JWT 与现有 Bearer 鉴权相同（HS256，sub 为用户 id），并保存在这台浏览器里。新用户带有起始模拟积分，可以直接创建并注资。"
    >
      <LoginForm />
    </Screen>
  );
}

import { useEffect } from "react";
import { LoginForm } from "./LoginForm";

/**
 * Modal login prompt for pages that require a signed-in user. The protected
 * page stays mounted behind it; once login succeeds (email form or the dev
 * quick identity) the parent sees signedIn=true and unmounts the gate, which
 * reveals the requested page without any extra navigation.
 */
export function LoginGate({ onClose }: { onClose: () => void }) {
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === "Escape") onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <div
      className="usercard-backdrop login-gate-backdrop"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget) onClose();
      }}
    >
      <div className="login-gate" role="dialog" aria-modal="true" aria-labelledby="login-gate-title">
        <button type="button" className="usercard-close" aria-label="关闭" onClick={onClose}>
          ×
        </button>
        <h2 id="login-gate-title" className="login-gate-title">
          登录后继续
        </h2>
        <p className="hint login-gate-hint">这个页面需要登录。登录成功后会自动回到这里。</p>
        <LoginForm onSuccess={onClose} />
      </div>
    </div>
  );
}

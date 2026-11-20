import { useEffect, useState, type ChangeEvent, type FormEvent } from "react";
import { isTaskGameError } from "taskgame-client";
import { Avatar } from "../Avatar";
import { AVATAR_SIZE, fileToAvatarDataUrl, notifyAvatar } from "../avatarUtils";
import { useApp } from "../context";
import { formatError, isAbort } from "../format";
import { DevIdentity } from "../identity";
import { notifyProfile } from "../names";
import { nicknameError, nicknameServerError } from "../person";
import { loggedOut } from "../settings";
import { Button, ErrorNote, Field, OkNote, Screen } from "../ui";

export function Settings() {
  const { client, clientError, settings, setSettings } = useApp();
  const [email, setEmail] = useState(settings.email);
  const [nickname, setNickname] = useState("");
  const [saved, setSaved] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [pending, setPending] = useState(false);
  const [avatarPreview, setAvatarPreview] = useState<string | null>(null);
  const [avatarBusy, setAvatarBusy] = useState(false);
  const [avatarError, setAvatarError] = useState<string | null>(null);
  const [avatarNote, setAvatarNote] = useState<string | null>(null);

  useEffect(() => {
    if (!client) return;
    const ac = new AbortController();
    setLoading(true);
    setError(null);
    client
      .me({ signal: ac.signal })
      .then((me) => {
        if (ac.signal.aborted) return;
        setEmail(me.email || settings.email);
        setNickname(me.nickname ?? "");
        setSaved(me.nickname ?? "");
        notifyProfile(me.id, me.nickname ?? "");
      })
      .catch((err: unknown) => {
        if (isAbort(err)) return;
        setError(profileError(err));
      })
      .finally(() => {
        if (!ac.signal.aborted) setLoading(false);
      });
    return () => ac.abort();
  }, [client, settings.email]);

  async function onSubmit(event: FormEvent) {
    event.preventDefault();
    setNote(null);
    const local = nicknameError(nickname);
    if (local) {
      setError(local);
      return;
    }
    if (!client) return;
    setPending(true);
    setError(null);
    try {
      const me = await client.updateMe({ nickname: nickname.trim() });
      setNickname(me.nickname ?? "");
      setSaved(me.nickname ?? "");
      setEmail(me.email || email);
      notifyProfile(me.id, me.nickname ?? "");
      setNote("已保存。刷新页面后仍然是这个昵称。");
    } catch (err) {
      setError(profileError(err));
    } finally {
      setPending(false);
    }
  }

  async function onPickAvatar(event: ChangeEvent<HTMLInputElement>) {
    const file = event.target.files?.[0];
    // Allow choosing the same file again after a failed attempt.
    event.target.value = "";
    if (!file) return;
    setAvatarError(null);
    setAvatarNote(null);
    setAvatarBusy(true);
    try {
      setAvatarPreview(await fileToAvatarDataUrl(file));
    } catch (err) {
      setAvatarPreview(null);
      setAvatarError(err instanceof Error ? err.message : formatError(err));
    } finally {
      setAvatarBusy(false);
    }
  }

  async function onSaveAvatar() {
    if (!client || !avatarPreview) return;
    setAvatarBusy(true);
    setAvatarError(null);
    setAvatarNote(null);
    try {
      const result = await client.putMeAvatar({ image: avatarPreview });
      notifyAvatar(result.user_id);
      setAvatarPreview(null);
      setAvatarNote(`已保存为 ${result.width}×${result.height} 头像，好友列表和聊天里会立即更新。`);
    } catch (err) {
      setAvatarError(formatError(err));
    } finally {
      setAvatarBusy(false);
    }
  }

  if (!client) {
    return (
      <Screen title="用户信息">
        <ErrorNote message={clientError} />
      </Screen>
    );
  }

  return (
    <Screen
      title="用户信息"
      lede="好友和会话里显示为一行「昵称(...用户 id 后 6 位)」。点别人的昵称会打开一对一会话，不需要先加好友。邮箱是登录账号，这里不能修改，密码也不在这一页更改。"
    >
      {loading ? <p className="hint">正在读取资料…</p> : null}
      <form className="form card" onSubmit={onSubmit}>
        <Field label="昵称" hint="去掉首尾空白后 1 到 32 个字符。还没设置时，别人只看到 ... 和用户 id 的后 6 位。">
          <input
            value={nickname}
            maxLength={64}
            autoComplete="nickname"
            placeholder="例如 阿宁"
            onChange={(event) => {
              setNickname(event.target.value);
              setError(null);
              setNote(null);
            }}
          />
        </Field>
        <Field label="邮箱" hint="邮箱是登录账号，这里只读。">
          <input value={email || "（开发密钥账号没有邮箱）"} readOnly />
        </Field>
        <Field label="用户 id" hint="用来区分同名账号。加好友时仍然使用这个 id。">
          <input value={settings.userId} readOnly spellCheck={false} />
        </Field>
        <ErrorNote message={error} />
        <OkNote message={note} />
        <Button type="submit" kind="primary" pending={pending} disabled={pending || loading}>
          {saved ? "更新" : "保存"}
        </Button>
      </form>

      <section className="settings-block">
        <h2>头像</h2>
        <p className="hint">
          上传后会居中裁剪成正方形，并统一转换为 {AVATAR_SIZE}×{AVATAR_SIZE} 的 JPEG。
          好友列表、资料卡和聊天消息都会显示这张图片；没有上传时显示昵称首字。
        </p>
        <div className="avatar-editor">
          {avatarPreview ? (
            <img className="avatar-editor-preview" src={avatarPreview} alt="头像预览" />
          ) : (
            <Avatar userId={settings.userId} name={saved || settings.userId} large />
          )}
          <div className="avatar-editor-actions">
            <label className="btn secondary">
              选择图片
              <input
                type="file"
                accept="image/*"
                hidden
                disabled={avatarBusy}
                onChange={onPickAvatar}
              />
            </label>
            <Button
              type="button"
              kind="primary"
              pending={avatarBusy}
              disabled={avatarBusy || !avatarPreview}
              onClick={onSaveAvatar}
            >
              保存头像
            </Button>
            {avatarPreview ? (
              <Button
                type="button"
                kind="ghost"
                disabled={avatarBusy}
                onClick={() => {
                  setAvatarPreview(null);
                  setAvatarNote(null);
                }}
              >
                取消
              </Button>
            ) : null}
            <small className="hint">支持 JPG / PNG / GIF。大图会先在本地裁剪压缩再上传。</small>
          </div>
        </div>
        <ErrorNote message={avatarError} />
        <OkNote message={avatarNote} />
      </section>

      <section className="settings-block">
        <h2>账号</h2>
        <p className="hint">{settings.email || "开发密钥账号没有邮箱"} · {settings.userId}</p>
        <div className="who-actions">
          <Button kind="secondary" onClick={() => setSettings(loggedOut(settings))}>
            退出登录
          </Button>
        </div>
      </section>

      <section className="settings-block">
        <h2>连接与开发身份</h2>
        <DevIdentity />
      </section>
    </Screen>
  );
}

function profileError(error: unknown): string {
  if (isTaskGameError(error)) {
    const mapped = nicknameServerError(error.message);
    if (mapped) return mapped;
  }
  return formatError(error);
}

import { useEffect, useState } from "react";
import { avatarUrl, getAvatarVersion, subscribeAvatar } from "./avatarUtils";
import { useApp } from "./context";
import { avatarColor, avatarInitial } from "./friendsView";
import { cx } from "./ui";

/** Cache-busting version for a user's avatar, refreshed across tabs. */
function useAvatarVersion(userId: string): number {
  const [version, setVersion] = useState(() => getAvatarVersion(userId));
  useEffect(() => {
    setVersion(getAvatarVersion(userId));
    return subscribeAvatar((changedId, next) => {
      if (changedId === userId) setVersion(next);
    });
  }, [userId]);
  return version;
}

/**
 * Shared avatar: loads the stored 720x720 picture and falls back to a
 * deterministic colour tile with the user's initial when there is no avatar
 * (404), the client is unavailable, or the image fails to load.
 */
export function Avatar({
  userId,
  name,
  large = false,
  className,
  title,
}: {
  userId: string;
  name?: string | null;
  large?: boolean;
  className?: string;
  title?: string;
}) {
  const { client } = useApp();
  const version = useAvatarVersion(userId);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    setFailed(false);
  }, [userId, version]);

  const classes = cx("avatar", large && "avatar-lg", className);
  if (!client || !userId || failed) {
    return (
      <span
        className={classes}
        style={{ backgroundColor: avatarColor(userId || "me"), color: "#ffffff" }}
        title={title}
        aria-hidden="true"
      >
        {avatarInitial(name || userId || "?", "?")}
      </span>
    );
  }
  return (
    <span className={classes} title={title} aria-hidden="true">
      <img src={avatarUrl(client, userId, version)} alt="" onError={() => setFailed(true)} />
    </span>
  );
}

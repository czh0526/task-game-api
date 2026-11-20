import type { Friend, FriendGroupWithFriends, FriendRequest } from "taskgame-client";

const AVATAR_COLORS = [
  "#5b8ff9",
  "#61ddaa",
  "#65789b",
  "#f6bd16",
  "#7262fd",
  "#78d3f8",
  "#9661bc",
  "#f08f56",
];

export interface FriendSectionView {
  key: string;
  title: string;
  friends: Friend[];
}

/** First visible character of a display name, with a stable fallback. */
export function avatarInitial(name: string | null | undefined, fallback = "?"): string {
  const chars = Array.from((name ?? "").trim());
  return chars.length > 0 ? chars[0].toLocaleUpperCase() : fallback;
}

/** Deterministic avatar colour so a user keeps the same tile across reloads. */
export function avatarColor(seed: string): string {
  let hash = 0;
  for (let index = 0; index < seed.length; index += 1) {
    hash = (hash * 31 + seed.charCodeAt(index)) | 0;
  }
  return AVATAR_COLORS[Math.abs(hash) % AVATAR_COLORS.length];
}

/**
 * Build the contact sections, optionally narrowed by a nickname/user-id query.
 * With `includeEmpty` every group (and the ungrouped bucket) is returned even
 * when it has no matching friends, so a drag can target an empty folder.
 */
export function friendSections(
  groups: FriendGroupWithFriends[],
  ungrouped: Friend[],
  query: string,
  includeEmpty = false,
): FriendSectionView[] {
  const needle = query.trim().toLocaleLowerCase();
  const matches = (friend: Friend): boolean =>
    !needle ||
    (friend.nickname ?? "").toLocaleLowerCase().includes(needle) ||
    friend.user_id.toLocaleLowerCase().includes(needle);
  const sections: FriendSectionView[] = [
    { key: "", title: "未分组", friends: ungrouped.filter(matches) },
    ...groups.map((group) => ({
      key: group.id,
      title: group.name,
      friends: group.friends.filter(matches),
    })),
  ];
  return includeEmpty ? sections : sections.filter((section) => section.friends.length > 0);
}

/** Whether dropping a friend on a group would actually change its placement. */
export function placementChanged(currentGroupId: string, targetGroupId: string): boolean {
  return (currentGroupId || "") !== (targetGroupId || "");
}

/** The other person in a friend request, relative to the signed-in user. */
export function requestPerson(request: FriendRequest, selfId: string): { userId: string; nickname?: string } {
  return request.to_user_id === selfId
    ? { userId: request.from_user_id, nickname: request.from_nickname }
    : { userId: request.to_user_id, nickname: request.to_nickname };
}

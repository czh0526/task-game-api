/**
 * Pure scroll math for the chat message panel.
 *
 * When a new message is displayed we want the first line of that message to
 * sit at the vertical middle of the panel, so it is easy to notice without
 * depending on which end of the list happens to be visible.
 */

/** Where a message sits inside the scrollable content, in pixels from the top. */
export function messageTopInContent(input: {
  messageViewportTop: number;
  panelViewportTop: number;
  panelScrollTop: number;
}): number {
  return input.messageViewportTop - input.panelViewportTop + input.panelScrollTop;
}

/**
 * Scroll offset that lands `messageTopInContent` at the vertical middle of the
 * panel. Clamped to the panel's real scroll range so browsers don't have to.
 * A panel with nothing to scroll always reports 0.
 */
export function centerMessageScrollTop(input: {
  messageTopInContent: number;
  panelHeight: number;
  maxScrollTop: number;
}): number {
  const { messageTopInContent, panelHeight, maxScrollTop } = input;
  if (maxScrollTop <= 0 || !Number.isFinite(messageTopInContent)) return 0;
  const wanted = messageTopInContent - panelHeight / 2;
  return Math.max(0, Math.min(Math.round(wanted), Math.max(0, Math.round(maxScrollTop))));
}

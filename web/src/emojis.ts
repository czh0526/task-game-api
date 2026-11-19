// Built-in sticker palette. Each sticker is a Unicode emoji carried in a
// message's `text` field with `type: "sticker"`, so no attachment upload is
// needed. Kept as plain data so it is easy to extend or test.

export interface EmojiSticker {
  char: string;
  label: string;
}

export const EMOJI_STICKERS: readonly EmojiSticker[] = [
  { char: "😀", label: "笑" },
  { char: "😂", label: "笑哭" },
  { char: "🤣", label: "笑翻" },
  { char: "😊", label: "微笑" },
  { char: "😍", label: "喜欢" },
  { char: "😎", label: "酷" },
  { char: "🥳", label: "庆祝" },
  { char: "😭", label: "哭" },
  { char: "😡", label: "生气" },
  { char: "😱", label: "震惊" },
  { char: "🤔", label: "思考" },
  { char: "😴", label: "困" },
  { char: "👍", label: "赞" },
  { char: "👎", label: "踩" },
  { char: "👏", label: "鼓掌" },
  { char: "🙏", label: "谢谢" },
  { char: "💪", label: "加油" },
  { char: "🤝", label: "合作" },
  { char: "❤️", label: "爱心" },
  { char: "🔥", label: "火" },
  { char: "🎉", label: "恭喜" },
  { char: "💰", label: "钱" },
  { char: "✅", label: "完成" },
  { char: "❓", label: "疑问" },
];

const labelByChar = new Map(EMOJI_STICKERS.map((item) => [item.char, item.label]));

/** Human label for a sticker message; falls back to the raw text. */
export function stickerLabel(text: string): string {
  return labelByChar.get(text.trim()) ?? text;
}

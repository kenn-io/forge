// GitHub's reaction choices, in the order its picker shows them. Glyphs are
// escapes so the source carries no emoji literals.
export const commentReactions = [
  { content: "+1", name: "Thumbs up", glyph: "\u{1F44D}" },
  { content: "-1", name: "Thumbs down", glyph: "\u{1F44E}" },
  { content: "laugh", name: "Laugh", glyph: "\u{1F604}" },
  { content: "hooray", name: "Hooray", glyph: "\u{1F389}" },
  { content: "confused", name: "Confused", glyph: "\u{1F615}" },
  { content: "heart", name: "Heart", glyph: "\u{2764}\u{FE0F}" },
  { content: "rocket", name: "Rocket", glyph: "\u{1F680}" },
  { content: "eyes", name: "Eyes", glyph: "\u{1F440}" },
] as const;

export type CommentReactionContent = (typeof commentReactions)[number]["content"];

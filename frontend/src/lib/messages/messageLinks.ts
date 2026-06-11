import type { MessageLinkRef, MessageLinksPatch } from "./types";

export interface MessageLinkInput {
  message_id: number;
  conversation_id?: number;
  subject: string;
  from: string;
  sent_at: string;
}

const MAX_SUBJECT_LEN = 500;
const MAX_FROM_LEN = 320;

export function computeAddMessageLinkPatch(
  currentLinks: readonly MessageLinkRef[],
  input: MessageLinkInput,
): MessageLinksPatch | null {
  validateLinkInput(input);
  if (currentLinks.some((link) => link.message_id === input.message_id)) {
    return null;
  }

  const link: MessageLinkRef = {
    message_id: input.message_id,
    ...(input.conversation_id !== undefined ? { conversation_id: input.conversation_id } : {}),
    subject: truncateSubject(input.subject),
    from: input.from.slice(0, MAX_FROM_LEN),
    sent_at: input.sent_at,
    added_at: new Date().toISOString(),
  };

  return { mail_links: [...currentLinks, link] };
}

export function computeRemoveMessageLinkPatch(
  currentLinks: readonly MessageLinkRef[],
  messageId: number,
): MessageLinksPatch | null {
  if (!currentLinks.some((link) => link.message_id === messageId)) {
    return null;
  }

  const next = currentLinks.filter((link) => link.message_id !== messageId);
  return { mail_links: next.length === 0 ? null : next };
}

export function readMessageLinks(metadata: { mail_links?: unknown } | undefined): MessageLinkRef[] {
  const raw = metadata?.mail_links;
  if (!Array.isArray(raw)) return [];
  return raw.filter(isMessageLinkRef);
}

function isMessageLinkRef(value: unknown): value is MessageLinkRef {
  if (value === null || typeof value !== "object") return false;
  const link = value as Record<string, unknown>;

  if (!Number.isInteger(link.message_id) || (link.message_id as number) <= 0) return false;
  if (
    link.conversation_id !== undefined &&
    (!Number.isInteger(link.conversation_id) || (link.conversation_id as number) <= 0)
  ) {
    return false;
  }
  return (
    typeof link.subject === "string" &&
    typeof link.from === "string" &&
    typeof link.sent_at === "string" &&
    typeof link.added_at === "string"
  );
}

function validateLinkInput(input: MessageLinkInput): void {
  if (!Number.isInteger(input.message_id) || input.message_id <= 0) {
    throw new Error("message link: message_id must be a positive integer");
  }
  if (input.conversation_id !== undefined && (!Number.isInteger(input.conversation_id) || input.conversation_id <= 0)) {
    throw new Error("message link: conversation_id must be a positive integer if provided");
  }
  if (!input.from || input.from.trim().length === 0) {
    throw new Error("message link: from is required");
  }
  if (Number.isNaN(Date.parse(input.sent_at))) {
    throw new Error("message link: sent_at is not a parseable datetime");
  }
}

function truncateSubject(subject: string): string {
  return subject.length > MAX_SUBJECT_LEN ? `${subject.slice(0, MAX_SUBJECT_LEN - 1)}\u2026` : subject;
}

import type { components } from "@middleman/ui/api/schema";

export type MessageSummary = components["schemas"]["MessageSummary"];

export interface MessageLinkRef {
  message_id: number;
  conversation_id?: number;
  subject: string;
  from: string;
  sent_at: string;
  added_at: string;
}

export interface MessageLinksPatch {
  mail_links: MessageLinkRef[] | null;
}

export interface IssueRef {
  uid: string;
  short_id: string;
  qualified_id: string;
  title: string;
  status: string;
}

export interface IssueSummary extends IssueRef {
  id?: number | undefined;
  metadata?: {
    mail_links?: unknown;
    [key: string]: unknown;
  };
}

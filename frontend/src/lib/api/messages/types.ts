import type { components } from "@middleman/ui/api/schema";

export type MessageAttachmentMeta = components["schemas"]["AttachmentMeta"];

export interface MessageSummary {
  id: number;
  conversation_id: number;
  subject: string;
  from: string;
  to: string[];
  cc: string[];
  bcc: string[];
  sent_at: string;
  snippet: string;
  labels: string[];
  has_attachments: boolean;
  size_bytes: number;
  deleted_at: string | null;
}

export interface MessageDetailData extends MessageSummary {
  body: string;
  body_html?: string;
  remote_image_count?: number;
  remote_image_token?: string;
  html_sanitization_failed?: boolean;
  attachments: MessageAttachmentMeta[];
}

export type MessagesSearchMode = "fts" | "vector" | "hybrid";

export interface MessagesSearchResult {
  query: string;
  mode: MessagesSearchMode;
  total: number;
  page: number;
  page_size: number;
  paginatable: boolean;
  messages: MessageSummary[];
}

export type MessagesAggregateView =
  | "senders"
  | "sender_names"
  | "recipients"
  | "recipient_names"
  | "domains"
  | "labels"
  | "time";

export type MessagesAggregateRow = components["schemas"]["AggregateRow"];

export interface MessagesAggregateResponse {
  view_type: MessagesAggregateView;
  rows: MessagesAggregateRow[];
}

export interface MessagesCapabilities {
  configured: boolean;
  ok: boolean;
  status?: "down" | "unauthorized" | "misconfigured" | "ok";
  status_detail?: string;
  modes: MessagesSearchMode[];
  features: {
    threads_endpoint: boolean;
    mutations: boolean;
    attachments_download: boolean;
    sse_events: boolean;
  };
  url?: string;
  api_key_env?: string;
}

export interface MessagesAPI {
  capabilities(): Promise<MessagesCapabilities>;
  search(
    q: string,
    opts?: { mode?: MessagesSearchMode; page?: number; pageSize?: number },
  ): Promise<MessagesSearchResult>;
  message(id: number): Promise<MessageDetailData>;
  inlineURL(id: number, cid: string): string;
  remoteImageURL(id: number, token: string, index: string): string;
  aggregates(
    view: MessagesAggregateView,
    opts?: {
      q?: string;
      hideDeleted?: boolean;
      sort?: "count" | "size" | "name";
      direction?: "asc" | "desc";
      limit?: number;
    },
  ): Promise<MessagesAggregateResponse>;
  thread(conversationId: number): Promise<MessageSummary[]>;
  configure(input: { url: string; api_key_env: string }): Promise<MessagesCapabilities>;
}

export interface MessagesAPIError extends Error {
  status: number;
  code?: string;
  reason?: string;
}

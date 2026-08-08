export interface KeySpec {
  key: string;
  ctrlOrMeta?: boolean;
  shift?: boolean;
  alt?: boolean;
}

export interface ModalFrameAction {
  id: string;
  label: string;
  binding: KeySpec | KeySpec[] | null;
  priority?: number;
  when?: (ctx: unknown) => boolean;
  handler: (ctx: unknown) => void | Promise<void>;
}

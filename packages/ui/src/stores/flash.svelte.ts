let message = $state<string | null>(null);
let timer: ReturnType<typeof setTimeout> | null = null;

export function showFlash(msg: string, durationMs = 4000): void {
  if (timer !== null) clearTimeout(timer);
  message = msg;
  timer = setTimeout(() => {
    message = null;
    timer = null;
  }, durationMs);
}

export function getFlashMessage(): string | null {
  return message;
}

export function dismissFlash(): void {
  if (timer !== null) clearTimeout(timer);
  message = null;
  timer = null;
}

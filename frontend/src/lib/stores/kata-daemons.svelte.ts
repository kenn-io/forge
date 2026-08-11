import type { KataDaemonInfo } from "../api/kata/integration.js";
import { fetchKataDaemons } from "../api/kata/integration.js";

interface KataDaemonsStoreOptions {
  fetchDaemons?: (signal?: AbortSignal) => Promise<readonly KataDaemonInfo[]>;
}

export interface KataDaemonsStore {
  daemons(): readonly KataDaemonInfo[];
  defaultDaemonID(): string;
  loading(): boolean;
  loaded(): boolean;
  error(): string | null;
  load(signal?: AbortSignal): Promise<void>;
}

class KataDaemonsStoreImpl implements KataDaemonsStore {
  #fetchDaemons: (signal?: AbortSignal) => Promise<readonly KataDaemonInfo[]>;
  #daemons = $state.raw<KataDaemonInfo[]>([]);
  #loading = $state(false);
  #loaded = $state(false);
  #error = $state<string | null>(null);
  #generation = 0;

  constructor(options: KataDaemonsStoreOptions) {
    this.#fetchDaemons = options.fetchDaemons ?? fetchKataDaemons;
  }

  daemons(): readonly KataDaemonInfo[] {
    return this.#daemons;
  }

  defaultDaemonID(): string {
    return this.#daemons.find((daemon) => daemon.default)?.id ?? this.#daemons[0]?.id ?? "";
  }

  loading(): boolean {
    return this.#loading;
  }

  loaded(): boolean {
    return this.#loaded;
  }

  error(): string | null {
    return this.#error;
  }

  async load(signal?: AbortSignal): Promise<void> {
    const generation = ++this.#generation;
    this.#loading = true;
    this.#loaded = false;
    this.#error = null;
    this.#daemons = [];
    try {
      const daemons = await this.#fetchDaemons(signal);
      if (generation !== this.#generation || signal?.aborted) return;
      this.#daemons = [...daemons];
    } catch (cause) {
      if (generation !== this.#generation || signal?.aborted) return;
      this.#error = cause instanceof Error ? cause.message : "Unable to load Kata daemons.";
    } finally {
      if (generation === this.#generation && !signal?.aborted) {
        this.#loading = false;
        this.#loaded = true;
      }
    }
  }
}

export function createKataDaemonsStore(options: KataDaemonsStoreOptions = {}): KataDaemonsStore {
  return new KataDaemonsStoreImpl(options);
}

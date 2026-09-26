// The room's connection to the server: one event stream in, small JSON
// posts out. The stream is the only thing that tells the island who else
// is in the room; the posts carry what the island has to say.

export interface RoomEvent {
  type: string;
  from?: string;
  to?: string;
  data?: unknown;
}

export interface Peer {
  id: string;
  name: string;
  role: string;
}

export interface Hello {
  self: string;
  peers: Peer[];
  doc: string[];
  language: string;
  source: string;
}

export type Listener = (ev: RoomEvent) => void;

// Reconnect backoff: the stream drops when the server restarts or a proxy
// gives up on it, and a fresh connection is a fresh hello.
const RETRY_MS = [1000, 2000, 4000, 8000];

export class Channel {
  private source: EventSource | null = null;
  private listeners: Listener[] = [];
  private retries = 0;
  private closed = false;

  constructor(
    readonly base: string,
    readonly csrf: string,
  ) {}

  on(fn: Listener): void {
    this.listeners.push(fn);
  }

  connect(): void {
    if (this.closed) return;
    const source = new EventSource(this.base + "/events");
    this.source = source;
    const relay = (ev: MessageEvent) => {
      this.retries = 0;
      let parsed: RoomEvent;
      try {
        parsed = JSON.parse(ev.data as string) as RoomEvent;
      } catch {
        return;
      }
      for (const fn of this.listeners) fn(parsed);
    };
    for (const type of ["hello", "peer-joined", "peer-left", "signal", "doc", "code"]) {
      source.addEventListener(type, relay as EventListener);
    }
    source.onerror = () => {
      source.close();
      if (this.source !== source || this.closed) return;
      this.source = null;
      const wait = RETRY_MS[Math.min(this.retries, RETRY_MS.length - 1)];
      this.retries++;
      for (const fn of this.listeners) fn({ type: "disconnected" });
      setTimeout(() => this.connect(), wait);
    };
  }

  close(): void {
    this.closed = true;
    this.source?.close();
    this.source = null;
  }

  async post<T = unknown>(path: string, body: unknown): Promise<T> {
    const res = await fetch(this.base + path, {
      method: "POST",
      credentials: "same-origin",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": this.csrf },
      body: JSON.stringify(body),
    });
    if (!res.ok) {
      let detail = res.statusText;
      try {
        detail = (await res.text()) || detail;
      } catch {
        // keep the status text
      }
      throw new Error(detail.trim());
    }
    if (res.status === 204) return undefined as T;
    return (await res.json()) as T;
  }
}

// Base64 helpers for the binary the editor sync carries.
export function toBase64(bytes: Uint8Array): string {
  let s = "";
  for (let i = 0; i < bytes.length; i += 0x8000) {
    s += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  }
  return btoa(s);
}

export function fromBase64(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

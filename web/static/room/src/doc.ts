// The shared editor's document: a Yjs text bound to CodeMirror, kept in
// step through the server. Every local change is posted as an update and
// every update on the stream is applied; the server replays its log to a
// newcomer, and asks for a snapshot when the log grows long.

import * as Y from "yjs";
import { Awareness, applyAwarenessUpdate, encodeAwarenessUpdate } from "y-protocols/awareness";
import { yCollab } from "y-codemirror.next";
import type { Extension } from "@codemirror/state";
import { Channel, fromBase64, toBase64 } from "./signal";

export class SharedDoc {
  readonly ydoc = new Y.Doc();
  readonly ytext: Y.Text;
  readonly awareness: Awareness;
  private self = "";
  private applyingRemote = false;

  constructor(
    private readonly channel: Channel,
    private readonly onLocalChange: () => void,
  ) {
    this.ytext = this.ydoc.getText("code");
    this.awareness = new Awareness(this.ydoc);
    this.ydoc.on("update", (update: Uint8Array, origin: unknown) => {
      if (this.applyingRemote || origin === "remote") return;
      void this.push(update, false);
      this.onLocalChange();
    });
    this.awareness.on("update", ({ added, updated, removed }: { added: number[]; updated: number[]; removed: number[] }, origin: unknown) => {
      if (origin === "remote" || !this.self) return;
      const changed = added.concat(updated, removed);
      const update = encodeAwarenessUpdate(this.awareness, changed);
      void this.channel.post("/signal", { from: this.self, to: "", data: { type: "awareness", update: toBase64(update) } }).catch(() => undefined);
    });
  }

  // begin seeds the document from the server's hello: the update log if
  // there is one, else the persisted source, else nothing.
  begin(self: string, name: string, role: string, log: string[], source: string): void {
    this.self = self;
    this.applyingRemote = true;
    try {
      for (const b64 of log) Y.applyUpdate(this.ydoc, fromBase64(b64), "remote");
    } finally {
      this.applyingRemote = false;
    }
    if (log.length === 0 && this.ytext.length === 0 && source) {
      this.ytext.insert(0, source);
    }
    this.awareness.setLocalStateField("user", { name, role, color: role === "candidate" ? "#a7a1db" : "#9184d9" });
  }

  applyRemote(b64: string): void {
    this.applyingRemote = true;
    try {
      Y.applyUpdate(this.ydoc, fromBase64(b64), "remote");
    } finally {
      this.applyingRemote = false;
    }
  }

  applyAwareness(b64: string): void {
    applyAwarenessUpdate(this.awareness, fromBase64(b64), "remote");
  }

  private async push(update: Uint8Array, snapshot: boolean): Promise<void> {
    try {
      const res = await this.channel.post<{ snapshot_wanted: boolean }>("/doc", {
        from: this.self,
        update: toBase64(update),
        snapshot,
      });
      if (res && res.snapshot_wanted && !snapshot) {
        await this.push(Y.encodeStateAsUpdate(this.ydoc), true);
      }
    } catch (err) {
      console.warn("doc sync failed", err);
    }
  }

  // reconnected re-seeds after the stream came back: whatever the server
  // has that we do not is applied; whatever we have that it lost is pushed
  // as a snapshot.
  reconnected(self: string, log: string[]): void {
    this.self = self;
    for (const b64 of log) this.applyRemote(b64);
    void this.push(Y.encodeStateAsUpdate(this.ydoc), true);
  }

  extension(): Extension {
    return yCollab(this.ytext, this.awareness);
  }

  text(): string {
    return this.ytext.toString();
  }

  destroy(): void {
    this.awareness.destroy();
    this.ydoc.destroy();
  }
}

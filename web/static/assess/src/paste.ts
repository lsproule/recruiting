// Paste classification: text copied from the page's own editor is hashed
// and remembered, so a paste whose hash matches is "internal" (moving code
// around) rather than material brought in from outside.

export async function sha256Hex(text: string): Promise<string> {
  const data = new TextEncoder().encode(text);
  const digest = await crypto.subtle.digest("SHA-256", data);
  return Array.from(new Uint8Array(digest), (b) => b.toString(16).padStart(2, "0")).join("");
}

export interface PasteRecord {
  len: number;
  sha256: string;
  internal: boolean;
}

export class PasteClassifier {
  private readonly internal = new Set<string>();

  constructor(private readonly storage: Storage | null, private readonly key: string) {
    try {
      const saved = storage?.getItem(key);
      if (saved) for (const h of JSON.parse(saved) as string[]) this.internal.add(h);
    } catch {
      // Without storage the set lives for the page only.
    }
  }

  // copied records text leaving the page's editor via copy or cut.
  async copied(text: string): Promise<void> {
    if (text.length === 0) return;
    this.internal.add(await sha256Hex(text));
    try {
      this.storage?.setItem(this.key, JSON.stringify(Array.from(this.internal)));
    } catch {
      // See constructor.
    }
  }

  async classify(text: string): Promise<PasteRecord> {
    const hash = await sha256Hex(text);
    return { len: text.length, sha256: hash, internal: this.internal.has(hash) };
  }
}

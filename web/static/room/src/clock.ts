// The sprint clock as the console and the lobby read it: the server says
// where the sprint stands and when that changes; the page counts down to it
// on its own clock, corrected by the offset it measured.

export type Phase = "before" | "round" | "break" | "after";

export interface RoundEntry {
  pairing: string;
  round: number;
  with: string;
  starts_at: number;
  ends_at: number;
  room: string;
  rated: boolean;
  rate?: string;
}

export interface SprintState {
  now: number;
  status: string;
  phase: Phase;
  round: number;
  rounds: number;
  next: number;
  starts_at: number;
  ends_at: number;
  round_seconds: number;
  break_seconds: number;
  mine: RoundEntry[];
  current: RoundEntry | null;
}

// offset is how far this browser's clock runs ahead of the server's, so a
// countdown here ends when the server says the round does.
export function clockOffset(state: SprintState, localNow: number): number {
  return localNow - state.now;
}

// formatCountdown writes milliseconds as m:ss, never negative.
export function formatCountdown(ms: number): string {
  const s = Math.max(0, Math.ceil(ms / 1000));
  const m = Math.floor(s / 60);
  return String(m) + ":" + String(s % 60).padStart(2, "0");
}

// nextMine is the viewer's first conversation that has not ended yet, by
// the server's clock.
export function nextMine(state: SprintState, serverNow: number): RoundEntry | null {
  let best: RoundEntry | null = null;
  for (const entry of state.mine) {
    if (entry.ends_at <= serverNow) continue;
    if (!best || entry.starts_at < best.starts_at) best = entry;
  }
  return best;
}

// lastEnded is the viewer's most recent conversation that has ended, which
// is the one the rating card asks about during a break.
export function lastEnded(state: SprintState, serverNow: number): RoundEntry | null {
  let best: RoundEntry | null = null;
  for (const entry of state.mine) {
    if (entry.ends_at > serverNow) continue;
    if (!best || entry.ends_at > best.ends_at) best = entry;
  }
  return best;
}

// headline is the one line under the title: what is happening and until when.
export function headline(state: SprintState, serverNow: number): string {
  switch (state.phase) {
    case "before":
      return "Starts in " + formatCountdown(state.starts_at - serverNow);
    case "round":
      return "Round " + (state.round + 1) + " of " + state.rounds + " · " + formatCountdown(state.next - serverNow) + " left";
    case "break":
      return "Break · round " + (state.round + 2) + " starts in " + formatCountdown(state.next - serverNow);
    default:
      return "The sprint has ended.";
  }
}

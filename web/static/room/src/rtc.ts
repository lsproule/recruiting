// The media mesh: one RTCPeerConnection per other participant, negotiated
// the "perfect negotiation" way so that two peers offering at once never
// wedge. Signalling goes through the server; media goes peer to peer.

export type SignalSender = (to: string, data: unknown) => void;

export interface TrackEvent {
  peer: string;
  stream: MediaStream;
  kind: "camera" | "screen";
}

export interface MeshCallbacks {
  onTrack(ev: TrackEvent): void;
  onStreamEnded(peer: string, streamId: string): void;
  onState(peer: string, state: RTCPeerConnectionState): void;
}

interface Signal {
  type: "description" | "candidate" | "screen" | "awareness";
  description?: RTCSessionDescriptionInit;
  candidate?: RTCIceCandidateInit | null;
  streamId?: string;
  update?: string;
}

// isPolite decides who yields in a glare: the lexically smaller id backs off
// and takes the other's offer. Both sides compute the same answer.
export function isPolite(selfId: string, peerId: string): boolean {
  return selfId < peerId;
}

class PeerLink {
  readonly pc: RTCPeerConnection;
  private makingOffer = false;
  private ignoreOffer = false;
  private polite: boolean;
  // screens is what the remote said it is sharing, by stream id, so the
  // tile it lands in is labelled before the frames arrive.
  private screens = new Set<string>();

  constructor(
    readonly id: string,
    selfId: string,
    ice: RTCIceServer[],
    private readonly send: SignalSender,
    private readonly cb: MeshCallbacks,
    private readonly local: MediaStream | null,
    private readonly screen: MediaStream | null,
  ) {
    this.polite = isPolite(selfId, id);
    this.pc = new RTCPeerConnection({ iceServers: ice });
    this.pc.onnegotiationneeded = () => void this.negotiate();
    this.pc.onicecandidate = (ev) => this.send(this.id, { type: "candidate", candidate: ev.candidate ? ev.candidate.toJSON() : null } as Signal);
    this.pc.onconnectionstatechange = () => this.cb.onState(this.id, this.pc.connectionState);
    this.pc.ontrack = (ev) => {
      const stream = ev.streams[0];
      if (!stream) return;
      const kind = this.screens.has(stream.id) ? "screen" : "camera";
      this.cb.onTrack({ peer: this.id, stream, kind });
      stream.onremovetrack = () => {
        if (stream.getTracks().length === 0) this.cb.onStreamEnded(this.id, stream.id);
      };
      ev.track.onended = () => this.cb.onStreamEnded(this.id, stream.id);
    };
    if (this.local) for (const track of this.local.getTracks()) this.pc.addTrack(track, this.local);
    if (this.screen) this.addScreen(this.screen);
  }

  private async negotiate(): Promise<void> {
    try {
      this.makingOffer = true;
      await this.pc.setLocalDescription();
      this.send(this.id, { type: "description", description: this.pc.localDescription!.toJSON() } as Signal);
    } catch (err) {
      console.warn("negotiation failed", err);
    } finally {
      this.makingOffer = false;
    }
  }

  async handle(sig: Signal): Promise<void> {
    if (sig.type === "screen" && sig.streamId) {
      this.screens.add(sig.streamId);
      return;
    }
    if (sig.type === "description" && sig.description) {
      const offerCollision = sig.description.type === "offer" && (this.makingOffer || this.pc.signalingState !== "stable");
      this.ignoreOffer = !this.polite && offerCollision;
      if (this.ignoreOffer) return;
      await this.pc.setRemoteDescription(sig.description);
      if (sig.description.type === "offer") {
        await this.pc.setLocalDescription();
        this.send(this.id, { type: "description", description: this.pc.localDescription!.toJSON() } as Signal);
      }
      return;
    }
    if (sig.type === "candidate") {
      try {
        await this.pc.addIceCandidate(sig.candidate ?? undefined);
      } catch (err) {
        if (!this.ignoreOffer) console.warn("candidate failed", err);
      }
    }
  }

  addScreen(stream: MediaStream): void {
    this.send(this.id, { type: "screen", streamId: stream.id } as Signal);
    for (const track of stream.getTracks()) this.pc.addTrack(track, stream);
  }

  removeStreamTracks(stream: MediaStream): void {
    for (const sender of this.pc.getSenders()) {
      if (sender.track && stream.getTracks().includes(sender.track)) this.pc.removeTrack(sender);
    }
  }

  replaceTrack(kind: "audio" | "video", track: MediaStreamTrack | null): void {
    for (const sender of this.pc.getSenders()) {
      if (sender.track && sender.track.kind === kind && (!this.screen || !this.screen.getTracks().includes(sender.track))) {
        void sender.replaceTrack(track);
      }
    }
  }

  close(): void {
    this.pc.close();
  }
}

export class Mesh {
  private links = new Map<string, PeerLink>();
  private local: MediaStream | null = null;
  private screen: MediaStream | null = null;

  constructor(
    readonly selfId: string,
    private readonly ice: RTCIceServer[],
    private readonly send: SignalSender,
    private readonly cb: MeshCallbacks,
  ) {}

  setLocalStream(stream: MediaStream | null): void {
    this.local = stream;
    for (const link of this.links.values()) {
      for (const kind of ["audio", "video"] as const) {
        link.replaceTrack(kind, stream?.getTracks().find((t) => t.kind === kind) ?? null);
      }
    }
  }

  // addPeer opens a link. Both sides call it — the newcomer for everyone it
  // found in the room, the others when told of the newcomer — and perfect
  // negotiation sorts out who ends up offering.
  addPeer(id: string): void {
    if (this.links.has(id) || id === this.selfId) return;
    this.links.set(id, new PeerLink(id, this.selfId, this.ice, this.send, this.cb, this.local, this.screen));
  }

  removePeer(id: string): void {
    this.links.get(id)?.close();
    this.links.delete(id);
  }

  async handle(from: string, data: unknown): Promise<void> {
    const link = this.links.get(from);
    if (!link) return;
    await link.handle(data as Signal);
  }

  shareScreen(stream: MediaStream): void {
    this.stopScreen();
    this.screen = stream;
    for (const link of this.links.values()) link.addScreen(stream);
  }

  stopScreen(): void {
    if (!this.screen) return;
    const stream = this.screen;
    this.screen = null;
    for (const link of this.links.values()) link.removeStreamTracks(stream);
    for (const track of stream.getTracks()) track.stop();
  }

  peers(): string[] {
    return [...this.links.keys()];
  }

  close(): void {
    for (const link of this.links.values()) link.close();
    this.links.clear();
    this.stopScreen();
  }
}

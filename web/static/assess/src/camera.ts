// The camera side of the integrity measures: opening the device, turning a
// live frame into a small JPEG, and posting one to the session's endpoints.
// getUserMedia is only ever called from a click on the consent screen, or —
// once permission is already granted — from the session it agreed to.

import { SNAPSHOT_MAX_WIDTH, SNAPSHOT_QUALITY, SNAPSHOT_TYPE } from "./integrity";

export interface Camera {
  stream: MediaStream;
  frame(): Promise<Blob | null>;
  stop(): void;
}

// openCamera asks for the webcam and answers once it is producing pictures,
// so a caller that resolves has something to show.
export async function openCamera(): Promise<Camera> {
  const media = navigator.mediaDevices;
  if (!media || typeof media.getUserMedia !== "function") throw new Error("this browser has no camera support");
  const stream = await media.getUserMedia({ video: { width: { ideal: 1280 } }, audio: false });
  const video = document.createElement("video");
  video.autoplay = true;
  video.muted = true;
  video.playsInline = true;
  video.srcObject = stream;
  await video.play().catch(() => undefined);
  return {
    stream,
    frame: () => frameFrom(video),
    stop: () => {
      for (const track of stream.getTracks()) track.stop();
      video.srcObject = null;
    },
  };
}

// frameFrom draws the current picture into a canvas no wider than a webcam
// still needs to be and encodes it as a JPEG the upload cap fits.
export function frameFrom(video: HTMLVideoElement): Promise<Blob | null> {
  const w = video.videoWidth;
  const h = video.videoHeight;
  if (!w || !h) return Promise.resolve(null);
  const scale = Math.min(1, SNAPSHOT_MAX_WIDTH / w);
  const canvas = document.createElement("canvas");
  canvas.width = Math.round(w * scale);
  canvas.height = Math.round(h * scale);
  const ctx = canvas.getContext("2d");
  if (!ctx) return Promise.resolve(null);
  ctx.drawImage(video, 0, 0, canvas.width, canvas.height);
  return new Promise((resolve) => canvas.toBlob((b) => resolve(b), SNAPSHOT_TYPE, SNAPSHOT_QUALITY));
}

// uploadFrame posts one JPEG as the multipart body the attempt API takes.
// The CSRF token travels as a header, which is what the middleware reads
// before it touches the body.
export async function uploadFrame(url: string, csrf: string, frame: Blob, seq?: number): Promise<void> {
  const form = new FormData();
  if (seq !== undefined) form.set("seq", String(seq));
  form.append("file", frame, "frame.jpg");
  const res = await fetch(url, { method: "POST", credentials: "same-origin", headers: { "X-CSRF-Token": csrf }, body: form });
  if (!res.ok) throw new Error("frame upload refused (" + res.status + ")");
}

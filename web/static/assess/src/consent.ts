// The consent screen's moving parts. The page itself is server-rendered and
// works without this: the checkbox gates the form, and the two buttons agree
// or decline. What is added here is the camera — a live preview, the one
// still of a photo ID — and the gate that keeps Start out of reach until the
// candidate has actually given what the session will collect.

import { openCamera, uploadFrame, type Camera } from "./camera";
import { type IntegrityConfig } from "./integrity";

export interface ConsentConfig {
  csrf: string;
  integrity?: IntegrityConfig;
}

function byId<T extends HTMLElement>(id: string): T | null {
  return document.getElementById(id) as T | null;
}

function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls?: string, text?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

// post sends a form the way the page would have, with the token as a header.
async function post(url: string, csrf: string, fields: Record<string, string>): Promise<void> {
  const res = await fetch(url, {
    method: "POST",
    credentials: "same-origin",
    headers: { "X-CSRF-Token": csrf, "Content-Type": "application/x-www-form-urlencoded" },
    body: new URLSearchParams(fields),
  });
  if (!res.ok) throw new Error(res.status === 410 ? "this invite is no longer open" : "the server refused the request (" + res.status + ")");
}

export function mountConsent(cfg: ConsentConfig): void {
  const form = byId<HTMLFormElement>("assess-consent-form");
  const agree = byId<HTMLInputElement>("assess-consent-agree");
  const start = byId<HTMLButtonElement>("assess-consent-start");
  const host = byId<HTMLElement>("assess-consent-camera");
  const status = byId<HTMLElement>("assess-consent-status");
  const integrity = cfg.integrity;
  if (!form || !agree || !start || !integrity) return;

  const say = (msg: string) => {
    if (status) status.textContent = msg;
  };

  let camera: Camera | null = null;
  let live = false;
  let idFrame: Blob | null = null;
  let halted = false;
  let told = false;

  const gate = () => {
    start.disabled = halted || !agree.checked || (integrity.webcam && !live) || (integrity.photo_id && idFrame === null);
  };

  // halt is the end of the road: the session cannot run without what it
  // asked for. The recruiter hears about it, because from their side an
  // invite that stops here looks like one that was ignored.
  const halt = (msg: string) => {
    halted = true;
    say(msg);
    gate();
    if (told) return;
    told = true;
    void post(form.action, cfg.csrf, { decision: "decline", reason: "camera" }).catch(() => undefined);
  };

  agree.addEventListener("change", gate);

  if (integrity.webcam && host) {
    const preview = el("video", "consent-preview");
    preview.autoplay = true;
    preview.muted = true;
    preview.playsInline = true;
    const actions = el("div", "consent-camera-actions");
    const turnOn = el("button", "consent-camera-btn", "Turn on the camera");
    turnOn.type = "button";
    actions.appendChild(turnOn);
    const shot = el("img", "consent-shot");
    shot.alt = "The photo of your ID that will be stored";
    shot.hidden = true;
    let capture: HTMLButtonElement | null = null;
    if (integrity.photo_id) {
      capture = el("button", "consent-camera-btn", "Take the ID photo");
      capture.type = "button";
      capture.disabled = true;
      actions.appendChild(capture);
    }
    const head = el("p", "consent-camera-head", "Camera check");
    const note = el(
      "p",
      "consent-camera-note",
      integrity.photo_id
        ? "Your camera is off. Turn it on to check what it sees, then take one photo of your ID."
        : "Your camera is off. Turn it on to check what it sees before you start.",
    );
    host.append(head, note, preview, shot, actions);
    host.hidden = false;

    turnOn.addEventListener("click", () => {
      turnOn.disabled = true;
      say("Asking your browser for the camera…");
      void openCamera()
        .then((cam) => {
          camera = cam;
          preview.srcObject = cam.stream;
          live = true;
          turnOn.textContent = "Camera is on";
          if (capture) capture.disabled = false;
          say(integrity.photo_id ? "Hold your photo ID up to the camera, then take the photo." : "The camera is on. You can start when you are ready.");
        })
        .catch(() => {
          turnOn.disabled = false;
          halt("This assessment needs a camera, and this browser could not give it one. You cannot start; we have told the recruiter, who can send you a new invite without the camera.");
        })
        .finally(gate);
    });

    capture?.addEventListener("click", () => {
      void camera?.frame().then((frame) => {
        if (!frame) {
          say("The camera gave a blank picture. Try again.");
          return;
        }
        idFrame = frame;
        shot.src = URL.createObjectURL(frame);
        shot.hidden = false;
        if (capture) capture.textContent = "Retake the ID photo";
        say("That photo will be stored with your session. Retake it if it is hard to read.");
        gate();
      });
    });
  }

  form.addEventListener("submit", (e) => {
    const submitter = (e as SubmitEvent).submitter as HTMLButtonElement | null;
    if (submitter?.value === "decline") return;
    if (!integrity.webcam && !integrity.photo_id) return; // the plain form post is enough
    e.preventDefault();
    start.disabled = true;
    say("Starting…");
    void (async () => {
      try {
        await post(form.action, cfg.csrf, { decision: "agree", agree: "on" });
        // The frame is stored against a session that has begun, so it goes
        // after the start rather than with the consent.
        if (idFrame) await uploadFrame(integrity.consent_url, cfg.csrf, idFrame);
        camera?.stop();
        window.location.reload();
      } catch (err) {
        say("Could not start: " + (err as Error).message);
        start.disabled = false;
      }
    })();
  });

  gate();
}

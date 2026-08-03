import "./style.css";
import appMarkup from "./app.html?raw";
import { NativeInputStatus } from "../wailsjs/go/main/App";

const offerPath = "/api/offer";
const pointerIntervalMilliseconds = 12;

interface SignalingAnswer {
  type: RTCSdpType;
  sdp: string;
}

interface SignalingError {
  error: string;
}

interface ControlStatusMessage {
  type: "control-status";
  enabled: boolean;
  message: string;
}

interface InputMessage {
  type: "control" | "pointer" | "button" | "wheel" | "key";
  enabled?: boolean;
  x?: number;
  y?: number;
  button?: number;
  delta?: number;
  code?: string;
  down?: boolean;
}

const appRoot = document.querySelector<HTMLDivElement>("#app");
if (appRoot === null) {
  throw new Error("Required element #app is missing");
}
appRoot.innerHTML = appMarkup;

function elementById<T extends HTMLElement>(id: string): T {
  const element = document.getElementById(id);
  if (!(element instanceof HTMLElement)) {
    throw new Error(`Required element #${id} is missing`);
  }
  return element as T;
}

const serverAddress = elementById<HTMLInputElement>("server-address");
const connectButton = elementById<HTMLButtonElement>("connect");
const disconnectButton = elementById<HTMLButtonElement>("disconnect");
const playVideoButton = elementById<HTMLButtonElement>("play-video");
const controlToggle = elementById<HTMLInputElement>("enable-control");
const statusOutput = elementById<HTMLOutputElement>("status");
const controlStatus = elementById<HTMLElement>("control-status");
const nativeInputStatus = elementById<HTMLElement>("native-input-status");
const videoPanel = elementById<HTMLElement>("video-panel");
const remoteVideo = elementById<HTMLVideoElement>("remote-screen");

let peerConnection: RTCPeerConnection | undefined;
let motionChannel: RTCDataChannel | undefined;
let controlActive = false;
let lastPointerSentAt = 0;

function setStatus(message: string): void {
  statusOutput.value = message;
  statusOutput.textContent = message;
}

function setConnectionControls(isConnectingOrConnected: boolean): void {
  connectButton.disabled = isConnectingOrConnected;
  disconnectButton.disabled = !isConnectingOrConnected;
  serverAddress.disabled = isConnectingOrConnected;
}

function setControlAvailability(available: boolean): void {
  controlToggle.disabled = !available;
  if (!available) {
    controlToggle.checked = false;
    setControlActive(false);
  }
}

function setControlActive(active: boolean): void {
  controlActive = active;
  videoPanel.dataset.controlActive = String(active);
}

function setControlStatus(message: string, state: "idle" | "pending" | "active" | "rejected" = "idle"): void {
  controlStatus.textContent = message;
  controlStatus.dataset.state = state;
}

function offerURL(): string {
  const rawAddress = serverAddress.value.trim();
  const url = new URL(rawAddress);
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error("The server address must start with http:// or https://");
  }
  url.pathname = offerPath;
  url.search = "";
  url.hash = "";
  return url.toString();
}

function isMotionChannelOpen(): boolean {
  return motionChannel?.readyState === "open";
}

function sendInput(message: InputMessage): boolean {
  const channel = motionChannel;
  if (channel === undefined || channel.readyState !== "open") {
    return false;
  }
  channel.send(JSON.stringify(message));
  return true;
}

function disableRemoteControl(notifyHost: boolean): void {
  const wasEnabled = controlActive || controlToggle.checked;
  controlToggle.checked = false;
  setControlActive(false);
  if (notifyHost && wasEnabled) {
    sendInput({ type: "control", enabled: false });
  }
}

async function startRemoteVideo(): Promise<void> {
  if (remoteVideo.srcObject === null) {
    return;
  }

  remoteVideo.muted = true;
  remoteVideo.defaultMuted = true;
  remoteVideo.volume = 0;
  try {
    await remoteVideo.play();
    playVideoButton.hidden = true;
  } catch {
    playVideoButton.hidden = false;
    setStatus("Video received. Select Start video to begin playback.");
  }
}

async function waitForIceGatheringComplete(connection: RTCPeerConnection): Promise<void> {
  if (connection.iceGatheringState === "complete") {
    return;
  }

  await new Promise<void>((resolve) => {
    const onGatheringStateChange = (): void => {
      if (connection.iceGatheringState === "complete") {
        connection.removeEventListener("icegatheringstatechange", onGatheringStateChange);
        resolve();
      }
    };
    connection.addEventListener("icegatheringstatechange", onGatheringStateChange);
  });
}

function closeConnection(showStatus = true): void {
  const connection = peerConnection;
  disableRemoteControl(true);
  peerConnection = undefined;
  motionChannel = undefined;
  remoteVideo.srcObject = null;
  playVideoButton.hidden = true;
  connection?.close();
  setConnectionControls(false);
  setControlAvailability(false);
  setControlStatus("Connect to check whether the host allows remote control.");
  if (showStatus) {
    setStatus("Disconnected");
  }
}

function configureMotionChannel(connection: RTCPeerConnection): void {
  const channel = connection.createDataChannel("motion-input");
  motionChannel = channel;

  channel.addEventListener("open", () => {
    if (peerConnection !== connection) {
      return;
    }
    setControlAvailability(true);
    setControlStatus("Host permission is required. Enable control, then click the video before using the keyboard.");
  });

  channel.addEventListener("close", () => {
    if (motionChannel !== channel) {
      return;
    }
    motionChannel = undefined;
    setControlAvailability(false);
    setControlStatus("The remote-control channel is closed.", "rejected");
  });

  channel.addEventListener("message", (event) => {
    if (typeof event.data !== "string") {
      return;
    }
    try {
      const message: unknown = JSON.parse(event.data);
      if (!isControlStatusMessage(message)) {
        return;
      }
      controlToggle.checked = message.enabled;
      setControlActive(message.enabled);
      setControlStatus(message.message, message.enabled ? "active" : "rejected");
    } catch {
      setControlStatus("The host returned an invalid control status.", "rejected");
    }
  });
}

function configurePeerConnection(connection: RTCPeerConnection): void {
  configureMotionChannel(connection);
  connection.addTransceiver("video", { direction: "recvonly" });

  connection.addEventListener("track", (event) => {
    const [stream] = event.streams;
    remoteVideo.srcObject = stream ?? new MediaStream([event.track]);
    void startRemoteVideo();
  });

  connection.addEventListener("connectionstatechange", () => {
    if (peerConnection !== connection) {
      return;
    }
    if (connection.connectionState === "connected") {
      setStatus("Connected. Waiting for desktop frames...");
      return;
    }
    setStatus(`WebRTC: ${connection.connectionState}`);
    if (connection.connectionState === "failed" || connection.connectionState === "closed") {
      closeConnection(false);
    }
  });
}

async function connect(): Promise<void> {
  closeConnection(false);
  setConnectionControls(true);
  setStatus("Connecting to the host...");

  const connection = new RTCPeerConnection({ iceServers: [] });
  peerConnection = connection;
  configurePeerConnection(connection);

  try {
    const offer = await connection.createOffer();
    await connection.setLocalDescription(offer);
    await waitForIceGatheringComplete(connection);

    const localDescription = connection.localDescription;
    if (localDescription === null) {
      throw new Error("The desktop application did not create a WebRTC offer");
    }

    const response = await fetch(offerURL(), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(localDescription),
    });
    const responseBody: unknown = await response.json();
    if (!response.ok) {
      const message = isSignalingError(responseBody) ? responseBody.error : response.statusText;
      throw new Error(`Signaling error: ${message}`);
    }
    if (!isSignalingAnswer(responseBody)) {
      throw new Error("The server returned an invalid WebRTC answer");
    }

    await connection.setRemoteDescription({ type: responseBody.type, sdp: responseBody.sdp });
  } catch (error: unknown) {
    setStatus(error instanceof Error ? error.message : "Unknown connection error");
    closeConnection(false);
  }
}

function pointerCoordinates(event: PointerEvent): { x: number; y: number } | undefined {
  const bounds = remoteVideo.getBoundingClientRect();
  if (bounds.width <= 0 || bounds.height <= 0) {
    return undefined;
  }

  let contentLeft = bounds.left;
  let contentTop = bounds.top;
  let contentWidth = bounds.width;
  let contentHeight = bounds.height;
  if (remoteVideo.videoWidth > 0 && remoteVideo.videoHeight > 0) {
    const sourceAspect = remoteVideo.videoWidth / remoteVideo.videoHeight;
    const elementAspect = bounds.width / bounds.height;
    if (elementAspect > sourceAspect) {
      contentWidth = bounds.height * sourceAspect;
      contentLeft += (bounds.width - contentWidth) / 2;
    } else {
      contentHeight = bounds.width / sourceAspect;
      contentTop += (bounds.height - contentHeight) / 2;
    }
  }

  const x = (event.clientX - contentLeft) / contentWidth;
  const y = (event.clientY - contentTop) / contentHeight;
  if (x < 0 || x > 1 || y < 0 || y > 1) {
    return undefined;
  }
  return { x, y };
}

function forwardPointer(event: PointerEvent, force = false): void {
  if (!controlActive) {
    return;
  }
  const now = performance.now();
  if (!force && now - lastPointerSentAt < pointerIntervalMilliseconds) {
    return;
  }
  const coordinates = pointerCoordinates(event);
  if (coordinates === undefined) {
    return;
  }
  lastPointerSentAt = now;
  sendInput({ type: "pointer", ...coordinates });
}

function forwardKey(event: KeyboardEvent, down: boolean): void {
  if (!controlActive) {
    return;
  }
  if (down && event.repeat) {
    event.preventDefault();
    return;
  }
  event.preventDefault();
  sendInput({ type: "key", code: event.code, down });
}

function isSignalingAnswer(value: unknown): value is SignalingAnswer {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const answer = value as Partial<SignalingAnswer>;
  return answer.type === "answer" && typeof answer.sdp === "string" && answer.sdp.length > 0;
}

function isSignalingError(value: unknown): value is SignalingError {
  return typeof value === "object" && value !== null && typeof (value as Partial<SignalingError>).error === "string";
}

function isControlStatusMessage(value: unknown): value is ControlStatusMessage {
  if (typeof value !== "object" || value === null) {
    return false;
  }
  const status = value as Partial<ControlStatusMessage>;
  return status.type === "control-status" && typeof status.enabled === "boolean" && typeof status.message === "string";
}

async function loadNativeInputStatus(): Promise<void> {
  try {
    const input = await NativeInputStatus();
    nativeInputStatus.textContent = `Global input: ${input.detail}`;
  } catch {
    nativeInputStatus.textContent = "Global input: native bridge unavailable in browser development mode.";
  }
}

connectButton.addEventListener("click", () => {
  void connect();
});

disconnectButton.addEventListener("click", () => {
  closeConnection();
});

playVideoButton.addEventListener("click", () => {
  void startRemoteVideo();
});

controlToggle.addEventListener("change", () => {
  if (!controlToggle.checked) {
    disableRemoteControl(true);
    setControlStatus("Remote control disabled.");
    return;
  }
  if (!isMotionChannelOpen()) {
    controlToggle.checked = false;
    setControlStatus("The remote-control channel is not ready.", "rejected");
    return;
  }
  const approved = window.confirm(
    "Remote mouse and keyboard input will be sent to the host PC. Enable control for this session?",
  );
  if (!approved) {
    controlToggle.checked = false;
    return;
  }
  videoPanel.focus();
  setControlActive(false);
  setControlStatus("Requesting host permission...", "pending");
  sendInput({ type: "control", enabled: true });
});

videoPanel.addEventListener("pointermove", (event) => {
  forwardPointer(event);
});

videoPanel.addEventListener("pointerdown", (event) => {
  if (!controlActive) {
    return;
  }
  event.preventDefault();
  videoPanel.focus();
  videoPanel.setPointerCapture(event.pointerId);
  const coordinates = pointerCoordinates(event);
  if (coordinates === undefined) {
    return;
  }
  forwardPointer(event, true);
  sendInput({ type: "button", button: event.button, down: true });
});

videoPanel.addEventListener("pointerup", (event) => {
  if (!controlActive) {
    return;
  }
  event.preventDefault();
  const coordinates = pointerCoordinates(event);
  if (coordinates !== undefined) {
    forwardPointer(event, true);
  }
  sendInput({ type: "button", button: event.button, down: false });
  if (videoPanel.hasPointerCapture(event.pointerId)) {
    videoPanel.releasePointerCapture(event.pointerId);
  }
});

videoPanel.addEventListener("pointercancel", () => {
  if (controlActive) {
    disableRemoteControl(true);
    setControlStatus("Remote control was disabled after pointer capture was cancelled.", "rejected");
  }
});

videoPanel.addEventListener("wheel", (event) => {
  if (!controlActive) {
    return;
  }
  event.preventDefault();
  videoPanel.focus();
  const delta = Math.max(-1200, Math.min(1200, Math.round(-event.deltaY)));
  if (delta !== 0) {
    sendInput({ type: "wheel", delta });
  }
}, { passive: false });

videoPanel.addEventListener("contextmenu", (event) => {
  if (controlActive) {
    event.preventDefault();
  }
});

videoPanel.addEventListener("keydown", (event) => {
  forwardKey(event, true);
});

videoPanel.addEventListener("keyup", (event) => {
  forwardKey(event, false);
});

window.addEventListener("blur", () => {
  if (controlActive) {
    disableRemoteControl(true);
    setControlStatus("Remote control paused because the application lost focus.", "rejected");
  }
});

remoteVideo.addEventListener("loadeddata", () => {
  void startRemoteVideo();
});

remoteVideo.addEventListener("playing", () => {
  if (peerConnection !== undefined) {
    setStatus("The Windows desktop is streaming.");
  }
});

void loadNativeInputStatus();

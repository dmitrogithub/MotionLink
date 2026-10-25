// MotionLink client UI. The same code runs in a phone or PC browser (served
// by the host or by a relay) and inside the Wails desktop application.

import appMarkup from "./app.html?raw";
import { RemoteInput, type InputMessage, type TouchMode } from "./input";
import {
  type ConnectionMode,
  type Profile,
  type SignedAnswer,
  formatKey,
  isPlausibleKey,
  normalizeKey,
  signOffer,
  verifyAnswer,
} from "./protocol";
import { SignalingError, type Target, exchangeOffer, fetchIceServers, fetchInfo, normalizeAddress } from "./signaling";

export interface AppOptions {
  platform: "web" | "desktop";
  /** Optional line about the native side (desktop application only). */
  nativeStatus?: () => Promise<string>;
}

interface Settings {
  mode: ConnectionMode;
  directAddress: string;
  relayAddress: string;
  quality: string;
  key: string;
  rememberKey: boolean;
  touchMode: TouchMode;
}

type Phase = "idle" | "connecting" | "viewing";

const settingsKey = "motionlink.settings.v2";
const iceGatherTimeout = 2500;
const disconnectGrace = 6000;
const maxReconnectAttempts = 6;

// Shown until the host reports its own presets (internal/ffmpeg/profile.go).
const defaultProfiles: Profile[] = [
  {
    id: "fps30",
    label: "30 FPS",
    description: "Saves bandwidth and CPU; best for mobile data and weak PCs",
    fps: 30,
    maxWidth: 1600,
    bitrateKbps: 4000,
  },
  {
    id: "fps60",
    label: "60 FPS",
    description: "Smooth motion for games and video, 1080p",
    fps: 60,
    maxWidth: 1920,
    bitrateKbps: 10000,
  },
  {
    id: "quality",
    label: "High quality",
    description: "Native resolution, 30 FPS and a high bitrate for sharp text; needs a fast network",
    fps: 30,
    maxWidth: 3840,
    bitrateKbps: 25000,
  },
];

function loadSettings(): Settings {
  const defaults: Settings = {
    mode: "direct",
    directAddress: "",
    relayAddress: "",
    quality: "fps60",
    key: "",
    rememberKey: true,
    touchMode: "direct",
  };
  try {
    const stored = localStorage.getItem(settingsKey);
    return stored === null ? defaults : { ...defaults, ...(JSON.parse(stored) as Partial<Settings>) };
  } catch {
    return defaults;
  }
}

function saveSettings(settings: Settings): void {
  try {
    localStorage.setItem(
      settingsKey,
      JSON.stringify({ ...settings, key: settings.rememberKey ? settings.key : "" }),
    );
  } catch {
    // Private mode or blocked storage: settings simply are not remembered.
  }
}

export function startApp(root: HTMLElement, options: AppOptions): void {
  root.innerHTML = appMarkup;

  function element<T extends HTMLElement>(id: string): T {
    const found = root.querySelector<T>(`#${id}`);
    if (found === null) {
      throw new Error(`Required element #${id} is missing`);
    }
    return found;
  }

  const app = root.querySelector<HTMLElement>(".app")!;
  const form = element<HTMLFormElement>("connect-form");
  const modeHint = element<HTMLElement>("mode-hint");
  const addressLabel = element<HTMLElement>("address-label");
  const addressInput = element<HTMLInputElement>("address");
  const keyInput = element<HTMLInputElement>("key");
  const qualityOptions = element<HTMLElement>("quality-options");
  const rememberKey = element<HTMLInputElement>("remember-key");
  const connectButton = element<HTMLButtonElement>("connect");
  const connectStatus = element<HTMLOutputElement>("connect-status");
  const videoPanel = element<HTMLElement>("video-panel");
  const video = element<HTMLVideoElement>("remote-screen");
  const playButton = element<HTMLButtonElement>("play-video");
  const overlay = element<HTMLElement>("overlay-status");
  const toolbar = element<HTMLElement>("toolbar");
  const toolbarToggle = element<HTMLButtonElement>("toolbar-toggle");
  const pathBadge = element<HTMLElement>("path-badge");
  const statsText = element<HTMLElement>("stats-text");
  const qualitySelect = element<HTMLSelectElement>("quality-select");
  const controlToggle = element<HTMLInputElement>("enable-control");
  const touchModeSelect = element<HTMLSelectElement>("touch-mode");
  const keyboardButton = element<HTMLButtonElement>("keyboard");
  const fullscreenButton = element<HTMLButtonElement>("fullscreen");
  const disconnectButton = element<HTMLButtonElement>("disconnect");
  const quickKeys = element<HTMLElement>("quick-keys");
  const controlStatus = element<HTMLElement>("control-status");
  const keyboardProxy = element<HTMLTextAreaElement>("keyboard-proxy");

  const settings = loadSettings();
  const isTouchDevice = navigator.maxTouchPoints > 0 || "ontouchstart" in window;
  let profiles = defaultProfiles;

  let peer: RTCPeerConnection | undefined;
  let channel: RTCDataChannel | undefined;
  let currentTarget: Target | undefined;
  let currentQuality = settings.quality;
  let clockOffset = 0;
  let userClosed = true;
  let reconnectAttempts = 0;
  let reconnectTimer = 0;
  let disconnectTimer = 0;
  let statsTimer = 0;
  let controlWanted = false;
  let hostAllowsControl = false;
  let wakeLock: { release(): Promise<void> } | undefined;
  let lastStats: { bytes: number; at: number } | undefined;

  const input = new RemoteInput(videoPanel, video, keyboardProxy, sendMessage);
  input.touchMode = settings.touchMode;
  touchModeSelect.value = settings.touchMode;

  // ---- connect form ----------------------------------------------------------

  function selectedMode(): ConnectionMode {
    const checked = form.querySelector<HTMLInputElement>('input[name="mode"]:checked');
    return checked?.value === "relay" ? "relay" : "direct";
  }

  function setMode(mode: ConnectionMode): void {
    const radio = form.querySelector<HTMLInputElement>(`input[name="mode"][value="${mode}"]`);
    if (radio !== null) {
      radio.checked = true;
    }
    if (mode === "relay") {
      addressLabel.textContent = "Relay server address";
      addressInput.placeholder = "relay.example.com";
      addressInput.value = settings.relayAddress;
      modeHint.textContent = "For connections from another network or over mobile data. Start the host with --relay.";
    } else {
      addressLabel.textContent = "Computer address";
      addressInput.placeholder = "192.168.1.10:8080";
      addressInput.value = settings.directAddress;
      modeHint.textContent = "A PC on the same network (Wi-Fi/LAN) or with a forwarded port.";
    }
    settings.mode = mode;
  }

  function renderQualityOptions(): void {
    qualityOptions.replaceChildren();
    qualitySelect.replaceChildren();
    if (!profiles.some((profile) => profile.id === currentQuality)) {
      currentQuality = profiles[0]?.id ?? "fps60";
    }
    for (const profile of profiles) {
      const option = document.createElement("label");
      option.className = "quality-option";
      option.innerHTML = `<input type="radio" name="quality"><span class="quality-name"></span><span class="quality-description"></span>`;
      const radio = option.querySelector("input")!;
      radio.value = profile.id;
      radio.checked = profile.id === currentQuality;
      option.querySelector(".quality-name")!.textContent = profile.label;
      option.querySelector(".quality-description")!.textContent = profile.description;
      qualityOptions.append(option);

      const item = document.createElement("option");
      item.value = profile.id;
      item.textContent = profile.label;
      qualitySelect.append(item);
    }
    qualitySelect.value = currentQuality;
  }

  function setConnectStatus(message: string, kind: "info" | "error" = "info"): void {
    connectStatus.textContent = message;
    connectStatus.dataset.kind = kind;
  }

  function setPhase(phase: Phase): void {
    app.dataset.phase = phase;
    connectButton.disabled = phase === "connecting";
    connectButton.textContent = phase === "connecting" ? "Connecting…" : "Connect";
  }

  function showOverlay(message: string | undefined): void {
    overlay.hidden = message === undefined;
    overlay.textContent = message ?? "";
  }

  form.addEventListener("change", (event) => {
    const target = event.target as HTMLInputElement;
    if (target.name === "mode") {
      setMode(selectedMode());
    } else if (target.name === "quality") {
      currentQuality = target.value;
    }
  });

  keyInput.addEventListener("blur", () => {
    if (keyInput.value.trim() !== "") {
      keyInput.value = formatKey(keyInput.value);
    }
  });

  form.addEventListener("submit", (event) => {
    event.preventDefault();
    userClosed = false;
    reconnectAttempts = 0;
    void connect();
  });

  // ---- connection --------------------------------------------------------------

  function readTarget(): Target {
    const mode = selectedMode();
    const key = keyInput.value;
    if (!isPlausibleKey(key)) {
      throw new SignalingError("Enter the access key shown by the host");
    }
    const baseURL = normalizeAddress(addressInput.value, mode);
    if (mode === "relay") {
      settings.relayAddress = addressInput.value.trim();
    } else {
      settings.directAddress = addressInput.value.trim();
    }
    settings.mode = mode;
    settings.key = formatKey(key);
    settings.rememberKey = rememberKey.checked;
    settings.quality = currentQuality;
    saveSettings(settings);
    return { mode, baseURL, key: normalizeKey(key) };
  }

  async function connect(): Promise<void> {
    window.clearTimeout(reconnectTimer);
    teardown();
    let target: Target;
    try {
      target = currentTarget !== undefined && reconnectAttempts > 0 ? currentTarget : readTarget();
    } catch (error: unknown) {
      setConnectStatus(messageOf(error), "error");
      return;
    }
    currentTarget = target;
    const reconnecting = reconnectAttempts > 0;
    if (reconnecting) {
      showOverlay(`Reconnecting… (attempt ${reconnectAttempts} of ${maxReconnectAttempts})`);
    } else {
      setPhase("connecting");
      setConnectStatus(target.mode === "relay" ? "Looking for the host through the relay…" : "Contacting the host…");
    }

    try {
      const iceServers = await fetchIceServers(target);
      if (userClosed) {
        return;
      }
      const connection = new RTCPeerConnection({ iceServers, bundlePolicy: "max-bundle" });
      peer = connection;
      configurePeer(connection);

      const offer = await connection.createOffer();
      await connection.setLocalDescription(offer);
      await waitForIceGathering(connection);
      const sdp = connection.localDescription?.sdp;
      if (sdp === undefined || peer !== connection) {
        return;
      }

      let signed = signOffer(target.key, sdp, currentQuality, clockOffset);
      let answer: SignedAnswer;
      try {
        answer = await exchangeOffer(target, signed);
      } catch (error: unknown) {
        // The phone's clock may be off; retry once with the host's time.
        if (error instanceof SignalingError && error.code === "clock" && error.serverTime > 0) {
          clockOffset = error.serverTime - Date.now();
          signed = signOffer(target.key, sdp, currentQuality, clockOffset);
          answer = await exchangeOffer(target, signed);
        } else {
          throw error;
        }
      }
      if (peer !== connection) {
        return;
      }
      if (!verifyAnswer(target.key, signed.nonce, answer)) {
        throw new SignalingError("The answer is not signed with this key; the connection was stopped for security");
      }
      await connection.setRemoteDescription({ type: "answer", sdp: answer.sdp });
      setPhase("viewing");
      showOverlay("Establishing the connection…");
      startStats();
    } catch (error: unknown) {
      if (userClosed) {
        return;
      }
      const message = messageOf(error);
      const fatal =
        error instanceof SignalingError && ["bad-key", "rate-limited", "wrong-mode", "replay"].includes(error.code);
      if (reconnecting && !fatal) {
        scheduleReconnect();
        return;
      }
      teardown();
      userClosed = true;
      setPhase("idle");
      setConnectStatus(message, "error");
    }
  }

  function configurePeer(connection: RTCPeerConnection): void {
    const dataChannel = connection.createDataChannel("motion-input", { ordered: true });
    channel = dataChannel;
    dataChannel.addEventListener("open", () => {
      if (channel !== dataChannel) {
        return;
      }
      controlToggle.disabled = false;
    });
    dataChannel.addEventListener("close", () => {
      if (channel !== dataChannel) {
        return;
      }
      setControlActive(false);
      controlToggle.disabled = true;
    });
    dataChannel.addEventListener("message", (event) => {
      if (typeof event.data === "string") {
        handleHostMessage(event.data);
      }
    });

    connection.addTransceiver("video", { direction: "recvonly" });
    connection.addEventListener("track", (event) => {
      // Ask the browser to play frames as soon as they are decodable.
      const receiver = event.receiver as RTCRtpReceiver & { jitterBufferTarget?: number; playoutDelayHint?: number };
      try {
        receiver.jitterBufferTarget = 0;
      } catch {
        // Not supported by this browser.
      }
      try {
        receiver.playoutDelayHint = 0;
      } catch {
        // Not supported by this browser.
      }
      video.srcObject = event.streams[0] ?? new MediaStream([event.track]);
      void playVideo();
    });

    connection.addEventListener("connectionstatechange", () => {
      if (peer !== connection) {
        return;
      }
      switch (connection.connectionState) {
        case "connected":
          window.clearTimeout(disconnectTimer);
          reconnectAttempts = 0;
          setPhase("viewing");
          if (video.readyState >= 2) {
            showOverlay(undefined);
          } else {
            showOverlay("Waiting for the picture…");
          }
          void requestWakeLock();
          break;
        case "disconnected":
          showOverlay("Connection interrupted, recovering…");
          window.clearTimeout(disconnectTimer);
          disconnectTimer = window.setTimeout(() => {
            if (peer === connection && connection.connectionState !== "connected") {
              scheduleReconnect();
            }
          }, disconnectGrace);
          break;
        case "failed":
          scheduleReconnect();
          break;
        default:
          break;
      }
    });
  }

  function scheduleReconnect(): void {
    if (userClosed) {
      return;
    }
    window.clearTimeout(reconnectTimer);
    reconnectAttempts += 1;
    if (reconnectAttempts > maxReconnectAttempts) {
      disconnect(`Connection lost.${pathBadge.dataset.path === "relay" ? "" : " If the devices are on different networks, try the Via relay mode."}`);
      return;
    }
    const delay = Math.min(1000 * 2 ** (reconnectAttempts - 1), 8000);
    showOverlay(`Connection lost. Reconnecting in ${Math.round(delay / 1000)} s…`);
    reconnectTimer = window.setTimeout(() => void connect(), delay);
  }

  async function waitForIceGathering(connection: RTCPeerConnection): Promise<void> {
    if (connection.iceGatheringState === "complete") {
      return;
    }
    // Non-trickle signaling: wait for candidates, but never longer than a
    // couple of seconds; host and STUN candidates arrive well before that.
    await new Promise<void>((resolve) => {
      const timer = window.setTimeout(done, iceGatherTimeout);
      function done(): void {
        window.clearTimeout(timer);
        connection.removeEventListener("icegatheringstatechange", onChange);
        resolve();
      }
      function onChange(): void {
        if (connection.iceGatheringState === "complete") {
          done();
        }
      }
      connection.addEventListener("icegatheringstatechange", onChange);
    });
  }

  function teardown(): void {
    window.clearTimeout(disconnectTimer);
    window.clearInterval(statsTimer);
    lastStats = undefined;
    const connection = peer;
    peer = undefined;
    channel = undefined;
    input.setEnabled(false);
    controlToggle.checked = false;
    controlToggle.disabled = true;
    connection?.close();
    video.srcObject = null;
    playButton.hidden = true;
  }

  function disconnect(reason?: string): void {
    userClosed = true;
    window.clearTimeout(reconnectTimer);
    if (controlWanted) {
      sendMessage({ type: "control", enabled: false });
    }
    controlWanted = false;
    teardown();
    void releaseWakeLock();
    if (document.fullscreenElement !== null) {
      void document.exitFullscreen().catch(() => undefined);
    }
    setPhase("idle");
    setConnectStatus(reason ?? "Disconnected", reason === undefined ? "info" : "error");
    showOverlay(undefined);
  }

  async function playVideo(): Promise<void> {
    if (video.srcObject === null) {
      return;
    }
    video.muted = true;
    try {
      await video.play();
      playButton.hidden = true;
    } catch {
      playButton.hidden = false;
    }
  }

  // ---- host messages -------------------------------------------------------

  function sendMessage(message: InputMessage | Record<string, unknown>): boolean {
    if (channel === undefined || channel.readyState !== "open") {
      return false;
    }
    channel.send(JSON.stringify(message));
    return true;
  }

  function handleHostMessage(raw: string): void {
    let message: Record<string, unknown>;
    try {
      message = JSON.parse(raw) as Record<string, unknown>;
    } catch {
      return;
    }
    switch (message.type) {
      case "hello": {
        hostAllowsControl = message.control === true;
        if (Array.isArray(message.profiles) && message.profiles.length > 0) {
          profiles = message.profiles as Profile[];
        }
        if (typeof message.quality === "string") {
          currentQuality = message.quality;
        }
        renderQualityOptions();
        setControlStatus(
          hostAllowsControl
            ? "Turn on Control to use the mouse and keyboard."
            : "The host runs without --enable-control: view only.",
        );
        // After an automatic reconnect, restore control the user already approved.
        if (controlWanted && hostAllowsControl) {
          sendMessage({ type: "control", enabled: true });
        }
        break;
      }
      case "control-status": {
        const enabled = message.enabled === true;
        setControlActive(enabled);
        if (!enabled) {
          controlWanted = false;
        }
        setControlStatus(typeof message.message === "string" ? translateControlMessage(message.message, enabled) : "");
        break;
      }
      case "quality-status": {
        if (typeof message.quality === "string") {
          currentQuality = message.quality;
          qualitySelect.value = currentQuality;
          settings.quality = currentQuality;
          saveSettings(settings);
        }
        qualitySelect.disabled = false;
        if (message.ok === true) {
          const label = profiles.find((profile) => profile.id === currentQuality)?.label ?? currentQuality;
          setControlStatus(`Quality: ${label}`);
        } else {
          setControlStatus(`Could not switch quality: ${String(message.message ?? "")}`);
        }
        break;
      }
      default:
        break;
    }
  }

  function translateControlMessage(message: string, enabled: boolean): string {
    if (enabled) {
      return isTouchDevice
        ? "Control is on. Tap to click, hold to right-click, use two fingers to scroll."
        : "Control is on. Click the video to send keyboard input.";
    }
    if (message.includes("--enable-control")) {
      return "The host does not allow control. Restart the host with --enable-control.";
    }
    return "Control is off.";
  }

  // ---- remote control ------------------------------------------------------

  function setControlActive(active: boolean): void {
    controlToggle.checked = active;
    input.setEnabled(active);
    touchModeSelect.hidden = !(active && isTouchDevice);
    keyboardButton.hidden = !(active && isTouchDevice);
    quickKeys.hidden = !(active && isTouchDevice);
  }

  function setControlStatus(message: string): void {
    controlStatus.textContent = message;
  }

  controlToggle.addEventListener("change", () => {
    if (!controlToggle.checked) {
      controlWanted = false;
      sendMessage({ type: "control", enabled: false });
      setControlActive(false);
      return;
    }
    controlToggle.checked = false;
    if (!hostAllowsControl) {
      setControlStatus("The host does not allow control. Restart the host with --enable-control.");
      return;
    }
    const approved = window.confirm("Mouse and keyboard input will be sent to the remote computer. Turn on control?");
    if (!approved) {
      return;
    }
    controlWanted = true;
    videoPanel.focus();
    setControlStatus("Asking the host for permission…");
    sendMessage({ type: "control", enabled: true });
  });

  touchModeSelect.addEventListener("change", () => {
    input.touchMode = touchModeSelect.value === "trackpad" ? "trackpad" : "direct";
    settings.touchMode = input.touchMode;
    saveSettings(settings);
  });

  keyboardButton.addEventListener("click", () => input.openKeyboard());

  quickKeys.addEventListener("click", (event) => {
    const button = (event.target as HTMLElement).closest<HTMLButtonElement>("button[data-combo]");
    if (button?.dataset.combo !== undefined) {
      input.sendCombo(button.dataset.combo.split(" "));
    }
  });
  // Keep the on-screen keyboard open while tapping quick keys.
  quickKeys.addEventListener("pointerdown", (event) => event.preventDefault());

  window.addEventListener("blur", () => {
    // A desktop window losing focus must not leave keys "held" remotely.
    if (!isTouchDevice && controlWanted) {
      controlWanted = false;
      sendMessage({ type: "control", enabled: false });
      setControlActive(false);
      setControlStatus("Control paused: the window lost focus.");
    }
  });

  // ---- viewer toolbar ------------------------------------------------------

  qualitySelect.addEventListener("change", () => {
    if (sendMessage({ type: "quality", quality: qualitySelect.value })) {
      qualitySelect.disabled = true;
      setControlStatus("Switching quality…");
    }
  });

  toolbarToggle.addEventListener("click", () => {
    const collapsed = toolbar.classList.toggle("collapsed");
    toolbarToggle.setAttribute("aria-expanded", String(!collapsed));
    toolbarToggle.textContent = collapsed ? "⌄" : "⌃";
  });

  fullscreenButton.addEventListener("click", () => {
    if (document.fullscreenElement !== null) {
      void document.exitFullscreen().catch(() => undefined);
      return;
    }
    void app.requestFullscreen?.({ navigationUI: "hide" }).then(
      () => {
        const orientation = screen.orientation as ScreenOrientation & { lock?: (value: string) => Promise<void> };
        void orientation.lock?.("landscape").catch(() => undefined);
      },
      () => undefined,
    );
  });

  disconnectButton.addEventListener("click", () => disconnect());
  playButton.addEventListener("click", () => void playVideo());
  video.addEventListener("loadeddata", () => void playVideo());
  video.addEventListener("playing", () => {
    if (peer?.connectionState === "connected") {
      showOverlay(undefined);
    }
  });

  window.addEventListener("online", () => {
    if (!userClosed && peer !== undefined && peer.connectionState !== "connected") {
      scheduleReconnect();
    }
  });

  document.addEventListener("visibilitychange", () => {
    if (document.visibilityState === "visible" && !userClosed) {
      void requestWakeLock();
    }
  });

  // ---- statistics ----------------------------------------------------------

  function startStats(): void {
    window.clearInterval(statsTimer);
    statsTimer = window.setInterval(() => void updateStats(), 1000);
  }

  async function updateStats(): Promise<void> {
    const connection = peer;
    if (connection === undefined) {
      return;
    }
    const report = await connection.getStats();
    let fps = 0;
    let bytes = 0;
    let width = 0;
    let height = 0;
    let rtt: number | undefined;
    let pairId: string | undefined;
    const byId = new Map<string, Record<string, unknown>>();
    report.forEach((entry: Record<string, unknown>) => {
      byId.set(entry.id as string, entry);
      if (entry.type === "inbound-rtp" && entry.kind === "video") {
        fps = Number(entry.framesPerSecond ?? 0);
        bytes = Number(entry.bytesReceived ?? 0);
        width = Number(entry.frameWidth ?? 0);
        height = Number(entry.frameHeight ?? 0);
      }
      if (entry.type === "transport" && typeof entry.selectedCandidatePairId === "string") {
        pairId = entry.selectedCandidatePairId;
      }
    });
    if (pairId === undefined) {
      report.forEach((entry: Record<string, unknown>) => {
        if (entry.type === "candidate-pair" && entry.nominated === true && entry.state === "succeeded") {
          pairId = entry.id as string;
        }
      });
    }
    const pair = pairId === undefined ? undefined : byId.get(pairId);
    if (pair !== undefined) {
      if (typeof pair.currentRoundTripTime === "number") {
        rtt = pair.currentRoundTripTime * 1000;
      }
      const local = byId.get(pair.localCandidateId as string);
      const remote = byId.get(pair.remoteCandidateId as string);
      updatePathBadge(local, remote);
    }

    const now = performance.now();
    let megabits = 0;
    if (lastStats !== undefined && bytes >= lastStats.bytes) {
      megabits = ((bytes - lastStats.bytes) * 8) / ((now - lastStats.at) * 1000);
    }
    lastStats = { bytes, at: now };

    const parts = [`${Math.round(fps)} fps`, `${megabits.toFixed(1)} Mbit/s`];
    if (rtt !== undefined) {
      parts.push(`${Math.round(rtt)} ms`);
    }
    if (width > 0) {
      parts.push(`${width}×${height}`);
    }
    statsText.textContent = parts.join(" · ");
  }

  function updatePathBadge(local: Record<string, unknown> | undefined, remote: Record<string, unknown> | undefined): void {
    if (local === undefined || remote === undefined) {
      return;
    }
    const localType = String(local.candidateType ?? "");
    const remoteType = String(remote.candidateType ?? "");
    const tcp = String(local.protocol ?? "") === "tcp" || String(local.relayProtocol ?? "") === "tcp";
    let label: string;
    let path: string;
    if (localType === "relay" || remoteType === "relay") {
      label = "TURN tunnel";
      path = "relay";
    } else if (localType === "host" && remoteType === "host") {
      label = "Local network";
      path = "lan";
    } else {
      label = "P2P through NAT";
      path = "p2p";
    }
    pathBadge.textContent = tcp ? `${label} · TCP` : label;
    pathBadge.dataset.path = path;
  }

  // ---- wake lock -----------------------------------------------------------

  async function requestWakeLock(): Promise<void> {
    const nav = navigator as Navigator & { wakeLock?: { request(type: "screen"): Promise<{ release(): Promise<void> }> } };
    if (wakeLock !== undefined || nav.wakeLock === undefined) {
      return;
    }
    try {
      wakeLock = await nav.wakeLock.request("screen");
    } catch {
      wakeLock = undefined;
    }
  }

  async function releaseWakeLock(): Promise<void> {
    const lock = wakeLock;
    wakeLock = undefined;
    await lock?.release().catch(() => undefined);
  }

  // ---- start-up ------------------------------------------------------------

  function messageOf(error: unknown): string {
    return error instanceof Error ? error.message : "Unknown connection error";
  }

  async function initialise(): Promise<void> {
    setMode(settings.mode);
    rememberKey.checked = settings.rememberKey;
    keyInput.value = settings.key;
    renderQualityOptions();
    setPhase("idle");

    // A page served by a host or relay pre-fills its own address.
    const servedOverHTTP = location.protocol === "http:" || location.protocol === "https:";
    const isWails = location.hostname === "wails.localhost" || location.protocol === "wails:";
    if (servedOverHTTP && !isWails) {
      const info = await fetchInfo(location.origin);
      if (info !== undefined) {
        // The full origin keeps the scheme: a relay page served over plain
        // HTTP must not be "upgraded" to HTTPS by address normalization.
        if (info.role === "host") {
          settings.directAddress = location.origin;
        } else {
          settings.relayAddress = location.origin;
        }
        setMode(info.role === "host" ? "direct" : "relay");
        if (info.profiles !== undefined && info.profiles.length > 0) {
          profiles = info.profiles;
          renderQualityOptions();
        }
      }
    }
    if (options.platform === "desktop" && settings.directAddress === "" && settings.mode === "direct") {
      settings.directAddress = "127.0.0.1:8080";
      addressInput.value = settings.directAddress;
    }

    // Links printed by the host (and its QR code) carry the key in the
    // fragment, which browsers never send to a server.
    const fragment = new URLSearchParams(location.hash.replace(/^#/, ""));
    const fragmentKey = fragment.get("key");
    const fragmentRelay = fragment.get("relay");
    if (fragmentRelay !== null) {
      settings.relayAddress = fragmentRelay;
      setMode("relay");
    }
    if (fragmentKey !== null && isPlausibleKey(fragmentKey)) {
      keyInput.value = formatKey(fragmentKey);
      history.replaceState(null, "", location.pathname + location.search);
      form.requestSubmit();
    }

    if (options.nativeStatus !== undefined) {
      try {
        const status = await options.nativeStatus();
        root.querySelector(".footnote")?.append(document.createElement("br"), status);
      } catch {
        // The native bridge is unavailable in browser development mode.
      }
    }
  }

  void initialise();
}

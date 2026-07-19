import "./style.css";

const offerURL = import.meta.env.VITE_SIGNAL_URL ?? "http://127.0.0.1:8080/api/offer";

interface SignalingAnswer {
  type: RTCSdpType;
  sdp: string;
}

interface SignalingError {
  error: string;
}

function elementById<T extends HTMLElement>(id: string): T {
  const element = document.getElementById(id);
  if (!(element instanceof HTMLElement)) {
    throw new Error(`Required element #${id} is missing`);
  }
  return element as T;
}

const connectButton = elementById<HTMLButtonElement>("connect");
const disconnectButton = elementById<HTMLButtonElement>("disconnect");
const playVideoButton = elementById<HTMLButtonElement>("play-video");
const statusOutput = elementById<HTMLOutputElement>("status");
const remoteVideo = elementById<HTMLVideoElement>("remote-screen");

let peerConnection: RTCPeerConnection | undefined;

function setStatus(message: string): void {
  statusOutput.value = message;
  statusOutput.textContent = message;
}

function setConnectedControls(isConnectingOrConnected: boolean): void {
  connectButton.disabled = isConnectingOrConnected;
  disconnectButton.disabled = !isConnectingOrConnected;
}

async function startRemoteVideo(): Promise<void> {
  if (remoteVideo.srcObject === null) {
    return;
  }

  // Edge may require the element properties (not just HTML attributes) to be
  // muted before it allows a WebRTC stream to start automatically.
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
  peerConnection = undefined;
  remoteVideo.srcObject = null;
  playVideoButton.hidden = true;
  connection?.close();
  setConnectedControls(false);
  if (showStatus) {
    setStatus("Disconnected");
  }
}

function configurePeerConnection(connection: RTCPeerConnection): void {
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
      setStatus("Connected. Waiting for desktop frames…");
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
  setConnectedControls(true);
  setStatus("Connecting to the desktop…");

  const connection = new RTCPeerConnection({ iceServers: [] });
  peerConnection = connection;
  configurePeerConnection(connection);

  try {
    const offer = await connection.createOffer();
    await connection.setLocalDescription(offer);
    await waitForIceGatheringComplete(connection);

    const localDescription = connection.localDescription;
    if (localDescription === null) {
      throw new Error("Browser did not create a local WebRTC description");
    }

    const response = await fetch(offerURL, {
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
      throw new Error("Signaling server returned an invalid answer");
    }

    await connection.setRemoteDescription({ type: responseBody.type, sdp: responseBody.sdp });
  } catch (error: unknown) {
    const message = error instanceof Error ? error.message : "Unknown connection error";
    setStatus(message);
    closeConnection(false);
  }
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

connectButton.addEventListener("click", () => {
  void connect();
});

disconnectButton.addEventListener("click", () => {
  closeConnection();
});

playVideoButton.addEventListener("click", () => {
  void startRemoteVideo();
});

remoteVideo.addEventListener("loadeddata", () => {
  void startRemoteVideo();
});

remoteVideo.addEventListener("playing", () => {
  if (peerConnection !== undefined) {
    setStatus("The Windows desktop is streaming.");
  }
});

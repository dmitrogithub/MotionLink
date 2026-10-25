// Translates mouse, touch and keyboard events over the video into the host's
// input protocol (internal/input). Coordinates are normalized to the video
// frame, so the host maps them onto its own desktop.

export interface InputMessage {
  type: "control" | "pointer" | "button" | "wheel" | "key" | "text";
  enabled?: boolean;
  x?: number;
  y?: number;
  button?: number;
  delta?: number;
  code?: string;
  down?: boolean;
  text?: string;
}

export type TouchMode = "direct" | "trackpad";

type Send = (message: InputMessage) => boolean;

const pointerIntervalMilliseconds = 12;
const tapMaxMilliseconds = 250;
const tapSlopPixels = 10;
const longPressMilliseconds = 550;
const doubleTapMilliseconds = 300;
const trackpadSpeed = 1.6;
const wheelPixelsPerNotch = 40;
const keyboardSentinel = "        ";

interface Point {
  x: number;
  y: number;
}

interface ContentBox {
  left: number;
  top: number;
  width: number;
  height: number;
}

export class RemoteInput {
  enabled = false;
  touchMode: TouchMode = "direct";

  private lastPointerSentAt = 0;
  // Touch gesture state.
  private touchStart: Point | undefined;
  private touchStartedAt = 0;
  private touchMoved = false;
  private buttonHeld = false;
  private longPressTimer = 0;
  private longPressFired = false;
  private twoFinger = false;
  private twoFingerMoved = false;
  private twoFingerStartedAt = 0;
  private lastTwoFingerY = 0;
  private wheelRemainder = 0;
  private lastTouch: Point | undefined;
  private lastTapEndedAt = 0;
  // Virtual cursor for trackpad mode, normalized to the video.
  private cursor: Point = { x: 0.5, y: 0.5 };
  private keyboardValue = keyboardSentinel;
  private composing = false;

  constructor(
    private readonly panel: HTMLElement,
    private readonly video: HTMLVideoElement,
    private readonly keyboardProxy: HTMLTextAreaElement,
    private readonly send: Send,
  ) {
    this.bindPointer();
    this.bindTouch();
    this.bindKeyboard();
    this.bindKeyboardProxy();
  }

  setEnabled(enabled: boolean): void {
    this.enabled = enabled;
    this.panel.dataset.controlActive = String(enabled);
    if (!enabled) {
      this.cancelTouch();
      this.keyboardProxy.blur();
    }
  }

  /** Presses the keys in order and releases them in reverse (e.g. Ctrl+C). */
  sendCombo(codes: string[]): void {
    if (!this.enabled) {
      return;
    }
    for (const code of codes) {
      this.send({ type: "key", code, down: true });
    }
    for (const code of [...codes].reverse()) {
      this.send({ type: "key", code, down: false });
    }
  }

  /** Opens the phone's on-screen keyboard. */
  openKeyboard(): void {
    if (!this.enabled) {
      return;
    }
    this.resetKeyboardProxy();
    this.keyboardProxy.focus({ preventScroll: true });
  }

  private contentBox(): ContentBox | undefined {
    const bounds = this.video.getBoundingClientRect();
    if (bounds.width <= 0 || bounds.height <= 0) {
      return undefined;
    }
    let { left, top, width, height } = bounds;
    if (this.video.videoWidth > 0 && this.video.videoHeight > 0) {
      const sourceAspect = this.video.videoWidth / this.video.videoHeight;
      if (bounds.width / bounds.height > sourceAspect) {
        width = bounds.height * sourceAspect;
        left += (bounds.width - width) / 2;
      } else {
        height = bounds.width / sourceAspect;
        top += (bounds.height - height) / 2;
      }
    }
    return { left, top, width, height };
  }

  private normalize(clientX: number, clientY: number, clamp: boolean): Point | undefined {
    const box = this.contentBox();
    if (box === undefined) {
      return undefined;
    }
    let x = (clientX - box.left) / box.width;
    let y = (clientY - box.top) / box.height;
    if (clamp) {
      x = Math.min(1, Math.max(0, x));
      y = Math.min(1, Math.max(0, y));
    } else if (x < 0 || x > 1 || y < 0 || y > 1) {
      return undefined;
    }
    return { x, y };
  }

  private movePointer(point: Point, force = false): void {
    const now = performance.now();
    if (!force && now - this.lastPointerSentAt < pointerIntervalMilliseconds) {
      return;
    }
    this.lastPointerSentAt = now;
    this.send({ type: "pointer", x: point.x, y: point.y });
  }

  private click(button: number): void {
    this.send({ type: "button", button, down: true });
    this.send({ type: "button", button, down: false });
  }

  private wheelBy(pixels: number): void {
    this.wheelRemainder += pixels;
    const notches = Math.trunc(this.wheelRemainder / wheelPixelsPerNotch);
    if (notches !== 0) {
      this.wheelRemainder -= notches * wheelPixelsPerNotch;
      this.send({ type: "wheel", delta: Math.max(-1200, Math.min(1200, notches * 120)) });
    }
  }

  // Mouse and pen ------------------------------------------------------------

  private bindPointer(): void {
    const panel = this.panel;
    panel.addEventListener("pointermove", (event) => {
      if (!this.enabled || event.pointerType === "touch") {
        return;
      }
      const point = this.normalize(event.clientX, event.clientY, panel.hasPointerCapture(event.pointerId));
      if (point !== undefined) {
        this.movePointer(point);
      }
    });

    panel.addEventListener("pointerdown", (event) => {
      if (!this.enabled || event.pointerType === "touch") {
        return;
      }
      event.preventDefault();
      panel.focus();
      const point = this.normalize(event.clientX, event.clientY, false);
      if (point === undefined) {
        return;
      }
      panel.setPointerCapture(event.pointerId);
      this.movePointer(point, true);
      this.send({ type: "button", button: event.button, down: true });
    });

    panel.addEventListener("pointerup", (event) => {
      if (!this.enabled || event.pointerType === "touch") {
        return;
      }
      event.preventDefault();
      const point = this.normalize(event.clientX, event.clientY, true);
      if (point !== undefined) {
        this.movePointer(point, true);
      }
      this.send({ type: "button", button: event.button, down: false });
      if (panel.hasPointerCapture(event.pointerId)) {
        panel.releasePointerCapture(event.pointerId);
      }
    });

    panel.addEventListener(
      "wheel",
      (event) => {
        if (!this.enabled) {
          return;
        }
        event.preventDefault();
        const pixels = event.deltaMode === WheelEvent.DOM_DELTA_LINE ? event.deltaY * 40 : event.deltaY;
        const delta = Math.max(-1200, Math.min(1200, Math.round(-pixels)));
        if (delta !== 0) {
          this.send({ type: "wheel", delta });
        }
      },
      { passive: false },
    );

    panel.addEventListener("contextmenu", (event) => {
      if (this.enabled) {
        event.preventDefault();
      }
    });
  }

  // Touch -------------------------------------------------------------------

  private bindTouch(): void {
    const options = { passive: false } as const;
    this.panel.addEventListener("touchstart", (event) => this.onTouchStart(event), options);
    this.panel.addEventListener("touchmove", (event) => this.onTouchMove(event), options);
    this.panel.addEventListener("touchend", (event) => this.onTouchEnd(event), options);
    this.panel.addEventListener("touchcancel", () => this.cancelTouch(), options);
  }

  private onTouchStart(event: TouchEvent): void {
    if (!this.enabled) {
      return;
    }
    event.preventDefault();
    if (event.touches.length >= 2) {
      this.beginTwoFinger(event);
      return;
    }
    const touch = event.touches[0];
    this.touchStart = { x: touch.clientX, y: touch.clientY };
    this.lastTouch = this.touchStart;
    this.touchStartedAt = performance.now();
    this.touchMoved = false;
    this.longPressFired = false;
    this.buttonHeld = false;

    if (this.touchMode === "direct") {
      const point = this.normalize(touch.clientX, touch.clientY, true);
      if (point !== undefined) {
        this.movePointer(point, true);
      }
      this.longPressTimer = window.setTimeout(() => {
        if (!this.touchMoved && !this.twoFinger) {
          this.longPressFired = true;
          this.click(2);
          navigator.vibrate?.(30);
        }
      }, longPressMilliseconds);
      return;
    }

    // Trackpad: a tap followed quickly by a touch starts a drag.
    if (this.touchStartedAt - this.lastTapEndedAt < doubleTapMilliseconds) {
      this.buttonHeld = true;
      this.send({ type: "button", button: 0, down: true });
    }
  }

  private onTouchMove(event: TouchEvent): void {
    if (!this.enabled) {
      return;
    }
    event.preventDefault();
    if (this.twoFinger) {
      if (event.touches.length >= 2) {
        const y = (event.touches[0].clientY + event.touches[1].clientY) / 2;
        const deltaY = y - this.lastTwoFingerY;
        this.lastTwoFingerY = y;
        if (Math.abs(deltaY) > 0.5) {
          this.twoFingerMoved = true;
          // Content follows the fingers, like scrolling a page on the phone.
          this.wheelBy(deltaY);
        }
      }
      return;
    }
    const touch = event.touches[0];
    if (touch === undefined || this.touchStart === undefined || this.lastTouch === undefined) {
      return;
    }
    const current = { x: touch.clientX, y: touch.clientY };
    if (Math.hypot(current.x - this.touchStart.x, current.y - this.touchStart.y) > tapSlopPixels) {
      this.touchMoved = true;
    }

    if (this.touchMode === "direct") {
      if (this.touchMoved && !this.buttonHeld && !this.longPressFired) {
        // Dragging from the start point presses the left button there.
        window.clearTimeout(this.longPressTimer);
        const start = this.normalize(this.touchStart.x, this.touchStart.y, true);
        if (start !== undefined) {
          this.movePointer(start, true);
        }
        this.buttonHeld = true;
        this.send({ type: "button", button: 0, down: true });
      }
      const point = this.normalize(current.x, current.y, true);
      if (point !== undefined) {
        this.movePointer(point);
      }
    } else {
      const box = this.contentBox();
      if (box !== undefined) {
        this.cursor = {
          x: Math.min(1, Math.max(0, this.cursor.x + ((current.x - this.lastTouch.x) / box.width) * trackpadSpeed)),
          y: Math.min(1, Math.max(0, this.cursor.y + ((current.y - this.lastTouch.y) / box.height) * trackpadSpeed)),
        };
        this.movePointer(this.cursor);
      }
    }
    this.lastTouch = current;
  }

  private onTouchEnd(event: TouchEvent): void {
    if (!this.enabled) {
      return;
    }
    event.preventDefault();
    if (this.twoFinger) {
      if (event.touches.length === 0) {
        if (!this.twoFingerMoved && performance.now() - this.twoFingerStartedAt < tapMaxMilliseconds * 2) {
          this.click(2);
        }
        this.twoFinger = false;
      }
      return;
    }
    if (event.touches.length > 0) {
      return;
    }
    window.clearTimeout(this.longPressTimer);
    const quick = performance.now() - this.touchStartedAt < tapMaxMilliseconds;

    if (this.buttonHeld) {
      this.send({ type: "button", button: 0, down: false });
      this.buttonHeld = false;
    } else if (!this.touchMoved && !this.longPressFired) {
      if (this.touchMode === "direct" || quick) {
        if (this.touchMode === "trackpad") {
          this.movePointer(this.cursor, true);
        }
        this.click(0);
        this.lastTapEndedAt = performance.now();
      }
    }
    this.touchStart = undefined;
    this.lastTouch = undefined;
  }

  private beginTwoFinger(event: TouchEvent): void {
    window.clearTimeout(this.longPressTimer);
    if (this.buttonHeld) {
      this.send({ type: "button", button: 0, down: false });
      this.buttonHeld = false;
    }
    this.twoFinger = true;
    this.twoFingerMoved = false;
    this.twoFingerStartedAt = performance.now();
    this.wheelRemainder = 0;
    this.lastTwoFingerY = (event.touches[0].clientY + event.touches[1].clientY) / 2;
  }

  private cancelTouch(): void {
    window.clearTimeout(this.longPressTimer);
    if (this.buttonHeld) {
      this.send({ type: "button", button: 0, down: false });
    }
    this.buttonHeld = false;
    this.twoFinger = false;
    this.touchStart = undefined;
    this.lastTouch = undefined;
  }

  // Physical keyboard -------------------------------------------------------

  private bindKeyboard(): void {
    this.panel.addEventListener("keydown", (event) => this.forwardKey(event, true));
    this.panel.addEventListener("keyup", (event) => this.forwardKey(event, false));
  }

  private forwardKey(event: KeyboardEvent, down: boolean): void {
    if (!this.enabled || event.code === "") {
      return;
    }
    event.preventDefault();
    if (down && event.repeat) {
      return;
    }
    this.send({ type: "key", code: event.code, down });
  }

  // On-screen keyboard ------------------------------------------------------
  //
  // Mobile keyboards rarely report real key codes (keyCode 229), and they
  // edit words through IME composition. The proxy textarea is treated as a
  // mirror of what was typed: every change is diffed against the previous
  // value and sent as Backspaces plus Unicode text.

  private bindKeyboardProxy(): void {
    const proxy = this.keyboardProxy;
    proxy.addEventListener("compositionstart", () => {
      this.composing = true;
    });
    proxy.addEventListener("compositionend", () => {
      this.composing = false;
      this.syncKeyboardProxy();
    });
    proxy.addEventListener("input", () => this.syncKeyboardProxy());
    proxy.addEventListener("keydown", (event) => {
      if (!this.enabled || event.isComposing || event.key === "Unidentified" || event.code === "") {
        return;
      }
      // Hardware keys (Bluetooth keyboard, arrows, shortcuts) go as key codes.
      const printable = event.key.length === 1 && !event.ctrlKey && !event.altKey && !event.metaKey;
      if (printable || event.key === "Backspace") {
        return;
      }
      event.preventDefault();
      if (!event.repeat) {
        this.send({ type: "key", code: event.code, down: true });
      }
    });
    proxy.addEventListener("keyup", (event) => {
      if (!this.enabled || event.key === "Unidentified" || event.code === "") {
        return;
      }
      const printable = event.key.length === 1 && !event.ctrlKey && !event.altKey && !event.metaKey;
      if (!printable && event.key !== "Backspace") {
        this.send({ type: "key", code: event.code, down: false });
      }
    });
    proxy.addEventListener("blur", () => this.resetKeyboardProxy());
  }

  private syncKeyboardProxy(): void {
    if (!this.enabled) {
      this.resetKeyboardProxy();
      return;
    }
    const previous = this.keyboardValue;
    const current = this.keyboardProxy.value;
    let common = 0;
    while (common < previous.length && common < current.length && previous[common] === current[common]) {
      common += 1;
    }
    const removed = [...previous.slice(common)].length;
    const added = current.slice(common);
    for (let index = 0; index < removed; index += 1) {
      this.sendCombo(["Backspace"]);
    }
    this.sendTypedText(added);
    this.keyboardValue = current;

    // Keep the sentinel so Backspace on an "empty" field still produces input.
    if (!this.composing && (current.length < keyboardSentinel.length / 2 || current.length > 200)) {
      this.resetKeyboardProxy();
    }
  }

  private sendTypedText(text: string): void {
    let buffer = "";
    const flush = (): void => {
      if (buffer !== "") {
        this.send({ type: "text", text: buffer });
        buffer = "";
      }
    };
    for (const character of text) {
      if (character === "\n") {
        flush();
        this.sendCombo(["Enter"]);
      } else if (character === "\t") {
        flush();
        this.sendCombo(["Tab"]);
      } else {
        buffer += character;
        if (buffer.length >= 64) {
          flush();
        }
      }
    }
    flush();
  }

  private resetKeyboardProxy(): void {
    this.keyboardProxy.value = keyboardSentinel;
    this.keyboardValue = keyboardSentinel;
    const end = keyboardSentinel.length;
    try {
      this.keyboardProxy.setSelectionRange(end, end);
    } catch {
      // Some browsers refuse selection on unfocused fields; harmless.
    }
  }
}

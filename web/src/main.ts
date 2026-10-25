import "./style.css";
import { startApp } from "./app";

const root = document.querySelector<HTMLElement>("#app");
if (root === null) {
  throw new Error("Required element #app is missing");
}
startApp(root, { platform: "web" });

// The service worker makes the client installable as a phone app (PWA). It
// only works on HTTPS or localhost, e.g. when served by a relay with TLS.
if ("serviceWorker" in navigator && window.isSecureContext) {
  window.addEventListener("load", () => {
    void navigator.serviceWorker.register("./sw.js").catch(() => undefined);
  });
}

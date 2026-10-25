// The desktop application reuses the shared MotionLink client (web/src), so
// the phone, the browser and the desktop app behave identically.
import "../../../web/src/style.css";
import { startApp } from "../../../web/src/app";
import { NativeInputStatus } from "../wailsjs/go/main/App";

const root = document.querySelector<HTMLElement>("#app");
if (root === null) {
  throw new Error("Required element #app is missing");
}

startApp(root, {
  platform: "desktop",
  nativeStatus: async () => {
    const status = await NativeInputStatus();
    return `Global input capture: ${status.detail}`;
  },
});

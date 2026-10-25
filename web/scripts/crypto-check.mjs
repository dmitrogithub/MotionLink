// Cross-checks the TypeScript signing code against the vectors pinned in
// internal/auth/auth_test.go, so the client and host cannot drift apart.
// Usage: npm run check:crypto
import { build } from "esbuild";
import assert from "node:assert/strict";

const bundle = await build({
  entryPoints: [new URL("../src/protocol.ts", import.meta.url).pathname.replace(/^\/([A-Za-z]:)/, "$1")],
  bundle: true,
  format: "esm",
  platform: "neutral",
  write: false,
});
const source = bundle.outputFiles[0].text;
const protocol = await import(`data:text/javascript;base64,${Buffer.from(source).toString("base64")}`);

assert.equal(
  protocol.sign("abcd-efgh", "offer", "1700000000000", "nonce123", "fps60", "v=0"),
  "e7c265a782bf05951ba7a9906131f024d1c0afa68bb39d977d92c16e76cf4d8a",
);
assert.equal(protocol.roomId("abcd-efgh"), "7e20519067b634b079e5e32b79cc8f63");
// A message longer than one SHA-256 block and non-ASCII text.
assert.equal(
  protocol.sign("k".repeat(80), "Grüße".repeat(40)),
  "943c738c185aac1d8794f803d6dc13cd035bb3bcf714f925da2b33a55916718f",
);
assert.equal(protocol.formatKey("abcdefghjklm"), "ABCD-EFGH-JKLM");
console.log("crypto vectors match the Go implementation");

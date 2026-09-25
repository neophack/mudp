// Boots a throwaway mudp instance with seeded data for the manual UI review,
// then keeps the node process alive so the spawned server stays up.
import { startServer, seed } from "./fixtures/server.js";

const PORT = 19321;

const server = await startServer({ port: PORT });
console.log(`[ui-review] server ready at ${server.url}`);

try {
  var info = await seed(server, { runId: "review" });
  console.log(`[ui-review] seeded: image=${info.imagePublished} adminContainer=${info.hasAdminContainer} userContainer=${info.hasUserContainer}`);
  console.log(`[ui-review] admin login: ${server.adminUser} / ${server.adminPassword}`);
  console.log(`[ui-review] user login: ${info.user.username} / ${info.user.password}`);
} catch (err) {
  console.error(`[ui-review] seed failed (server still up):`, err.message);
}

// Touch a file so the caller can confirm readiness from the filesystem.
import fs from "node:fs";
fs.writeFileSync(new URL("./ui-review.ready", import.meta.url), server.url);

setInterval(() => {}, 60_000);

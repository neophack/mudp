import { describe, it, expect } from "vitest";
import { resolveNextRedirect } from "../../lib/common.js";

// TestResolveNextRedirect is a regression test: the login page's post-login
// ?next= redirect (used by forward_auth.go to send the browser back to a
// gated forwarded port) once checked only the URL scheme, not the host, so
// ?next=https://evil.example/phish was honoured verbatim -- an open
// redirect an attacker could use for phishing right after a real login. The
// fix restricts next= to the console's own hostname (the forwarded port
// lives on the same host, just a different port).
describe("resolveNextRedirect", () => {
  const origin = "http://mudp.local:9000";

  it("allows a same-host URL with a different port (the forward-auth case)", () => {
    expect(resolveNextRedirect("http://mudp.local:10001/app", origin)).toBe("http://mudp.local:10001/app");
  });

  it("allows a same-host relative path", () => {
    expect(resolveNextRedirect("/dashboard", origin)).toBe("http://mudp.local:9000/dashboard");
  });

  it("rejects a different host even with the same scheme", () => {
    expect(resolveNextRedirect("https://evil.example/phish", origin)).toBeNull();
  });

  it("rejects a different host disguised with the console's port", () => {
    expect(resolveNextRedirect("http://evil.example:9000/", origin)).toBeNull();
  });

  it("rejects a non-http(s) scheme", () => {
    expect(resolveNextRedirect("javascript:alert(1)", origin)).toBeNull();
  });

  it("rejects a malformed URL instead of throwing", () => {
    expect(resolveNextRedirect("http://[::not-valid", origin)).toBeNull();
  });

  it("returns null when next is absent", () => {
    expect(resolveNextRedirect("", origin)).toBeNull();
    expect(resolveNextRedirect(null, origin)).toBeNull();
  });
});

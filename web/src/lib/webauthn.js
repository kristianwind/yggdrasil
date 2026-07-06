// Passkey (WebAuthn) helpers: bridge the base64url wire format used by the Go
// backend and the ArrayBuffers that navigator.credentials expects.
import { api, setToken } from "./api.js";

function b64urlToBuf(s) {
  s = s.replace(/-/g, "+").replace(/_/g, "/");
  const pad = s.length % 4 ? 4 - (s.length % 4) : 0;
  s += "=".repeat(pad);
  const bin = atob(s);
  const buf = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i);
  return buf.buffer;
}

function bufToB64url(buf) {
  const bytes = new Uint8Array(buf);
  let bin = "";
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
  return btoa(bin).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

// Passkeys need a secure context (HTTPS or localhost) + platform support.
export function passkeysSupported() {
  return (
    typeof window !== "undefined" &&
    window.isSecureContext &&
    !!window.PublicKeyCredential &&
    !!(navigator.credentials && navigator.credentials.create)
  );
}

function credentialToJSON(cred, kind) {
  const r = cred.response;
  const out = {
    id: cred.id,
    rawId: bufToB64url(cred.rawId),
    type: cred.type,
    clientExtensionResults: cred.getClientExtensionResults ? cred.getClientExtensionResults() : {},
  };
  if (kind === "attestation") {
    out.response = {
      attestationObject: bufToB64url(r.attestationObject),
      clientDataJSON: bufToB64url(r.clientDataJSON),
    };
    if (r.getTransports) {
      try {
        out.response.transports = r.getTransports();
      } catch {
        /* not all authenticators expose transports */
      }
    }
  } else {
    out.response = {
      authenticatorData: bufToB64url(r.authenticatorData),
      clientDataJSON: bufToB64url(r.clientDataJSON),
      signature: bufToB64url(r.signature),
      userHandle: r.userHandle ? bufToB64url(r.userHandle) : null,
    };
  }
  return out;
}

// Register a new passkey for the currently-logged-in user.
export async function registerPasskey(name) {
  const begin = await api.post("/auth/passkey/register/begin", {});
  const pk = begin.publicKey;
  pk.challenge = b64urlToBuf(pk.challenge);
  pk.user.id = b64urlToBuf(pk.user.id);
  if (pk.excludeCredentials) {
    pk.excludeCredentials = pk.excludeCredentials.map((c) => ({ ...c, id: b64urlToBuf(c.id) }));
  }
  const cred = await navigator.credentials.create({ publicKey: pk });
  const q = new URLSearchParams({ session: begin.session, name: name || "passkey" });
  return api.post(`/auth/passkey/register/finish?${q}`, credentialToJSON(cred, "attestation"));
}

// Passwordless sign-in with a passkey. Stores the session token on success and
// returns { token, username, role }.
export async function loginWithPasskey() {
  const begin = await api.post("/auth/passkey/login/begin", {}, { allow401: true });
  const pk = begin.publicKey;
  pk.challenge = b64urlToBuf(pk.challenge);
  if (pk.allowCredentials) {
    pk.allowCredentials = pk.allowCredentials.map((c) => ({ ...c, id: b64urlToBuf(c.id) }));
  }
  const cred = await navigator.credentials.get({ publicKey: pk });
  const q = new URLSearchParams({ session: begin.session });
  const res = await api.post(`/auth/passkey/login/finish?${q}`, credentialToJSON(cred, "assertion"), {
    allow401: true,
  });
  if (res && res.token) setToken(res.token);
  return res;
}

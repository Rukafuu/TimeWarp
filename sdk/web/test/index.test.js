import assert from "node:assert/strict";
import test from "node:test";

import { TimeWarpWeb, captureDOM } from "../src/index.js";

function installBrowser(fetchImpl = async () => ({ status: 202, ok: true, headers: new Headers() })) {
  const original = {
    fetch: globalThis.fetch,
    location: globalThis.location,
    document: globalThis.document,
    history: globalThis.history,
    addEventListener: globalThis.addEventListener,
    removeEventListener: globalThis.removeEventListener,
  };
  const listeners = new Map();
  globalThis.fetch = fetchImpl;
  globalThis.location = { href: "https://app.test/orders?token=secret#private", origin: "https://app.test" };
  globalThis.document = { referrer: "https://search.test/?q=private" };
  globalThis.history = { pushState() {}, replaceState() {} };
  globalThis.addEventListener = (type, handler) => listeners.set(type, handler);
  globalThis.removeEventListener = (type) => listeners.delete(type);
  return {
    listeners,
    restore() { Object.assign(globalThis, original); },
  };
}

test("batches events and strips URL secrets", async () => {
  const payloads = [];
  const browser = installBrowser(async (_url, init) => {
    payloads.push(JSON.parse(init.body));
    return { status: 202, ok: true, headers: new Headers() };
  });
  try {
    const recorder = new TimeWarpWeb({ service: "storefront", mode: "errors", flushIntervalMs: 60_000 }).start();
    recorder.captureRenderIssue({ component: "Cart", message: "wrong alignment", tags: { token: "secret", theme: "dark" } });
    assert.equal(await recorder.flush(), true);
    assert.equal(payloads.length, 1);
    assert.equal(payloads[0].events.length, 2);
    assert.equal(payloads[0].events[0].parent_id, "");
    assert.equal(payloads[0].events[0].metadata.route, "https://app.test/orders");
    assert.equal(payloads[0].events[1].metadata.tags.token, "[redacted]");
    recorder.stop();
  } finally {
    browser.restore();
  }
});

test("instruments application fetch without recursively recording collector delivery", async () => {
  const calls = [];
  const payloads = [];
  const browser = installBrowser(async (url, init = {}) => {
    calls.push(String(url));
    if (String(url).endsWith("/v1/events")) payloads.push(JSON.parse(init.body));
    return { status: 202, ok: true, headers: new Headers() };
  });
  try {
    const recorder = new TimeWarpWeb({ service: "storefront", mode: "telemetry", flushIntervalMs: 60_000 }).start();
    await globalThis.fetch("https://api.test/orders?token=private");
    await recorder.flush();
    assert.deepEqual(calls, ["https://api.test/orders?token=private", "http://localhost:7777/v1/events"]);
    const network = payloads[0].events.find((event) => event.type === "HTTP_CLIENT");
    assert.equal(network.metadata.url, "https://api.test/orders");
    recorder.stop();
  } finally {
    browser.restore();
  }
});

test("captures global errors without requiring diagnostic consent", async () => {
  const payloads = [];
  const browser = installBrowser(async (_url, init) => {
    payloads.push(JSON.parse(init.body));
    return { status: 202, ok: true, headers: new Headers() };
  });
  try {
    const recorder = new TimeWarpWeb({ service: "storefront", flushIntervalMs: 60_000 }).start();
    browser.listeners.get("error")({ message: "render failed", filename: "https://app.test/app.js?key=x", lineno: 4, colno: 2 });
    await recorder.flush();
    const error = payloads[0].events.find((event) => event.type === "ERROR");
    assert.equal(error.metadata.message, "render failed");
    assert.equal(error.metadata.filename, "https://app.test/app.js");
    recorder.stop();
  } finally {
    browser.restore();
  }
});

test("requires temporary consent before DOM capture", () => {
  const browser = installBrowser();
  try {
    const recorder = new TimeWarpWeb({ service: "storefront", mode: "diagnostic", flushIntervalMs: 60_000 }).start();
    assert.throws(() => recorder.captureRenderIssue({ includeDOM: true, root: fakeDOM() }), /consent/);
    recorder.grantDiagnosticConsent(60_000);
    assert.doesNotThrow(() => recorder.captureRenderIssue({ includeDOM: true, root: fakeDOM() }));
    recorder.revokeDiagnosticConsent();
    assert.throws(() => recorder.captureRenderIssue({ includeDOM: true, root: fakeDOM() }), /consent/);
    recorder.stop();
  } finally {
    browser.restore();
  }
});

test("DOM snapshots omit text and sensitive attributes", () => {
  globalThis.Node = { ELEMENT_NODE: 1, TEXT_NODE: 3 };
  const snapshot = captureDOM(fakeDOM(), { includeText: false });
  assert.equal(snapshot.root.tag, "main");
  assert.equal(snapshot.root.attributes["data-testid"], "checkout");
  assert.equal(snapshot.root.attributes["data-token"], undefined);
  assert.equal(JSON.stringify(snapshot).includes("card number"), false);
});

test("retries 503 batches and preserves a single payload", async () => {
  let calls = 0;
  const browser = installBrowser(async () => {
    calls += 1;
    return { status: calls === 1 ? 503 : 202, ok: calls > 1, headers: new Headers({ "retry-after": "0" }) };
  });
  try {
    const recorder = new TimeWarpWeb({ service: "storefront", maxRetries: 1, retryBaseMs: 0, flushIntervalMs: 60_000 }).start();
    assert.equal(await recorder.flush(), true);
    assert.equal(calls, 2);
    recorder.stop();
  } finally {
    browser.restore();
  }
});

function fakeDOM() {
  const text = { nodeType: 3, textContent: "card number 4111" };
  return {
    nodeType: 1,
    tagName: "MAIN",
    attributes: [
      { name: "data-testid", value: "checkout" },
      { name: "data-token", value: "secret" },
    ],
    childNodes: [text],
    getBoundingClientRect: () => ({ x: 1.2, y: 2.4, width: 300.2, height: 100.8 }),
  };
}

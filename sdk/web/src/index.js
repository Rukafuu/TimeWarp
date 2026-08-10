const EVENT = Object.freeze({
  HTTP_CLIENT: "HTTP_CLIENT",
  CUSTOM: "CUSTOM_EVENT",
  ERROR: "ERROR",
  TIMEOUT: "TIMEOUT",
});

const DEFAULTS = Object.freeze({
  endpoint: "http://localhost:7777",
  mode: "errors",
  flushIntervalMs: 100,
  maxBatchSize: 50,
  maxQueueSize: 1000,
  maxRetries: 4,
  retryBaseMs: 100,
  requestTimeoutMs: 3000,
  includeURLQuery: false,
  domMaxNodes: 500,
  domMaxDepth: 12,
  domIncludeText: false,
});

const SENSITIVE_KEY = /authorization|cookie|token|secret|password|passwd|api[-_]?key|session/i;
const ALLOWED_DOM_ATTRIBUTES = new Set([
  "id", "class", "role", "type", "name", "data-testid", "aria-label",
  "aria-hidden", "aria-expanded", "aria-selected", "disabled", "checked",
]);

export class TimeWarpWeb {
  constructor(options) {
    if (!options || !cleanString(options.service, 128)) {
      throw new Error("TimeWarpWeb requires a non-empty service");
    }
    this.options = { ...DEFAULTS, ...options };
    if (!new Set(["errors", "telemetry", "diagnostic"]).has(this.options.mode)) {
      throw new Error("mode must be errors, telemetry, or diagnostic");
    }
    this.options.endpoint = String(this.options.endpoint).replace(/\/$/, "");
    this.options.maxBatchSize = clampInteger(this.options.maxBatchSize, 1, 500);
    this.options.maxQueueSize = clampInteger(this.options.maxQueueSize, this.options.maxBatchSize, 10_000);
    this.options.flushIntervalMs = clampInteger(this.options.flushIntervalMs, 10, 60_000);
    this.options.maxRetries = clampInteger(this.options.maxRetries, 0, 10);
    this.options.retryBaseMs = clampInteger(this.options.retryBaseMs, 0, 30_000);
    this.options.requestTimeoutMs = clampInteger(this.options.requestTimeoutMs, 100, 30_000);
    this.traceId = cleanString(options.traceId, 256) || makeID("trace");
    this.pageEventId = makeID("page");
    this.queue = [];
    this.timer = null;
    this.started = false;
    this.disposed = false;
    this.flushing = null;
    this.restore = [];
    this.droppedEvents = 0;
    this.diagnosticConsentUntil = 0;
  }

  start() {
    if (this.started || this.disposed) return this;
    this.started = true;
    this.record("CUSTOM_EVENT", {
      name: "browser.page",
      route: safeURL(globalThis.location?.href, this.options.includeURLQuery),
      referrer: safeURL(globalThis.document?.referrer, false),
      viewport: viewport(),
      mode: this.options.mode,
    }, { eventId: this.pageEventId, parentId: "" });
    this.installErrors();
    if (this.options.mode !== "errors") {
      this.installNavigation();
      this.installFetch();
      this.installPerformance();
    }
    const onPageHide = () => this.flush({ beacon: true });
    globalThis.addEventListener?.("pagehide", onPageHide);
    this.restore.push(() => globalThis.removeEventListener?.("pagehide", onPageHide));
    return this;
  }

  stop() {
    if (this.disposed) return;
    this.disposed = true;
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
    for (const restore of this.restore.splice(0).reverse()) restore();
  }

  grantDiagnosticConsent(ttlMs = 15 * 60 * 1000) {
    if (this.options.mode !== "diagnostic") {
      throw new Error("diagnostic consent requires mode=diagnostic");
    }
    if (!Number.isFinite(ttlMs) || ttlMs < 60_000 || ttlMs > 24 * 60 * 60 * 1000) {
      throw new Error("diagnostic consent TTL must be between 1 minute and 24 hours");
    }
    this.diagnosticConsentUntil = Date.now() + ttlMs;
    return this.diagnosticConsentUntil;
  }

  revokeDiagnosticConsent() {
    this.diagnosticConsentUntil = 0;
  }

  captureRenderIssue(input = {}) {
    const metadata = {
      name: "browser.render_issue",
      component: cleanString(input.component, 160),
      message: cleanString(input.message, 1000),
      route: safeURL(globalThis.location?.href, this.options.includeURLQuery),
      viewport: viewport(),
      tags: sanitizeObject(input.tags),
    };
    if (input.includeDOM) {
      this.requireDiagnosticConsent();
      metadata.dom = captureDOM(input.root || globalThis.document?.documentElement, {
        maxNodes: this.options.domMaxNodes,
        maxDepth: this.options.domMaxDepth,
        includeText: this.options.domIncludeText,
      });
    }
    return this.record(input.error ? "ERROR" : "CUSTOM_EVENT", metadata, {
      durationMs: finiteNonNegative(input.durationMs),
    });
  }

  measureRender(component, operation) {
    if (typeof operation !== "function") throw new Error("operation must be a function");
    const started = now();
    try {
      const result = operation();
      if (result && typeof result.then === "function") {
        return result.then(
          (value) => {
            this.captureRenderIssue({ component, message: "render completed", durationMs: now() - started });
            return value;
          },
          (error) => {
            this.captureRenderIssue({ component, message: errorMessage(error), error: true, durationMs: now() - started });
            throw error;
          },
        );
      }
      this.captureRenderIssue({ component, message: "render completed", durationMs: now() - started });
      return result;
    } catch (error) {
      this.captureRenderIssue({ component, message: errorMessage(error), error: true, durationMs: now() - started });
      throw error;
    }
  }

  record(type, metadata = {}, extra = {}) {
    if (this.disposed) return null;
    const event = {
      event_id: extra.eventId || makeID("evt"),
      trace_id: this.traceId,
      parent_id: extra.parentId === undefined ? this.pageEventId : extra.parentId,
      service: this.options.service,
      instance: cleanString(this.options.instance, 128),
      type,
      timestamp: Date.now(),
      duration_ms: Math.round(finiteNonNegative(extra.durationMs) || 0),
      metadata: sanitizeObject(metadata),
    };
    if (this.queue.length >= this.options.maxQueueSize) {
      this.queue.shift();
      this.droppedEvents += 1;
    }
    this.queue.push(event);
    if (this.queue.length >= this.options.maxBatchSize) this.flush();
    else this.scheduleFlush();
    return event.event_id;
  }

  async flush(options = {}) {
    if (this.flushing) return this.flushing;
    if (this.timer) clearTimeout(this.timer);
    this.timer = null;
    if (!this.queue.length) return true;
    const batch = this.queue.splice(0, this.options.maxBatchSize);
    if (this.droppedEvents) {
      batch[0].metadata = { ...batch[0].metadata, sdk_dropped_events: this.droppedEvents };
      this.droppedEvents = 0;
    }
    const payload = JSON.stringify({ events: batch });
    this.flushing = this.send(payload, options).then((sent) => {
      if (!sent) this.requeue(batch);
      return sent;
    }).finally(() => {
      this.flushing = null;
      if (this.queue.length && !this.disposed) this.scheduleFlush();
    });
    return this.flushing;
  }

  async send(payload, { beacon = false } = {}) {
    const url = `${this.options.endpoint}/v1/events`;
    if (beacon && globalThis.navigator?.sendBeacon) {
      return globalThis.navigator.sendBeacon(url, new Blob([payload], { type: "application/json" }));
    }
    let delay = this.options.retryBaseMs;
    for (let attempt = 0; attempt <= this.options.maxRetries; attempt += 1) {
      const controller = new AbortController();
      const timeout = setTimeout(() => controller.abort(), this.options.requestTimeoutMs);
      try {
        const response = await globalThis.fetch(url, {
          method: "POST",
          headers: { "content-type": "application/json" },
          body: payload,
          keepalive: true,
          signal: controller.signal,
          [INTERNAL_REQUEST]: true,
        });
        if (response.status === 202) return true;
        if (response.status !== 429 && response.status !== 503) return false;
        const retryAfter = parseRetryAfter(response.headers?.get?.("retry-after"));
        if (retryAfter !== null) delay = retryAfter;
      } catch {
        // Network and timeout errors use the same bounded retry policy.
      } finally {
        clearTimeout(timeout);
      }
      if (attempt < this.options.maxRetries) await sleep(delay);
      delay = Math.min(delay * 2, 5000);
    }
    return false;
  }

  installErrors() {
    const onError = (event) => this.record("ERROR", {
      name: "browser.error",
      message: cleanString(event.message || errorMessage(event.error), 1000),
      filename: safeURL(event.filename, this.options.includeURLQuery),
      line: event.lineno,
      column: event.colno,
      stack: sanitizeStack(event.error?.stack),
    });
    const onRejection = (event) => this.record("ERROR", {
      name: "browser.unhandled_rejection",
      message: errorMessage(event.reason),
      stack: sanitizeStack(event.reason?.stack),
    });
    globalThis.addEventListener?.("error", onError);
    globalThis.addEventListener?.("unhandledrejection", onRejection);
    this.restore.push(() => globalThis.removeEventListener?.("error", onError));
    this.restore.push(() => globalThis.removeEventListener?.("unhandledrejection", onRejection));
  }

  installNavigation() {
    const recordRoute = (source) => this.record("CUSTOM_EVENT", {
      name: "browser.navigation",
      source,
      route: safeURL(globalThis.location?.href, this.options.includeURLQuery),
    });
    for (const method of ["pushState", "replaceState"]) {
      const original = globalThis.history?.[method];
      if (typeof original !== "function") continue;
      globalThis.history[method] = function (...args) {
        const result = original.apply(this, args);
        recordRoute(method);
        return result;
      };
      this.restore.push(() => { globalThis.history[method] = original; });
    }
    const onPopState = () => recordRoute("popstate");
    globalThis.addEventListener?.("popstate", onPopState);
    this.restore.push(() => globalThis.removeEventListener?.("popstate", onPopState));
  }

  installFetch() {
    const original = globalThis.fetch;
    if (typeof original !== "function") return;
    const recorder = this;
    globalThis.fetch = async function (input, init = {}) {
      if (init?.[INTERNAL_REQUEST]) return original.call(this, input, stripInternal(init));
      const url = requestURL(input);
      if (url.startsWith(recorder.options.endpoint)) return original.call(this, input, init);
      const started = now();
      try {
        const response = await original.call(this, input, init);
        recorder.record("HTTP_CLIENT", {
          name: "browser.fetch",
          method: requestMethod(input, init),
          url: safeURL(url, recorder.options.includeURLQuery),
          status_code: response.status,
          success: response.ok,
        }, { durationMs: now() - started });
        return response;
      } catch (error) {
        recorder.record("ERROR", {
          name: "browser.fetch_error",
          method: requestMethod(input, init),
          url: safeURL(url, recorder.options.includeURLQuery),
          message: errorMessage(error),
        }, { durationMs: now() - started });
        throw error;
      }
    };
    this.restore.push(() => { globalThis.fetch = original; });
  }

  installPerformance() {
    if (typeof globalThis.PerformanceObserver !== "function") return;
    const observe = (type, handle) => {
      try {
        const observer = new PerformanceObserver((list) => handle(list.getEntries()));
        observer.observe({ type, buffered: true });
        this.restore.push(() => observer.disconnect());
      } catch {
        // Unsupported performance entry types are optional.
      }
    };
    observe("largest-contentful-paint", (entries) => {
      const entry = entries.at(-1);
      if (entry) this.record("CUSTOM_EVENT", { name: "browser.web_vital", metric: "LCP", value: entry.startTime });
    });
    let cls = 0;
    observe("layout-shift", (entries) => {
      for (const entry of entries) if (!entry.hadRecentInput) cls += entry.value || 0;
      this.record("CUSTOM_EVENT", { name: "browser.web_vital", metric: "CLS", value: cls });
    });
    observe("longtask", (entries) => {
      for (const entry of entries) this.record("CUSTOM_EVENT", {
        name: "browser.long_task", duration_ms: entry.duration,
      }, { durationMs: entry.duration });
    });
  }

  requireDiagnosticConsent() {
    if (this.options.mode !== "diagnostic" || Date.now() >= this.diagnosticConsentUntil) {
      throw new Error("active diagnostic consent is required for DOM capture");
    }
  }

  scheduleFlush() {
    if (this.timer || this.disposed) return;
    this.timer = setTimeout(() => this.flush(), this.options.flushIntervalMs);
  }

  requeue(events) {
    const available = Math.max(0, this.options.maxQueueSize - this.queue.length);
    if (available > 0) this.queue.unshift(...events.slice(-available));
  }
}

const INTERNAL_REQUEST = Symbol("timewarp-internal-request");

export function createTimeWarpWeb(options) {
  return new TimeWarpWeb(options);
}

export function captureDOM(root, options = {}) {
  if (!root || typeof root !== "object") return null;
  const maxNodes = clamp(options.maxNodes ?? DEFAULTS.domMaxNodes, 1, 2000);
  const maxDepth = clamp(options.maxDepth ?? DEFAULTS.domMaxDepth, 1, 30);
  const includeText = options.includeText === true;
  let count = 0;
  const visit = (node, depth) => {
    if (!node || count >= maxNodes || depth > maxDepth) return null;
    const elementType = globalThis.Node?.ELEMENT_NODE ?? 1;
    const textType = globalThis.Node?.TEXT_NODE ?? 3;
    if (node.nodeType === textType) {
      if (!includeText) return null;
      const text = cleanString(node.textContent, 120);
      return text ? { text: "[redacted]", length: text.length } : null;
    }
    if (node.nodeType !== elementType) return null;
    count += 1;
    const item = { tag: String(node.tagName || "unknown").toLowerCase() };
    const attributes = {};
    for (const attribute of Array.from(node.attributes || [])) {
      if (!ALLOWED_DOM_ATTRIBUTES.has(attribute.name) || SENSITIVE_KEY.test(attribute.name)) continue;
      attributes[attribute.name] = cleanString(attribute.value, 160);
    }
    if (Object.keys(attributes).length) item.attributes = attributes;
    const rect = node.getBoundingClientRect?.();
    if (rect) item.rect = roundedRect(rect);
    const children = [];
    for (const child of Array.from(node.childNodes || [])) {
      const captured = visit(child, depth + 1);
      if (captured) children.push(captured);
      if (count >= maxNodes) break;
    }
    if (children.length) item.children = children;
    return item;
  };
  return { root: visit(root, 0), nodes: count, truncated: count >= maxNodes };
}

function sanitizeObject(value, depth = 0) {
  if (depth > 5 || value == null) return value == null ? value : "[truncated]";
  if (typeof value === "string") return cleanString(value, 1000);
  if (typeof value === "number" || typeof value === "boolean") return value;
  if (Array.isArray(value)) return value.slice(0, 50).map((item) => sanitizeObject(item, depth + 1));
  if (typeof value !== "object") return String(value);
  const output = {};
  for (const [key, item] of Object.entries(value).slice(0, 100)) {
    output[key] = SENSITIVE_KEY.test(key) ? "[redacted]" : sanitizeObject(item, depth + 1);
  }
  return output;
}

function safeURL(value, includeQuery) {
  if (!value) return "";
  try {
    const base = globalThis.location?.origin || "http://localhost";
    const url = new URL(String(value), base);
    url.username = "";
    url.password = "";
    url.hash = "";
    if (!includeQuery) url.search = "";
    return url.toString();
  } catch {
    return "[invalid-url]";
  }
}

function sanitizeStack(stack) {
  return cleanString(stack, 4000)?.split("\n").slice(0, 20).join("\n") || "";
}

function errorMessage(error) {
  if (error instanceof Error) return cleanString(error.message, 1000);
  if (typeof error === "string") return cleanString(error, 1000);
  try { return cleanString(JSON.stringify(sanitizeObject(error)), 1000); }
  catch { return "unknown error"; }
}

function viewport() {
  return {
    width: finiteNonNegative(globalThis.innerWidth),
    height: finiteNonNegative(globalThis.innerHeight),
    device_pixel_ratio: finiteNonNegative(globalThis.devicePixelRatio),
  };
}

function roundedRect(rect) {
  return {
    x: Math.round(rect.x), y: Math.round(rect.y),
    width: Math.round(rect.width), height: Math.round(rect.height),
  };
}

function requestURL(input) {
  if (typeof input === "string" || input instanceof URL) return String(input);
  return String(input?.url || "");
}

function requestMethod(input, init) {
  return cleanString(init?.method || input?.method || "GET", 16).toUpperCase();
}

function stripInternal(init) {
  const clean = { ...init };
  delete clean[INTERNAL_REQUEST];
  return clean;
}

function parseRetryAfter(value) {
  if (!value) return null;
  const seconds = Number(value);
  if (Number.isFinite(seconds)) return clamp(seconds * 1000, 0, 30_000);
  const date = Date.parse(value);
  return Number.isFinite(date) ? clamp(date - Date.now(), 0, 30_000) : null;
}

function cleanString(value, limit) {
  if (value == null) return "";
  return String(value).replace(/[\u0000-\u001f\u007f]/g, " ").trim().slice(0, limit);
}

function finiteNonNegative(value) {
  return Number.isFinite(Number(value)) ? Math.max(0, Number(value)) : 0;
}

function clamp(value, min, max) {
  return Math.min(max, Math.max(min, Number(value)));
}

function clampInteger(value, min, max) {
  const numeric = Number(value);
  return Number.isFinite(numeric) ? Math.round(clamp(numeric, min, max)) : min;
}

function now() {
  return globalThis.performance?.now?.() ?? Date.now();
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function makeID(prefix) {
  const value = globalThis.crypto?.randomUUID?.() || `${Date.now().toString(36)}${Math.random().toString(36).slice(2)}`;
  return `${prefix}_${value}`;
}

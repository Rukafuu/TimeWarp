# Universal Web SDK

`@timewarp/web` instruments browser applications without depending on a UI
framework or company-specific domain. It sends standard TimeWarp events to
`POST /v1/events` and correlates page, render, navigation, network, error, and
performance evidence under one trace.

## Capture modes

| Mode | Captures |
|---|---|
| `errors` | Page lifecycle, global errors, unhandled promise rejections, and explicit render issues. |
| `telemetry` | Errors plus route changes, fetch method/status/timing, LCP, CLS, and long tasks. |
| `diagnostic` | Telemetry plus bounded structural DOM snapshots after temporary runtime consent. |

Initialization is explicit; merely loading the module starts no collection.
Fetch bodies and headers, form values, cookies, storage, screenshots, canvas
pixels, and stylesheet contents are never collected. Query strings and URL
fragments are removed unless the host explicitly enables query capture.

## Render diagnosis

Use `captureRenderIssue` from a framework error boundary, layout assertion,
visual regression hook, or support workflow:

```js
timewarp.captureRenderIssue({
  component: "AccountSummary",
  message: "card overlaps actions at mobile width",
  durationMs: 87,
  error: false,
});
```

This produces a normal `CUSTOM_EVENT` or `ERROR`, so it is searchable and
causally related to the surrounding page and network events.

DOM capture is intentionally separate. The host application must run in
`diagnostic` mode, gather user consent, activate a grant lasting from one
minute to 24 hours, and explicitly request `includeDOM`. Revocation takes
effect immediately. The snapshot:

- has node and depth limits;
- captures element names, a small attribute allowlist, and rectangles;
- omits text by default and never records the text itself even when enabled;
- excludes form values and credential-like attributes;
- is treated as sensitive payload by MCP and therefore requires
  `payload:read` in addition to `trace:read`.

## Delivery and backpressure

The SDK batches up to 50 events or 100 ms by default. The queue is bounded,
oldest events are dropped under sustained pressure, and the next successful
batch reports the drop count. HTTP `429` and `503`, network errors, and timeouts
use bounded exponential backoff. `pagehide` attempts a final `sendBeacon`
flush. Calls made by the SDK itself are excluded from fetch instrumentation.

See [`sdk/web/README.md`](../sdk/web/README.md) for installation and API usage.

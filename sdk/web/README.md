# `@timewarp/web`

Universal, dependency-free browser instrumentation for TimeWarp. It records
events the application explicitly enables; it is not a screen recorder and
does not capture every browser action automatically.

```js
import { createTimeWarpWeb } from "@timewarp/web";

const timewarp = createTimeWarpWeb({
  service: "storefront",
  endpoint: "http://localhost:7777",
  mode: "telemetry",
}).start();
```

Modes:

- `errors`: page lifecycle, global errors, and unhandled promise rejections.
- `telemetry`: errors plus navigation, fetch timing/status, LCP, CLS, and long tasks.
- `diagnostic`: telemetry plus an API for consented structural DOM snapshots.

Fetch capture excludes request/response headers and bodies. URL query strings
and fragments are removed by default. Metadata keys that resemble credentials,
cookies, tokens, passwords, or session identifiers are redacted.

## Render failures

Framework error boundaries can report render failures directly:

```js
timewarp.captureRenderIssue({
  component: "CheckoutSummary",
  message: error.message,
  error: true,
  durationMs: 42,
});
```

For a structural DOM snapshot, initialize `mode: "diagnostic"`, obtain user
consent in the host application, and activate a temporary grant:

```js
timewarp.grantDiagnosticConsent(5 * 60 * 1000);
timewarp.captureRenderIssue({
  component: "CheckoutSummary",
  message: "layout differs from expected state",
  includeDOM: true,
  root: document.querySelector("main"),
});
timewarp.revokeDiagnosticConsent();
```

DOM capture is structural and bounded. It uses an attribute allowlist, omits
text by default, rejects credential-like attributes, and records element
rectangles for layout diagnosis. It does not capture screenshots, form values,
canvas pixels, stylesheets, local storage, cookies, or browser storage.

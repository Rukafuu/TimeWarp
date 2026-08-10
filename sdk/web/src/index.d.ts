export type CaptureMode = "errors" | "telemetry" | "diagnostic";

export interface TimeWarpWebOptions {
  service: string;
  instance?: string;
  endpoint?: string;
  traceId?: string;
  mode?: CaptureMode;
  flushIntervalMs?: number;
  maxBatchSize?: number;
  maxQueueSize?: number;
  maxRetries?: number;
  retryBaseMs?: number;
  requestTimeoutMs?: number;
  includeURLQuery?: boolean;
  domMaxNodes?: number;
  domMaxDepth?: number;
  domIncludeText?: boolean;
}

export interface RenderIssue {
  component?: string;
  message?: string;
  error?: boolean;
  durationMs?: number;
  tags?: Record<string, unknown>;
  includeDOM?: boolean;
  root?: Element;
}

export interface DOMCaptureOptions {
  maxNodes?: number;
  maxDepth?: number;
  includeText?: boolean;
}

export declare class TimeWarpWeb {
  readonly traceId: string;
  constructor(options: TimeWarpWebOptions);
  start(): this;
  stop(): void;
  grantDiagnosticConsent(ttlMs?: number): number;
  revokeDiagnosticConsent(): void;
  captureRenderIssue(input?: RenderIssue): string | null;
  measureRender<T>(component: string, operation: () => T): T;
  record(type: string, metadata?: Record<string, unknown>, extra?: { eventId?: string; parentId?: string; durationMs?: number }): string | null;
  flush(options?: { beacon?: boolean }): Promise<boolean>;
}

export declare function createTimeWarpWeb(options: TimeWarpWebOptions): TimeWarpWeb;
export declare function captureDOM(root: Element, options?: DOMCaptureOptions): unknown;

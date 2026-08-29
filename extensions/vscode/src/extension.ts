import * as vscode from "vscode";
import { ChildProcess, execFile, spawn } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
interface Trace { TraceID: string; RootService: string; Status: string; StartedAt: number; DurationMS: number; EventCount: number; }
interface TraceEvent { event_id: string; parent_id?: string; service: string; type: string; timestamp: number; duration_ms?: number; metadata?: Record<string, unknown>; http?: Record<string, unknown>; }

class TimeWarpClient {
  private collector?: ChildProcess;
  private settings() {
    const config = vscode.workspace.getConfiguration("timewarp");
    const folder = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath ?? process.cwd();
    return {
      binary: config.get<string>("binaryPath", "timewarp"),
      database: config.get<string>("databasePath", "${workspaceFolder}/timewarp.db").replace("${workspaceFolder}", folder),
      address: config.get<string>("collectorAddress", ":7777")
    };
  }
  private async run(args: string[]) {
    const { binary, database } = this.settings();
    const { stdout } = await execFileAsync(binary, args, { cwd: vscode.workspace.workspaceFolders?.[0]?.uri.fsPath, env: { ...process.env, TIMEWARP_DB: database }, windowsHide: true, maxBuffer: 10 * 1024 * 1024 });
    return stdout;
  }
  async traces(): Promise<Trace[]> { return JSON.parse(await this.run(["traces", "--json", "--limit", "100"])) as Trace[]; }
  async events(traceID: string): Promise<TraceEvent[]> { return JSON.parse(await this.run(["inspect", traceID, "--json"])) as TraceEvent[]; }
  startCollector(output: vscode.OutputChannel) {
    if (this.collector && !this.collector.killed) return;
    const { binary, database, address } = this.settings();
    this.collector = spawn(binary, ["serve", "--addr", address], { cwd: vscode.workspace.workspaceFolders?.[0]?.uri.fsPath, env: { ...process.env, TIMEWARP_DB: database }, windowsHide: true });
    this.collector.stdout?.on("data", data => output.append(data.toString()));
    this.collector.stderr?.on("data", data => output.append(data.toString()));
    this.collector.once("exit", code => { output.appendLine(`Collector stopped (${code ?? "signal"}).`); this.collector = undefined; });
    output.show(true);
  }
  stopCollector() { this.collector?.kill(); this.collector = undefined; }
}

class TraceItem extends vscode.TreeItem {
  constructor(readonly trace: Trace) {
    super(trace.RootService || trace.TraceID, vscode.TreeItemCollapsibleState.None);
    this.description = `${trace.Status} · ${trace.DurationMS}ms · ${trace.EventCount} events`;
    this.tooltip = trace.TraceID;
    this.iconPath = new vscode.ThemeIcon(trace.Status === "ERROR" ? "error" : "pass");
    this.command = { command: "timewarp.openTrace", title: "Open Trace", arguments: [trace] };
  }
}

class TraceProvider implements vscode.TreeDataProvider<TraceItem> {
  private readonly changed = new vscode.EventEmitter<void>();
  readonly onDidChangeTreeData = this.changed.event;
  constructor(private readonly client: TimeWarpClient) {}
  refresh() { this.changed.fire(); }
  getTreeItem(item: TraceItem) { return item; }
  async getChildren() {
    try { return (await this.client.traces()).map(trace => new TraceItem(trace)); }
    catch (error) { void vscode.window.showErrorMessage(`TimeWarp: ${messageOf(error)}`); return []; }
  }
}

export function activate(context: vscode.ExtensionContext) {
  const client = new TimeWarpClient();
  const provider = new TraceProvider(client);
  const output = vscode.window.createOutputChannel("TimeWarp");
  context.subscriptions.push(
    output,
    vscode.window.registerTreeDataProvider("timewarp.traces", provider),
    vscode.commands.registerCommand("timewarp.refresh", () => provider.refresh()),
    vscode.commands.registerCommand("timewarp.startCollector", () => client.startCollector(output)),
    vscode.commands.registerCommand("timewarp.stopCollector", () => client.stopCollector()),
    vscode.commands.registerCommand("timewarp.openTrace", async (trace?: Trace) => {
      if (!trace) {
        const traces = await client.traces();
        trace = await vscode.window.showQuickPick(traces.map(item => ({ label: item.RootService, description: item.TraceID, item }))).then(pick => pick?.item);
      }
      if (!trace) return;
      const events = await client.events(trace.TraceID);
      const panel = vscode.window.createWebviewPanel("timewarp.trace", `TimeWarp · ${trace.RootService}`, vscode.ViewColumn.One, {});
      panel.webview.html = renderTrace(trace, events);
    })
  );
}
export function deactivate() {}
function messageOf(error: unknown) { return error instanceof Error ? error.message : String(error); }
function escapeHTML(value: unknown) { return String(value).replace(/[&<>"']/g, char => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[char]!); }
function renderTrace(trace: Trace, events: TraceEvent[]) {
  const start = events.length ? Math.min(...events.map(event => event.timestamp)) : 0;
  const rows = events.map(event => {
    const detail = escapeHTML(JSON.stringify({ metadata: event.metadata, http: event.http }, null, 2));
    return `<details class="event"><summary><span class="type">${escapeHTML(event.type)}</span><strong>${escapeHTML(event.service)}</strong><span>+${event.timestamp - start}ms</span><span>${event.duration_ms ?? 0}ms</span></summary><pre>${detail}</pre></details>`;
  }).join("");
  return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><style>body{font:13px var(--vscode-font-family);color:var(--vscode-foreground);padding:24px;max-width:1100px;margin:auto}.hero{display:flex;justify-content:space-between;align-items:end;border-bottom:1px solid var(--vscode-panel-border);padding-bottom:16px;margin-bottom:18px}h1{margin:0;font-size:22px}.muted{color:var(--vscode-descriptionForeground)}.event{border:1px solid var(--vscode-panel-border);border-radius:6px;margin:8px 0;background:var(--vscode-editor-background)}summary{cursor:pointer;display:grid;grid-template-columns:150px 1fr 90px 80px;gap:12px;padding:12px;align-items:center}.type{color:var(--vscode-symbolIcon-functionForeground)}pre{overflow:auto;padding:12px;border-top:1px solid var(--vscode-panel-border);margin:0}</style></head><body><section class="hero"><div><h1>${escapeHTML(trace.RootService)}</h1><div class="muted">${escapeHTML(trace.TraceID)}</div></div><strong>${escapeHTML(trace.Status)} · ${trace.DurationMS}ms</strong></section>${rows || "<p>No events.</p>"}</body></html>`;
}

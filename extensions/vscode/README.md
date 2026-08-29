# TimeWarp for VS Code-compatible editors

The extension is a local UI for the TimeWarp Go binary. It works in VS Code and compatible editors such as Cursor.

## Development

```sh
go build -o timewarp ./cmd/timewarp
cd extensions/vscode
npm install
npm run compile
```

Open `extensions/vscode` in VS Code and press `F5`. Configure `timewarp.binaryPath` when the binary is not available on `PATH`; the extension shares `timewarp.databasePath` with the collector and MCP server.

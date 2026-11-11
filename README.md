# CWG2 Uploader

A Windows system-tray application written in Go. It watches CWG2 saves and automatically submits stable snapshots to the [CWG2 public leaderboard](https://cwg2-leaderboard.rskiland.workers.dev/#leaderboard).

**Starting the app enables automatic uploads by default, including existing saves.** Saves go to a third-party service and may appear on its public leaderboard. This app is not affiliated with the leaderboard service. It never modifies game saves.

## Run

1. Place `cwg2-uploader.exe` in a permanent location and launch it.
2. Read the first-run notice. Look for the uploader in the Windows notification area (possibly under the hidden-icons arrow).
3. The app finds your Windows Documents folder and watches `Warcraft III\CustomMapData\CWG2`. Usually this is `%USERPROFILE%\Documents\Warcraft III\CustomMapData\CWG2`.
4. If needed, use **Choose save folder...** in the tray menu.

The app polls every five seconds and waits at least five seconds of observed stability before uploading. Internet failures retry automatically. Invalid submissions stop retrying until the saves change or you choose **Upload now**.

### Tray menu

- **Status:** click the top item for the full message and save folder.
- **Last upload:** last confirmed successful submission.
- **Automatic uploads:** uncheck to stop sending new saves automatically. Already queued uploads and retries continue until they finish.
- **Upload now:** send the current saves even with Automatic uploads off. Stability checks and server retry delays still apply.
- **Choose save folder...:** change the watched folder when no upload is in flight.
- **Launch at Windows sign-in:** register or remove a current-user startup entry. No administrator rights needed. Moving the executable requires updating the entry; restart the app at its new location to do so.
- **Open leaderboard**, **Open logs**, and **Open config.json**.
- **Quit:** cancel active network work, stop retries, and exit.

Successes stay quiet. Check the tray status for errors; bounded logs provide diagnostics. Only one instance can run per Windows session.

## Configuration and logs

Application data lives in `%LOCALAPPDATA%\CWG2Uploader\`, not alongside the executable:

- `config.json`: human-editable settings.
- `state.json`: the last successful snapshot fingerprint and upload summary.
- `uploader.log`: automatically rotates at 2 MiB, keeping three previous files. Entries identify their source with `[app]`, `[poller]`, `[uploader]`, or `[main]`.

Example `config.json`:

```json
{
  "save_folder": "%USERPROFILE%\\Documents\\Warcraft III\\CustomMapData\\CWG2",
  "poll_interval_seconds": 5,
  "upload_automatically": true,
  "launch_at_login": false,
  "proxy_url": ""
}
```

Initial discovery honors redirected Documents folders, including OneDrive. Configured `%VARIABLE%` references are expanded at runtime. Restart after manual configuration edits; tray changes are persisted immediately. To disable automatic uploading before a first real launch, create this configuration with `upload_automatically` set to `false`.

Do not delete `state.json` unless you want existing saves submitted again. A corrupted configuration or state file produces an error rather than silently resetting preferences or upload history. Outgoing save contents are not logged. Error messages include a quoted preview of up to 512 response-body bytes, with a truncation marker when needed. Full error response bodies are additionally logged only when smaller than 4 KiB. Newlines and control characters are escaped, and valid UTF-8 characters are not split. A server response could echo sensitive data, so review logs before sharing them.

### Proxy settings

Set `proxy_url` in `config.json` and restart the app:

- `""` (default): use environment proxy settings, otherwise connect directly. Uploads use HTTPS, so `HTTPS_PROXY` is the relevant proxy variable.
- `"http://127.0.0.1:8080"`: HTTP proxy (HTTPS uploads use CONNECT tunneling).
- `"https://proxy.example:8443"`: TLS connection to the proxy.
- `"socks5://127.0.0.1:1080"` or `"socks5h://127.0.0.1:1080"`: SOCKS5 proxy with proxy-side destination resolution.
- `"direct"`: connect directly unless an environment proxy is set for the request's scheme.

Proxy selection is environment-first per request scheme: `HTTPS_PROXY` or `HTTP_PROXY`, then configured `proxy_url`, then direct. Lowercase environment names are also supported. `NO_PROXY` bypasses both environment and configured proxies; localhost and loopback addresses also bypass proxying, following Go's standard behavior. Environment settings are captured when the HTTP client is created.

Optional credentials use `scheme://username:password@host:port`; percent-encode special characters. Credentials are stored as plaintext in `config.json`, so do not share it. Invalid proxy configuration stops startup instead of silently connecting directly. TLS certificate validation remains enabled.

Windows system proxy settings and PAC scripts are not automatically imported. Restart after changing configuration or environment variables; changing sign-in startup behavior does not import a terminal's proxy environment into Windows Explorer.

## Supported save layout

The first version reads only files directly inside the selected folder:

- `HERO_*.pld`
- `ACCOUNT_*.pld`
- `ENTITLEMENTS_*.pld`
- `LEGACY_*.pld`
- `SETTINGS_*.pld`

Matching is case-insensitive. `ACCOUNT_VERIFY_*` is always excluded. At least one hero and exactly one account file are required; folders containing multiple account files are rejected rather than guessing which account to submit. Nested layouts and account selection are not supported yet.

Files must be valid UTF-8. Safety limits are 8 MiB per file and 32 MiB per snapshot. Open handles do not prove completion; the app checks sizes and modification times before and after reads. This is a practical stability heuristic, not a game-level transaction guarantee.

## Build and test

Requires Windows, Go 1.24 or newer, and Windows PowerShell for the built-in folder picker. Prefer a current supported Go toolchain. No C compiler is required for a normal Windows build.

```powershell
.\build.ps1
```

This runs tests and `go vet`, then produces:

- `dist\cwg2-uploader.exe`: tray application, no console window.
- `dist\cwg2-uploader-console.exe`: console-enabled build for diagnostics.

Or run commands individually:

```powershell
go test ./...
go vet ./...
go build -trimpath -ldflags="-H=windowsgui -s -w" -o dist/cwg2-uploader.exe .
```

Inspect one stable snapshot without submitting anything:

```powershell
.\dist\cwg2-uploader-console.exe --dry-run
```

Dry run prints the eligible file count and exits after reading a valid stable snapshot. It does not upload or write successful-upload state; it still creates default configuration and logs if absent. If saves are missing, it keeps waiting; press Ctrl+C to stop. `--console` runs the normal automatic-upload workflow without the tray. Do not use that flag when you only want a dry run.

Automated tests use temporary save fixtures and local HTTP servers, not the real leaderboard. A live upload still needs compatibility verification with explicitly approved saves.

## Distribution

Distribute the tray executable; players do not need Go installed. Builds are currently unsigned, so Windows SmartScreen may display a warning. Keep third-party license notices with releases (see `THIRD_PARTY_NOTICES.md`).

To uninstall, first disable **Launch at Windows sign-in**, quit, and delete the executable. Optionally delete `%LOCALAPPDATA%\CWG2Uploader\`. Removing local data does not remove submissions already held by the leaderboard service.

## Development documentation

- [DESIGN.md](DESIGN.md): behavior and architecture decisions.
- [DOCS.md](DOCS.md): package organization and reference project.

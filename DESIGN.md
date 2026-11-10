# CWG2 Automatic Save Uploader

## Status

Draft design for a Go application that automatically submits Warcraft III CWG2 saves to the [CWG2 leaderboard](https://cwg2-leaderboard.rskiland.workers.dev/#leaderboard).

Confirmed decisions:

- Windows system-tray application distributed to other players.
- Default save location: `%USERPROFILE%\Documents\Warcraft III\CustomMapData\CWG2`, resolved through the Windows Documents known folder to support redirection.
- Automatically upload existing saves on startup, including first launch; no separate first-upload confirmation.
- Human-editable `config.json`, successful-upload state, and automatically rotating logs under `%LOCALAPPDATA%\CWG2Uploader\`.
- Optional launch at Windows sign-in, controlled from the tray menu.

The first implementation reads direct children only and rejects folders with more than one account file. Nested layouts and account selection are deferred rather than guessed. See [README.md](README.md) for operation and [DOCS.md](DOCS.md) for the implemented package layout.

Implementation verification uses temporary fixtures and local HTTP servers. Real leaderboard uploads and interactive tray behavior still require manual acceptance testing.

## Goals

- Detect new or updated local saves and upload them automatically.
- Run unobtrusively on Windows as a single executable.
- Never modify or delete game save files.
- Avoid duplicate submissions and recover from temporary network failures.
- Clearly report pending uploads, successful submissions, and errors.

## Non-goals

- Automating the browser or interacting with Warcraft III itself.
- Creating saves or triggering the game's save command.
- Reimplementing the game's save parser or leaderboard calculations.
- Providing a local hero viewer or leaderboard replacement.

## Existing Website Integration

The website's publicly served frontend currently submits saves directly to:

```text
POST https://cwg2-leaderboard.rskiland.workers.dev/api/upload
Content-Type: application/json
```

Example request structure:

```json
{
  "files": {
    "HERO_example.pld": {
      "text": "<file contents>",
      "mtime": 1730000000000
    },
    "ACCOUNT_example.pld": {
      "text": "<file contents>",
      "mtime": 1730000000000
    }
  }
}
```

The keys are file basenames. `mtime` is the file modification time in Unix milliseconds.

The frontend:

- Reads `.pld` files and excludes `ACCOUNT_VERIFY_*`.
- Submits files with the prefixes `HERO_`, `ACCOUNT_`, `ENTITLEMENTS_`, `LEGACY_`, and `SETTINGS_`.
- Requires at least one hero file and an `ACCOUNT_*.pld` file before submitting.
- Makes the upload request without an explicit login or authorization header.
- Uses response fields including `user_hash`, `player`, `heroes`, `replaced`, and `kept_newer` to report the result.

These observations come from the current frontend, not a documented stable API contract. Server-side limits, validation rules, and compatibility must be verified during implementation. No test uploads have been made.

## User Experience

Provide a Windows system-tray application for other players, with a console mode for development and troubleshooting.

Tray actions:

- View current status, last successful upload, pending changes, and the latest error.
- Pause or resume automatic uploads.
- Upload now.
- Choose or change the save folder.
- Launch at Windows sign-in (checked when enabled).
- Open the leaderboard.
- Open logs.
- Quit.

Optional launch at Windows sign-in should be explicitly enabled by the user. Successful background uploads should generally remain quiet; persistent errors should be visible without repeated notifications.

First-run setup must clearly explain that selected saves will be sent to a third-party service and used for its public leaderboard. Existing saves upload automatically without a separate first-upload confirmation.

### Launch at Sign-in

The checked tray menu action enables or disables a current-user Windows `Run` registry entry. No administrator privileges are required. Quote the executable path correctly, and persist the preference in configuration. If the executable moves, the startup entry must be updated.

## Save Folder Discovery

Try the Windows Documents known folder, including redirected or OneDrive-backed locations, followed by:

```text
Warcraft III\CustomMapData\CWG2
```

The usual unredirected path is `%USERPROFILE%\Documents\Warcraft III\CustomMapData\CWG2`. Resolve the Documents known folder rather than hardcoding a username or assuming that Documents is always beneath `%USERPROFILE%`.

Allow a user-selected folder override and persist it in `config.json`. Support expanding `%USERPROFILE%` and `%LOCALAPPDATA%` in configured paths. Do not assume the repository directory is the save directory.

Missing or inaccessible folders should produce a clear waiting/error status rather than terminate the application.

## Synchronization Workflow

1. Load configuration and the last successfully uploaded snapshot fingerprint.
2. Locate the configured save folder.
3. Scan eligible files every five seconds.
4. When a change is detected, require the eligible filenames, sizes, and modification times to remain unchanged across consecutive scans spanning at least a five-second quiet period.
5. Read a candidate snapshot and recheck the file set and metadata afterward. If they changed during reading, discard it and wait again.
6. Confirm the snapshot contains the required hero and account files.
7. Compute a deterministic fingerprint of filenames and content hashes, excluding modification times.
8. Skip snapshots matching the last successfully uploaded fingerprint.
9. Upload the complete eligible snapshot using one HTTP request.
10. On confirmed success, atomically persist the submitted fingerprint and update status.

Polling is preferred for the first version: it is simple, handles replacement writes, and is fast enough for game saves. A filesystem watcher can be added later if measurements justify it.

Do not use access time to determine stability: reads can affect it, and Windows may defer or disable its updates. Open file handles are not reliable completion signals either: the game may retain a handle after writing or permit concurrent reads while writing. Treat sharing violations and other temporary read failures as reasons to postpone reading and retry.

Settling checks reduce incomplete reads but cannot guarantee that multiple files were written as one game-level transaction. The server remains responsible for validating saves. Inspect representative saves for additional completeness checks before implementation.

### Snapshot Rules

- Match the website's filename filtering and preserve file basenames.
- Exclude `ACCOUNT_VERIFY_*` explicitly.
- Include all eligible files rather than guessing which files the server needs for an update.
- Compute SHA-256 for each file's contents, then hash a deterministic, unambiguously encoded, sorted mapping of filenames to content hashes into one snapshot fingerprint. Exclude modification times so timestamp-only changes do not trigger uploads; retain them for stability checks and the upload payload.
- A hash tree is unnecessary for this small folder. Persisting per-file hashes is only an optional optimization; the last successfully submitted snapshot fingerprint is sufficient for deduplication.
- Skip unreadable or changing snapshots instead of sending partial data.
- Do not follow directory links outside the selected folder.
- Read direct children only in the first version; do not recurse or follow directory links.
- Require exactly one account file. Reject multiple-account folders until explicit account selection/grouping is implemented.
- Apply safety limits of 8 MiB per file and 32 MiB per snapshot; reject invalid UTF-8.

## Upload Scheduling and Recovery

- Allow only one upload in flight.
- Coalesce changes while an upload is pending; prefer the latest stable snapshot rather than retaining a queue of historical snapshots.
- After an upload succeeds, mark only that submitted snapshot as successful. Changes made during the request must still be detected and uploaded later.
- Use request timeouts and exponential backoff with a cap and jitter for network failures and transient server errors.
- Respect `Retry-After` when applicable, including rate limiting.
- Surface non-retryable validation failures and wait for changed saves or a manual retry rather than repeatedly submitting the same rejected snapshot.
- Do not persist success after a failed request or an unrecognized success response.

A timeout can occur after the server has already accepted a submission. Exactly-once delivery is not possible without server-side idempotency support; verify that repeated identical submissions are safe.

On restart, rescan local files and compare against persisted successful state. There is no need to store copies of save contents in a durable queue.

## Components

Keep the implementation small, separating these responsibilities:

- **Application coordinator:** startup, lifecycle, synchronization workflow, and shutdown.
- **Save scanner:** filename filtering, stability checks, snapshot reads, and fingerprinting.
- **Upload client:** request encoding, HTTP calls, response validation, and error classification.
- **Local storage:** configuration and atomically persisted successful state.
- **User interface:** tray controls, folder selection, and status presentation.

Use Go's standard library for HTTP, JSON, hashing, logging, and polling. Select suitable Windows tray and folder-dialog dependencies during implementation.

## Configuration and Local State

Store per-user application data under `%LOCALAPPDATA%\CWG2Uploader\`, not alongside the executable or inside the game save folder. A distributed executable may be installed in an unwritable location such as Program Files. Portable mode can be considered later.

Use a human-editable `config.json`, for example:

```json
{
  "save_folder": "%USERPROFILE%\\Documents\\Warcraft III\\CustomMapData\\CWG2",
  "poll_interval_seconds": 5,
  "upload_automatically": true,
  "launch_at_login": false
}
```

The example path describes the usual location; initial discovery must honor the Windows Documents known folder and persist the discovered path when redirected. Automatic uploads default to enabled; launch at sign-in defaults to disabled.

Atomically persist successful-upload state in `%LOCALAPPDATA%\CWG2Uploader\state.json`, including:

- The last successfully uploaded snapshot fingerprint, scoped to the selected folder and destination.
- The last successful upload time.
- A minimal server-reported result for status display, if useful.

### Automatic Log Rotation

Write `%LOCALAPPDATA%\CWG2Uploader\uploader.log`, capped at 2 MiB, retaining three previous files. Rotation is built into the application and requires no external tool or user maintenance.

Log upload summaries and diagnostic errors, never raw save contents. Save files remain read-only to the application. The tray status is the primary visibility mechanism; logs are for troubleshooting. Keep successful uploads quiet and notify only for actionable, persistent problems.

## Security and Privacy

- Use HTTPS with normal certificate validation.
- Upload only explicitly eligible save files from the selected folder.
- Do not upload unrelated documents or verification files.
- Do not store save contents in logs or additional local copies by default.
- Treat server response text and save filenames as untrusted display data.
- Make clear that this client is not the leaderboard service and cannot control its storage or publication policies.

## Verification Plan

Use local fixtures and a mock HTTP server rather than real leaderboard submissions for automated tests.

Cover filename filtering, modification-time encoding, deterministic fingerprints, missing required files, incomplete writes, read failures, restart deduplication, retry/backoff behavior, response validation, and changes occurring during an upload.

Before release, perform an explicitly approved live submission with real saves to confirm API compatibility. Manually verify Windows folder discovery, tray controls, pause/resume, offline recovery, clean shutdown, and optional startup registration.

## Suggested Delivery Sequence

1. Inspect representative save filenames/layout and resolve recursive scanning and multi-account behavior.
2. Implement and test the scanner, state persistence, and upload client in console mode with dry-run support.
3. Verify one authorized live upload.
4. Add the chosen Windows interface and optional sign-in startup.
5. Package the executable and document setup, privacy implications, and troubleshooting.

## Remaining Questions

1. **Additional folder layouts:** should a future release support recursive discovery? The first version supports direct children only.
2. **Multiple accounts:** how should explicit account selection/grouping work in a future release? The first version rejects multiple-account folders.
3. **Completeness checks:** do representative saves expose reliable structural markers that supplement the quiet-period heuristic?
4. **Service compatibility:** verify the current upload contract with an explicitly approved live submission before broad distribution.

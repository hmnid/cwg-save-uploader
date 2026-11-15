# CWG2 Automatic Save Uploader

## Status

Draft design for a Go application that automatically submits Warcraft III CWG2 saves to the [CWG2 leaderboard](https://cwg2-leaderboard.rskiland.workers.dev/#leaderboard).

Confirmed decisions:

- Windows system-tray application distributed to other players.
- Default save location: `%USERPROFILE%\Documents\Warcraft III\CustomMapData\CWG2`, resolved through the Windows Documents known folder to support redirection.
- Automatically upload existing saves on startup, including first launch; no separate first-upload confirmation.
- Human-editable `config.json`, successful-upload state, and automatically rotating logs under `%LOCALAPPDATA%\CWG2Uploader\`.
- Optional launch at Windows sign-in, controlled from the tray menu.

The first implementation reads direct children only and includes all eligible account files, leaving multiple-account validation to the backend. Nested layouts and account selection are deferred rather than guessed. See [README.md](README.md) for operation and [DOCS.md](DOCS.md) for the implemented package layout.

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

The observed web frontend behaves as follows (the uploader deliberately uses the broader eligibility and priority policy described under Snapshot Rules):

- Reads `.pld` files and excludes `ACCOUNT_VERIFY_*`.
- Submits files with the prefixes `HERO_`, `ACCOUNT_`, `ENTITLEMENTS_`, `LEGACY_`, and `SETTINGS_`.
- Requires at least one hero file and an `ACCOUNT_*.pld` file before submitting.
- Makes the upload request without an explicit login or authorization header.
- Uses response fields including `user_hash`, `player`, `heroes`, `replaced`, and `kept_newer` to report the result.

These observations come from the current frontend, not a documented stable API contract. Server-side limits, validation rules, and compatibility must be verified during implementation. No test uploads have been made.

## User Experience

Provide a Windows system-tray application for other players, plus a separate console executable for development and troubleshooting. Both share application code: the normal build defaults to tray mode and uses the Windows GUI subsystem; the `console` Go build tag selects console mode and the console subsystem. No `--console` flag is needed. Separate binaries avoid console attachment/allocation machinery and give command-line users normal output, redirection, and shell waiting. The console binary uploads according to configuration unless `--dry-run` is supplied.

Tray actions:

- View current status, last successful upload, pending changes, and the latest error.
- Pause or resume automatic uploads.
- Upload now.
- Choose or change the save folder.
- Launch at Windows sign-in (checked when enabled).
- Open the leaderboard.
- Open logs.
- Quit.

The tray owns a generic, non-inheritable Windows Job Object with kill-on-close enabled for subprocesses that must not outlive the uploader. Launch owned children atomically into that job using the Windows 10+ process job-list attribute, so normal tray shutdown, crashes, and forced uploader termination cannot leave them running. The folder picker is currently its only consumer; keep its script, timeout value, and path decoding separate from generic process handling. Browsers and editors opened through the shell remain independent. Preserve whitespace in the actual selected path, removing only an optional leading UTF-8 BOM. Limit tray tooltips by UTF-16 units, reserving the terminator and avoiding split surrogate pairs; menu labels may use rune-based readability limits.

Optional launch at Windows sign-in should be explicitly enabled by the user. Successful background uploads should generally remain quiet; persistent errors should be visible without repeated notifications.

Before the first normal run, clearly explain that selected saves will be sent to a third-party service and used for its public leaderboard. Present this through a dialog in tray mode or stdout in console mode before starting the coordinator. Track successful presentation with the app-managed `upload_notice_shown` configuration field, independently of config creation. Mark it only after presentation and persistence succeed. Dry runs print an appropriately worded notice but leave the normal-run notice pending; earlier startup failures must not consume it. Existing saves upload automatically without a separate first-upload confirmation.

### Launch at Sign-in

The tray directly reads and changes a current-user Windows `Run` registry entry through `StartupEnabled` and `SetStartup`. The registration is the source of truth, not app configuration; no startup app command, rollback protocol, polling, or upload-status publication is needed. No administrator privileges are required. Quote the executable path correctly and check that registration targets the current executable. Launching the app must not reapply registration. If the executable moves, the user can enable the option at its new location. Windows startup policy may still prevent registered apps from running. Ignore legacy `launch_at_login` JSON fields.

## Save Folder Discovery

Try the Windows Documents known folder, including redirected or OneDrive-backed locations, followed by:

```text
Warcraft III\CustomMapData\CWG2
```

The usual unredirected path is `%USERPROFILE%\Documents\Warcraft III\CustomMapData\CWG2`. Resolve the Documents known folder rather than hardcoding a username or assuming that Documents is always beneath `%USERPROFILE%`.

Resolve the Documents default lazily, only for a missing configuration or an omitted `save_folder`; a complete configured override must not depend on Documents discovery succeeding. An explicitly empty or null folder remains invalid.

Allow a user-selected folder override and persist it in `config.json`. Support expanding `%USERPROFILE%` and `%LOCALAPPDATA%` in configured paths. Do not assume the repository directory is the save directory.

Missing or inaccessible folders should produce a clear waiting/error status rather than terminate the application.

## Synchronization Workflow

1. Load configuration and the last successfully uploaded snapshot fingerprint.
2. Locate the configured save folder. Seed the poller's baseline from the last successful fingerprint only when the save folder and destination match; use an empty baseline otherwise (and for dry runs).
3. With Automatic uploads enabled, scan eligible files every five seconds. When disabled, scan only to fulfill an explicit Upload now request.
4. Inventory the bounded directory and check required hero/account names and essential file counts. For every eligible file, track a candidate content hash and quiet-period start plus its last settled text and mtime. Matching observations spanning five seconds promote a candidate. Changed or unreadable candidates never replace settled contents early. Snapshot versions may have settled on different scans.
5. Require a cached settled version for every current essential filename; until each file has settled at least once, produce no snapshot. Include every essential version first, retaining previous settled data while replacements settle. Supplementary settled versions use remaining capacity; files without an available settled version may be omitted. All reads check identity, size, and modification time. There is no folder-wide consistency check or simultaneous quiet-period requirement.
6. Keep the complete settled essential set and admitted supplementary versions. Bound the retained payload cache to the same 32 MiB/1,024-version budgets as snapshots; only hashes/times, not candidate payloads, remain while versions settle. Rebuilding can temporarily retain old/new bounded caches. Cache eviction and version updates must not mutate previously returned snapshots. Report save warnings for candidate failures/cached fallback and optional omissions.
7. Compute a deterministic fingerprint of filenames and content hashes, excluding modification times.
8. The folder poller returns a snapshot when its fingerprint differs from the last emitted snapshot, or when Upload now explicitly requests it. Record the emitted fingerprint when returning the snapshot; the coordinator forwards it directly to the uploader.
9. The coordinator forwards events to the uploader, which retains only the latest pending snapshot. Folder scanning continues during active uploads.
10. The uploader trusts each emitted snapshot, handles retries/rejections independently of further folder events, and uploads the complete snapshot using one HTTP request. It performs no fingerprint deduplication of its own.
11. The uploader emits a typed success event. The coordinator updates upload history, atomically persists only the submitted snapshot's fingerprint, and refreshes rendered status.

Polling is preferred for the first version: it is simple, handles replacement writes, and is fast enough for game saves. A filesystem watcher can be added later if measurements justify it.

Do not use access time to determine stability: reads can affect it, and Windows may defer or disable its updates. Open file handles are not reliable completion signals either: the game may retain a handle after writing or permit concurrent reads while writing. Treat sharing violations and other temporary read failures as reasons to postpone reading and retry.

Settling checks reduce incomplete reads but cannot guarantee that multiple files were written as one game-level transaction. The server remains responsible for validating saves. Inspect representative saves for additional completeness checks before implementation.

### Snapshot Rules

- Preserve file basenames. All regular `.pld` files are eligible except `PRIVATE_*`, matched case-insensitively. Include `ACCOUNT_VERIFY_*` and unknown categories when admissible.
- Essential saves are all `HERO_*`, `ACCOUNT_*` (including `ACCOUNT_VERIFY_*`), `ENTITLEMENTS_*`, `SETTINGS_*`, and `LEGACY_*` files. Require at least one hero and one non-verification account, and never truncate the essential set. Eligible files with unknown category prefixes are supplementary.
- Use one settlement/cache mechanism for all eligible files. Keep a candidate hash/time and the last settled text/mtime for each filename; new versions must settle before replacing cached data. Essential files with no settled version block the snapshot; supplementary files with no available settled version are omitted. Prioritize essential cache/snapshot admission. Observe supplementary candidates even while the initial essential gate is closed. Prune deleted names and their versions; new/reappearing essential names reopen the gate. Startup/folder selection creates a fresh cache.
- Compute SHA-256 for each file's contents, then hash a deterministic, unambiguously encoded, sorted mapping of filenames to content hashes into one snapshot fingerprint. Exclude modification times and warning messages from fingerprints; retain mtimes for stability checks and the upload payload. Supplementary omission/recovery changes the selected file set and can therefore trigger another emission without essential content changes.
- A hash tree is unnecessary for this small folder. The poller's last-emitted fingerprint is in-memory event deduplication state. Persist only successful-upload history; the coordinator uses a matching successful fingerprint as the poller's initial baseline after restart. Never persist an emitted event as a success.
- Never omit an essential filename: use its cached settled version when a candidate is changing or fails, or reject the snapshot if no cache exists. Apply the same fallback to supplementary files when capacity permits. Expose filename-specific warnings and a general save-warning count; deduplicate repeated diagnostics in poller logs.
- Do not follow directory links outside the selected folder.
- Read direct children only in the first version; do not recurse or follow directory links.
- Require at least one account file. Include all eligible account files and leave multiple-account validation to the backend.
- Apply safety limits of 8 MiB per version, 32 MiB per snapshot/cache, 1,024 cached/uploaded versions, and 4,096 directory entries. Snapshot byte budgets apply to settled selected versions, not unsettled candidate totals. Oversized/invalid candidates may retain a valid cached version; without one, an essential file blocks and a supplementary file is omitted. A newly settled essential set exceeding capacity blocks rather than producing a partial snapshot. Admit essential shrinks before growth, then supplementary versions in deterministic filename order; evict optional payloads first. Bound supplementary candidate reads by remaining bytes. Directory enumeration/entry-limit failures remain fatal.
- Enumerate directories in batches of 128 and sort collected metadata before stability comparisons. Check cancellation during directory enumeration, between files, at file-read entry and after reading contents, and during fingerprinting; propagate verification errors rather than treating them as settling. Scanning remains synchronous, and cancellation cannot interrupt an already-blocked filesystem call.

## Upload Scheduling and Recovery

- Allow only one upload in flight.
- Coalesce undelivered folder events and pending uploads to the latest stable snapshot rather than retaining a queue of historical snapshots. Folder updates contain save-folder identity and snapshot data; the coordinator adds a monotonic work ID when forwarding accepted work.
- Upload now calls the poller's `requestEmission()` directly before an immediate tick. This preserves an already-latched `forceNextEmission`, treating the next stable snapshot as new even if unchanged. Clear the latch only after emission; replacing the poller discards its un-emitted force request but never changes accepted delivery work. The poller scans when automatic polling is enabled or a forced emission is pending. User-intent flags do not travel in events or results.
- Automatic uploads controls creation of new automatic work, not delivery. Turning it off stops automatic folder polling; already accepted snapshots, active requests, and retries continue. Upload now remains available without special delivery permissions. Quit cancels active network work and stops retries. Subscribe to both `os.Interrupt` and Windows' `syscall.SIGTERM` notification for console-close/logoff/shutdown events, allowing best-effort cancellation and successful-event persistence before the operating system's termination deadline.
- Retain pending data after transient failures and retry it even if no new folder event arrives. Prefer newer pending data over the failed snapshot when available.
- Before each attempt, the single uploader worker takes the latest snapshot from a mutex-protected pending slot, preferring it over the failed snapshot after backoff. Do not wait for a future emission or consult the poller's force flag. Preserve Retry-After using a private retry timer independent of polling ticks; new snapshots never interrupt or postpone that wait.
- After an upload succeeds, mark only that submitted snapshot as successful. Changes made during the request must still be detected and uploaded later.
- If successful-upload history cannot be written, retry the latest in-memory history on each tick before polling, independently of Automatic uploads and scan results. Reuse the persistence error as the pending-write indicator, clear it after recovery, and log only new or changed failures. Never retry HTTP merely to recover a history write.
- Use request timeouts and exponential backoff with a cap and jitter for network failures and transient server errors.
- Respect `Retry-After` when applicable, including rate limiting.
- Surface non-retryable validation failures and wait for changed saves or a manual retry rather than repeatedly submitting the same rejected snapshot. Emit a snapshot-specific rejection event only when no newer event is pending; always log the failed request. The renderer retains rejection information, so the uploader needs no separate rejection object. New snapshots do not reset delivery backoff.
- Do not persist success after a failed request or an unrecognized success response.

A timeout can occur after the server has already accepted a submission. Exactly-once delivery is not possible without server-side idempotency support; verify that repeated identical submissions are safe.

On restart, the coordinator seeds the poller from persisted successful history scoped to the same save folder and destination. Unchanged successful saves therefore produce no event. Changes emitted before shutdown but never successfully uploaded still differ from that baseline and are emitted again. A forced emission bypasses the baseline; the uploader always trusts the resulting snapshot. There is no need to store copies of save contents in a durable queue.

## Components

Keep the implementation small, separating these responsibilities:

- **Application coordinator:** startup baseline selection, forwarding snapshots, consuming the uploader's lifecycle-event stream, successful-upload persistence, commands, status, and shutdown. No worker-result handling or upload dispatch.
- **Save scanner:** filename filtering, stability checks, snapshot reads, and fingerprinting.
- **Folder poller:** scan scheduling and event deduplication against the last emitted snapshot; no knowledge of successful uploads or retries.
- **Snapshot uploader:** a concurrency-safe queue API with a mutex-protected latest-pending snapshot and a buffered wake-up signal. One worker performs HTTP synchronously, owns retries, and sends lifecycle events directly. No per-request goroutine, private result channel, lifecycle outbox, or separate delivery-state object. The mutex is never held during HTTP, retry waits, or event sends. Retry timers are armed only on retryable failures. No persistence or presentation logic.
- **Status renderer:** pure composition of detection, delivery, and successful-history facts into UI messages. No status-priority ordering substitutes for representing independent facts.
- **Upload client:** request encoding, HTTP calls, response validation, and error classification.
- **Local storage:** configuration and atomically persisted successful state.
- **User interface:** tray controls, folder selection, and status presentation.

Component observations and lifecycle events contain data, not UI text. Upload lifecycle events are separate value types implementing a private marker interface, not an enum-tagged struct with unrelated optional fields. They carry source identity and event-specific metadata, never file contents or full snapshots; retry events identify the failed attempt's folder and work ID while describing shared delivery backoff. Only success events carry a fingerprint, for persistence. Every event exposes its source folder and work ID. The coordinator records pending acceptance synchronously after queueing succeeds; lifecycle events acknowledge worker consumption and update a metadata-only delivery projection. Uploads report started and dry runs report previewed, without a separate queue event. Work IDs distinguish repeated emissions of identical snapshots, so delayed older events cannot clear newer pending work. The projection owns no delivery scheduling. Detection reports unchanged scans as "No new changes", not upload completion; activity, history, and errors remain independently visible. The renderer uses type switches and success persistence accepts the concrete success type. Folder observations return to the coordinator. The uploader accepts immutable snapshots through a mutex-protected pending slot and exposes one reliable lifecycle-event channel, without callbacks or raw worker results. Direct event sends may pause the worker, but queueing never waits for event delivery, preventing blocking input/event cycles without an outbox. Successes are never dropped or coalesced; only final UI status delivery keeps the latest message. Polling never queries uploader internals or suppresses scan errors during an upload. Operational logging stays within each component and is independent of rendering.

Changing the watched folder saves configuration, replaces the poller, and clears selected-folder rejection/preview feedback; a failed write leaves the existing selection, presentation, and delivery untouched. Retain pending and active/backoff activity in the delivery projection, labeling other-folder work as "previous folder" because it still occupies the shared worker. Filter terminal rejection/preview feedback by source folder, but always record and persist actual successes as history. The uploader has no reset/configuration protocol. Accepted jobs retain their source identity and continue, retry backoff is not cleared, and new-source snapshots enter the same pending-data coalescing policy. Completion history is scoped to the snapshot's actual source, not the current configured folder.

Shutdown cancels HTTP work or the retry wait, drains all lifecycle events (including actual successes), then waits for the single worker to exit. HTTP runs synchronously in that worker, so it cannot exit before the client returns and needs no separate result channel or WaitGroup.

Use Go's standard library for HTTP, JSON, hashing, logging, and polling. Select suitable Windows tray and folder-dialog dependencies during implementation.

## Configuration and Local State

Store per-user application data under `%LOCALAPPDATA%\CWG2Uploader\`, not alongside the executable or inside the game save folder. A distributed executable may be installed in an unwritable location such as Program Files. Portable mode can be considered later.

Use a human-editable `config.json`, for example:

```json
{
  "save_folder": "%USERPROFILE%\\Documents\\Warcraft III\\CustomMapData\\CWG2",
  "poll_interval_seconds": 5,
  "upload_automatically": true,
  "proxy_url": ""
}
```

The example path describes the usual location; initial discovery must honor the Windows Documents known folder and persist the discovered path when redirected. Automatic uploads default to enabled. Launch at sign-in is read from Windows and is never created automatically.

Share configuration validation between loading and saving, including first-run defaults. Reject null configuration documents and null values for known settings; omitted settings retain their defaults. The expanded save folder must be absolute, and the polling interval must be between 1 and 3600 seconds. Preserve environment-variable references in stored settings. Runtime commands edit a config value copy and replace the live config only after validation and persistence succeed.

Atomically persist successful-upload state in `%LOCALAPPDATA%\CWG2Uploader\state.json`, including:

- The last successfully uploaded snapshot fingerprint, scoped to the selected folder and destination.
- The last successful upload time.
- The server-reported player and positive hero count for status display.

Validate history before both loading and saving: require a nonempty save folder, destination, fingerprint, and player, a nonzero upload timestamp, and at least one hero. A missing history file means no previous upload; an invalid existing configuration or history file stops startup without overwriting it.

### Proxy Configuration

`proxy_url` accepts an HTTP, HTTPS, SOCKS5, or SOCKS5H proxy URL, optionally with credentials. Per-request-scheme precedence is environment (`HTTP_PROXY` or `HTTPS_PROXY`, also supporting lowercase names), then configured URL, then direct. Empty or `direct` disables only the configured fallback, not environment proxies. `NO_PROXY` applies to both sources; localhost and loopback addresses are also bypassed. Capture settings at client construction using `golang.org/x/net/http/httpproxy` to reuse Go's matching rules without process-global environment caching. Proxy settings apply after restart. Invalid configured URLs fail startup, without a silent direct fallback.

Clone the default HTTP transport per client, preserve certificate verification, request timeout, and redirect rejection, and close idle connections during shutdown. HTTPS destinations use CONNECT through HTTP proxies. Windows system proxy/PAC discovery is not included. Proxy credentials are plaintext in configuration and must not be included in configuration validation errors.

### Automatic Log Rotation

Write `%LOCALAPPDATA%\CWG2Uploader\uploader.log`, capped at 2 MiB, retaining three previous files. Rotation is built into the application and requires no external tool or user maintenance. Stage the closed current log under a unique sibling filename before aging backups, so a blocked current-log rename leaves backups untouched on every retry. If backup rotation fails, restore the staged current log; if restoration also fails, retain the staged file and include its path in the returned error. This is not a transaction across all backup files.

Truncate individual disk-log entries to at most 256 KiB, including an explicit marker and without splitting valid UTF-8 characters. Do not modify the caller's buffer or truncate console output; successful writes report the entire input consumed.

Use named loggers for the coordinator (`[app]`), poller (`[poller]`), and uploader (`[uploader]`), without coupling tray-only startup registration to uploader status. Log upload summaries and diagnostic errors, not outgoing save contents. Include a quoted preview of up to 512 response-body bytes in error messages, preserving valid UTF-8 characters and marking truncation. Escape newlines and control characters. Additionally log full HTTP error response bodies only when strictly smaller than 4 KiB; larger bodies appear only as bounded previews. Save files remain read-only to the application. The tray status is the primary visibility mechanism; logs are for troubleshooting. Keep successful uploads quiet and notify only for actionable, persistent problems.

## Security and Privacy

- Use HTTPS with normal certificate validation.
- Upload only explicitly eligible save files from the selected folder.
- Do not upload unrelated non-PLD documents or `PRIVATE_*` saves. Verification `.pld` files are essential data, but do not satisfy the required non-verification account check.
- Do not log outgoing save contents or create additional local copies by default. Small logged error responses may echo sensitive data; review logs before sharing them.
- Treat server response text and save filenames as untrusted display data.
- Make clear that this client is not the leaderboard service and cannot control its storage or publication policies.

## Verification Plan

Use local fixtures and a mock HTTP server rather than real leaderboard submissions for automated tests.

Cover filename filtering, modification-time encoding, deterministic fingerprints, missing required files, incomplete writes, read failures, restart deduplication, retry/backoff behavior, response validation, and changes occurring during an upload.

Before release, perform an explicitly approved live submission with real saves to confirm API compatibility. Manually verify Windows folder discovery, tray controls, toggling Automatic uploads without interrupting accepted work, offline recovery, clean shutdown, and optional startup registration.

## Suggested Delivery Sequence

1. Inspect representative save filenames/layout and resolve recursive scanning and multi-account behavior.
2. Implement and test the scanner, state persistence, and upload client in console mode with dry-run support.
3. Verify one authorized live upload.
4. Add the chosen Windows interface and optional sign-in startup.
5. Package the executable and document setup, privacy implications, and troubleshooting.

## Remaining Questions

1. **Additional folder layouts:** should a future release support recursive discovery? The first version supports direct children only.
2. **Multiple accounts:** how should explicit account selection/grouping work in a future release? The first version submits all eligible account files for backend validation.
3. **Completeness checks:** do representative saves expose reliable structural markers that supplement the quiet-period heuristic?
4. **Service compatibility:** verify the current upload contract with an explicitly approved live submission before broad distribution.

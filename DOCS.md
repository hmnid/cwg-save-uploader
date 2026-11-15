# Code Organization

This is the implementation layout for the CWG2 uploader. Product behavior and technical decisions are documented in [DESIGN.md](DESIGN.md).

## Layout

Use one Go module with a small number of packages:

```text
go.mod
go.sum
main.go                # Windows entry point
mode_tray_windows.go   # Default tray mode (without console build tag)
mode_console_windows.go # Default console mode (console build tag)
build.ps1              # Test, vet, and build release executables
README.md
DESIGN.md
DOCS.md
scripts/
  notices.ps1          # Generate dependency license notices

internal/
  app/
    app.go             # Initialization, event loop, and per-run state
    poll.go            # Stable folder events, last-emitted deduplication
    upload.go          # Mutex-protected pending snapshot and single HTTP worker
    events.go          # Typed folder observations and upload transitions
    render.go          # Pure, centralized UI message rendering
    logging.go         # Named component loggers
    commands.go        # Tray commands and configuration changes
    status.go          # Nonblocking status publishing to the tray

  saves/
    scan.go            # Discover eligible files, read stable snapshots, hash contents

  leaderboard/
    client.go          # Upload API, response parsing, error classification
    transport.go       # HTTP client construction and proxy configuration

  storage/
    config.go          # config.json defaults, shared validation, loading, saving
    state.go           # Atomically persist successful-upload state
    logging.go         # Built-in log rotation

  windows/
    tray_windows.go    # Tray menu, status, actions, generated icon
    folders_windows.go # Known folders, folder picker, shell actions, instance lock
    subprocess_windows.go # Owned subprocess Job Object, launch, output, timeout
    startup_windows.go # Current-user sign-in startup registration
```

Tests live alongside implementations as `*_test.go`. Place sample saves in the relevant package's `testdata/` directory; use synthetic or sanitized fixtures, not personal save data.

## Responsibilities

- **`main.go`:** wire dependencies and start the application; keep application logic elsewhere. The `console` build tag selects console mode by default; without it the executable defaults to tray mode. `--dry-run` requests local inspection in either build; there is no `--console` flag. Present the upload disclosure before starting the coordinator, recording `Config.UploadNoticeShown` only after a normal-run presentation and config save succeed. Dry runs leave it pending. Listen for both interrupt and Windows SIGTERM notifications, then cancel and drain the coordinator before closing resources; Windows close/logoff/shutdown cleanup remains subject to OS deadlines.
- **`app`:** own the synchronization workflow, lifecycle, and retry scheduling. Permit only one upload at a time.
- **`saves`:** observe every eligible file's candidate contents, cache last-settled versions, require a settled version for every essential filename, prioritize essentials within bounded cache/snapshot budgets, and fingerprint the selected settled versions. All non-private `.pld` categories are eligible, including account verification. No HTTP or tray dependencies.
- **`leaderboard`:** implement the upload protocol and classify errors. Do not read local files or retry indefinitely; scheduling belongs to `app`.
- **`storage`:** manage application configuration, successful-upload state, and rotating logs. `UploadState`, loaded/saved via `LoadUploadState` and `SaveUploadState`, is exposed as `App.LastUpload`. It records the uploaded `SaveFolder` as `save_folder` in `state.json`. Folder events carry `saveFolder`; no compatibility alias for the old `folder` key is provided. Never modify game saves.
- **`windows`:** implement OS integration without making synchronization decisions.

The `leaderboard` package owns the JSON wire types `UploadRequest` and `UploadFile`. The `app` package converts local `saves.Snapshot` values into upload requests, stripping one leading UTF-8 BOM to match the website uploader's text decoding without mutating the snapshot. The scanner preserves original UTF-8 text, including BOMs, and fingerprints that original text. Snapshot fingerprints stay local; scanner types have no protocol JSON tags. Neither `leaderboard` nor `saves` imports the other.

## Snapshot Selection

`Scanner` treats `HERO_*`, `ACCOUNT_*` (including `ACCOUNT_VERIFY_*`), `ENTITLEMENTS_*`, `SETTINGS_*`, and `LEGACY_*` as essential; eligible unknown categories are supplementary. Required-name validation still needs a hero and a non-verification account. The same per-file settlement mechanism applies to essential and supplementary files. `fileSettlement` keeps the candidate content hash, observation time/validity, and an optional last-settled `File` containing text and mtime. Matching observations spanning the quiet period promote the current version; changed or failed candidates leave the previous settled payload intact. Reading a known settled content version refreshes its mtime without another wait. No snapshot is produced until every current essential filename has a cached settled version. Their stable intervals need not overlap. Supplementary files without a cached version are omitted, never used unsettled.

`scanResult` builds a fresh selected cache and snapshot. Essential names/counts are checked before reading; their existing versions reserve capacity before updates, with potential shrinks considered before growth. Byte limits apply to selected settled versions, so oversized/unsettled on-disk candidates do not invalidate a fitting cached snapshot. A newly settled essential set exceeding 32 MiB is rejected without returning partial essential data. Supplementary versions use deterministic filename order and remaining capacity, falling back to a previous settled version when it fits or dropping optional cache payloads when necessary. Supplementary reads have a remaining-byte budget to avoid loading unselectable bulk candidates. Candidate metadata is bounded by the 4,096-entry directory limit; the retained payload cache is bounded by 32 MiB/1,024 versions. During rebuilding, old and new bounded caches may coexist. Snapshot maps copy immutable file values/strings, so cache updates or eviction cannot mutate accepted work.

Observations and cached versions are pruned for names absent from a successful inventory. New/reappearing essential files must obtain a settled version; a fresh scanner after restart/folder selection has no cached payloads. Directory enumeration/entry-limit failures remain fatal. Individual reads verify identity, size, and mtime; failed essential candidates may use cached versions with warnings, but a failed essential file without a cache prevents a snapshot. There is no folder-wide atomicity claim. Reads remain synchronous and cooperatively cancellable, not interruptible filesystem calls.

`Snapshot.Warnings` are not part of the fingerprint or HTTP request. The poller logs new/changed save diagnostics before event deduplication and exposes warnings even on an unchanged scan. Warnings describe either omitted supplementary files or reuse of cached settled data after a candidate failure. The renderer adds a general save-warning count without hiding delivery status. Omission or recovery can change the selected filenames and hence the fingerprint, even with unchanged essential contents. No snapshot or file maps are added to upload lifecycle events.

## Coordination

`App.Run` handles polling ticks, uploader lifecycle events, and user commands. `runState.tick` records a typed folder observation and forwards any returned immutable snapshot directly to the uploader. The upload command calls `poller.requestEmission()` directly, preserving the poller's latch across settling ticks. It never drives result handling, dispatch, or retry scheduling. `folderPoller.poll` scans the save folder and returns an observation carrying an optional changed snapshot in its `update` field. An unexported `runState` coordinates a `folderPoller` and a `snapshotUploader`:

```text
save scanner -> folderPoller -> coordinator -> mutex-protected pending snapshot
                                                | buffered wake-up signal
                                                v
                                          single HTTP worker
                                                |
coordinator <- reliable lifecycle-event channel -+
     |
     +-> upload history + renderer -> latest-status UI channel
```

The poller compares stable snapshots against its in-memory `lastEmittedFingerprint`. The coordinator seeds that initial baseline from `App.LastUpload` only when the save folder and destination match (excluding dry runs). Snapshots return synchronously to the coordinator without a poller-owned channel. The uploader shares one latest-pending snapshot under a mutex. One worker owns synchronous HTTP calls, the failed-snapshot fallback, retry timing, and the failure counter. Its handle receives a lifetime context, upload client, dry-run setting, and named logger; it has no `App` reference, persistence access, or callback. `queue(ctx, snapshot)` replaces the pending snapshot under a short mutex lock and sends a nonblocking wake-up signal. It is safe from any goroutine and never waits for HTTP, retries, or lifecycle-event delivery. A successful return means acceptance, not upload completion; pending work may be superseded by newer snapshots. There are no command kinds, reply messages, or reset protocol. Snapshots must remain immutable after handoff. The coordinator consumes every successful-upload event, including successes from a previously selected folder, updates `App.LastUpload`, and persists history. If that write fails, `view.persistenceError` also marks history as needing another write: every tick retries the latest `App.LastUpload` before polling, even with Automatic uploads off or a scan failure. Recovery clears the warning and refreshes status; identical consecutive errors are not logged repeatedly. This retries bookkeeping, never HTTP, and needs no extra flag or timer. A transient failure retains pending data and retries without requiring another folder event. Newer events supersede unsent data, including failed data awaiting retry. The uploader trusts every event; it does not suppress snapshots based on previous success or rejection. A permanent failure is always logged and emits a snapshot-specific rejection event only if no newer event is pending. There is no stored rejection object in the uploader: the renderer retains the lifecycle event for display. New snapshots do not clear shared delivery backoff.

The coordinator loop exclusively owns application, poller, and view state. The uploader's single worker takes the latest pending snapshot, calls the HTTP client synchronously, reports the outcome, and waits for backoff when needed. Only the pending slot is shared with queue callers; its mutex is never held during HTTP, event sends, or retry waits. There is no per-request goroutine, private result channel, or separate uploader-owned delivery-state object. After reporting a result, the worker takes the latest eligible pending snapshot without coordinator intervention. Scanning continues while uploads are active. Command handlers trigger immediate checks for upload, Automatic uploads changes, and folder changes. Windows startup registration belongs entirely to the tray and platform integration: it is read from Windows, changed directly, and never sent through app commands, configuration, upload status, or polling. Upload now calls `poller.requestEmission()` before the immediate tick. The poller clears its latch only after emitting a stable snapshot, bypassing event deduplication even for unchanged files. Selecting another folder replaces only the poller, discarding its un-emitted request. The poller receives the Automatic uploads setting and owns the decision to scan: automatic polling is disabled when the setting is off, but a forced emission continues through settling. Folder updates carry `saveFolder` and `snapshot`; the coordinator adds a monotonic work ID before forwarding. Neither folder updates nor upload lifecycle events carry a force/manual flag. Once emitted, work is accepted for delivery regardless of the setting. Queued uploads, active requests, and retries continue independently. No one-off permission state is needed; Quit cancels delivery.

On restart, matching successful history supplies the poller's baseline. This avoids re-emitting unchanged successful data while allowing emitted-but-undelivered changes to be emitted again. A forced emission is always actionable, even when its fingerprint matches previous upload history.

Before each attempt, the worker takes the latest pending snapshot, preferring it over failed data retained for retry. With no work, it waits for a buffered wake-up signal or cancellation; it never polls the save folder. A retry timer is armed only on a retryable failure, before reporting the retry event, so a busy event consumer does not shift the deadline. Incoming snapshots replace pending work without interrupting or postponing backoff. Retry timing operates independently of application polling ticks and Automatic uploads.

### Component Events and Presentation

`events.go` defines folder observations and upload lifecycle events without user-facing text. Upload events implement a private `uploadEvent` interface through a marker method and common `sourceFolder` and `workID` accessors, with separate value types: `uploadStarted`, `uploadRetryScheduled`, `uploadRejected`, `uploadSucceeded`, and `uploadPreviewed`. Each type carries only its own metadata, never a snapshot or file map. Events include the work ID and source folder; successes add the fingerprint, server result, and completion time, previews add the file count, and rejections add the error. Retry scheduling carries the failed attempt's identity and the retry deadline. This identity supports presentation filtering; it does not reserve the next attempt, because newer pending work can replace failed data while backoff remains shared. Consumers use type assertions or switches; `recordUpload` accepts only `uploadSucceeded`. The initial delivery projection has no pending or active work. Go checks interface implementation but does not enforce exhaustive type switches.

Folder observations return directly to the coordinator. The uploader has exactly one outbound lifecycle-event stream, with no callbacks, raw result channel, or outbox. The worker sends events directly and may pause until the coordinator receives them. Queueing snapshots needs only the pending-slot mutex, so it remains available during event backpressure and cannot form an input/event blocking cycle. Success events are never coalesced or dropped; the separate UI status channel may still keep only its latest message. There is no general-purpose event bus.

`delivery.go` projects accepted work and lifecycle transitions into latest-pending identity, active/backoff activity, and selected-folder rejection/preview feedback. The coordinator records pending acceptance immediately after `queue` succeeds, without waiting for worker events. A lifecycle event acknowledges that the worker took that emission; uploads report started and dry runs report previewed, with no separate queue event. Work IDs distinguish repeated emissions of identical snapshots: delayed events for older work cannot clear the latest pending emission. This projection owns no scheduling or save contents.

`render.go` composes independent detection, delivery, and successful-history facts, plus command and persistence errors. History is passed directly from `App.LastUpload`, not duplicated in the view. An unchanged scan means only "No new changes", never "Up to date". Paused or settling detection remains visible alongside accepted delivery and historical success. Pending work remains visible during another folder's request or retry; old-folder activity is labeled "previous folder" rather than hidden. Polling never inspects uploader state, and the uploader never calls a renderer or changes `App.LastUpload`. Scan and persistence errors remain visible during active delivery.

Operational logging is independent of UI rendering. Components use `[poller]`, `[uploader]`, and `[app]` logger names. Per-work coordinator and uploader messages include `work_id=N` context; retries retain the same session-local ID, while each newly accepted emission gets a new ID. Child loggers route writes through the shared base logger, preserving serialization even when the underlying writer is not concurrency-safe. Rendering/status delivery does not duplicate those logs.

Configuration loading and saving both call `Config.Validate`, including on first-run defaults. `LoadConfig` takes a default-folder resolver and invokes it only for absent configuration or an omitted save-folder setting, never for a complete override or an explicitly invalid folder. Validation checks polling bounds and that the expanded save folder is absolute, while preserving environment-variable references in stored settings. Command handlers edit a local config copy, validate and save it, and only then replace `App.Config`; invalid candidates or failed writes never reach the live config. No per-field property setters are needed.

Choosing another save folder writes configuration, replaces the poller, and clears selected-folder rejection/preview feedback. Pending and active/backoff work remain represented because accepted delivery continues. Later terminal feedback is shown only when its source folder matches the current expanded selection, using Windows case-insensitive comparison; old-folder successes are always recorded and persisted as history. Old-folder events do not clear current command errors. A failed config write leaves existing polling, presentation, and delivery unchanged. The uploader is neither reset nor notified about the selection: active and pending jobs continue, backoff remains in force, and new snapshots replace pending data through the existing coalescing policy. Source-folder identity travels with each immutable snapshot and its completion event, so an old job never gets recorded as originating from the newly selected folder.

Shutdown cancels the uploader's HTTP call or retry wait and drains lifecycle events through channel closure before waiting for the single worker. The worker cannot exit before its synchronous HTTP call returns, so no private result channel or worker `WaitGroup` is needed. A request that actually succeeded is still reported and persisted during shutdown. New queue calls fail once stopping begins.

The tray sends commands such as toggling Automatic uploads and Upload now to the application coordinator. Its sign-in checkbox independently queries `StartupEnabled` and invokes `SetStartup`; startup changes only update that checkbox, and errors are shown directly. The registration must target the current executable. Launching the app does not recreate or overwrite registration, and legacy `launch_at_login` JSON fields are ignored. Windows policy can independently suppress registered startup entries. The coordinator publishes status updates back to the tray through a thread-safe boundary. Background workers must not directly modify UI state; UI updates must follow the tray library's threading requirements.

The tray owns a generic `subprocessJob`: a non-inheritable, kill-on-close Job Object for children that must not outlive the uploader. Native process creation uses `PROC_THREAD_ATTRIBUTE_JOB_LIST` to assign membership atomically on Windows 10+, with an explicit inherited-handle list limited to child standard I/O. Closing the tray closes its job; forced uploader termination also closes the last job handle and kills owned children. A short mutex protects process creation against concurrent job closure, but is not held while waiting for a child. Output is drained concurrently with a caller-supplied process wait/timeout. The folder picker is currently the only consumer; its PowerShell script, five-minute timeout, and BOM-only path decoding belong to `folders_windows.go`. Whitespace in selected paths is preserved. Browsers and editors opened with `ShellExecute` are independent and are not placed in the job. Tooltips fit within 127 UTF-16 units plus the Windows terminator, while menu-title limits remain rune-based.

Keep core save scanning and HTTP behavior independent of Windows UI code so they can be tested without launching the tray. Windows-specific files use `_windows.go` filenames; `main.go` has a Windows build constraint. Run whole-project tests on Windows.

## Implementation Style

- Avoid a generic event bus, dependency-injection framework, or interfaces for every type.
- Introduce narrow interfaces where they provide a concrete testing boundary, such as substituting a fake upload client.
- Put each file's main workflow first, followed by its direct helpers and lower-level utilities.
- Keep this small structure and split packages only when real complexity justifies it.
- Run `go test ./...` and `go vet ./...`; additionally use `go test -race ./internal/app` when a CGO-capable C compiler is installed. Ordinary Windows builds do not require that compiler.
- The upload-client interface is the network test seam. Public uploader tests use queue calls and lifecycle events to cover concurrent callers, queueing under event backpressure, latest-pending coalescing, real retry timers, cancellation, and reliable success events during shutdown. Pure retry-policy tests cover classification, jitter ranges, the backoff cap, and Retry-After. Lifecycle tests cover superseded rejection events without depending on internal state objects. Application tests cover persistence, polling during uploads, config-write failures, folder changes during active delivery, origin metadata, retained backoff across source changes, and explicit uploads with Automatic uploads off. Renderer tests cover composed detection, delivery, history, and errors. Sequence tests cover pending new-folder work during old-folder backoff, delayed lifecycle events for identical emissions, and pausing or settling after success.

## Reference Project

[restic/restic](https://github.com/restic/restic) is a real Go application illustrating a similar responsibility-based organization:

- [`cmd/restic/`](https://github.com/restic/restic/tree/master/cmd/restic): executable entry point and commands.
- [`internal/`](https://github.com/restic/restic/tree/master/internal): implementation packages separated by responsibility.
- Tests alongside their implementations.

Restic is much larger and is not a tray application. It is a structural reference, not an exact template: the uploader layout above is tailored to this application rather than copied from restic.

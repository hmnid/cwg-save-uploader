# Code Organization

This is the implementation layout for the CWG2 uploader. Product behavior and technical decisions are documented in [DESIGN.md](DESIGN.md).

## Layout

Use one Go module with a small number of packages:

```text
go.mod
go.sum
main.go                # Windows entry point
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
    upload.go          # Pending snapshots, dispatch, retries, lifecycle events
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
    config.go          # config.json defaults, loading, saving
    state.go           # Atomically persist successful-upload state
    logging.go         # Built-in log rotation

  windows/
    tray_windows.go    # Tray menu, status, actions, generated icon
    folders_windows.go # Known folders, folder picker, shell actions, instance lock
    startup_windows.go # Current-user sign-in startup registration
```

Tests live alongside implementations as `*_test.go`. Place sample saves in the relevant package's `testdata/` directory; use synthetic or sanitized fixtures, not personal save data.

## Responsibilities

- **`main.go`:** wire dependencies and start the application; keep application logic elsewhere.
- **`app`:** own the synchronization workflow, lifecycle, and retry scheduling. Permit only one upload at a time.
- **`saves`:** discover and read local saves, check stability, and fingerprint snapshots. No HTTP or tray dependencies.
- **`leaderboard`:** implement the upload protocol and classify errors. Do not read local files or retry indefinitely; scheduling belongs to `app`.
- **`storage`:** manage application configuration, successful-upload state, and rotating logs. `UploadState`, loaded/saved via `LoadUploadState` and `SaveUploadState`, is exposed as `App.LastUpload`. It records the uploaded `SaveFolder` as `save_folder` in `state.json`. Folder events carry `saveFolder`; no compatibility alias for the old `folder` key is provided. Never modify game saves.
- **`windows`:** implement OS integration without making synchronization decisions.

The `leaderboard` package owns the JSON wire types `UploadRequest` and `UploadFile`. The `app` package converts local `saves.Snapshot` values into upload requests. Snapshot fingerprints stay local; scanner types have no protocol JSON tags. Neither `leaderboard` nor `saves` imports the other.

## Coordination

`App.Run` dispatches timer ticks, folder events, upload results, and commands. `runState.tick` calls `pollFolder` and `dispatchUploads`. The first consumes runtime `App.ForceUploadRequested` through `poller.requestEmission()`, preserving the poller's latch across settling ticks, then records a typed folder observation. The second delivers ready snapshots and advances uploads independently. `folderPoller.poll` specifically scans the save folder and emits changes. An unexported `runState` coordinates a `folderPoller` and a `snapshotUploader`:

```text
save scanner -> folderPoller -> folderUpdate channel -> snapshotUploader
                                                     -> upload worker -> result channel
```

The poller compares stable snapshots against its in-memory `lastEmittedFingerprint`. The coordinator seeds that initial baseline from `App.LastUpload` only when the save folder and destination match (excluding dry runs). Its single-slot event channel coalesces undelivered changes. The uploader owns the latest pending event, active request, retry deadline, and rejection state. It receives only the upload client, dry-run setting, named logger, and a typed event sink; it has no `App` reference, persistence access, or UI rendering callback. The coordinator consumes successful-upload events, updates `App.LastUpload`, and persists history. A transient failure retains pending data and retries without requiring another folder event. Newer events supersede unsent data, including failed data awaiting retry. The uploader trusts every event; it does not suppress snapshots based on previous success or rejection. An optional `rejection` pairs the failed snapshot's fingerprint with its message for idle status. A permanent failure is always logged but is retained as rejection state only if no newer event is pending. Queueing or starting another upload clears rejection metadata without changing the shared delivery backoff (`attempt` and `retryAt`).

Only the event loop mutates component state. The network worker returns its result through a buffered channel, and shutdown waits for it. Scanning continues while uploads are active. Command handlers trigger immediate checks for upload, Automatic uploads changes, and folder changes; startup preference changes only refresh status. Upload now sets `App.ForceUploadRequested`; the immediate tick transfers and clears that request. The poller clears its latch only after emitting a stable snapshot, bypassing event deduplication even for unchanged files. Folder resets clear the app request and replace the poller. The poller receives the Automatic uploads setting and owns the decision to scan: automatic polling is disabled when the setting is off, but a forced emission continues through settling. Events contain only `saveFolder` and `snapshot`; results likewise carry no force/manual flag. Once emitted, work is accepted for delivery regardless of the setting. Queued uploads, active requests, and retries continue independently. No one-off permission state is needed; Quit cancels delivery.

On restart, matching successful history supplies the poller's baseline. This avoids re-emitting unchanged successful data while allowing emitted-but-undelivered changes to be emitted again. A forced emission is always actionable, even when its fingerprint matches previous upload history.

Before dispatching an upload or due retry, the coordinator nonblockingly delivers an already-ready folder event, replacing older pending data. If no event is ready, existing work proceeds without waiting for another emission or for files to settle. `dispatchUploads` does not inspect `forceNextEmission` or channel length; retry deadlines remain the uploader's responsibility.

### Component Events and Presentation

`events.go` defines folder observations and upload lifecycle events without user-facing text. Snapshot delivery and asynchronous network results use channels. Folder observations are returned directly; upload lifecycle events use a small synchronous typed sink on the same event loop, avoiding another goroutine or a potentially blocking event queue. There is no general-purpose event bus.

`render.go` converts the coordinator's latest folder/upload observations into UI messages. Polling never inspects uploader state, and the uploader never calls a renderer or changes `App.LastUpload`. A scan failure is recorded and logged even during an active upload; the renderer can keep the active-upload message visible and expose the scan error afterward.

Operational logging is independent of UI rendering. Components use `[poller]`, `[uploader]`, and `[app]` logger names (`[main]` for startup integration). Rendering/status delivery does not duplicate those logs. The coordinator reports persistence and command errors with its own logger.

Uploader reset is an explicit operation: a preflight checks that no upload is active before saving a changed folder configuration, then `reset()` owns clearing pending data, rejection, and backoff. The coordinator never writes those fields directly. Shutdown uses the uploader's wait operation.

The tray sends commands such as toggling Automatic uploads and Upload now to the application coordinator. The coordinator publishes status updates back to the tray through a thread-safe boundary. Background workers must not directly modify UI state; UI updates must follow the tray library's threading requirements.

Keep core save scanning and HTTP behavior independent of Windows UI code so they can be tested without launching the tray. Windows-specific files use `_windows.go` filenames; `main.go` has a Windows build constraint. Run whole-project tests on Windows.

## Implementation Style

- Avoid a generic event bus, dependency-injection framework, or interfaces for every type.
- Introduce narrow interfaces where they provide a concrete testing boundary, such as substituting a fake upload client.
- Put each file's main workflow first, followed by its direct helpers and lower-level utilities.
- Keep this small structure and split packages only when real complexity justifies it.
- The uploader interface in `app` is the test boundary for network operations. Coordinator tests exercise cancellation, retries, restart deduplication, and saves changing during an upload using explicit request/reply channels. Poller tests cover forced emission, baseline scoping, deduplication, and coalescing. Uploader tests advance retry deadlines directly to verify retries without new events, pending-data replacement, and trusting emitted snapshots. Coordinator tests cover disabled automatic polling, explicit uploads while it is off, and queued/active work continuing through retries after the setting is turned off. Boundary tests verify named logging during concurrent scan/upload activity, uploader isolation from persistence, coordinator-owned upload history, and reset safety. Renderer tests cover status priority and wording.

## Reference Project

[restic/restic](https://github.com/restic/restic) is a real Go application illustrating a similar responsibility-based organization:

- [`cmd/restic/`](https://github.com/restic/restic/tree/master/cmd/restic): executable entry point and commands.
- [`internal/`](https://github.com/restic/restic/tree/master/internal): implementation packages separated by responsibility.
- Tests alongside their implementations.

Restic is much larger and is not a tray application. It is a structural reference, not an exact template: the uploader layout above is tailored to this application rather than copied from restic.

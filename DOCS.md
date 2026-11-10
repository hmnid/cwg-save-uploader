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
    app.go             # Startup, polling, upload coordination, shutdown
    status.go          # Current status exposed to the tray

  saves/
    scan.go            # Discover eligible files, read stable snapshots, hash contents

  leaderboard/
    client.go          # Upload API, response parsing, error classification

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
- **`storage`:** manage application configuration, successful-upload state, and rotating logs. Never modify game saves.
- **`windows`:** implement OS integration without making synchronization decisions.

## Coordination

The tray sends commands such as Pause, Resume, and Upload now to the application coordinator. The coordinator publishes status updates back to the tray through a thread-safe boundary. Background workers must not directly modify UI state; UI updates must follow the tray library's threading requirements.

Keep core save scanning and HTTP behavior independent of Windows UI code so they can be tested without launching the tray. Windows-specific files use `_windows.go` filenames; `main.go` has a Windows build constraint. Run whole-project tests on Windows.

## Implementation Style

- Avoid a generic event bus, dependency-injection framework, or interfaces for every type.
- Introduce narrow interfaces where they provide a concrete testing boundary, such as substituting a fake upload client.
- Put each file's main workflow first, followed by its direct helpers and lower-level utilities.
- Keep this small structure and split packages only when real complexity justifies it.
- The uploader interface in `app` is the test boundary for network operations. Coordinator tests exercise cancellation, retries, restart deduplication, and saves changing during an upload using explicit request/reply channels.

## Reference Project

[restic/restic](https://github.com/restic/restic) is a real Go application illustrating a similar responsibility-based organization:

- [`cmd/restic/`](https://github.com/restic/restic/tree/master/cmd/restic): executable entry point and commands.
- [`internal/`](https://github.com/restic/restic/tree/master/internal): implementation packages separated by responsibility.
- Tests alongside their implementations.

Restic is much larger and is not a tray application. It is a structural reference, not an exact template: the uploader layout above is tailored to this application rather than copied from restic.

# UX audit — 2026-10-04

This pass covers the two-panel manager, selection and filtering, keyboard/mouse
input, connection settings, copy/move/delete dialogs, cancellation, progress,
error recovery, theme/layout behavior, and the corresponding help text. Fixes
are local; this document is not a release announcement.

## Findings and fixes

| Problem | Change |
| --- | --- |
| Large listings were filtered, allocated, and sorted repeatedly during cursor movement and every transfer redraw. | Cache the visible listing until its data, filter, sort, or hidden-file setting changes. |
| Toolbar click regions did not match the rendered buttons; narrow windows cut off Options. | Rendering and hit testing share one layout. Compact labels fit the 64-column minimum. |
| Mouse events with modifier bits were ignored by several views. | Normalize Shift/Meta/Ctrl bits while preserving wheel direction, drag, and release events. |
| Stopping active work required quitting the application. | Esc stops the operation, waits for its worker, then reconnects when necessary. Current folders, surviving selections, and filters remain available. Ctrl-C still stops and quits. |
| Pasted command letters/newlines could trigger actions outside text fields. | Enable bracketed paste in the manager; accept pasted text only in editable fields. Reject oversized pastes instead of silently changing a path. |
| Long transfer errors were cut off. | Add a scrollable Details view, including long paths. |
| Error buttons captured navigation even though the pane appeared available. | Make the error dialog explicitly modal until dismissed or resolved. |
| Retry after a partially completed move/delete restarted the entire original operation. | Offer Retry for scan errors. After execution starts, offer Details, Options, and Dismiss so the user can inspect the current state before starting another action. |
| Dismissing an error claimed that the operation had been cancelled. | Report that it stopped and keep the selection. Cancellation after execution also says completed changes remain. |
| The validation before deletion showed no useful progress and made sequential network requests. | Show checked-entry counts and pipeline up to eight read-only checks, respecting the server's handle limit. All checks must finish before any deletion. |
| Move showed copy counters and a completed byte total while removing sources. | Reset progress for the removal phase and count entries. Update activity timestamps after each deletion. |
| Marks could refer to entries deleted outside Hop. | Successful directory refreshes reconcile marks in that directory; failed listings and marks in other directories are preserved. |
| Hidden or filtered marks were easy to overlook. | Show the number of selected items outside the current view. |
| Refreshing the same folder cleared the filter. | Keep the query and focused item when reloading that folder. |
| A failed path/folder request discarded its input. | Keep the editable value and error so it can be corrected. |
| Local deletion unnecessarily waited for the opposite panel to load. | Only require the source panel for deletion. |
| Help disagreed with cancellation behavior and described outdated transfer paths. | Update command help and the usage guide. Keep the README installation instructions unchanged. |

## Verification

Regression tests cover listing cache invalidation, selection reconciliation,
rendered toolbar hit targets at multiple widths, modified wheel events,
long error details, interrupted work, removal counters, and paste packet splits.

Real pseudo-terminal tests exercise navigation, confirmations, copy and move in
both directions, deletion, failed path editing, paste, Ctrl-C, SIGINT, and Esc
during a deliberately stalled connection. They verify terminal restoration.
The stalled-connection fixture uses separate request and response pipes; it
must not accidentally read its own requests as server responses.

`make check` passed: the complete Go race suite, `go vet`, five installer tests,
and formatting checks. The Linux amd64 cross-build also passed. No user data or
remote project directories were used as deletion fixtures in this pass.

The 24,000-entry listing benchmark measures only reuse of the cached listing,
not network speed or total frame rendering. The cached lookup allocates nothing.
Existing deletion tests still require all source checks before removal and
preserve changed files, changed directory contents, and symlink targets.

## Practical limits

- Actual Warp interaction has not been verified. Protocol tests cannot establish
  whether Warp is forwarding mouse events in a particular configuration.
- Esc is a stop request, not rollback. Completed copies/deletions remain, and
  in-flight work may finish while cancellation is being handled. Reconnection
  can fail or require authentication; Options remains the recovery route.
- Remote deletion itself still performs ordered SFTP operations and checks
  ancestors. Faster preflight does not make the deletion phase constant-time.
- Bracketed-paste protection requires terminal support. It is enabled in the
  two-panel manager; the standalone legacy pickers were not changed here.

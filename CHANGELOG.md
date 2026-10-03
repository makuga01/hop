# Changelog

## 0.2.0 — Faster transfers and clearer controls

- Download batches over a compressed SSH stream when Python 3 is already available on the server. No remote installation is needed; SFTP remains the fallback.
- Skip identical small files during confirmed replacements, reuse repeated source files, and stream new large files with bounded memory.
- Reuse unchanged blocks in existing large files where supported, with checksum verification and atomic commits.
- Overlap SFTP requests and file transfers; scan directory trees and prepare destination folders faster.
- Show scan progress, effective MB/s, transfer ETA, and the active transfer backend.
- Select confirmation buttons with arrow keys or Tab, then activate with Enter. Mouse targets now match the displayed buttons.
- Add a pure black theme, available in Options or with `hop --theme black`.
- Correct cancellation handling during stream completion and keep sources when a move fails.
- Add a checksum-verifying installer that leaves PATH and shell profiles unchanged.

Performance depends on the connection, server, and files. See [measured results](https://github.com/makuga01/hop/blob/v0.2.0/docs/performance.md) for workloads and limitations.

## 0.1.0 — First release

Hop is an SSH-only, two-panel terminal file manager for macOS and Linux.

- Pick machines from recent SSH connections and SSH config aliases, with resolved user, hostname, and port shown beside each alias.
- Browse local and remote files with keyboard, Vim-style movement, and mouse support.
- Mark multiple files or directories; copy, move, delete, and create folders in the workspace.
- Follow source symlinks when browsing/copying, with recursive-loop checks.
- Keep both panels visible during transfers, with batch progress and overwrite decisions.
- Filter, sort, toggle hidden files, and change connection options without restarting.
- Choose Lagoon, Cobalt, or Afterhours themes.
- Ship standalone binaries for macOS/Linux on AMD64 and ARM64.

This is the first release under the Hop name of the former two-panel Hops app.
The original single-panel Hop is archived separately as hop-classic.

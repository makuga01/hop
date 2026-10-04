# Transfer optimization, second local pass

October 2, 2026, macOS arm64. Changes remain local and unreleased.

## Larger fixtures: first optimization vs second optimization

One measured run per version/direction, with a local OpenSSH SFTP server and
20 ms of added response latency. Includes planning, file transfer, verification,
commit, download fsync, and history persistence. All copied contents are checked
with SHA-256 after timing.

| Workload | First pass | Second pass | Additional speedup |
| --- | ---: | ---: | ---: |
| 256 MiB, upload | 3.179 s | 1.127 s | 2.8× |
| 256 MiB, download | 3.167 s | 1.121 s | 2.8× |
| 1,024 × 4 KiB, upload | 28.187 s | 5.598 s | 5.0× |
| 1,024 × 4 KiB, download | 26.756 s | 7.736 s | 3.5× |

## Original fixtures: three-run medians

Original (`5040e99`) and first-pass measurements are from September 30;
second-pass measurements are from October 2. Same fixture and 20 ms delay.

| Workload | Original | First pass | Second pass | Additional speedup |
| --- | ---: | ---: | ---: | ---: |
| 32 MiB, upload | 1.674 s | 0.595 s | 0.341 s | 1.7× |
| 32 MiB, download | 1.666 s | 0.540 s | 0.302 s | 1.8× |
| 64 × 4 KiB, upload | 14.667 s | 1.808 s | 0.437 s | 4.1× |
| 64 × 4 KiB, download | 14.932 s | 1.736 s | 0.516 s | 3.4× |

These tests exercise real local SFTP operations and disk I/O. They omit SSH
encryption, network bandwidth limits, packet loss, and SSH channel flow control.
Actual Internet/LAN gains may differ substantially. Zero-added-latency cases
are in the logs and are noisy; no universal LAN speedup is claimed.

## What changed in this pass

- **Larger packets where supported:** query the advertised server limits once.
  Use up to 256 KiB payloads within its packet/read/write limits, including
  framing and handle overhead. Unknown servers retain 32 KiB packets.
- **Larger bounded transfer window:** up to 8 MiB and 256 requests per large
  file on servers with negotiated limits. Unknown servers retain the first
  pass's 2 MiB / 64-request window.
- **Small-file concurrency:** up to 32 files, limited further by the advertised
  handle budget. Batches containing a file over 1 MiB retain at most eight
  file workers. At most about 64 MiB of large-file payload can be pending
  across eight windows, plus protocol/allocation overhead.
- **One packet assembly copy:** pooled output buffers replace three successive
  payload-sized concatenations. Profiling eight 32 MiB uploads showed whole-test
  allocations fall from roughly 1,447 MiB to 527 MiB. Both profiles include
  roughly 512 MiB of fixture creation/content-check allocations.
- **Fewer metadata round trips:** batch transfers use their already-resolved
  exact destinations, skipping redundant remote-home and directory-target
  resolution. Destination type/overwrite and upload-parent checks still run.
  A path replaced by a directory after planning is rejected, rather than
  silently copying into that directory.
- **Slow-transfer timeout handling:** incoming transfer replies refresh the
  inactivity deadline while the sender is still filling its window.

Limit negotiation follows [OpenSSH's documented SFTP limits extension](https://github.com/openssh/openssh-portable/blob/master/PROTOCOL).
No extra SSH connections, remote helper, or server installation is required.

## Preserved behavior and verification

Source-change checks, temporary files, atomic/no-clobber commits, symlink
rejection at the file destination, and download fsync remain enabled.
On failure, scheduling stops and active files finish safely. Moves only remove
sources after the entire copy succeeds. Directory traversal/creation remains
ordered; deeply nested directory trees can still be latency-bound.

The complete race suite and `go vet ./...` passed. Tests additionally cover:

- Negotiated/unknown/unlimited/tight server limits and packet overhead.
- One-time negotiation under concurrent callers, unsupported extensions,
  and malformed replies.
- Short packet writes, write errors, short reads and out-of-order responses.
- Concurrent cancellation, reply dispatch failures and worker shutdown.
- Exact-destination directory replacement and concurrent progress/history.
- An upload that takes longer than its timeout to fill the window but keeps
  receiving replies.

## Reproduce

Requires local OpenSSH `sftp-server`. Fixtures and settings are confined to test
temporary directories; the benchmark never connects to saved SSH hosts.

```sh
# Original-size fixtures; three repetitions
HOP_PERF=1 go test ./cmd/hop -run '^TestTransferPerformance$' -v -count=3 -timeout 180s

# Larger fixtures, 20 ms simulated response latency
HOP_PERF=1 HOP_PERF_SCALE=1 go test ./cmd/hop \
  -run '^TestTransferPerformance$/.*/.*/rtt=20ms$' -v -count=1 -timeout 180s
```

Run versions separately. The same performance test is compatible with all
three implementations. Benchmarks are skipped in normal test runs.

## Live SSH test — October 2, 2026

Tested against an authorized live Linux SSH host, using the normal system
OpenSSH connection and real network transport. SFTP stat round trips averaged
46–50 ms. Versions ran sequentially, with one timed pass per workload/version;
these are observations, not stable statistical estimates.

| Workload | Released code | First optimization | Latest optimization | Released/latest |
| --- | ---: | ---: | ---: | ---: |
| 8 MiB random file, upload | 36.16 s | 38.09 s | 35.15 s | 1.0× |
| 8 MiB random file, download | 11.65 s | 9.81 s | 9.18 s | 1.3× |
| 128 × 4 KiB files, upload | 62.34 s | 9.30 s | 3.41 s | 18.3× |
| 128 × 4 KiB files, download | 63.96 s | 7.35 s | 2.35 s | 27.2× |

The small-file improvement is substantial on this host. The large upload is
essentially unchanged; the modest download difference needs repeated trials
before attributing it to the code. The earlier simulated-latency large-file
speedups do not describe this live connection.

Fixtures used cryptographically random contents to avoid compression/sparse-file
shortcuts. Uploads were hashed on the server with SHA-256; downloads were hashed
locally. Every completed transfer matched. Timings include preflight, transfer,
commit and history persistence; fixture creation, SSH setup and independent
checksum checks are excluded. No personal files or saved Hop paths were used.

An initial 256 MiB trial was stopped after the observed upload rate made the
workload impractical. Its partial fixture was cleaned up. It is not included
in the timing table. All three completed runs also confirmed remote cleanup.

Reproduce only against a host you authorize:

```sh
HOP_LIVE_HOST=your-host HOP_LIVE_LARGE_MIB=8 HOP_LIVE_SMALL_FILES=128 \
  go test ./cmd/hop -run '^TestLiveTransfer$' -v -count=1 -timeout 10m
```

The live test is skipped unless `HOP_LIVE_HOST` is explicitly set. It creates
only a unique `/tmp/hop-benchmark.*` directory on the remote host and temporary
local fixtures/settings. SSH authentication is noninteractive. The final
harness uses a five-minute connection deadline to allow cleanup before the
outer Go test timeout.

## Optional rsync updates — October 2, 2026

Live SSH test, local rsync 3.4.4 and remote rsync 3.4.1. One measured pass per
operation, using generated incompressible data, on the same authorized host as
the preceding live tests. First copy the 8 MiB file, then change 4 KiB in its
middle and update the existing destination. Full-copy and update workloads
are different; these figures show the benefit of reusing existing data, not a
speedup for every transfer.

| Direction | Full SFTP copy, 8 MiB | Hop rsync update, 4 KiB changed | Time ratio |
| --- | ---: | ---: | ---: |
| Upload | 36.60 s | 1.37 s | 26.7× |
| Download | 11.54 s | 0.73 s | 15.9× |

The update timing includes Hop's preflight, rsync detection when needed,
filesystem filename-collision checks, staged transfer, source verification,
permission restoration, atomic/no-clobber commit, and history persistence.
All destination SHA-256 hashes matched. Remote test directories were removed.
Fixture generation, SSH setup and the independent hash checks are outside the
transfer timer.

A separate raw-rsync trial sent 5,792 literal bytes for the 4 KiB edit, matching
8,382,816 bytes from the old 8 MiB file. Its staging-only upload took 0.53 s;
the complete Hop path above includes additional checks and commits.

Raw rsync was not faster for new uploads on this link. Raw small-file downloads
were faster, but Hop's staged version measured only a small end-to-end gain
(2.21 s versus the previous 2.35 s for 128 files, in single runs). Fresh copies
therefore retain the SFTP backend by default.

### Selection and behavior

- Detect a compatible rsync on both ends; never install anything automatically.
- Automatically use it for authorized overwrite selections with at least 1 MiB
  of existing files to replace. The current backend supports matching filenames
  in one source directory and one destination directory, up to 8,192 files.
- File-manager and compatible single-file CLI updates can use the backend.
  Renamed destinations, nested selections and linked sources retain SFTP.
- A failed availability/noninteractive-auth probe falls back to SFTP. After a
  transfer starts, errors are reported rather than silently retrying it.
- Use NUL-delimited explicit file lists and protected arguments, with no globs,
  recursive selection, delete, inplace update, or source-removal flags.
- Stage into a private directory on the receiving filesystem. Reserve and
  remove placeholder names first to detect case/Unicode filename collisions.
- Reuse the old destination through `--copy-dest`; verify sources and staged
  files, then apply Hop's regular-file, overwrite, permission and commit rules.
- Rsync uses an additional SSH session; the original SFTP session handles checks
  and remote commits. Canceling Hop also cancels rsync and its SSH subprocess.
- Cleanup is best effort after connection loss; any remaining staging directory
  is reported. Successful live trials left no remote fixtures.

`HOP_TRANSFER_BACKEND=sftp` disables the optional backend.
`HOP_TRANSFER_BACKEND=rsync` requests it for compatible selections for testing;
availability/layout restrictions still fall back to SFTP.

Full race tests, vet and formatting checks passed. Added tests cover staged
round trips and deltas, read-only files, quoted/newline names, source changes,
concurrent destination creation, failed rsync processes, cancellation,
filename collisions, availability fallback, and filename-preserving CLI routing.

The behavior uses rsync's documented [delta transfer, protected file lists,
and copy-dest staging](https://download.samba.org/pub/rsync/rsync.1).

Reproduce the live full-copy/update comparison on an authorized host:

```sh
HOP_LIVE_HOST=your-host HOP_LIVE_LARGE_MIB=8 HOP_LIVE_SMALL_FILES=128 \
  HOP_LIVE_RSYNC_DELTA=1 go test ./cmd/hop \
  -run '^TestLiveTransfer$' -v -count=1 -timeout 10m
```

## Native delta transfers (2026-10-02)

The default backend no longer discovers or executes rsync. Overwrites of at
least 1 MiB try Hop's native fixed-block SHA-256 delta path. Existing blocks are
indexed by checksum, including blocks moved by a whole-block offset. Blocks
start at 64 KiB and grow for larger files to bound manifests to 4,096 blocks.
At least half of the new file must be reusable; otherwise full pipelined SFTP
is used. Fresh transfers and the existing small-file concurrency are unchanged.

Remote checksum reads use an ephemeral, quoted SSH command. Python 3, if
present, hashes in one process with bounded buffers. Otherwise POSIX sh/dd and
sha256sum, shasum or OpenSSL are used. No files, packages or executables are
installed remotely. Upload reuse requires advertised `copy-data` version 1;
download reuse only needs the checksum command. Unsupported shell commands,
missing capabilities and SFTP-only servers fall back to the full SFTP path.
After a delta has started, any failed check aborts rather than silently committing.

Uploads assemble an exclusive temporary file using pipelined server-side copies
and changed-range writes, then verify its complete SHA-256 remotely. Downloads
assemble a local temporary file from local blocks and remote reads, then verify
its complete SHA-256 locally. Existing source-change, permission and atomic
commit checks still apply. Buffers and outstanding requests are bounded.

Local OpenSSH integration test: an 8 MiB file with a 4 KiB edit reused 8,323,072
bytes and transferred 65,536 file-data bytes in **each** direction. This is 128×
less file data, excluding checksum/SSH/SFTP traffic; it is **not** a measured
128× speedup. Both assembled files passed SHA-256 verification. Tests also cover
append, truncation, unchanged files, moved blocks, hostile-looking filenames,
POSIX checksum fallback, missing helpers, and checksum failures preserving the
destination and removing partial files. Full Go race suite and vet passed.

Live testing succeeded on the authorized Linux test server using its configured
SSH account. No SSH configuration or agent changes were needed.

| Live workload | Upload | Download |
| --- | ---: | ---: |
| Full 8 MiB file | 40.27 s | 11.21 s |
| Native update, 4 KiB changed | **1.42 s** | **0.69 s** |
| Fresh 128 × 4 KiB files | 4.51 s | 2.15 s |

Both native updates reused 8,323,072 bytes and transferred 65,536 file-data
bytes, plus checksum/protocol traffic. The test confirmed two native transfers,
no rsync capability detection, matching SHA-256 checksums, and remote cleanup.
The earlier rsync update measured 1.37 s upload / 0.73 s download, on a separate
run. These are single runs under varying network conditions, not a statistical
claim that one backend is faster. Log: `hop-live-native-success.log` in the
local native performance deliverables.

Limitations: fixed blocks do not match rsync's rolling algorithm for arbitrary
insertions; hashing rereads files and costs CPU/disk time; small overwrites still
use full SFTP; older servers without copy-data cannot reuse upload blocks.
The universal fallback is functional, not a promise of equal speed everywhere.

Reproduce the native data-reuse test:

```sh
go test ./cmd/hop -run '^TestNative' -v -count=1
HOP_TRANSFER_BACKEND=native HOP_LIVE_HOST=your-test-host \
  HOP_LIVE_LARGE_MIB=8 HOP_LIVE_SMALL_FILES=128 HOP_LIVE_DELTA=1 \
  go test ./cmd/hop -run '^TestLiveTransfer$' -v -count=1 -timeout 10m
```

The live command creates and cleans generated fixtures under its own unique
remote `/tmp/hop-benchmark.*` directory. `HOP_TRANSFER_BACKEND=sftp` forces the
full-transfer baseline. Rsync remains an explicit-only experimental backend
under `HOP_TRANSFER_BACKEND=rsync`.

## Rolling native delta validation (2026-10-02)

## What changed

The earlier fixed-block implementation fell back to full transfers after a
4 KiB insertion near the start of a file. The new path searches local data with
a rolling Adler-32 checksum, confirms each candidate with SHA-256, and verifies
the whole assembled file before commit. This recovers blocks shifted by arbitrary
insertions and deletions. Local tests cover 1-byte and 4 KiB insertions/deletions,
weak-checksum collisions, and cancellation after an unaligned match.

Rolling signatures use Python's existing standard library on the server. Without
Python, the existing POSIX checksum helper still supports fixed-block reuse.
Without helpers, full pipelined SFTP remains available. Upload reuse additionally
requires advertised SFTP copy-data. No rsync or installation is required for the
native path. Existing rsync was used solely as an explicit benchmark comparator.

## Repeated 2 MiB comparisons

Three repetitions per workload, backend and direction. Entries are median seconds
[min–max]. Each direction starts with the original destination restored, or no
destination for fresh copies. Generated random fixtures are identical across
backends within each run. Initial backend order rotates by repetition. The revised
native run happened afterward under changing network conditions; small timing
differences between runs should not be attributed to code changes.

| Workload | Direction | Full SFTP | Revised native | rsync |
| --- | --- | ---: | ---: | ---: |
| edit | upload | 11.11 [11.05–12.28] | 1.32 [1.30–1.33] | 1.37 [1.19–1.46] |
| edit | download | 3.70 [2.94–3.70] | 0.64 [0.63–0.77] | 0.80 [0.63–1.54] |
| append | upload | 12.43 [10.90–12.86] | 0.87 [0.81–0.98] | 1.56 [1.38–1.62] |
| append | download | 3.78 [2.96–4.29] | 0.55 [0.53–0.75] | 0.87 [0.67–0.87] |
| insert | upload | 12.60 [11.11–13.31] | 0.99 [0.82–1.38] | 1.26 [1.21–1.39] |
| insert | download | 3.72 [3.11–4.07] | 0.70 [0.67–0.96] | 0.80 [0.76–0.81] |
| fresh | upload | 13.28 [11.58–13.88] | 11.54 [11.18–11.79] | 13.15 [12.31–14.16] |
| fresh | download | 3.98 [2.77–4.18] | 3.34 [3.15–3.51] | 4.08 [3.13–4.55] |
| replace | upload | 12.24 [11.32–14.98] | 11.01 [10.84–11.05] | 14.11 [11.71–14.19] |
| replace | download | 3.76 [2.96–5.34] | 3.00 [2.90–3.15] | 4.20 [3.38–4.95] |

Edit replaces 4 KiB in the middle. Append adds 4 KiB. Insert adds 4 KiB at one
quarter of the original file. Replace uses unrelated random contents.

The insertion case improved from **13.25 s upload / 3.92 s download** in the old
native implementation to **0.99 s / 0.70 s**. The mechanism changed demonstrably:
the old implementation reused zero bytes, while all revised upload trials reused
2,097,152 bytes (4 KiB literal data) and downloads reused 2,031,616 bytes (68 KiB
literal data). Those figures exclude checksum/protocol traffic.

Fresh files and complete replacements gain no delta benefit. Their varying times
reflect full transfers and network variability; there is no claim of improvement.

## 256 MiB updates

Three alternating trials per backend/direction with 4 KiB changed in the middle.
Identical, generated pseudorandom fixtures were created locally and remotely and
checksum-verified before testing. This avoids a long initial upload; consequently
these results measure updates, not fresh 256 MiB transfer throughput.

| Backend | Upload median [min–max], seconds | Download median [min–max], seconds |
| --- | ---: | ---: |
| native | 2.70 [2.52–2.75] | 2.15 [1.96–2.24] |
| rsync | 2.19 [1.80–2.48] | 1.68 [1.67–2.00] |

Native reused 268,369,920 bytes in every trial and transferred 64 KiB of file data,
plus checksum/protocol traffic. It was approximately **23% slower uploading and
28% slower downloading** than rsync by these medians. This is a remaining gap.

## Compatibility and correctness

- 138 live transfers passed SHA-256 checks: 90 original matrix + 30 revised matrix,
  six capability tests, and 12 large-file updates. Every remote fixture cleanup
  was confirmed by its test log.
- On the live Linux server, a restricted command PATH containing only dd and
  sha256sum exercised the no-Python path: native uploads/downloads passed.
- An empty helper PATH forced full SFTP in both directions; both passed.
- Suppressing advertised copy-data forced full uploads while download reuse
  remained functional; both passed. This models capability loss on the same
  server, not an independently tested old server implementation.
- Checksum helpers passed on macOS with separate restricted paths for sha256sum,
  shasum and OpenSSL; and in existing Alpine/BusyBox and Ubuntu containers with
  networking disabled and no package installation. Container tests exercise the
  helper, not a full SSH/SFTP server.
- Full Go race suite and targeted rolling/cancellation tests passed. Vet and
  macOS ARM64/Linux AMD64 builds passed.

Windows servers, other SFTP implementations, WAN conditions other than this link,
and cold-cache workloads remain unverified. Rolling scans are skipped when fixed
blocks already reuse at least about 90%, or blocks exceed the 8 MiB scan-memory
cap. Blocks grow for large files, and hashing costs CPU/disk reads. This is not a
claim of rsync feature parity or universal compatibility.

## Reproduce

From the source repository, with authorization for the selected test host:

```sh
go test -race ./... -timeout 120s
HOP_MATRIX_HOST=your-test-host go test ./cmd/hop \
  -run '^TestLiveTransferMatrix$' -v -count=1 -timeout 25m
HOP_MATRIX_HOST=your-test-host go test ./cmd/hop \
  -run '^TestLivePortableTransfer$|^TestLiveLargeDelta$' -v -count=1 -timeout 15m
HOP_COMPAT_DOCKER=1 go test ./cmd/hop \
  -run '^TestNativeHashLinuxContainers$' -v -count=1
```

Matrix/large comparisons require existing rsync on both ends solely for the
comparator; fixture generation needs Python. Set HOP_MATRIX_BACKEND=native for a
native-only small matrix. Container tests use existing images and never pull or
install packages. Temporary directories are unique and contain generated data.
Timing includes planning, Hop's transfer/verification/commit, and history save;
fixture generation/reset and the independent final checksum are outside timing.

Use the commands above to generate raw results. Benchmark logs and local source
manifests are intentionally not checked into the repository.

## Download scan optimization

A read-only scan of the same remote tree produced 23,540 planned files and
1,860 directories, including copies reached through directory symlinks.
No source contents were downloaded for this measurement.

| Implementation | Planning time |
| --- | ---: |
| Concurrent SFTP scan and per-file open/close checks (earlier run) | 32.074 s |
| Server-side scan, uncompressed metadata (one run) | 7.236 s |
| Server-side scan, gzip metadata (three subsequent runs) | 1.051 / 1.093 / 1.088 s |

The median of the last three runs is 1.088 s, about 29× faster than the earlier
SFTP measurement. These are sequential runs on one host, not a cold-cache or
cross-platform benchmark. Times cover planning, with the SFTP connection already
established; the helper's additional SSH connection is included.

An ephemeral Python 3 command follows the same selected trees, rejects cycles
and special files, and opens regular files read-only to check access. It sends
bounded, compressed metadata with byte-safe paths. Hop validates tree structure,
root metadata, and canonical directory roots before accepting it. Local destination
checks remain in place. Failed or unavailable helpers fall back to SFTP. These
measurements cover planning only; download streaming is measured separately below.

### Mapping repeated directory trees

The scan now reuses directory listings and `DirEntry` metadata within a
single scan, including when several symlinks lead to the same tree. It derives
canonical paths for ordinary child directories from their parent and only resolves
roots and symlinks. Read-access checks are reused for identical file identities
(device, inode, mode, size, modification time and change time). Logical copies
through different aliases remain separate in the copy plan. Nothing is cached
between scans.

Metadata records now carry a parent index and basename instead of repeating the
full path. The decoder rejects invalid parent references and path traversal.
Three further planning runs on the same tree took **0.810 / 0.811 / 0.774 s**,
compared with the previous median of 1.088 s: about **26% less planning time**.
The source counts remained 23,540 files and 1,860 directories. These sequential
measurements include the helper SSH connection but exclude initial SFTP setup.

## Small-file download stream

On one remote host, a generated fixture of 512 files of 18 KiB plus one 2.25 MiB
file (513 files, 11.25 MiB total) took **18.968 s** with SFTP and **5.903 s** with
the small-file stream: about **3.2× faster**. Both runs used the corrected SFTP
scheduler, which limits large-file concurrency separately instead of limiting
the entire mixed batch to eight workers. Every downloaded byte was checked
against the generated fixture; the remote temporary directory was removed.

This is a single sequential comparison on compressible synthetic data, not a
measurement of a typical project. Timing excludes planning and connection setup.
The version measured here streamed new files up to 1 MiB with eight local commit
workers, per-file SHA-256 verification, fsync and atomic no-clobber commits. It
used SFTP/native delta for larger and existing files. Subsequent changes below
extend streaming to new large files and confirmed small-file replacements.
Reproduce on an authorized test host with `HOP_SMALL_HOST` and
`go test ./cmd/hop -run '^TestLiveSmallBatch$' -v -count=1`.

### Replacing existing small files

The initial stream skipped every existing destination, so a repeated download
with Replace still used per-file SFTP operations. The stream now includes
explicitly authorized replacements up to 1 MiB. Exact content comparisons avoid
rewriting identical local files; changed files are synced before atomic rename.
Symlink destinations and unexpected destination changes are rejected.

A mixed fixture (one third identical, one third changed, one third absent among
512 small files, plus one new 2.25 MiB file) took **18.153 s over SFTP** and
**5.657 s with streaming**. All downloaded contents were verified. Separate
local-only measurements for 512 files with eight commit workers were **2.007 s
fresh**, **2.211 s changed**, and **0.012 s identical**. Thus per-file disk syncs
remain a real cost; unchanged files avoid them when permissions also match.
These are single runs on synthetic, compressible data, not a guarantee for
another workload or filesystem. Use `HOP_SMALL_REPLACE=1` with the live test to
exercise this case. `HOP_SMALL_COUNT` controls the small-file count, and
`HOP_SMALL_BACKEND=native` selects just the streaming run.

A subsequent streaming Replace run with 10,000 small files and the same large
file (10,001 files, 178.03 MiB) completed in **46.943 s**. The small files were
again split between identical, changed and absent local destinations. Every
result was compared with generated contents and the remote fixture was removed.

### Full-tree diagnostics and streaming large files

A later opt-in test copied a real authorized tree with **23,541 files**, **1,860
directories**, and **395,626,591 bytes (377.3 MiB)** into a unique temporary
folder on the requested local filesystem. The completed run took **182.262 s**
for copying, plus **0.9 s** for planning. Temporary test data was removed. This
is one completed run on that connection and filesystem, not a universal rate.
Previous attempts before these changes failed; their partial durations are not
valid completed-transfer baselines. Early UI ETAs of 11–14 minutes were estimates,
not measured completion times.

The changes compress the request manifest as well as file data, stream new large
files through bounded buffers into temporary files, and reuse verified local
copies when multiple selected paths refer to the same remote inode/metadata.
Reference copies receive another checksum check and remain independent local
files, not hardlinks. Existing large files retain the native delta path.

The old “Creating destination folders” phase incorrectly remained visible during
request transmission and copying. Preparation now reports actual completed/total
folder counts and resets the phase before copying. Folder creation and final
permissions run concurrently within each tree level, preserving parent/child
ordering and rejecting destination symlinks.

### Resuming the same destination

Small replacement candidates now send local SHA-256 signatures in the compressed
request. If the remote content matches, the server sends a verified reference
instead of another payload. Hop rechecks the local content before marking it
complete, including permission handling. Modified or missing candidates still
receive the verified file data.

A separate diagnostic copy of the actual partial destination contained **662
files / 10,584,092 bytes**. Replacing just those existing files took **0.922 s**
after **0.831 s** of planning; payloads for identical files were not retransmitted.
The original destination was not modified and the diagnostic copy was removed.

An independent 4 MiB random-data SSH probe on that connection measured **0.754
MB/s** with one stream and **0.741 MB/s** with four concurrent streams, including
connection overhead. This is observed throughput for those probes, not a fixed
network limit. Compression and reuse improve effective throughput, but short-term
rates vary substantially across this tree; early ETAs are not reliable benchmarks.

## Batched small-file commits — October 3, 2026

The download stream now prepares bounded batches of up to 64 files or about
8 MiB of buffered payload, with a 100 ms maximum collection delay. Writes,
synchronization and publication run as separate phases. References to pending
files request an immediate flush, so they do not wait for the batch to fill.

On macOS APFS/HFS, every changed file first passes `fsync`, followed by one
`F_FULLFSYNC` drive-cache barrier per filesystem device in the batch. Only then
are files closed and published. This follows Apple's documented distinction
between [file synchronization and flushing the drive cache](https://developer.apple.com/library/archive/documentation/System/Conceptual/ManPages_iPhoneOS/man2/fsync.2.html).
Other filesystems and operating systems retain a full `File.Sync` per file.
Checksums, overwrite revalidation, no-clobber creation and temporary-file cleanup
remain enabled. A failed sync prevents publication of the pending batch.

One sequential live comparison used the existing synthetic Replace fixture:
4,096 small files split between identical, changed and missing destinations,
plus one new large file (4,097 files / 74.25 MiB). Both runs checked every result
against the generated contents and removed the remote fixture.

| Implementation | Copy time |
| --- | ---: |
| v0.3.0, immediate per-file commits | 17.472 s |
| Separate write/sync/publication phases, per-file full sync | 10.634 s |
| Batched device barriers on APFS | 7.020 s |

The final run was about 2.5× faster than v0.3.0 on this compressible fixture.
These are single runs, not an expected speedup for arbitrary project data.
Planning, fixture setup and independent content checks are outside the timer.

A subsequent comparison downloaded the same stable dependency tree from an
authorized live project: **18,791 files / 353,893,595 bytes**, with matching
counts and sizes in both runs. Each run used its own temporary local destination
and the stream's SHA-256 verification; the source was not modified. v0.3.0 took
**154.787 s**, and the batched implementation took **94.274 s**, about **39% less
time (1.64× faster)**. Planning was approximately one second, outside these
copy times. This is one sequential comparison on the measured connection,
not a controlled-bandwidth or cold-cache benchmark. Both temporary copies
were removed.

Tests cover source references across batches, cancellation, malformed frames,
concurrent destination changes (including identical files), and injected
file-sync/device-barrier failures. Publication is forbidden before synchronization.

### Transport investigation

Short SSH probes varied considerably across connections. Changing `IPQoS` and
opening independent connections did not reliably reproduce their apparent gains
in complete transfers, so those experimental settings were **not retained**.
One raw 64 MiB download, without Hop, took 86.3 s and settled near 0.75 MB/s.
The server reported 1,128 retransmitted TCP segments and a roughly 46 ms RTT
near its end. This is evidence of a transport problem during that run, not a
measurement of the user's available Internet bandwidth or proof of its cause.


## Zstandard and unchanged large replacements — October 4, 2026

The same authorized stable dependency tree (18,791 files, 981 directories,
353,893,595 logical bytes) was measured again. All runs used isolated temporary
local destinations. Replace runs seeded those destinations from the same existing
local copy; seed creation is outside the timer. Neither original tree was modified.

| Workload | Previous local build (gzip, batched commits) | This pass |
| --- | ---: | ---: |
| Fresh download | 94.274 s | 61.479 s |
| Replace existing matching tree | 10.548 s | 5.783 s |

These are single sequential observations on a variable live connection, not
controlled-bandwidth benchmarks. Copy times include local checksum preparation,
transfer, verification and commits; planning (roughly 0.7–1 second), connection
setup and fixture setup are excluded. Fresh and Replace are different workloads:
the 5.783-second result mostly checks data already present locally. Temporary
copies were removed after completion.

- Download streaming prefers Zstandard when Python's `compression.zstd` module
  or the `zstd` executable is already available remotely. It installs nothing.
  Hosts without either retain gzip level 1; hosts without Python retain SFTP.
  The local decoder is compiled into Hop and has bounded memory/window limits.
- Zstandard level 9 reduced the compressed unique-inode payload in a separate
  remote compression probe from 53,961,603 bytes (gzip level 1) to 40,266,397 bytes.
  That probe excludes protocol framing and is not the whole-transfer wire count.
  Its compression CPU/wall time increased from 2.751 to 5.032 seconds. Level 9
  trades CPU for fewer network bytes and may be slower on fast LANs or weak CPUs.
- Authorized large replacements now participate in whole-file SHA-256 comparison.
  Matching files are rechecked locally and retain their existing inode and data;
  source permissions are applied through a checked file handle. Changed large
  files remain unfinished by the stream and continue through native delta/SFTP.
- Eight local workers prepare replacement hashes. The request carries each
  distinct hash once and refers to it by index, reducing repeated manifest data.
  Source checks, per-file checksums and destination revalidation remain enabled.

The fresh run measured the codec change; the final Replace run also includes
parallel hashing and the deduplicated hash manifest. Local regression tests cover
fresh copies, changed/unchanged large replacements, references to deferred files,
codec failures/truncation, destination changes and cancellation.


### Longer compression history and corrected ETA — October 4, 2026

A further fresh download of the same 18,791-file / 353,893,595-byte tree took
**50.336 seconds** with a 64 MiB Zstandard history and long-distance matching,
compared with the preceding 61.479-second observation. These remain single runs
on a variable live connection. All files passed the normal stream checksum and
commit checks; the isolated destination was removed.

A local compression-only comparison concatenated identical unique file contents
from that existing tree, in sorted traversal order: 139,041,439 bytes before
compression. Level 9 with the default history produced 40,397,080 bytes in
1.320 seconds; level 9 with long-distance matching and a 64 MiB history produced
34,360,147 bytes in 1.141 seconds. These are local CPU measurements, exclude file
framing, and do not imply that every host or workload compresses faster. Larger
histories and level 12 were also examined; the shipped setting keeps level 9
and the 64 MiB history to limit CPU and memory costs. Python's optional native
[advanced compression parameters](https://docs.python.org/3.14/library/compression.zstd.html#advanced-parameter-control)
and the existing zstd command use the same settings. The local decoder rejects
windows over 64 MiB and sets its decoder memory limit to 128 MiB; other transfer
buffers and application memory are additional.

An independent diagnostic separated the preceding codec's network stage from
local replay: receiving its 40,764,363-byte wire stream took 87.032 seconds;
the whole copy took 95.929 seconds. Feeding the captured stream into the local
receiver took 6.165 seconds, with remaining time spent on commits and orchestration.
The diagnostic buffered the entire stream solely to separate these measurements;
normal transfers still decompress and write while downloading. This identifies
transport as the dominant cost in that run, not a fixed available-bandwidth limit.

ETA no longer extrapolates all remaining file counts from the last few completed
files while bytes remain. One large file had made that estimate grow beyond a day.
The displayed effective rate uses approximately five seconds of history; the
approximate byte-based ETA uses up to twenty seconds. Startup, stalls and sharp
rate changes suppress the estimate, and changing backends resets its history.
Final commits with all bytes received do not extrapolate the earlier file rate.
All-empty selections retain a file-based estimate. Regression tests cover the
large-file/many-small-files case, startup, stalls, retries and backend changes.


## Upload streaming and destination checks — October 4, 2026

Uploads now aggregate at least 32 eligible files, or a fresh file of at least
8 MiB, into one gzip-compressed SSH stream. Python 3 on a compatible POSIX host
receives explicit byte-safe destination paths and bounded file chunks. No helper
is installed and no complete intermediate archive is created. Unsupported hosts
retain SFTP. Existing files over 1 MiB retain native delta/SFTP instead of losing
block reuse. Authorized small replacements use the stream.

Before the sender emits each checksum, it rechecks the local source's identity,
size and modification time. The receiver validates SHA-256, writes exclusive
temporary files relative to open parent-directory descriptors, and synchronizes
bounded groups of up to 32 files or 8 MiB (one large file may exceed that byte
threshold). Eight persistent workers handle remote fsync. Acknowledgements follow
successful no-clobber creation or authorized atomic replacement, including checks
for concurrent destination and parent changes. EOF, process success and all
acknowledgements are required for overall success; a failed copy cannot authorize
source removal. Cancellation closes input so the receiver can clean staging files,
with a bounded shutdown timeout. Cleanup remains best effort after hard process,
connection or filesystem failures.

A read-only helper also batches upload destination metadata checks. It validates
that the shell and SFTP resolve the destination root to the same canonical path.
Malformed/unavailable helpers fall back to the original SFTP checks; symlink and
overwrite checks remain active. The write helper repeats namespace validation
before creating any file. Folder creation and final directory permissions still
use the existing ordered SFTP implementation.

Live tests used generated fixtures in unique remote temporary directories on an
authorized Linux host. All uploaded files were independently checked with SHA-256,
and remote cleanup was verified. Each figure is a single run on a variable link:

| Generated workload | SFTP copy | Stream copy |
| --- | ---: | ---: |
| 2,048 text files, 32,768,000 bytes | 165.251 s | 6.704 s |
| 128 random files, 2,048,000 bytes | 48.864 s | 20.180 s |

The text fixture compresses very well, so its ratio is not representative of
incompressible content. These copy times exclude planning, fixture generation,
independent final verification and SSH setup. The later random-data comparison
uses the final destination-check helper and verifies that gains also occur without
compression savings; link conditions differed from the preceding text comparison.

After adding persistent sync workers and avoiding a 256 KiB allocation for every
small file, a final text-fixture run completed in **4.876 seconds**, with **0.405
seconds** of planning. It passed independent checksums and verified cleanup. The
original 165.251-second SFTP observation was not rerun alongside this final pass.

Reproduce against an authorized test host:

```sh
HOP_UPLOAD_HOST=your-test-host go test ./cmd/hop \
  -run '^TestLiveUploadStream$' -v -count=1 -timeout 8m
HOP_UPLOAD_HOST=your-test-host HOP_UPLOAD_COUNT=128 HOP_UPLOAD_RANDOM=1 \
  go test ./cmd/hop -run '^TestLiveUploadStream$' -v -count=1 -timeout 3m
```

`HOP_UPLOAD_BACKEND=native` or `sftp` limits the benchmark to one copy path.
Normal test runs do not contact SSH machines. Tests cover replacements, new large
files, empty files, unusual names, source changes, corruption, destination races,
symlinks, namespace mismatch, sync failures, cancellation cleanup and metadata
fallback. The full race suite and vet passed; macOS ARM64 and Linux AMD64 build.

### Download parallelism experiment

Four compressed streams on the existing shared SSH connection took 57.210 seconds
for the stable 18,791-file / 353,893,595-byte download tree. Four independent SSH
connections took 48.830 seconds, compared with the preceding single-stream
50.336-second observation. That small difference on a variable connection does
not justify extra connections and memory. Neither experiment was retained in
production; the single-stream download remains in place.


## Scanning before deletion — October 4, 2026

The deletion review previously performed a sequential SFTP `lstat` for every
entry, plus directory listings. A separate read-only Python helper now walks the
selected roots on the host using `scandir` and `stat(follow_symlinks=False)` and
streams compressed metadata. It never removes files or follows directory links.
The client checks the shell/SFTP parent namespace and root metadata, validates
parent/child relationships and selection boundaries, rejects incomplete/oversized
manifests, and constructs a postorder plan. Existing deletion revalidation and
individual removal operations still execute after confirmation.

If the helper is unavailable, the SFTP walk reuses complete attributes already
returned by directory listings. Missing size/mode/time attributes still trigger
an explicit `lstat`. The deletion dialog now shows elapsed time and live file and
folder counts; cancellation stops the helper. Move planning uses this scanner too.

One live comparison scanned a generated tree of 512 regular files, one broken
symlink, 16 subdirectories and its root (530 entries):

| Scan | Time |
| --- | ---: |
| Previous per-entry SFTP stat walk | 31.621 s |
| New remote metadata helper | 0.322 s |

Both plans contained the same paths, modes, sizes, times and directory children.
A separate larger generated fixture with 23,541 regular files, one broken symlink,
1,024 subdirectories and its root (24,567 entries) scanned in **0.950 seconds**.
The larger fixture did not run the slow baseline. These are single observations
on one live connection; they measure planning before confirmation, not deletion.
The fixtures were checked to remain present after scanning, then their unique
test directories were cleaned up and cleanup verified.

Local tests cover symlink targets, overlapping selections, postorder plans,
changed sources, cancellation, namespace mismatch, unavailable helpers, malformed
manifests and listing-attribute reuse. The existing deletion and Move safeguards
are exercised against the new plans as well.

```sh
HOP_REMOVAL_HOST=your-test-host go test ./cmd/hop \
  -run '^TestLiveRemovalScan$' -v -count=1 -timeout 4m
HOP_REMOVAL_HOST=your-test-host HOP_REMOVAL_COUNT=23541 \
  HOP_REMOVAL_DIRS=1024 HOP_REMOVAL_BASELINE=0 go test ./cmd/hop \
  -run '^TestLiveRemovalScan$' -v -count=1 -timeout 3m
```

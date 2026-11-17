# M3 security review (step 3.11)

Scope: `internal/fm` (path policy, descriptor-based operations, trash, editor, uploads, archives) and the `/api/fm/*` routes in `internal/httpapi`, as of `449fd8c` (core) and `b5a4802` (API).
Method: read the code, not the reports. Fixes and regression tests landed in the commit this file is committed with.

**Three defects found, all fixed. Nothing Critical or High remains open.**

## Findings

| # | Sev | Where | Finding | Disposition |
|---|-----|-------|---------|-------------|
| 1 | High | `internal/fm/upload_linux.go` `UploadChunk` | The `.part` file was opened with `os.OpenFile(..., os.O_WRONLY, 0)` — no `O_NOFOLLOW`. A symlink swapped in for the part file (its name is a hex ID in a world-writable-by-root-only directory, but the panel tree may be shared) redirected every `WriteAt` to the link's target: chunk bytes could be written to any file on disk, root-owned, mode preserved. | **Fixed.** The part file is opened with `unix.O_WRONLY|unix.O_NOFOLLOW`; a swapped link fails the chunk with `ELOOP` instead of writing through it. Test: `TestUploadChunkRefusesSwappedPartSymlink` (removes the part file, plants a symlink to a victim file, asserts the chunk fails and the victim is untouched). |
| 2 | Medium | `internal/fm/fs_linux.go` `Touch`, `Mkdir`, `Move`, `Copy` | These four operations never consulted `internalTree`, so a client-supplied destination inside the panel's `trash/` or `uploads/` tree wrote bytes there with no index row — an orphaned bin entry or an upload part the sweep would not own. | **Fixed.** Each now runs the guard on the resolved destination. The guard lives on `FS` and is set by `Service.New`; it refuses a real path inside the trash or uploads trees (`ErrProtected`). Tests: `TestInternalTreeOffLimitsToGenericOperations` (Touch in both trees, Move into uploads), `TestUploadCompleteRefusesInternalTreeAfterDirSwap` (directory swapped between start and complete). |
| 3 | Medium | `internal/fm/upload_linux.go` `UploadComplete` | The target directory was re-judged with `CheckWrite` only. A directory swapped for the upload's target between `UploadStart` and `UploadComplete` (rename the real dir away, plant a symlink to `panel/uploads`) passed `CheckWrite` and landed the finished file inside the panel's uploads tree with no `fm_uploads` row. | **Fixed.** `internalTree` is checked after `CheckWrite` on the resolved directory. Test: `TestUploadCompleteRefusesInternalTreeAfterDirSwap`. |

## Checked and sound

- **Path policy.** `Clean` rejects empty, NUL, non-UTF-8 and relative paths; `CheckName` bounds one component; `CheckDestructive` and `CheckConfirm` guard delete, move, recursive chmod/chown; `hardDeny` covers the system directories as targets. All judged on the real path resolved from `/proc/self/fd` (pinned by `TestFMBrowseAndGuardrails`).
- **Descriptor discipline.** Every walk (`removeTree`, `treeSize`, `copyAt`, `walkAttr`, `addTree`, `mkdirAll`, `statComps`) opens directories `O_NOFOLLOW`; a symlink swapped in for any component is an error, never a detour. Recursive chmod/chown never follow links (`chmodAt` re-opens `O_PATH|O_NOFOLLOW` and re-checks).
- **Archive extraction.** Two-pass extract (validate, then write); entry names rejected for absolute paths, `..`, NUL; symlink targets bounded and relative; hardlinks must point at a file earlier in the archive; device and FIFO entries refused; decompressed bytes counted against `ArchiveMaxBytes` (not the header's claim) and entries against `ArchiveMaxEntries`; the archive being written is excluded by inode. `mkdirAll` opens every component `O_NOFOLLOW`, so a planted link in the way of a directory chain is refused, not traversed.
- **Upload assembly.** Chunks are offset-checked (`offset != received` is a 409 with the server's count), SHA-256'd per chunk, bounded by the announced size, and the assembled file is re-verified on complete; the part file is written `O_NOFOLLOW` (finding 1). The sweep takes each upload's lock `TryLock` and re-reads `updated_at` under it, so a sweep cannot delete bytes out from under a chunk or a complete in flight.
- **Download.** Always `application/octet-stream` + `attachment`, so stored HTML/SVG never renders in the panel's origin; ranges served by `http.ServeContent`. Session and CSRF coverage confirmed: every unsafe route answers 401 without a session and 403 without a matching token (`TestEveryUnsafeRouteRequiresCSRF` covers the table; `TestFMRoutesNeedAuthAndAnswer503WithoutFiles` covers the fm routes).
- **Trash restore/purge.** Restore never overwrites; the payload path is re-derived from the row's hex ID and checked with `CheckName`; purge drops by row ID, not by client path, and `dropTrash` re-validates the payload before `RemoveTree`.
- **chmod/chown target validation.** Recursive changes are refused on `/` and the deny list; non-recursive changes go through `chmodAt` with `O_NOFOLLOW` re-checks; `lookupID` bounds ids to non-negative numbers.

## Limits accepted (not defects)

- The daemon runs as root and the file manager writes anywhere by design; systemd hardening narrows but does not contain a compromised session (plan §3.2). Authentication is the real boundary.
- The global lockout (finding 8 of the M0 review) lets one window lock out everyone including the operator; recovery is a daemon restart. Accepted in M0, unchanged here.
- `pb_session` has no `__Host-` prefix (M0 finding 7). Open there, unchanged here.

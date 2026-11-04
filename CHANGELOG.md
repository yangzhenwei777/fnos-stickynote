# Changelog

## 2.4.0

- Search syntax: `q` now accepts `tag:`, `folder:` (quote names containing spaces), `sort:updated|created|title`, `since:YYYY-MM-DD` and `until:YYYY-MM-DD`. Unrecognized or invalid tokens fall back to plain keywords. All values are parameterized.
- Editor: typing `/` at the start of a paragraph opens an insert menu (headings 1–3, bullet/ordered/task lists, quote, code block, table, divider) with keyword filtering, arrow-key navigation, Enter to run and Esc to close.
- Reading view: notes with two or more headings (h1–h3) show a sticky outline on the right; click to jump. Hidden on narrow screens and mobile.
- No schema changes, no new dependencies.

## 2.3.3

- Refactor: the six oversized Vue components (note list, editor toolbar, note workspace, profile, sidebar, desktop account card) are now all under the 800-line soft limit. Logic moved into composables; profile tabs became child components; list/profile styles moved to shared prefixed CSS. Zero UI or behavior change (templates/styles byte-compared against the 2.3.2 baseline).
- fpk: full icon set (icon_0/64/128/256/512, 512 root icon) and stricter package assertions (ELF arch, single binary, CRLF, transparent icon corners).
- No schema changes, no new dependencies.

## 2.3.2

- Fix: opening a note, entering edit mode without changing anything, then switching back to preview no longer marks the note as modified or moves it to the top of the list.
- Root cause: the editor's normalization of stored HTML (line breaks, link `target/rel`, `b`→`strong`, `div`→`p`, style order, table width projection) was treated as a user edit.
- Fix A: the editor flush baseline now uses the editor's normalized HTML (captured synchronously on `onMount`).
- Fix B: the save layer keeps a `title/content/tags` baseline and skips no-op writes entirely.
- Shared by desktop and web; no schema changes, no new dependencies.

## 2.3.1

- Desktop/web API contract alignment: the "Export all" summary now reports "N notes in encrypted folders"; the note list re-sorts by updated time after saving on desktop; operations on locked folders return 423 with an "unlock first" message; restored notes expose `folder_locked`; moved notes return `version`; desktop `public-config` carries the default trash retention days.
- No schema changes, no new dependencies.

## 2.3.0

- Folder encryption (access lock): any folder can be encrypted from its context menu. Once locked, its notes disappear from all notes/search/tags/trash/counts/exports; unlock per session with the folder password; relocks on logout, browser tab close, or desktop app exit. Orthogonal to per-note encryption. "Export all" follows the current unlock state. No in-app password reset — recovery is admin-side only.
- Backend adds three columns (`is_encrypted`, `encrypt_password_hash`, `encrypted_at`), lock/unlock endpoints and HMAC unlock grants bound to user/token version/password fingerprint; every read path fails closed; locked-folder writes/deletes return 423 `FOLDER_LOCKED`; the desktop keeps unlock state in process memory; sync payloads carry the lock fields (absent = keep current lock state).
- No new dependencies.

## 2.2.2

- New: export a note as a long image (PNG) from the note view "more" menu — fixed 800px light poster layout with title/date header and footer, task-list checkboxes rendered as static symbols, same-origin images inlined automatically. The menu entry sits above "Download" with a divider; long code lines soft-wrap in the exported image instead of being clipped.
- Very long notes are handled by windowed PNG encoding (no canvas height cap), with adaptive DPR fallback for size/sharpness balance.
- Markdown export now embeds images as base64 data URIs (single-file, renders in Typora/VSCode/Obsidian); failed or non-whitelisted sources gracefully fall back to the original reference.
- Table column widths resized in the editor are now persisted and preserved across read/preview/long-image views (new colgroup/col whitelist on both backend sanitizer and frontend sanitizer, plus visible drag handles).
- Encrypted notes require the note password before export; the plaintext is rendered in-browser only (nothing is stored, quota-free, excluded from backups).
- New dependency: @zumer/snapdom 3.5.0 (MIT, zero runtime dependencies), loaded lazily on first export.

## 2.2.1

- Preview: empty paragraphs now keep the same line height as the editor, so blank lines no longer collapse in read/preview mode.
- TXT import (and plain-text paste) now creates one paragraph per line, so block formatting (e.g. heading) applies to a single line instead of the whole block.
- Markdown paste is handled by the official editor paste hook (single insert) with a tightened trigger regex; plain text like `-2026` lists is no longer misconverted.
- Long unbroken tokens (Base64/URLs) now wrap in preview containers.
- Notes imported via the web UI now land in the currently selected folder.
- New admin endpoint and button to clear finished backup job records (running jobs are always preserved).

## 2.2.0

- Unified fnOS gateway mode: unix socket + TCP dual-track listeners.
- Desktop client improvements for session recovery, bootstrap visibility and local-first sync.
- CORS handling for fnOS App / FN Connect relay forms.
- Security hardening and dependency updates.

## 2.1.x

- Mobile startup fixes, CORS refinements and admin route compatibility.
- Blob payload channel and binding-layer timeout guards.
- Desktop sync and session recovery improvements.

## 2.0.0

- First public-facing stable baseline.
- Rich-text notes, full-text search, encryption, TOTP, multi-user isolation.
- fnOS FPK packaging and Windows desktop client.

Earlier changes are summarized in GitHub Releases.

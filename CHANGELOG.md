# Changelog

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

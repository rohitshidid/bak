# bak

`bak` is `cp file.bak` upgraded: automatic timestamps, many versions, list/diff/restore, and storage outside your current directory.

It is meant for the single loose files that are too small or awkward for git: `/etc/hosts`, `~/.zshrc`, `.env`, an nginx config, a generated prompt, or a file you are about to rewrite with a script.

## Install

From this tap after you publish it:

```sh
brew tap rohitshidid/bak
brew install bak
```

From a local checkout:

```sh
go build -o bak .
./bak --help
```

## Quick Start

```sh
bak nginx.conf
bak nginx.conf -m "before tls"
bak list nginx.conf
bak diff nginx.conf v1
bak restore nginx.conf v1
```

Restore is deliberately non-lossy. If the live file has changes that are not already in history, `bak restore` snapshots the live file first and then restores the requested version.

```text
$ bak restore nginx.conf v2
current nginx.conf differs from all snapshots.
  -> saving current as v5 first
restored nginx.conf <- v2   (undo: bak restore nginx.conf v5)
```

## Commands

```text
bak <file>                    snapshot now
bak <file> -m "before tls"    snapshot with a note
bak list <file>               list versions for a file
bak list --all                list every tracked file
bak diff <file>               compact changed hunks: latest snapshot vs current file
bak diff <file> v2            v2 vs current file
bak diff <file> v2 v4         v2 vs v4
bak diff -f <file> v2         full-file diff, including unchanged lines
bak restore <file>            interactive restore picker
bak restore <file> v2         restore a specific version
bak show <file> v2            print a version to stdout
bak rm <file>                 drop history for a file
bak prune <file> --keep 5     trim old versions
bak gc                        purge histories for deleted files
```

## Flags

```text
-m, --note TEXT       add a version note
    --keep N          auto-prune on snapshot, keeping newest N
    --yes             skip confirmation prompts
    --json            machine-readable output where supported
-f, --full           show full-file diff instead of compact hunks
    --no-compress     store raw snapshots instead of zstd
    --no-deref        snapshot a symlink itself instead of target content
    --max-size SIZE   require --yes above this size, default 100MB
```

`SIZE` accepts values like `100MB`, `2g`, or `512k`.

## Storage

By default, `bak` stores history in `~/.bak`. Set `BAK_DIR` to relocate it:

```sh
export BAK_DIR="$HOME/Library/Mobile Documents/com~apple~CloudDocs/bak"
```

Layout:

```text
~/.bak/
├── index.json
└── objects/
    └── <sha256(abspath)>/
        ├── meta.json
        ├── v1.zst
        ├── v2.zst
        └── v3.zst
```

Files are keyed by absolute path hash, so `/etc/hosts` and `~/hosts` never collide. Snapshots store mode bits and SHA-256 hashes. If a file has not changed since the latest version, `bak` no-ops instead of creating a duplicate version.

## Examples

```text
$ bak list nginx.conf
v5   2026-05-23 14:40     1.2 KB   (current, "auto before restore")
v4   2026-05-23 14:02     1.2 KB   "before tls"
v3   2026-05-22 09:10     1.1 KB
v2   2026-05-20 16:44     1.0 KB
v1   2026-05-18 11:30     0.9 KB

$ bak diff nginx.conf v2
--- nginx.conf v2 (2026-05-20 16:44)
+++ nginx.conf current
@@ -12,3 +12,4 @@
   listen 80;
+  listen 443 ssl;

$ bak list --all
~/Pictures/note.txt                             3 versions   last 2h ago
/etc/hosts                                      7 versions   last 1d ago
~/.zshrc                                       12 versions   last 3d ago
```

## Use Cases

- System config edits: `bak /etc/hosts` before touching DNS or local overrides.
- Dotfile tinkering: snapshot `~/.zshrc`, try a plugin, revert if shell startup gets slow.
- Iteration: snapshot each config/query/prompt attempt and diff the winner against prior versions.
- Pre-codemod guard: snapshot a file before `sed`, formatter, or migration rewrites it.
- Secrets: keep local `.env` history without committing it to git.
- Deleted file recovery: if the file was snapshotted, `bak restore <file>` recreates it.

## Limits

- Local only. If the disk dies, history dies too. This is scratch undo, not a 3-2-1 backup system.
- Single files. Use git for multi-file atomic changes.
- No encryption at rest. Secrets are stored locally in compressed form, not encrypted.
- Manual trigger. You need to run `bak` before editing.

## Development

```sh
go test ./...
go build -ldflags "-s -w -X main.version=dev" -o bak .
```

The Homebrew formula lives in `Formula/bak.rb`.

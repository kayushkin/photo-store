# About photo-store

## What it owns

`:8329`, loopback only. What is known about the user's own photos and videos: where each original is (`source_machine`, `source_path`; the originals never leave that machine), when it was taken and how far to trust that (`date_source`: `exif`, `filename`, `mtime`, `unknown`), where (GPS, when the file has it), a thumbnail, and a description. Ids are `photo_000001`, registered in kanban-store's entity-type registry as type `photo`, so a timeline item or a note cites a photo as a chip. **These are private photos**: dash is the front door, and nothing here is copied to noteboard. `CONTRACT.md` is the route table.

# How it works

## Imports and descriptions

Photos arrive from the machine that holds the originals: a helper there reads each file's EXIF, makes a JPEG thumbnail and writes `index.jsonl` beside the thumbnails; `~/bin/photo-import -dir <dir>` posts them to `POST /imports`, upserting by the original's `sha256`. **An import never touches a description or tags**, so re-importing after a move only updates where the original is. A description is written with `PATCH /photos/{id}` and must say who wrote it (`described_by`). Every update, import change, delete and restore first copies the whole row to `photo_revisions`; nothing is hard-deleted, and a deleted photo's thumbnail still serves so a citation keeps drawing. Photos sent in the user's chats come from multichat with `photo-import-messages` (rooms of at most `-max-members` members, never Discord server channels, plus the user's own servers' saved attachments with `-discord-archive`); `-discord-direct-messages <archive>/discord/direct-messages` and `-facebook-messages <export>/your_facebook_activity/messages` (with `-chats=false` to skip multichat) import the images from fetched Discord DMs and a Facebook Messenger export, each cited by the journal-store transcript of the day it was sent (found by the Discord message id or the file name, `JOURNAL_STORE_URL`), under folders `discord/direct-messages/<chat>` and `messages/facebook/<chat>`; their `source_path` is the `message_…` ref they came in and their `date_source` is `message`. Which machines and folders this host's photos came from is not in this repo: see the noteboard note tagged `photo-store`.

# Working in this repo

## Build, test and deploy

Module `github.com/kayushkin/photo-store`, root package `photostore`, server `cmd/photo-store`, importer `cmd/photo-import`. No build tags. Every variable it reads is declared once in `settings.go` with llm-bridge `servicesettings` (hence `replace ../llm-bridge`), served at `GET /settings`; `TestEveryEnvironmentVariableTheServiceReadsIsDeclared` fails on an undeclared `os.Getenv`. `./deploy.sh` runs the shared deploy gate, tests, checks the running unit's environment, installs `photo-store` and `photo-import` to `~/bin`, restarts the user unit and fails unless `/health` is ok and the port is bound to loopback only. Tests use synthetic bytes: **no real photo belongs in this repo.** `./generate-ts.sh` renders `photo.go` and `vocabulary.go` to `ts/model.ts`, published source-only as `@kayushkin/photo-store-types` (`file:../photo-store/ts`); bridge-ui's photo chip reads it.

## Public repository: nothing personal

This repo is meant to be public. Nothing about the user's own data goes in it — not in code, tests, comments, commit messages or this prompt: no machine names, folder or file paths of their photos, names of people or chats, account names, home directories or tokens. Tests use made-up machines and folders (`laptop`, `Pictures/Camera`). A fact an agent here needs about this host's photos goes in a noteboard note tagged `photo-store`.

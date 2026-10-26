-- photo-store schema. Create-only and idempotent: Open() runs this whole file
-- on every boot. A new column goes in an additive migration in store.go, never
-- in an edit to a CREATE TABLE below.

PRAGMA foreign_keys = ON;

-- One photo or video from the user's own files. The original stays where it
-- was found (source_machine, source_path); this store keeps a thumbnail
-- (thumbnails/<sha256>.jpg in the data directory) and what is known about it.
-- Nothing is hard-deleted: DELETE stamps deleted_at, and every update, delete
-- and restore first copies the whole row into photo_revisions.
CREATE TABLE IF NOT EXISTS photos (
    id              TEXT PRIMARY KEY,              -- photo_000001
    seq             INTEGER NOT NULL UNIQUE,
    sha256          TEXT NOT NULL UNIQUE,          -- of the original file; what an import finds a photo again by
    source_machine  TEXT NOT NULL,                 -- where the original is (a machine name)
    source_path     TEXT NOT NULL,                 -- its path there, as found
    folder          TEXT NOT NULL DEFAULT '',      -- the collection it came from (Pictures/Camera)
    media_type      TEXT NOT NULL,                 -- image/jpeg, video/mp4
    taken_at        INTEGER NOT NULL DEFAULT 0,    -- epoch seconds; 0 = unknown
    date_source     TEXT NOT NULL,                 -- vocabulary.go: exif | filename | mtime | unknown
    latitude        REAL,                          -- NULL when the file carries no GPS
    longitude       REAL,
    width           INTEGER NOT NULL DEFAULT 0,
    height          INTEGER NOT NULL DEFAULT 0,
    description     TEXT NOT NULL DEFAULT '',      -- markdown; [text](ref:<id>) links name people and events
    described_by    TEXT NOT NULL DEFAULT '',      -- who wrote the description: a model, a session, the user
    tags            TEXT NOT NULL DEFAULT '[]',    -- JSON array of strings
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    deleted_at      INTEGER NOT NULL DEFAULT 0,
    purged_at       INTEGER NOT NULL DEFAULT 0     -- when its thumbnail and description were removed for good; 0 = never
);
CREATE INDEX IF NOT EXISTS idx_photos_taken  ON photos(deleted_at, taken_at, seq);
CREATE INDEX IF NOT EXISTS idx_photos_folder ON photos(folder, deleted_at, taken_at);

CREATE TABLE IF NOT EXISTS photo_revisions (
    revision_id     INTEGER PRIMARY KEY AUTOINCREMENT,
    photo_id        TEXT NOT NULL REFERENCES photos(id),
    reason          TEXT NOT NULL,                 -- update | import | delete | restore
    replaced_at     INTEGER NOT NULL,
    row_json        TEXT NOT NULL                  -- the whole row as it was, as JSON
);
CREATE INDEX IF NOT EXISTS idx_photo_revisions_photo ON photo_revisions(photo_id, revision_id DESC);

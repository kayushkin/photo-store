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

-- Faces a face-recognition model found on a photo's thumbnail. A photo is
-- scanned once per model; face_scans records the scan even when it found no
-- face, so a scanner knows what is left. The box is in fractions of the
-- thumbnail's width and height, and may run past its edges a little where a
-- face is cut off. The embedding is the model's description of the face:
-- little-endian float32s, normalised to length 1, so two faces' cosine
-- similarity is their dot product.
CREATE TABLE IF NOT EXISTS face_scans (
    photo_id        TEXT NOT NULL REFERENCES photos(id),
    model           TEXT NOT NULL,
    face_count      INTEGER NOT NULL,
    scanned_at      INTEGER NOT NULL,
    PRIMARY KEY (photo_id, model)
);

CREATE TABLE IF NOT EXISTS faces (
    id              TEXT PRIMARY KEY,              -- face_000001
    seq             INTEGER NOT NULL UNIQUE,
    photo_id        TEXT NOT NULL REFERENCES photos(id),
    model           TEXT NOT NULL,
    box_left        REAL NOT NULL,
    box_top         REAL NOT NULL,
    box_right       REAL NOT NULL,
    box_bottom      REAL NOT NULL,
    detection_score REAL NOT NULL,
    embedding       BLOB NOT NULL,
    created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_faces_photo ON faces(photo_id, model);

-- Who is in a photo: a person in people-store (person_000001), by id, never
-- by name. A link is suggested (a matcher's guess, waiting) or confirmed, or
-- rejected, which no matcher may undo: a later suggestion for a rejected pair
-- is kept out. face_id is the face the person is, when known. evidence says
-- why in markdown, with [text](ref:<id>) chips for the chats and photos it
-- rests on. Every change first copies the row to photo_person_revisions.
CREATE TABLE IF NOT EXISTS photo_people (
    id              TEXT PRIMARY KEY,              -- photo_person_000001
    seq             INTEGER NOT NULL UNIQUE,
    photo_id        TEXT NOT NULL REFERENCES photos(id),
    person_id       TEXT NOT NULL,
    face_id         TEXT REFERENCES faces(id),
    status          TEXT NOT NULL CHECK (status IN ('suggested', 'confirmed', 'rejected')),
    confidence      REAL NOT NULL DEFAULT 0,
    evidence        TEXT NOT NULL DEFAULT '',
    proposed_by     TEXT NOT NULL,
    decided_by      TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL,
    UNIQUE (photo_id, person_id)
);
CREATE INDEX IF NOT EXISTS idx_photo_people_person ON photo_people(person_id, status);

CREATE TABLE IF NOT EXISTS photo_person_revisions (
    revision_id     INTEGER PRIMARY KEY AUTOINCREMENT,
    photo_person_id TEXT NOT NULL REFERENCES photo_people(id),
    replaced_at     INTEGER NOT NULL,
    row_json        TEXT NOT NULL
);

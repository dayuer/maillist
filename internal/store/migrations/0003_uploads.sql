-- Images uploaded through `mailcheap upload`.
--
-- The bytes live in <data-dir>/uploads/<id>.<ext>; this table is what makes a URL resolvable and
-- lets an operator see what a newsletter is using.
CREATE TABLE uploads (
    id         TEXT PRIMARY KEY,
    file_name  TEXT NOT NULL,      -- the name the operator's file had, for humans
    slug       TEXT NOT NULL,      -- slugify(file_name), kept for future titles and alt text
    media_type TEXT NOT NULL,      -- image/png | image/jpeg | image/gif | image/webp
    extension  TEXT NOT NULL,      -- png | jpg | gif | webp
    bytes      INTEGER NOT NULL,
    created_at TEXT NOT NULL
);
CREATE INDEX uploads_created ON uploads(created_at DESC);

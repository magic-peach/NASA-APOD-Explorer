-- Initial schema. IF NOT EXISTS makes this safe to apply to databases that
-- were created before migrations were tracked.

CREATE TABLE IF NOT EXISTS users (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    username TEXT UNIQUE NOT NULL,
    email TEXT UNIQUE NOT NULL,
    password_hash TEXT NOT NULL,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

-- UNIQUE(user_id, apod_date) makes saving the same day twice impossible at
-- the database level, whatever the application code does.
CREATE TABLE IF NOT EXISTS favorites (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    apod_date TEXT NOT NULL,
    image_url TEXT,
    title TEXT,
    saved_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(user_id, apod_date)
);

CREATE TABLE IF NOT EXISTS history (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    apod_date TEXT NOT NULL,
    title TEXT,
    viewed_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS apod_cache (
    date TEXT PRIMARY KEY,
    title TEXT,
    explanation TEXT,
    image_url TEXT,
    media_type TEXT,
    hd_url TEXT,
    stored_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS comments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    image_date TEXT,
    comment_text TEXT,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS ratings (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    image_date TEXT,
    rating INTEGER CHECK(rating BETWEEN 1 AND 5),
    UNIQUE(user_id, image_date)
);

CREATE TABLE IF NOT EXISTS image_stats (
    image_date TEXT PRIMARY KEY,
    views INTEGER DEFAULT 0,
    likes INTEGER DEFAULT 0,
    comments INTEGER DEFAULT 0
);

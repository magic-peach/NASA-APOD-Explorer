-- Keep the credit line and video thumbnail when caching APOD entries, so
-- cached entries render exactly like fresh ones.
ALTER TABLE apod_cache ADD COLUMN copyright TEXT;
ALTER TABLE apod_cache ADD COLUMN thumbnail_url TEXT;

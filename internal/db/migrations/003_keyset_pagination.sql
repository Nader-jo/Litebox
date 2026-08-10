CREATE INDEX idx_threads_folder_page
    ON threads(is_trashed, is_archived, latest_message_at DESC, id DESC);

CREATE INDEX idx_threads_starred_page
    ON threads(is_trashed, is_starred, latest_message_at DESC, id DESC);

CREATE INDEX idx_threads_latest_page
    ON threads(latest_message_at DESC, id DESC);

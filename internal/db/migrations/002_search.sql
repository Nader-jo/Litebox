CREATE VIRTUAL TABLE message_search USING fts5(
    message_id UNINDEXED,
    thread_id UNINDEXED,
    subject,
    sender,
    recipients,
    body,
    tokenize='unicode61'
);

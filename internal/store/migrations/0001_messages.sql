CREATE TABLE messages (
    id INTEGER PRIMARY KEY,
    received_at INTEGER NOT NULL,
    raw TEXT NOT NULL,
    xml TEXT,
    transform_error TEXT CHECK (transform_error IN ('COULD_NOT_PARSE_HL7')),
    ack_sent_at INTEGER,
    ack TEXT,
    ack_outcome TEXT CHECK (ack_outcome IN ('SUCCESS', 'FAILED')),
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch()),
    CHECK (xml IS NULL OR transform_error IS NULL),
    CHECK ((ack IS NULL) = (ack_sent_at IS NULL)),
    CHECK ((ack IS NULL) = (ack_outcome IS NULL))
) STRICT;
CREATE TABLE delivery_attempts (
    id INTEGER PRIMARY KEY,
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    attempted_at INTEGER NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('SUCCESS', 'FAILED')),
    response_status_code INTEGER,
    response_body TEXT,
    payload TEXT NOT NULL,
    origin TEXT NOT NULL CHECK (origin IN ('INBOUND', 'REPLAY')),
    created_at INTEGER NOT NULL DEFAULT (unixepoch()),
    updated_at INTEGER NOT NULL DEFAULT (unixepoch())
) STRICT;
CREATE INDEX delivery_attempts_by_message ON delivery_attempts(message_id);
CREATE INDEX messages_by_received_at ON messages(received_at);
CREATE TRIGGER messages_touch_updated_at AFTER UPDATE ON messages FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE messages SET updated_at = unixepoch() WHERE id = NEW.id;
END;
CREATE TRIGGER delivery_attempts_touch_updated_at AFTER UPDATE ON delivery_attempts FOR EACH ROW
WHEN NEW.updated_at = OLD.updated_at
BEGIN
    UPDATE delivery_attempts SET updated_at = unixepoch() WHERE id = NEW.id;
END;

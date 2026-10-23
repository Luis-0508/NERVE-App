-- Secrets removed from command output before storage, counted per kind
-- (internal/redact). Results are write-once, so the counts are set on insert.
-- Numbered 000013: 000011 and 000012 are reserved for agent deployment keys
-- (K-22) and device classification (K-16).
ALTER TABLE remote_command_results
    ADD COLUMN redactions jsonb NOT NULL DEFAULT '{}'::jsonb
        CHECK (jsonb_typeof(redactions) = 'object' AND octet_length(redactions::text) <= 1024);

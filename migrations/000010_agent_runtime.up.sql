-- Agent process lifecycle as reported in heartbeats. All columns are nullable:
-- agents released before this migration do not report them.
ALTER TABLE agents
    ADD COLUMN run_mode text CHECK (run_mode IN ('windows_service', 'systemd', 'foreground')),
    ADD COLUMN process_started_at timestamptz,
    ADD COLUMN host_boot_at timestamptz,
    -- Set by the final heartbeat of a graceful shutdown. An agent is shown as
    -- stopped (not merely offline) while this is newer than last_seen_at.
    ADD COLUMN last_shutdown_at timestamptz;

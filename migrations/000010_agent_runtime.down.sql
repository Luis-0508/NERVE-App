ALTER TABLE agents
    DROP COLUMN IF EXISTS last_shutdown_at,
    DROP COLUMN IF EXISTS host_boot_at,
    DROP COLUMN IF EXISTS process_started_at,
    DROP COLUMN IF EXISTS run_mode;

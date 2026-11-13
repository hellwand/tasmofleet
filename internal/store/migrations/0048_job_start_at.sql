-- 0048_job_start_at: an update job can wait for a start time (#603).
--
-- start_at is when the job may start, in milliseconds like deadline_at; 0 starts at once, which is what every
-- job made before this migration did. not_started marks a job that ended cancelled without starting because
-- its start time had passed by more than the late-start window (nothing was sent to the device). Additive only:
-- the index ota_jobs_one_active is unchanged, so a waiting job (state 'scheduled') holds its device like any
-- other job that is not finished.
ALTER TABLE ota_jobs ADD COLUMN start_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE ota_jobs ADD COLUMN not_started INTEGER NOT NULL DEFAULT 0;

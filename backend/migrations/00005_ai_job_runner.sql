-- +goose Up
-- The lease alone cannot decide who owns a running job: it is a wall-clock
-- deadline, and a host that sleeps or a container whose clock jumps makes it
-- read as expired within seconds. A second worker then claims a job the first
-- is still running, and both drive the whole conveyor.
--
-- runner_id names the claim itself. Every write a worker makes is conditional
-- on still holding it, so a worker that lost the job stops at the next stage
-- instead of duplicating the work and overwriting the result.
ALTER TABLE ai_jobs ADD COLUMN runner_id uuid;

-- +goose Down
ALTER TABLE ai_jobs DROP COLUMN runner_id;

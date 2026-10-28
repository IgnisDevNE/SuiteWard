-- +goose Up
--
-- The canonical job whose counts an approval-missing check states (docs/contracts/persistence.md, "Changes to pr_checks"). The first message of that check
-- goes out while the canonical run is pending, and the reason does not change when the run completes, so the ledger compares this id: the job that completes
-- gives the check a new generation and a new message with the counts. 0 is none. pr_checks is updated in place, so a row from before this migration reads 0: it
-- counted no job.
ALTER TABLE pr_checks
    ADD COLUMN counted_job bigint NOT NULL DEFAULT 0,
    ADD CONSTRAINT pr_checks_counted_job_check CHECK (counted_job >= 0);

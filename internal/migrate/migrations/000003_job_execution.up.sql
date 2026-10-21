ALTER TABLE jobs
    DROP CONSTRAINT jobs_state_check,
    ADD CONSTRAINT jobs_state_check CHECK (state IN ('available', 'running', 'succeeded')),
    ADD COLUMN completed_at timestamptz,
    ADD CONSTRAINT jobs_completion_check CHECK ((state = 'succeeded') = (completed_at IS NOT NULL));

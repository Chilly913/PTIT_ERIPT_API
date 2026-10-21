DROP TABLE IF EXISTS attempt_files;
DROP INDEX IF EXISTS ix_quiz_attempts_graded_by;
DROP INDEX IF EXISTS ix_quiz_attempts_grading_pending;
ALTER TABLE quiz_attempts
    DROP CONSTRAINT fk_quiz_attempts_graded_by,
    DROP CONSTRAINT ck_quiz_attempts_grading,
    DROP CONSTRAINT ck_quiz_attempts_grading_status,
    DROP COLUMN graded_at,
    DROP COLUMN graded_by,
    DROP COLUMN grading,
    DROP COLUMN grading_status;

-- Questions of the new formats cannot exist under the old constraint, so they are removed first.
DELETE FROM questions WHERE parent_question_id IS NOT NULL;
DELETE FROM questions WHERE type IN ('cloze', 'classification', 'passage', 'file_upload');
DROP INDEX IF EXISTS ix_questions_parent_question_id;
ALTER TABLE questions
    DROP CONSTRAINT ck_questions_parent,
    DROP CONSTRAINT fk_questions_parent_question_id,
    DROP COLUMN parent_question_id;
ALTER TABLE questions DROP CONSTRAINT ck_questions_type;
ALTER TABLE questions ADD CONSTRAINT ck_questions_type CHECK (type IN (
    'single_choice', 'multiple_choice', 'true_false', 'essay',
    'matching', 'ordering', 'short_answer', 'numerical'));

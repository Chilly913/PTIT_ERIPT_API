-- 000095_question_formats: more question formats.
--   cloze           text with {{1}} {{2}} blanks; one question_answers row per accepted text, sub_content = blank id
--   classification  drag items into categories; content = item, sub_content = category
--   passage         a reading text that groups child questions (questions.parent_question_id)
--   file_upload     the learner attaches a file; graded by a human (answer storage comes with the grading flow)
-- parent_question_id: child questions of a passage. They are never drawn or picked on their own, only through the passage.
ALTER TABLE questions DROP CONSTRAINT ck_questions_type;
ALTER TABLE questions ADD CONSTRAINT ck_questions_type CHECK (type IN (
    'single_choice', 'multiple_choice', 'true_false', 'essay',
    'matching', 'ordering', 'short_answer', 'numerical',
    'cloze', 'classification', 'passage', 'file_upload'));

ALTER TABLE questions
    ADD COLUMN parent_question_id bigint,
    ADD CONSTRAINT fk_questions_parent_question_id FOREIGN KEY (parent_question_id)
        REFERENCES questions (id) ON DELETE CASCADE,
    ADD CONSTRAINT ck_questions_parent CHECK (parent_question_id IS NULL OR parent_question_id <> id);
CREATE INDEX ix_questions_parent_question_id ON questions (parent_question_id) WHERE parent_question_id IS NOT NULL;

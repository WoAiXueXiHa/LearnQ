-- Old question sets retain their original points without inferred associations.
ALTER TABLE practice_questions ADD COLUMN reference_items_json JSON NULL;

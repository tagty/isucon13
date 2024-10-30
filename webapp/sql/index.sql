ALTER TABLE livestream_tags
	ADD INDEX idx_livestream_id (livestream_id);

ALTER TABLE livestream_tags
	ADD INDEX idx_tag_id_livestream_id (tag_id, livestream_id);

ALTER TABLE icons
	ADD INDEX idx_user_id (user_id);

ALTER TABLE themes
	ADD INDEX idx_user_id (user_id);

ALTER TABLE reservation_slots
	ADD INDEX idx_end_at (end_at);

ALTER TABLE ng_words
	ADD INDEX idx_livestream_id_user_id (livestream_id, user_id);

ALTER TABLE livestreams
	ADD INDEX idx_user_id (user_id);

ALTER TABLE livecomments
	ADD INDEX idx_livestream_id (livestream_id);

ALTER TABLE reactions
	ADD INDEX idx_livestream_id (livestream_id, created_at);

ALTER TABLE livestream_viewers_history
	ADD INDEX idx_user_id_livestream_id (user_id, livestream_id);

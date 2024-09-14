
ALTER TABLE livestream_tags
	ADD INDEX idx_livestream_id (livestream_id);

ALTER TABLE icons
	ADD INDEX idx_user_id (user_id);

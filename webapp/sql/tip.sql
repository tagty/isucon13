ALTER TABLE livestreams
	ADD `tip` BIGINT NOT NULL DEFAULT 0;

ALTER TABLE users
	ADD `tip` BIGINT NOT NULL DEFAULT 0;

ALTER TABLE livestreams
	ADD `reactions_count` BIGINT NOT NULL DEFAULT 0;

ALTER TABLE users
	ADD `reactions_count` BIGINT NOT NULL DEFAULT 0;

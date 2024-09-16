DELIMITER $$ CREATE TRIGGER update_icons
	BEFORE UPDATE ON icons
	FOR EACH ROW
BEGIN
	SET NEW.icon_hash = SHA2(NEW.image, 256);
END $$

DELIMITER $$ CREATE TRIGGER insert_icons
	BEFORE INSERT ON icons
	FOR EACH ROW
BEGIN
	SET NEW.icon_hash = SHA2(NEW.image, 256);
END $$

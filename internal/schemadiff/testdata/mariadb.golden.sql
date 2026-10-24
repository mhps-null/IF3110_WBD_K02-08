CREATE TABLE `app space`.`select` (
  `a" b``c()` int
);

DROP INDEX `idx" space``` ON `app space`.`order.items`;

ALTER TABLE `audit`.`order.items` ADD COLUMN `select` text;

CREATE INDEX `idx" space``` USING BTREE ON `app space`.`order.items` (`a" b``c()`, `select`);

ALTER TABLE `app space`.`order.items` ADD CONSTRAINT `fk" space``` FOREIGN KEY (`a" b``c()`) REFERENCES `app space`.`parent.table` (`select`) ON DELETE CASCADE ON UPDATE SET NULL;

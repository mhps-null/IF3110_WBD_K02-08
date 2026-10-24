
-- pg_b
CREATE TABLE qa11_a."order ""demo""" (
"select" INTEGER,
"nama kolom" TEXT,
"kutip""kolom" TEXT
);

-- maria_b
CREATE TABLE `qa11_order``demo` (`select` INT, `nama kolom` TEXT, `kutip``kolom` TEXT) ENGINE=InnoDB;

-- pg_a
CREATE INDEX qa11_partial ON qa11_a.items (id) WHERE id > 0;

-- pg_b
CREATE INDEX qa11_partial ON qa11_a.items (id) WHERE id > 0; DROP INDEX qa11_a.qa11_partial; CREATE INDEX qa11_partial ON qa11_a.items (id) WHERE id > 10;

-- maria_a
CREATE TABLE qa11_parent (id INT PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE qa11_child (id INT PRIMARY KEY, parent_id INT NULL, CONSTRAINT qa11_fk FOREIGN KEY (parent_id) REFERENCES qa11_parent(id) ON DELETE RESTRICT ON UPDATE RESTRICT) ENGINE=InnoDB;

-- maria_b
CREATE TABLE qa11_parent (id INT PRIMARY KEY) ENGINE=InnoDB;
CREATE TABLE qa11_child (id INT PRIMARY KEY, parent_id INT NULL, CONSTRAINT qa11_fk FOREIGN KEY (parent_id) REFERENCES qa11_parent(id) ON DELETE RESTRICT ON UPDATE RESTRICT) ENGINE=InnoDB;

-- maria_b
ALTER TABLE qa11_child DROP FOREIGN KEY qa11_fk; ALTER TABLE qa11_child ADD CONSTRAINT qa11_fk FOREIGN KEY (parent_id) REFERENCES qa11_parent(id) ON DELETE CASCADE ON UPDATE SET NULL;

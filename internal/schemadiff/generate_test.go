package schemadiff

import (
	"strings"
	"testing"

	"github.com/example/if3110-milestone1/internal/contract"
)

func TestGenerateSQL_Postgres(t *testing.T) {
	changes := []Change{
		{
			Type: DropTable, TableName: "old_logs", Destructive: true,
		},
		{
			Type: CreateTable, TableName: "users",
			TableDef: &contract.Table{
				Name: "users",
				Columns: []contract.Column{
					{Name: "id", DataType: "int", Nullable: false},
					{Name: "email", DataType: "varchar(100)", Nullable: false},
				},
			},
		},
		{
			Type: AlterColumn, TableName: "users", ColumnName: "email",
			OldColumn:   &contract.Column{Name: "email", DataType: "varchar(100)", Nullable: false},
			NewColumn:   &contract.Column{Name: "email", DataType: "varchar(255)", Nullable: true},
			Destructive: true,
		},
	}

	sql := GenerateSQL(changes, contract.EnginePostgres)

	// cek apakah ada peringatan destruktif
	if !strings.Contains(sql, "-- WARNING: DESTRUCTIVE CHANGE") {
		t.Error("missing destructive warning")
	}

	// cek CREATE TABLE
	if !strings.Contains(sql, `CREATE TABLE "users"`) {
		t.Error("missing CREATE TABLE")
	}
	if !strings.Contains(sql, `"email" varchar(100) NOT NULL`) {
		t.Error("missing column definition")
	}

	// cek ALTER COLUMN (Postgres menggunakan TYPE)
	if !strings.Contains(sql, `ALTER TABLE "users" ALTER COLUMN "email" TYPE varchar(255);`) {
		t.Error("missing ALTER TYPE")
	}
	if !strings.Contains(sql, `ALTER TABLE "users" ALTER COLUMN "email" DROP NOT NULL;`) {
		t.Error("missing DROP NOT NULL")
	}

	// cek DROP TABLE diposisikan paling bawah
	dropIndex := strings.Index(sql, `DROP TABLE "old_logs";`)
	createIndex := strings.Index(sql, `CREATE TABLE "users"`)
	if dropIndex < createIndex {
		t.Error("DROP TABLE should be executed after CREATE TABLE")
	}
}

func TestGenerateSQL_MySQL(t *testing.T) {
	changes := []Change{
		{
			Type: AlterColumn, TableName: "users", ColumnName: "email",
			OldColumn: &contract.Column{Name: "email", DataType: "varchar(100)", Nullable: false},
			NewColumn: &contract.Column{Name: "email", DataType: "varchar(255)", Nullable: true},
		},
	}

	sql := GenerateSQL(changes, contract.EngineMySQL)

	// MySQL pakai MODIFY COLUMN untuk mengubah tipe + nullability sekaligus
	expected := "ALTER TABLE `users` MODIFY COLUMN `email` varchar(255);"
	if !strings.Contains(sql, expected) {
		t.Errorf("expected MySQL alter column, got:\n%s", sql)
	}
}

func TestGenerateSQL_ConstraintAndIndexDDLPostgres(t *testing.T) {
	changes := []Change{
		{Type: DropTable, TableName: "teams", Destructive: true},
		{Type: DropConstraint, TableName: "users", Constraint: &contract.Constraint{Name: "fk_users_teams", Type: "FOREIGN KEY"}, Destructive: true},
		{Type: AddConstraint, TableName: "users", Constraint: &contract.Constraint{Name: "fk_users_groups", Type: "FOREIGN KEY", Definition: "FOREIGN KEY (group_id) REFERENCES public.groups(id) ON DELETE CASCADE"}},
		{Type: AddConstraint, TableName: "users", Constraint: &contract.Constraint{Name: "ck_users_age", Type: "CHECK", Definition: "CHECK ((age > 0))"}},
		{Type: AddIndex, TableName: "users", Index: &contract.Index{Name: "idx_users_email", Columns: []string{"email"}, Unique: true, Method: "btree"}},
		{Type: DropIndex, TableName: "users", Index: &contract.Index{Name: "idx_users_old", Columns: []string{"old"}}},
	}
	sql := GenerateSQL(changes, contract.EnginePostgres)
	for _, expected := range []string{
		`ALTER TABLE "users" DROP CONSTRAINT "fk_users_teams";`,
		`ALTER TABLE "users" ADD CONSTRAINT "fk_users_groups" FOREIGN KEY (group_id) REFERENCES public.groups(id) ON DELETE CASCADE;`,
		`ALTER TABLE "users" ADD CONSTRAINT "ck_users_age" CHECK ((age > 0));`,
		`CREATE UNIQUE INDEX "idx_users_email" ON "users" USING btree ("email");`,
		`DROP INDEX "idx_users_old";`,
	} {
		if !strings.Contains(sql, expected) {
			t.Errorf("generated SQL missing %q:\n%s", expected, sql)
		}
	}
	if strings.Index(sql, `DROP CONSTRAINT "fk_users_teams"`) > strings.Index(sql, `DROP TABLE "teams"`) {
		t.Error("foreign key must be dropped before referenced table is dropped")
	}
}

func TestGenerateSQL_ConstraintAndIndexDDLMySQL(t *testing.T) {
	changes := []Change{
		{Type: DropConstraint, TableName: "users", Constraint: &contract.Constraint{Name: "fk_users_team", Type: "FOREIGN KEY"}},
		{Type: AddConstraint, TableName: "users", Constraint: &contract.Constraint{Name: "fk_users_team", Type: "FOREIGN KEY", Columns: []string{"team_id"}, RefTable: "app.teams", RefColumns: []string{"id"}}},
		{Type: AddConstraint, TableName: "users", Constraint: &contract.Constraint{Name: "ck_users_age", Type: "CHECK", Definition: "age > 0"}},
		{Type: AddIndex, TableName: "users", Index: &contract.Index{Name: "idx_users_email", Columns: []string{"email"}, Unique: true, Method: "BTREE"}},
		{Type: DropIndex, TableName: "users", Index: &contract.Index{Name: "idx_users_old", Columns: []string{"old"}}},
	}
	sql := GenerateSQL(changes, contract.EngineMySQL)
	for _, expected := range []string{
		"ALTER TABLE `users` DROP FOREIGN KEY `fk_users_team`;",
		"ALTER TABLE `users` ADD CONSTRAINT `fk_users_team` FOREIGN KEY (`team_id`) REFERENCES `app`.`teams` (`id`);",
		"ALTER TABLE `users` ADD CONSTRAINT `ck_users_age` CHECK (age > 0);",
		"CREATE UNIQUE INDEX `idx_users_email` USING BTREE ON `users` (`email`);",
		"DROP INDEX `idx_users_old` ON `users`;",
	} {
		if !strings.Contains(sql, expected) {
			t.Errorf("generated SQL missing %q:\n%s", expected, sql)
		}
	}
}

func TestGenerateSQL_MySQLSpecialIndexTypes(t *testing.T) {
	changes := []Change{
		{Type: AddIndex, TableName: "articles", Index: &contract.Index{Name: "ft_articles_body", Columns: []string{"body"}, Method: "FULLTEXT"}},
		{Type: AddIndex, TableName: "places", Index: &contract.Index{Name: "sp_places_location", Columns: []string{"location"}, Method: "SPATIAL"}},
	}
	sql := GenerateSQL(changes, contract.EngineMySQL)
	for _, expected := range []string{
		"CREATE FULLTEXT INDEX `ft_articles_body` ON `articles` (`body`);",
		"CREATE SPATIAL INDEX `sp_places_location` ON `places` (`location`);",
	} {
		if !strings.Contains(sql, expected) {
			t.Errorf("generated SQL missing %q:\n%s", expected, sql)
		}
	}
}

func TestGenerateSQL_MariaDBDropsCheckConstraint(t *testing.T) {
	sql := GenerateSQL([]Change{{
		Type: DropConstraint, TableName: "users",
		Constraint: &contract.Constraint{Name: "ck_users_age", Type: "CHECK"},
	}}, contract.EngineMariaDB)
	if !strings.Contains(sql, "ALTER TABLE `users` DROP CONSTRAINT `ck_users_age`;") {
		t.Fatalf("MariaDB CHECK constraint removal is wrong: %s", sql)
	}
}

func TestGenerateSQL_QuotesNonstandardIndexColumnNames(t *testing.T) {
	sql := GenerateSQL([]Change{{
		Type: AddIndex, TableName: "users",
		Index: &contract.Index{Name: "idx_users_last_name", Columns: []string{"last-name"}},
	}}, contract.EnginePostgres)
	if !strings.Contains(sql, `CREATE INDEX "idx_users_last_name" ON "users" ("last-name");`) {
		t.Fatalf("nonstandard index column identifier was not quoted: %s", sql)
	}
}

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

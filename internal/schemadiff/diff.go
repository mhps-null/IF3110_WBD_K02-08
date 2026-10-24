package schemadiff

import (
	"fmt"

	"github.com/example/if3110-milestone1/internal/contract"
)

type ChangeType string

const (
	CreateTable ChangeType = "CREATE_TABLE"
	DropTable ChangeType = "DROP_TABLE"
	AddColumn ChangeType = "ADD_COLUMN"
	DropColumn ChangeType = "DROP_COLUMN"
	AlterColumn ChangeType = "ALTER_COLUMN"
)

// merepresentasikan 1 operasi DDL yg dibutuhkan utk ngubah schema 'a' menjadi schema 'b'
type Change struct {
	Type ChangeType
	TableName string
	ColumnName string
	OldColumn *contract.Column // diisi utk DROP_COLUMN atau ALTER_COLUMN
	NewColumn *contract.Column // diisi utk ADD_COLUMN atau ALTER_COLUMN
	TableDef *contract.Table // diisi utk CREATE_TABLE
	Destructive bool // true jika operasi menghilangkan data (DROP atau ALTER tipe)
}

// Diff membandingkan 2 SchemaSnapshot dan mengembalikan daftar perubahan yg diperlukan utk mengubah 'a' menjadi 'b'
func Diff(a, b contract.SchemaSnapshot) []Change {
	var changes []Change

	// map tabel berdasarkan nama
	tablesA := make(map[string]contract.Table)
	for _, t := range a.Tables {
		tablesA[t.Name] = t
	}

	tablesB := make(map[string]contract.Table)
	for _, t := range b.Tables {
		tablesB[t.Name] = t
	}

	// cari tabel yg dihapus atau dimodifikasi
	for name, tA := range tablesA {
		tB, exists := tablesB[name]
		if !exists {
			// tabel ada di A, tidak ada di B -> DROP TABLE
			changes = append(changes, Change{
				Type: DropTable,
				TableName: name,
				Destructive: true,
			})
			continue
		}

		// tabel ada di keduanya -> bandingkan kolom
		colChanges := diffColumns(tA, tB)
		changes = append(changes, colChanges...)
	}

	// cari tabel yg baru ditambahkan
	for name, tB := range tablesB {
		if _, exists := tablesA[name]; !exists {
			// tabel ada di B, tidak ada di A -> CREATE TABLE
			tableCopy := tB // hindari pointer ke variabel loop
			changes = append(changes, Change{
				Type: CreateTable,
				TableName: name,
				TableDef: &tableCopy,
			})
		}
	}

	return changes
}

// membandingkan kolom antara 2 tabel yang namanya sama
func diffColumns(tA, tB contract.Table) []Change {
	var changes []Change

	colsA := make(map[string]contract.Column)
	for _, c := range tA.Columns {
		colsA[c.Name] = c
	}

	colsB := make(map[string]contract.Column)
	for _, c := range tB.Columns {
		colsB[c.Name] = c
	}

	// kolom dihapus atau diubah
	for name, cA := range colsA {
		cB, exists := colsB[name]
		if !exists {
			changes = append(changes, Change{
				Type: DropColumn,
				TableName: tA.Name,
				ColumnName: name,
				OldColumn: &cA,
				Destructive: true,
			})
			continue
		}

		// kolom ada di keduanya -> cek apakah ada perubahan tipe, nullable, atau default
		if cA.DataType != cB.DataType || cA.Nullable != cB.Nullable || !defaultsEqual(cA.Default, cB.Default) {
			// menghapus NULL atau mengubah tipe data dianggap destruktif
			isDestructive := cA.DataType != cB.DataType || (cA.Nullable && !cB.Nullable)
			changes = append(changes, Change{
				Type: AlterColumn,
				TableName: tA.Name,
				ColumnName: name,
				OldColumn: &cA,
				NewColumn: &cB,
				Destructive: isDestructive,
			})
		}
	}

	// kolom baru ditambahkan
	for name, cB := range colsB {
		if _, exists := colsA[name]; !exists {
			colCopy := cB
			changes = append(changes, Change{
				Type: AddColumn,
				TableName: tB.Name,
				ColumnName: name,
				NewColumn: &colCopy,
			})
		}
	}

	return changes
}

// membantu membandingkan dua *string (nilai default kolom)
func defaultsEqual(a, b *string) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return *a == *b
}

//  helper utk menampilkan ringkasan perubahan di UI
func FormatChange(c Change) string {
	switch c.Type {
	case CreateTable:
		return fmt.Sprintf("Create table %s", c.TableName)
	case DropTable:
		return fmt.Sprintf("Drop table %s", c.TableName)
	case AddColumn:
		return fmt.Sprintf("Add column %s.%s (%s)", c.TableName, c.ColumnName, c.NewColumn.DataType)
	case DropColumn:
		return fmt.Sprintf("Drop column %s.%s", c.TableName, c.ColumnName)
	case AlterColumn:
		return fmt.Sprintf("Alter column %s.%s (from %s to %s)", c.TableName, c.ColumnName, c.OldColumn.DataType, c.NewColumn.DataType)
	default:
		return string(c.Type)
	}
}

package postgres_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Cagritasoz/concurrent-ticket-booking-system/internal/platform/postgres"
)

func TestIsViolation(t *testing.T) {
	// The constraint names are the ones Postgres generates: table_column_fkey, _key and _check.
	pool := newPool(t, `
		CREATE TABLE parents (id int PRIMARY KEY);
		CREATE TABLE children (
			id        int  PRIMARY KEY,
			parent_id int  NOT NULL REFERENCES parents,
			name      text NOT NULL UNIQUE,
			age       int  CHECK (age >= 0)
		);
		INSERT INTO parents VALUES (1);
		INSERT INTO children VALUES (1, 1, 'taken', 5);
	`)

	// insert tries to add a child and returns Postgres's error, if any.
	insert := func(parentID int, name string, age int) error {
		_, err := pool.Exec(t.Context(), "INSERT INTO children VALUES (2, $1, $2, $3)", parentID, name, age)
		return err
	}

	tests := []struct {
		name                   string
		err                    error
		unique, foreign, check bool // what IsUniqueViolation, IsForeignKeyViolation, IsCheckViolation report
		constraint             string
	}{
		{name: "duplicate value", err: insert(1, "taken", 5), unique: true, constraint: "children_name_key"},
		{name: "missing parent", err: insert(99, "new", 5), foreign: true, constraint: "children_parent_id_fkey"},
		{name: "broken check", err: insert(1, "new", -1), check: true, constraint: "children_age_check"},
		{name: "wrapped with %w", err: fmt.Errorf("create child: %w", insert(1, "taken", 5)), unique: true, constraint: "children_name_key"},
		{name: "another constraint", err: insert(1, "taken", 5), constraint: "children_other_key"},
		{name: "no error", err: nil, constraint: "children_name_key"},
		{name: "not a Postgres error", err: errors.New("children_name_key"), constraint: "children_name_key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := postgres.IsUniqueViolation(tt.err, tt.constraint); got != tt.unique {
				t.Errorf("IsUniqueViolation = %v, want %v (err: %v)", got, tt.unique, tt.err)
			}
			if got := postgres.IsForeignKeyViolation(tt.err, tt.constraint); got != tt.foreign {
				t.Errorf("IsForeignKeyViolation = %v, want %v (err: %v)", got, tt.foreign, tt.err)
			}
			if got := postgres.IsCheckViolation(tt.err, tt.constraint); got != tt.check {
				t.Errorf("IsCheckViolation = %v, want %v (err: %v)", got, tt.check, tt.err)
			}
		})
	}
}

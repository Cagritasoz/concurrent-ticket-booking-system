package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// Postgres reports every error with a five-character SQLSTATE code. These are the ones for broken
// constraints; the full list is at https://www.postgresql.org/docs/17/errcodes-appendix.html.
const (
	codeForeignKeyViolation = "23503"
	codeUniqueViolation     = "23505"
	codeCheckViolation      = "23514"
	codeExclusionViolation  = "23P01"
)

// The functions below let a module turn a broken constraint into its own error, like Spring's
// exception translation (DataIntegrityViolationException), but by constraint name, so each rule
// gets its own meaning:
//
//	if postgres.IsUniqueViolation(err, "layouts_hall_id_name_key") {
//		return ErrLayoutNameTaken
//	}
//
// Constraint names are part of the schema: Postgres generates them (table_column_check,
// table_column_key, table_column_fkey) unless the migration names them with CONSTRAINT.
// A row that wasn't found is not a constraint error: check it with errors.Is(err, pgx.ErrNoRows).

// IsUniqueViolation reports whether err is Postgres refusing a duplicate value of this unique
// constraint or unique index.
func IsUniqueViolation(err error, constraint string) bool {
	return isViolation(err, codeUniqueViolation, constraint)
}

// IsForeignKeyViolation reports whether err is Postgres refusing a row that points to a row that
// doesn't exist, or the delete of a row that others still point to, for this foreign key.
func IsForeignKeyViolation(err error, constraint string) bool {
	return isViolation(err, codeForeignKeyViolation, constraint)
}

// IsCheckViolation reports whether err is Postgres refusing a value that breaks this CHECK
// constraint.
func IsCheckViolation(err error, constraint string) bool {
	return isViolation(err, codeCheckViolation, constraint)
}

// IsExclusionViolation reports whether err is Postgres refusing a row that conflicts with another
// row under this EXCLUDE constraint, e.g. two events in one hall at overlapping times.
func IsExclusionViolation(err error, constraint string) bool {
	return isViolation(err, codeExclusionViolation, constraint)
}

func isViolation(err error, code, constraint string) bool {
	// pgx returns server errors as *pgconn.PgError, which has the code and the constraint name.
	// errors.As also finds it when it was wrapped with %w on the way up.
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code && pgErr.ConstraintName == constraint
}

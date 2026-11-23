# 11 · Migration failures and recovery

What happens when a migration fails, in each direction and at each version, and how to use
`make migrate-force` to recover. Adding a migration and the rules for migration files are in
[08 · Development](08-development.md#database-migrations).

## How the state is stored

golang-migrate keeps the state in the `schema_migrations` table, which holds **one row**:
`(version, dirty)`. It is a bookmark, not a history: Flyway's `flyway_schema_history` keeps a row
per migration, golang-migrate overwrites its single row.

- **version** is the number of the last migration that is fully applied, the six-digit prefix of
  its files: `000002_create_halls.up.sql` is version 2.
- **No migration applied** is an empty table. golang-migrate calls this version `-1`; our code
  shows it as 0 (in `make migrate-version` and in error messages), and `force version=0` means it.
- **dirty** means a migration started and was never marked as finished.

A Postgres advisory lock makes sure only one `cmd/migrate` changes a database at a time.

## How one migration file runs

golang-migrate runs every migration file in three steps:

| Step | What happens | Transaction |
|---|---|---|
| 1 | write the version the database is moving to, with `dirty = true` | its own |
| 2 | run the SQL file | one transaction for the whole file |
| 3 | write the same version with `dirty = false` | its own |

The version written in steps 1 and 3 is the version the database will be at **afterwards**:

| Running | Version written |
|---|---|
| the up file of migration N | N |
| the down file of migration N > 1 | N−1 |
| the down file of migration 1 | "no version", stored as a `-1` row (the only time a `-1` row exists, so the dirty flag has somewhere to live) |

If step 2 fails, or the process is stopped during it (Ctrl+C, a crash, a lost connection), Postgres
rolls the whole file back, but step 3 never runs, so the row stays dirty. While it is dirty, `up`
and `down` refuse to run with "the database is marked dirty at version N". `version` still works,
and `force` is the only command that clears the flag.

golang-migrate refuses even though Postgres rolled the file back, because it also supports
databases (e.g. MySQL) that can't roll back `CREATE TABLE`, where a failure can leave half a
migration behind. It can't tell which happened, so a person has to check.

## The four failure cases

In each case below the file failed, so Postgres rolled it back. That is what normally happens; the
one exception is in [the rare case](#the-rare-case-the-file-committed-but-the-version-wasnt-cleared).

### 1. The up file of migration 1 fails

Starting from an empty database:

| | `schema_migrations` |
|---|---|
| before | empty (no version) |
| step 1 writes | `(1, true)` |
| file 1 fails and is rolled back | `(1, true)` stays |

- `make migrate-version`: version 1, dirty.
- `make migrate-up` / `migrate-down`: "dirty at version 1".
- **Really applied:** nothing.
- **Fix:** correct file 1, `make migrate-force version=0`, `make migrate-up`.

`force version=0` sends golang-migrate `-1`, which empties the table. golang-migrate's own version 0
would mean "a migration numbered 0 is applied", and there is no such file.

### 2. The up file of migration N > 1 fails

For example migration 2, starting from version 1:

| | `schema_migrations` |
|---|---|
| before | `(1, false)` |
| step 1 writes | `(2, true)` |
| file 2 fails and is rolled back | `(2, true)` stays |

- `make migrate-version`: version 2, dirty.
- `make migrate-up` / `migrate-down`: "dirty at version 2".
- **Really applied:** 1.
- **Fix:** correct file 2, `make migrate-force version=1`, `make migrate-up` (it runs the corrected
  file 2).

This is the simplest case: the dirty version is the migration that failed.

### 3. The down file of migration N > 1 fails

For example migration 2, starting from version 2:

| | `schema_migrations` |
|---|---|
| before | `(2, false)` |
| step 1 writes | `(1, true)`, the version it is moving down to |
| file 2's down fails and is rolled back | `(1, true)` stays |

- `make migrate-version`: version 1, dirty.
- `make migrate-up` / `migrate-down`: "dirty at version 1".
- **Really applied:** 2. The rollback was undone, so everything migration 2 created still exists.
- **Fix:** correct file 2's down file, `make migrate-force version=2`, `make migrate-down`.

The trap: the dirty version (1) is **not** the migration that failed. Forcing 0 or 1 here would make
golang-migrate believe migration 2's tables are gone while they still exist.

### 4. The down file of migration 1 fails

Starting from version 1:

| | `schema_migrations` |
|---|---|
| before | `(1, false)` |
| step 1 writes | `(-1, true)`, "no version, dirty" |
| file 1's down fails and is rolled back | `(-1, true)` stays |

- `make migrate-version`: version 0, dirty.
- `make migrate-up` / `migrate-down`: "dirty at version 0".
- **Really applied:** 1. Everything migration 1 created still exists.
- **Fix:** correct file 1's down file, `make migrate-force version=1`, `make migrate-down`.

The same trap as case 3: "version 0" here does not mean the database is empty.

### Summary

| What failed | Row left behind | Shown as | Really applied | Force |
|---|---|---|---|---|
| up of 1 | `(1, true)` | 1, dirty | nothing | 0 |
| up of N > 1 | `(N, true)` | N, dirty | N−1 | N−1 |
| down of N > 1 | `(N−1, true)` | N−1, dirty | N | N |
| down of 1 | `(-1, true)` | 0, dirty | 1 | 1 |

A shortcut that follows from it: after a failed **up**, force one **below** the dirty version;
after a failed **down**, force one **above** it. Use it only after checking the database (step 3
below).

## Using force correctly

`make migrate-force version=N` overwrites the row with `(N, false)`, or empties the table for 0. It
runs no SQL and doesn't look at your tables. It only tells golang-migrate what is in the database,
and golang-migrate trusts it completely: the next `up` runs migration N+1, the next `down` runs
migration N's down file.

The one thing it checks is that N is 0 or the number of a migration in `migrations/`. Anything else
is refused before the row is touched, e.g. `force version 7: no migration has version 7; the latest
is 2, and 0 means no migration applied`. golang-migrate's own `force` accepts any number.

So the rule is: **force the version that is really in the database, found by looking, not the
version you would like it to be.**

1. Run `make migrate-version` and note the dirty version.
2. Read the error from the failed run. It is the Postgres error (e.g. `syntax error at or near
   "SELEC"`) and says which file failed and in which direction. The "dirty" error only appears on
   the runs after that.
3. Check the database by hand, e.g. in `psql`: `\dt` lists the tables, `\d venues` shows one table
   with its constraints. Does what the failed migration creates exist?
   - It exists: that migration is applied, force its number.
   - It doesn't: it isn't, force the number below it (0 for migration 1).
   - Some of it exists: undo the leftovers by hand and force the number below, or finish the
     change by hand and force its number. With Postgres this shouldn't happen, because each file is
     one transaction.
4. Fix the SQL file that failed. A failed up file was never applied, so editing it is fine; a fixed
   down file changes nothing a database already ran.
5. Force the version from step 3.
6. Run the command that failed again.

`force` can always be run again, so a wrong force is fixed by forcing the right number, as long as
no `up` or `down` ran in between.

## The rare case: the file committed but the version wasn't cleared

Steps 2 and 3 are separate transactions. If the process dies or the connection drops in the moment
after the file's transaction committed but before step 3, the row stays dirty although the file
**was** fully applied. Then:

| What was running | Row left behind | Really applied | Force |
|---|---|---|---|
| up of N | `(N, true)` | N | N |
| down of N | `(N−1, true)`, or `(-1, true)` for N = 1 | N−1 (0 for N = 1) | N−1 (0 for N = 1) |

The row looks exactly like a normal failure, and there is no Postgres error to read. Only checking
the database (step 3) tells the two apart, which is why the steps never skip it.

## What a wrong force leads to

| Mistake | What happens next |
|---|---|
| Forced too high: golang-migrate believes a migration is applied that isn't | `up` skips it, and the app fails on the first query that needs its tables. `down` runs its down file against tables that don't exist, which fails and makes the database dirty again. |
| Forced too low: golang-migrate believes a migration isn't applied that is | `up` runs its up file again, `CREATE TABLE` fails with "already exists", and the database is dirty again. Worse, `down` runs the down file of the migration **below** it, which can drop a table that still holds data. |
| Forced a number that has no migration file | Not possible: `force` refuses it and says which is the latest version. |

## How the code handles it

All in `internal/platform/postgres/migrate.go`:

- `Version` reads `schema_migrations` through the golang-migrate driver. golang-migrate's own
  `Version` turns a `-1` row into "no version" and drops the dirty flag, which would hide case 4.
- `Down` says "nothing to roll back" only at version 0 when it is not dirty. At a dirty 0 (case 4)
  it lets golang-migrate return its dirty error.
- `explain` turns golang-migrate's dirty error into a message with the dirty version (`-1` shown as
  0), what that version means after a failed up or down, and the `make migrate-force` command. It
  doesn't name a failed migration, because the dirty version alone doesn't tell which one failed.
- `Force(0)` sends `-1`, golang-migrate's "no version".
- `Force` refuses any other version that has no migration file, by walking the versions of the
  migration source (`First`, then `Next`). It checks each number, not just the range, so a
  number missing between two versions is refused too.

Tests in `migrate_test.go`, against a real database:

| Test | Case |
|---|---|
| `TestMigrator_FailedFirstMigrationIsForcedToZero` | 1: up of migration 1 fails, `force 0` gives a clean "no version", and `up` runs again from migration 1 |
| `TestMigrator_FailedMigrationMarksTheDatabaseDirty` | 2: up of migration 2 fails, the file is rolled back as a whole, `force 1` recovers |
| `TestMigrator_FailedDownMarksTheVersionBelowDirty` | 3: down of migration 2 fails, shown as 1, the table still exists, `force 2` recovers |
| `TestMigrator_FailedDownOfTheFirstMigrationStaysDirty` | 4: down of migration 1 fails, shown as 0 and dirty, `force 1` recovers |
| `TestMigrator_ForceRejectsAVersionWithoutAMigration` | `force` refuses versions without a file (also inside a gap), changes nothing then, and accepts real versions and 0 |
| `TestMigrator_ForceWithoutMigrationsOnlyAcceptsZero` | with no migrations at all, only `force 0` is accepted |

The rare case can't be produced reliably in a test (the process would have to die between two
transactions), and the code can't detect it either: only checking the database can.

package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Migration is one forward-only schema change.
type Migration struct {
	Version  int
	Name     string
	SQL      string
	Checksum string
}

// LoadMigrations reads migrations from an fs.FS. Files are named
// "NNNN_description.sql", for example "0001_evidence.sql".
//
// Migrations are forward-only: there are no down files. A down migration on a
// schema holding regulated financial records is a fiction — it cannot restore
// dropped rows, and in a system where audit records may not be deleted, running
// one would be a safety-invariant breach rather than a rollback. Recovery from a
// bad migration is a new forward migration plus, if data was lost, a restore.
func LoadMigrations(fsys fs.FS, dir string) ([]Migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("db: read migrations from %s: %w", dir, err)
	}

	var migrations []Migration
	seen := map[int]string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationName(entry.Name())
		if err != nil {
			return nil, err
		}
		if previous, dup := seen[version]; dup {
			return nil, fmt.Errorf("db: migration version %d is used by both %q and %q", version, previous, entry.Name())
		}
		seen[version] = entry.Name()

		raw, err := fs.ReadFile(fsys, path.Join(dir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("db: read migration %s: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(raw)
		migrations = append(migrations, Migration{
			Version:  version,
			Name:     name,
			SQL:      string(raw),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	return migrations, nil
}

func parseMigrationName(filename string) (version int, name string, err error) {
	base := strings.TrimSuffix(filename, ".sql")
	prefix, rest, found := strings.Cut(base, "_")
	if !found {
		return 0, "", fmt.Errorf("db: migration %q must be named NNNN_description.sql", filename)
	}
	version, err = strconv.Atoi(prefix)
	if err != nil {
		return 0, "", fmt.Errorf("db: migration %q has a non-numeric version prefix", filename)
	}
	if version <= 0 {
		return 0, "", fmt.Errorf("db: migration %q must have a version greater than zero", filename)
	}
	return version, rest, nil
}

const migrationTableDDL = `
CREATE TABLE IF NOT EXISTS schema_migration (
    version     integer     PRIMARY KEY,
    name        text        NOT NULL,
    checksum    text        NOT NULL,
    applied_at  timestamptz NOT NULL DEFAULT now()
)`

// Migrate applies any migrations not yet recorded, in version order.
//
// Each migration runs in its own transaction together with the insert into
// schema_migration, so a failure cannot leave a migration half-applied but
// recorded, or applied but unrecorded.
//
// Checksums of already-applied migrations are verified. An edited migration file
// is reported as an error rather than ignored: the environments that ran the old
// text and the ones that would run the new text have silently different schemas,
// and that difference surfaces later as a production-only failure.
func Migrate(ctx context.Context, pool *Pool, migrations []Migration) (applied []int, err error) {
	if pool == nil || pool.pool == nil {
		return nil, errors.New("db: pool is not open")
	}
	if _, err := pool.pool.Exec(ctx, migrationTableDDL); err != nil {
		return nil, fmt.Errorf("db: create schema_migration: %w", err)
	}

	rows, err := pool.pool.Query(ctx, `SELECT version, name, checksum FROM schema_migration`)
	if err != nil {
		return nil, fmt.Errorf("db: read schema_migration: %w", err)
	}
	type record struct {
		name     string
		checksum string
	}
	existing := map[int]record{}
	for rows.Next() {
		var version int
		var rec record
		if err := rows.Scan(&version, &rec.name, &rec.checksum); err != nil {
			rows.Close()
			return nil, fmt.Errorf("db: scan schema_migration: %w", err)
		}
		existing[version] = rec
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: iterate schema_migration: %w", err)
	}

	for _, m := range migrations {
		if prior, done := existing[m.Version]; done {
			if prior.checksum != m.Checksum {
				return applied, fmt.Errorf(
					"db: migration %04d_%s has been modified since it was applied (recorded checksum %s, file checksum %s); add a new forward migration instead of editing an applied one",
					m.Version, m.Name, prior.checksum[:12], m.Checksum[:12])
			}
			continue
		}

		err := pool.InTx(ctx, func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, m.SQL); err != nil {
				return fmt.Errorf("db: apply migration %04d_%s: %w", m.Version, m.Name, err)
			}
			if _, err := tx.Exec(ctx,
				`INSERT INTO schema_migration (version, name, checksum) VALUES ($1, $2, $3)`,
				m.Version, m.Name, m.Checksum); err != nil {
				return fmt.Errorf("db: record migration %04d_%s: %w", m.Version, m.Name, err)
			}
			return nil
		})
		if err != nil {
			return applied, err
		}
		applied = append(applied, m.Version)
	}
	return applied, nil
}

// MigrateFS is the common case: load from an embedded FS and apply.
func MigrateFS(ctx context.Context, pool *Pool, fsys fs.FS, dir string) ([]int, error) {
	migrations, err := LoadMigrations(fsys, dir)
	if err != nil {
		return nil, err
	}
	return Migrate(ctx, pool, migrations)
}

package store

import (
	"context"
	"fmt"
)

func (s *Store) prepareRelationshipSupersededColumn(ctx context.Context) error {
	query := `SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'person_relationships'),
		NOT EXISTS (SELECT 1 FROM pragma_table_info('person_relationships') WHERE name = 'superseded_at')`
	column := `ALTER TABLE person_relationships ADD COLUMN superseded_at DATETIME`
	if s.IsPostgreSQL() {
		query = `SELECT to_regclass('person_relationships') IS NOT NULL, NOT EXISTS (
			SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema()
			AND table_name = 'person_relationships' AND column_name = 'superseded_at')`
		column = `ALTER TABLE person_relationships ADD COLUMN IF NOT EXISTS superseded_at TIMESTAMPTZ`
	}
	var exists, missing bool
	if err := s.db.QueryRowContext(ctx, query).Scan(&exists, &missing); err != nil {
		return fmt.Errorf("inspect relationship supersession column: %w", err)
	}
	if !exists {
		return nil
	}
	return s.withTxContext(ctx, func(tx *loggedTx) error {
		if missing {
			if _, err := tx.ExecContext(ctx, column); err != nil {
				return fmt.Errorf("add relationship supersession column: %w", err)
			}
		}
		if _, err := tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_person_relationships_active_unique_v2
			ON person_relationships(source_person_id, target_person_id, relationship_type_id)
			WHERE end_year IS NULL AND superseded_at IS NULL`); err != nil {
			return fmt.Errorf("create current relationship uniqueness index: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS idx_person_relationships_active_unique`); err != nil {
			return fmt.Errorf("replace relationship uniqueness index: %w", err)
		}
		if missing {
			for _, index := range []string{"idx_person_relationships_target_active", "idx_person_relationships_source_current_edge", "idx_person_relationships_target_current_edge"} {
				if _, err := tx.ExecContext(ctx, `DROP INDEX IF EXISTS `+index); err != nil {
					return fmt.Errorf("drop stale relationship index: %w", err)
				}
			}
		}
		return nil
	})
}

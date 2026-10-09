package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitSchemaRelationshipSupersessionMigration(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	st, personID := newPersonFactLedgerStore(t)
	participantID, err := st.EnsureParticipant("peer@example.test", "Peer", "example.test")
	require.NoError(err)
	peer, _, err := st.CreatePersonFromParticipant(participantID)
	require.NoError(err)
	edge, err := st.AddPersonRelationshipContext(t.Context(), PersonRelationshipInput{
		SourcePersonID: personID, TargetPersonID: peer.ID, TypeSlug: "friend", Source: ProvenanceUser, Actor: "user",
		StartDate: &PartialDate{Year: new(2020)}, Notes: new("Private relationship history"),
	})
	require.NoError(err)
	for _, index := range []string{"idx_person_relationships_active_unique_v2", "idx_person_relationships_target_active", "idx_person_relationships_source_current_edge", "idx_person_relationships_target_current_edge"} {
		_, err := st.db.ExecContext(t.Context(), `DROP INDEX `+index)
		require.NoError(err)
	}
	_, err = st.db.ExecContext(t.Context(), `ALTER TABLE person_relationships DROP COLUMN superseded_at`)
	require.NoError(err)
	_, err = st.db.ExecContext(t.Context(), `ALTER TABLE persons DROP COLUMN carddav_cleanup_retained`)
	require.NoError(err)
	_, err = st.db.ExecContext(t.Context(), `CREATE UNIQUE INDEX idx_person_relationships_active_unique ON person_relationships(source_person_id, target_person_id, relationship_type_id) WHERE end_year IS NULL`)
	require.NoError(err)
	for _, index := range []struct{ name, columns string }{
		{"idx_person_relationships_target_active", "target_person_id, relationship_type_id"},
		{"idx_person_relationships_source_current_edge", "source_person_id, id"},
		{"idx_person_relationships_target_current_edge", "target_person_id, id"},
	} {
		_, err := st.db.ExecContext(t.Context(), `CREATE INDEX `+index.name+` ON person_relationships (`+index.columns+`) WHERE end_year IS NULL`)
		require.NoError(err)
	}
	_, err = st.db.ExecContext(t.Context(), `UPDATE archive_metadata SET value = '1' WHERE key = 'schema_version'`)
	require.NoError(err)

	require.NoError(st.InitSchemaContext(t.Context()))
	require.NoError(st.InitSchemaContext(t.Context()))
	for _, name := range []string{"idx_person_relationships_target_active", "idx_person_relationships_source_current_edge", "idx_person_relationships_target_current_edge"} {
		query := `SELECT sql FROM sqlite_master WHERE type = 'index' AND name = ?`
		if st.IsPostgreSQL() {
			query = `SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`
		}
		var definition string
		require.NoError(st.db.QueryRowContext(t.Context(), st.Rebind(query), name).Scan(&definition))
		assert.Contains(definition, "superseded_at")
	}
	var cleanupRetained bool
	require.NoError(st.db.QueryRowContext(t.Context(), st.Rebind(`SELECT carddav_cleanup_retained FROM persons WHERE id = ?`), personID).Scan(&cleanupRetained))
	assert.False(cleanupRetained, "existing people retain ordinary cleanup unless publication was cancelled")
	retained, err := st.GetPersonRelationshipContext(t.Context(), edge.ID)
	require.NoError(err)
	assert.Equal(edge, retained)
	require.NoError(st.withTxContext(t.Context(), func(tx *loggedTx) error {
		return st.supersedePersonRelationshipTx(t.Context(), tx, retained)
	}))
	_, err = st.AddPersonRelationshipContext(t.Context(), PersonRelationshipInput{SourcePersonID: personID, TargetPersonID: peer.ID, TypeSlug: "friend", Source: ProvenanceUser, Actor: "user"})
	require.NoError(err)
	current, err := st.ListPersonRelationshipsContext(t.Context(), personID, PersonRelationshipListOptions{})
	require.NoError(err)
	assert.Len(current, 1)
	history, err := st.ListPersonRelationshipsContext(t.Context(), personID, PersonRelationshipListOptions{IncludeEnded: true})
	require.NoError(err)
	assert.Len(history, 2)
}

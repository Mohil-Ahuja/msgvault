package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitSchemaRelationshipSupersessionMigration(t *testing.T) {
	st, personID := newPersonFactLedgerStore(t)
	participantID, err := st.EnsureParticipant("peer@example.test", "Peer", "example.test")
	require.NoError(t, err)
	peer, _, err := st.CreatePersonFromParticipant(participantID)
	require.NoError(t, err)
	edge, err := st.AddPersonRelationshipContext(t.Context(), PersonRelationshipInput{
		SourcePersonID: personID, TargetPersonID: peer.ID, TypeSlug: "friend", Source: ProvenanceUser, Actor: "user",
		StartDate: &PartialDate{Year: new(2020)}, Notes: new("Private relationship history"),
	})
	require.NoError(t, err)
	for _, index := range []string{"idx_person_relationships_active_unique_v2", "idx_person_relationships_target_active", "idx_person_relationships_source_current_edge", "idx_person_relationships_target_current_edge"} {
		_, err := st.db.ExecContext(t.Context(), `DROP INDEX `+index)
		require.NoError(t, err)
	}
	_, err = st.db.ExecContext(t.Context(), `ALTER TABLE person_relationships DROP COLUMN superseded_at`)
	require.NoError(t, err)
	_, err = st.db.ExecContext(t.Context(), `CREATE UNIQUE INDEX idx_person_relationships_active_unique ON person_relationships(source_person_id, target_person_id, relationship_type_id) WHERE end_year IS NULL`)
	require.NoError(t, err)
	_, err = st.db.ExecContext(t.Context(), `UPDATE archive_metadata SET value = '1' WHERE key = 'schema_version'`)
	require.NoError(t, err)

	require.NoError(t, st.InitSchemaContext(t.Context()))
	require.NoError(t, st.InitSchemaContext(t.Context()))
	retained, err := st.GetPersonRelationshipContext(t.Context(), edge.ID)
	require.NoError(t, err)
	assert.Equal(t, edge, retained)
	require.NoError(t, st.withTxContext(t.Context(), func(tx *loggedTx) error {
		return st.supersedePersonRelationshipTx(t.Context(), tx, retained)
	}))
	_, err = st.AddPersonRelationshipContext(t.Context(), PersonRelationshipInput{SourcePersonID: personID, TargetPersonID: peer.ID, TypeSlug: "friend", Source: ProvenanceUser, Actor: "user"})
	require.NoError(t, err)
	current, err := st.ListPersonRelationshipsContext(t.Context(), personID, PersonRelationshipListOptions{})
	require.NoError(t, err)
	assert.Len(t, current, 1)
	history, err := st.ListPersonRelationshipsContext(t.Context(), personID, PersonRelationshipListOptions{IncludeEnded: true})
	require.NoError(t, err)
	assert.Len(t, history, 2)
}

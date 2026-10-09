package carddav

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProjectConflictContactAllowListsNormalizesAndBoundsFields(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	long := strings.Repeat("界", 90)
	raw := "BEGIN:VCARD\r\nVERSION:4.0\r\n" +
		"UID:private-uid\r\nURL:https://dav.invalid/private\r\n" +
		"NOTE:Authorization: Bearer private-token\r\nX-API-KEY:private-key\r\n" +
		"FN:\t Alice\u0001   Example \r\nFN:Ignored Second Name\r\n" +
		"EMAIL:MAILTO:alice@example.test\r\nEMAIL:alice@example.test\r\n" +
		"EMAIL:" + long + "\r\n" +
		"EMAIL:e2@example.test\r\nEMAIL:e3@example.test\r\nEMAIL:e4@example.test\r\n" +
		"EMAIL:e5@example.test\r\nEMAIL:e6@example.test\r\nEMAIL:e7@example.test\r\nEMAIL:e8@example.test\r\n" +
		"TEL:TEL:+1  555  0100\r\nTEL:+1 555 0100\r\nEND:VCARD\r\n"

	got := projectConflictContact([]byte(raw), false)
	require.Equal(ConflictSidePresent, got.State)
	assert.Equal("Alice Example", got.DisplayName)
	require.Len(got.Emails, 8)
	assert.Equal("alice@example.test", got.Emails[0])
	assert.Equal([]string{"+1 555 0100"}, got.Phones)
	assert.True(got.Truncated)
	assert.LessOrEqual(len(got.Emails[1]), 256)
	assert.True(utf8.ValidString(got.Emails[1]))

	encoded, err := json.Marshal(got)
	require.NoError(err)
	text := string(encoded)
	for _, forbidden := range []string{"private-uid", "dav.invalid", "Authorization", "private-token", "private-key", "X-API-KEY", "Ignored Second Name"} {
		assert.NotContains(text, forbidden)
	}
}

func TestProjectConflictContactTombstoneAndMalformedAreFixed(t *testing.T) {
	deleted := projectConflictContact([]byte("credential=private-secret"), true)
	assert.Equal(t, ContactSummary{State: ConflictSideDeleted, Emails: []string{}, Phones: []string{}}, deleted)

	unavailable := projectConflictContact([]byte("not a vcard private-parser-marker"), false)
	assert.Equal(t, ContactSummary{State: ConflictSideUnavailable, Emails: []string{}, Phones: []string{}}, unavailable)
	t.Run("undecodable optional address preserves email", func(t *testing.T) {
		require := require.New(t)
		assert := assert.New(t)
		body := []byte("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:Example\r\nEMAIL:person@example.test\r\nADR;CHARSET=unsupported:;;private-address;;;;\r\nEND:VCARD\r\n")
		got := projectConflictContact(body, false)
		assert.Equal(ConflictSidePresent, got.State)
		assert.Equal("Example", got.DisplayName)
		assert.Equal([]string{"person@example.test"}, got.Emails)
		assert.Empty(got.Fields)
		assert.True(got.Truncated)
		remoteBody := []byte(strings.ReplaceAll(string(body), "private-address", "private-remote"))
		local, remote := projectConflictComparison(body, remoteBody, false, false)
		for index, summary := range []ContactSummary{local, remote} {
			assert.Equal(ConflictSidePresent, summary.State)
			assert.Equal([]string{"person@example.test"}, summary.Emails)
			assert.True(summary.Truncated)
			assert.Contains(summary.Fields, ContactField{Name: "ADR", Value: "Contents hidden", Labels: []string{[]string{"Address fields differ from remote", "Address fields differ from local"}[index]}})
		}
		encoded, err := json.Marshal([]ContactSummary{local, remote})
		require.NoError(err)
		assert.NotContains(string(encoded), "private-")
	})
	encoded, err := json.Marshal(unavailable)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "private-parser-marker")
}

func TestProjectConflictContactPreservesWhitespaceSeparatorsAndExactDeduplication(t *testing.T) {
	assert := assert.New(t)
	prefix := strings.Repeat("a", 256)
	raw := "BEGIN:VCARD\r\nVERSION:4.0\r\n" +
		"FN:Alice\\nExample\tPerson\r\n" +
		"EMAIL:" + prefix + "x@example.test\r\n" +
		"EMAIL:" + prefix + "y@example.test\r\n" +
		"EMAIL:" + prefix + "x@example.test\r\n" +
		"END:VCARD\r\n"

	got := projectConflictContact([]byte(raw), false)
	assert.Equal("Alice Example Person", got.DisplayName)
	require.Len(t, got.Emails, 2, "distinct normalized values sharing a clipped prefix are not duplicates")
	assert.Equal(got.Emails[0], got.Emails[1])
	assert.Len(got.Emails[0], 256)
	assert.True(got.Truncated)
}

func TestProjectConflictContactIgnoresLaterFNWithoutReportingTruncation(t *testing.T) {
	raw := "BEGIN:VCARD\r\nVERSION:4.0\r\nFN:First Name\r\nFN:" +
		strings.Repeat("private-later-name", 40) + "\r\nEND:VCARD\r\n"
	got := projectConflictContact([]byte(raw), false)
	assert.Equal(t, "First Name", got.DisplayName)
	assert.False(t, got.Truncated)
}

func TestProjectConflictContactShowsLabelsAndProfileFields(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	body := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Example Person\r\nEMAIL;TYPE=home;PREF=2:person@example.test\r\nADR;TYPE=work;LABEL=Office:;;123 Example Street;Example City;;;\r\nTITLE:Engineer\r\nEND:VCARD\r\n")
	got := projectConflictContact(body, false)
	require.Len(got.Fields, 3)
	assert.Equal(ContactField{Name: "EMAIL", Value: "person@example.test", Labels: []string{"TYPE=home", "PREF=2"}}, got.Fields[0])
	assert.Contains(got.Fields[1].Value, "123 Example Street")
	assert.Equal([]string{"TYPE=work", "LABEL=Office"}, got.Fields[1].Labels)
	assert.Equal("Engineer", got.Fields[2].Value)
	edited := strings.ReplaceAll(string(body), "TYPE=home", "TYPE=work")
	remote := projectConflictContact([]byte(edited), false)
	assert.Equal([]string{"person@example.test"}, remote.Emails)
	assert.NotEqual(got.Fields, remote.Fields)
}

func TestProjectConflictStructuredComponentsPreservePositionsAndEscapes(t *testing.T) {
	for _, tc := range []struct{ name, version, property, want string }{
		{"street", "4.0", "ADR:;;Example;;;;", `Street: "Example"`},
		{"po-box", "4.0", "ADR:Example;;;;;;", `PO box: "Example"`},
		{"escaped", "4.0", `ADR:;;Example\;Suite;;;;`, `Street: "Example;Suite"`},
		{"separated", "4.0", "ADR:;;Example;Suite;;;", `Street: "Example"; City: "Suite"`},
		{"family", "3.0", "N:Example;;;;", `Family: "Example"`},
		{"given", "3.0", "N:;Example;;;", `Given: "Example"`},
		{"organization", "4.0", "ORG:Example", `Organization: "Example"`},
		{"unit", "4.0", "ORG:;Example", `Unit 1: "Example"`},
		{"quoted-printable", "2.1", `ADR;ENCODING=QUOTED-PRINTABLE:;;Example=5C=3BSuite;;;;`, `Street: "Example;Suite"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			body := []byte("BEGIN:VCARD\r\nVERSION:" + tc.version + "\r\nFN:Example\r\n" + tc.property + "\r\nEND:VCARD\r\n")
			got := projectConflictContact(body, false)
			require.Equal(ConflictSidePresent, got.State)
			require.Len(got.Fields, 1)
			assert.Equal(tc.want, got.Fields[0].Value)
		})
	}
}

func TestProjectConflictComparisonMarksHiddenAndBoundedDifferences(t *testing.T) {
	for _, name := range []string{"IMPP", "NICKNAME", "LANG", "SOUND", "KEY", "NOTE", "RELATED", "X-SECRET-NAME"} {
		t.Run(name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			body := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Example\r\n" + name + ":private-local\r\nEND:VCARD\r\n")
			remoteBody := []byte(strings.ReplaceAll(string(body), "private-local", "private-remote"))
			local, remote := projectConflictComparison(body, remoteBody, false, false)
			require.Len(local.Fields, 1)
			require.Len(remote.Fields, 1)
			assert.Equal("Contents hidden", remote.Fields[0].Value)
			if name != "X-SECRET-NAME" {
				assert.Equal(name, remote.Fields[0].Name)
			}
			assert.NotEqual(local.Fields[0].Labels, remote.Fields[0].Labels)
			encoded, err := json.Marshal([]ContactSummary{local, remote})
			require.NoError(err)
			assert.NotContains(string(encoded), "private-")
			assert.NotContains(string(encoded), "X-SECRET-NAME")
		})
	}
	for _, scenario := range []string{"clipped-value", "clipped-parameter", "overflow", "normalized"} {
		t.Run(scenario, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			property := "TITLE:" + strings.Repeat("界", 100) + "local"
			if scenario == "clipped-parameter" {
				property = "EMAIL;LABEL=" + strings.Repeat("x", 260) + "local:example@example.test"
			}
			if scenario == "normalized" {
				property = "TITLE:local  value"
			}
			if scenario == "overflow" {
				property = strings.Repeat("ADR:;;Example Street;;;;\r\n", 9) + "IMPP:local"
			}
			body := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Example\r\n" + property + "\r\nEND:VCARD\r\n")
			remoteBody := []byte(strings.ReplaceAll(string(body), "local", "remote"))
			if scenario == "normalized" {
				remoteBody = []byte(strings.ReplaceAll(string(body), "local  value", "local value"))
			}
			local, remote := projectConflictComparison(body, remoteBody, false, false)
			assert.NotEqual(local.Fields, remote.Fields)
			for _, summary := range []ContactSummary{local, remote} {
				assert.LessOrEqual(len(summary.Fields), maxPublicContactValues)
				for _, field := range summary.Fields {
					assert.LessOrEqual(len(field.Value), maxPublicCardDAVValueBytes)
					assert.True(utf8.ValidString(field.Value))
					assert.LessOrEqual(len(field.Labels), maxPublicContactValues)
				}
			}
			if scenario != "normalized" {
				require.True(remote.Truncated)
				assert.Equal("Additional differences omitted", remote.Fields[len(remote.Fields)-1].Value)
			}
		})
	}
}

func TestProjectConflictRepeatedFieldLabelsDescribeTheGroup(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	body := []byte("BEGIN:VCARD\r\nVERSION:4.0\r\nFN:Example\r\nEMAIL:first@example.test\r\nEMAIL;TYPE=home:second@example.test\r\nEND:VCARD\r\n")
	remoteBody := []byte(strings.ReplaceAll(string(body), "TYPE=home", "TYPE=work"))
	local, remote := projectConflictComparison(body, remoteBody, false, false)
	require.Len(local.Fields, 2)
	require.Len(remote.Fields, 2)
	assert.Equal("first@example.test", remote.Fields[0].Value)
	assert.Equal([]string{"Email fields differ from remote"}, local.Fields[0].Labels)
	assert.Equal([]string{"Email fields differ from local"}, remote.Fields[0].Labels)
}

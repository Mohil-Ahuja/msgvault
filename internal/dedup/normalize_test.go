package dedup

import (
	"bytes"
	"context"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func TestInspectDuplicateRawMIME_BodyHashNULFields(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := storetest.New(t)
	var rows []store.DuplicateMessageRow
	for _, fields := range []struct{ text, html string }{{"A\x00B", "C"}, {"A", "B\x00C"}, {"A\x00B", "C"}} {
		id := f.NewMessage().Create(t, f.Store)
		raw := "MIME-Version: 1.0\nContent-Type: multipart/mixed; boundary=parts\n\n--parts\nContent-Type: multipart/alternative; boundary=bodies\n\n--bodies\nContent-Type: text/plain\nContent-Transfer-Encoding: base64\n\n" + base64.StdEncoding.EncodeToString([]byte(fields.text)) + "\n--bodies\nContent-Type: text/html\nContent-Transfer-Encoding: base64\n\n" + base64.StdEncoding.EncodeToString([]byte(fields.html)) + "\n--bodies--\n--parts\nContent-Type: application/octet-stream\nContent-Disposition: attachment; filename=one.bin\nX-Apple-Content-Length: 200\n\n\n--parts--\n"
		// Raw MIME stores bytes, so NUL body fields never enter PostgreSQL text columns.
		require.NoError(f.Store.UpsertMessageRaw(id, []byte(raw)))
		rows = append(rows, store.DuplicateMessageRow{ID: id, HasRawMIME: true})
	}
	engine := NewEngine(f.Store, Config{}, nil)
	info, err := engine.inspectDuplicateRawMIME(context.Background(), map[string][]store.DuplicateMessageRow{"bodies": rows})
	require.NoError(err)
	require.NotEmpty(info[rows[0].ID].bodyHash)
	require.NotEmpty(info[rows[1].ID].bodyHash)
	require.NotEmpty(info[rows[2].ID].bodyHash)
	assert.NotEqual(info[rows[0].ID].bodyHash, info[rows[1].ID].bodyHash)
	assert.Equal(info[rows[0].ID].bodyHash, info[rows[2].ID].bodyHash)
}

func TestNormalizeRawMIME(t *testing.T) {
	tests := []struct {
		name     string
		input    []byte
		wantSame bool   // true if output should equal input
		contains string // substring the output must contain
		excludes string // substring the output must NOT contain
	}{
		{
			name:     "strips Received header (CRLF)",
			input:    []byte("Received: from mx1.google.com\r\nFrom: alice@example.com\r\nSubject: Hi\r\n\r\nBody"),
			contains: "From: alice@example.com",
			excludes: "Received",
		},
		{
			name:     "strips multiple transport headers",
			input:    []byte("Delivered-To: bob@example.com\r\nX-Gmail-Labels: INBOX\r\nAuthentication-Results: spf=pass\r\nFrom: alice@example.com\r\nSubject: Test\r\n\r\nBody"),
			contains: "From: alice@example.com",
			excludes: "Delivered-To",
		},
		{
			name:     "preserves non-transport headers",
			input:    []byte("From: alice@example.com\r\nTo: bob@example.com\r\nSubject: Meeting\r\nDate: Mon, 1 Jan 2024 12:00:00 +0000\r\n\r\nBody text"),
			contains: "Subject: Meeting",
		},
		{
			name:     "handles LF-only line endings",
			input:    []byte("Received: from mx1\nFrom: alice@example.com\nSubject: Test\n\nBody with LF"),
			contains: "From: alice@example.com",
			excludes: "Received",
		},
		{
			name:     "no header/body separator returns raw unchanged",
			input:    []byte("This is just a blob of text with no headers"),
			wantSame: true,
		},
		{
			name:     "empty body preserved",
			input:    []byte("From: alice@example.com\r\nSubject: Empty\r\n\r\n"),
			contains: "Subject: Empty",
		},
		{
			name:     "preserves body content exactly",
			input:    []byte("Received: from mx1\r\nFrom: a@b.com\r\n\r\nExact body content here."),
			contains: "Exact body content here.",
		},
		{
			name:     "LF headers with CRLF in body uses earliest boundary",
			input:    []byte("From: a@b.com\nSubject: Test\n\nBody has \r\n\r\n inside"),
			contains: "Body has \r\n\r\n inside",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			inputCopy := make([]byte, len(tt.input))
			copy(inputCopy, tt.input)

			result := normalizeRawMIME(tt.input)

			assert.True(bytes.Equal(tt.input, inputCopy), "normalizeRawMIME mutated its input buffer")

			if tt.wantSame {
				assert.True(bytes.Equal(result, tt.input), "expected unchanged output, got:\n%s", result)
				return
			}
			if tt.contains != "" {
				assert.True(bytes.Contains(result, []byte(tt.contains)), "output missing %q:\n%s", tt.contains, result)
			}
			if tt.excludes != "" {
				assert.False(bytes.Contains(result, []byte(tt.excludes)), "output should not contain %q:\n%s", tt.excludes, result)
			}
		})
	}
}

func TestNormalizeRawMIME_DeterministicOutput(t *testing.T) {
	raw1 := []byte("Received: from mx1.google.com\r\nFrom: sender@example.com\r\nSubject: Meeting\r\nDate: Mon, 1 Jan 2024 12:00:00 +0000\r\n\r\nLet's meet at 3pm.")
	raw2 := []byte("Received: from mx2.google.com\r\nDelivered-To: other@example.com\r\nFrom: sender@example.com\r\nSubject: Meeting\r\nDate: Mon, 1 Jan 2024 12:00:00 +0000\r\n\r\nLet's meet at 3pm.")

	hash1 := sha256Hex(normalizeRawMIME(raw1))
	hash2 := sha256Hex(normalizeRawMIME(raw2))
	assert.Equal(t, hash1, hash2, "same message with different transport headers produced different hashes")
}

func TestContentHashConflictsWithMessageIDGroup(t *testing.T) {
	tests := []struct {
		name      string
		group     DuplicateGroup
		survivors map[int64]bool
		want      bool
	}{
		{
			name: "two Message-ID survivors",
			group: DuplicateGroup{Messages: []DuplicateMessage{
				{ID: 1}, {ID: 2},
			}},
			survivors: map[int64]bool{1: true, 2: true},
			want:      true,
		},
		{
			name: "Message-ID survivor and sent orphan",
			group: DuplicateGroup{Messages: []DuplicateMessage{
				{ID: 1}, {ID: 2, IsFromMe: true},
			}},
			survivors: map[int64]bool{1: true},
			want:      true,
		},
		{
			name: "Message-ID survivor and received orphan",
			group: DuplicateGroup{Messages: []DuplicateMessage{
				{ID: 1}, {ID: 2},
			}},
			survivors: map[int64]bool{1: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want,
				contentHashConflictsWithMessageIDGroup(tt.group, tt.survivors))
		})
	}
}

func TestPreserveMessageIDSurvivor(t *testing.T) {
	group := DuplicateGroup{
		Messages: []DuplicateMessage{{ID: 1}, {ID: 2}},
		Survivor: 0,
	}

	preserveMessageIDSurvivor(&group, map[int64]bool{2: true})

	assert.Equal(t, 1, group.Survivor)
}

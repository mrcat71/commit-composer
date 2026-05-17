package tui

import (
	"encoding/json"
	"strings"
	"testing"
)

func samplePools() []ProposalPool {
	return []ProposalPool{
		{
			SHA:      "aaaaaaa0000000000000000000000000000000aa",
			PoolSize: 2,
			Groups: []ProposalGroup{
				{Files: []string{"auth.go", "auth_test.go"}, Message: "feat: Auth helper"},
				{Files: []string{"docs/auth.md"}, Message: "docs: explain Auth"},
			},
		},
		{
			SHA:      "bbbbbbb0000000000000000000000000000000bb",
			PoolSize: 1,
			Groups: []ProposalGroup{
				{Files: []string{"main.go"}, Message: "chore: tidy main"},
			},
		},
	}
}

func TestRevisedPoolsNoEdits(t *testing.T) {
	m := NewReview(ReviewOptions{Pools: samplePools()})
	out := m.RevisedPools()
	if len(out) != 2 {
		t.Fatalf("len: %d", len(out))
	}
	if out[0].PoolSize != 2 || out[1].PoolSize != 1 {
		t.Errorf("pool sizes: got %+v", []int{out[0].PoolSize, out[1].PoolSize})
	}
	if len(out[0].Groups) != 2 || len(out[1].Groups) != 1 {
		t.Errorf("group counts: got %d, %d", len(out[0].Groups), len(out[1].Groups))
	}
}

func TestRevisedPoolsSquash(t *testing.T) {
	m := NewReview(ReviewOptions{Pools: samplePools()})
	// Cursor on row 1 (second group of first pool). Squash into the first group.
	m.cursor = 1
	m = m.applySquash()
	if m.statusErr {
		t.Fatalf("squash failed: %s", m.status)
	}
	out := m.RevisedPools()
	// Pool 0 should now have 1 active group with both files combined.
	if len(out[0].Groups) != 1 {
		t.Fatalf("expected 1 group after squash, got %d", len(out[0].Groups))
	}
	got := out[0].Groups[0]
	if len(got.Files) != 3 {
		t.Errorf("expected 3 combined files, got %v", got.Files)
	}
	if !strings.Contains(got.Message, "feat: Auth helper") || !strings.Contains(got.Message, "docs: explain Auth") {
		t.Errorf("squashed message missing parts: %q", got.Message)
	}
}

func TestRevisedPoolsDrop(t *testing.T) {
	m := NewReview(ReviewOptions{Pools: samplePools()})
	m.cursor = 1
	m = m.applyDrop()
	if m.statusErr {
		t.Fatalf("drop failed: %s", m.status)
	}
	out := m.RevisedPools()
	if len(out[0].Groups) != 1 {
		t.Fatalf("expected 1 group after drop, got %d", len(out[0].Groups))
	}
	got := out[0].Groups[0]
	if len(got.Files) != 3 {
		t.Errorf("expected 3 files (folded), got %v", got.Files)
	}
	// Drop discards the dropped group's message, keeping only the previous one.
	if got.Message != "feat: Auth helper" {
		t.Errorf("drop should keep previous message only: got %q", got.Message)
	}
}

func TestSquashFirstGroupFails(t *testing.T) {
	m := NewReview(ReviewOptions{Pools: samplePools()})
	m.cursor = 0 // first row of first pool - no previous to squash into
	m = m.applySquash()
	if !m.statusErr {
		t.Errorf("expected statusErr=true squashing the first group, got status=%q", m.status)
	}
}

func TestOutcomeFlags(t *testing.T) {
	m := NewReview(ReviewOptions{Pools: samplePools()})
	// Edit a message.
	m.rows[0].message = "different message"
	// Add a comment on another.
	m.rows[2].comment = "make this conventional commits"
	out := m.Outcome()
	if !out.GroupsChanged {
		t.Errorf("expected GroupsChanged=true")
	}
	if !out.HasComments {
		t.Errorf("expected HasComments=true")
	}
	if !out.Accept {
		t.Errorf("default Accept should be true (cancelled defaults to false)")
	}
}

func TestMarshalOutcomeIncludesPools(t *testing.T) {
	m := NewReview(ReviewOptions{Pools: samplePools()})
	data, err := m.MarshalOutcome()
	if err != nil {
		t.Fatalf("MarshalOutcome: %v", err)
	}
	var parsed struct {
		Accept       bool           `json:"accept"`
		HasComments  bool           `json:"has_comments"`
		Pools        []ProposalPool `json:"pools"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("Unmarshal: %v\n%s", err, data)
	}
	if len(parsed.Pools) != 2 {
		t.Errorf("expected 2 pools in outcome, got %d", len(parsed.Pools))
	}
}

func TestStripCommentLines(t *testing.T) {
	in := "# header\n# more header\n\nthe real comment\n# trailing"
	got := stripCommentLines(in)
	want := "the real comment"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

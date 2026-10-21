package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestEnqueueRejectsInvalidInputBeforeUsingDatabase(t *testing.T) {
	store := New(nil)
	for _, test := range []struct {
		kind    string
		payload json.RawMessage
		want    error
	}{
		{kind: "", payload: json.RawMessage(`{}`), want: ErrInvalidKind},
		{kind: " bad", payload: json.RawMessage(`{}`), want: ErrInvalidKind},
		{kind: "1bad", payload: json.RawMessage(`{}`), want: ErrInvalidKind},
		{kind: strings.Repeat("a", 129), payload: json.RawMessage(`{}`), want: ErrInvalidKind},
		{kind: "echo", payload: nil, want: ErrInvalidPayload},
		{kind: "echo", payload: json.RawMessage(`{`), want: ErrInvalidPayload},
		{kind: "echo", payload: json.RawMessage{'"', 0xff, '"'}, want: ErrInvalidPayload},
	} {
		job, err := store.Enqueue(context.Background(), test.kind, test.payload)
		if !errors.Is(err, test.want) || job.ID != "" || job.Payload != nil {
			t.Fatal("invalid input must return a zero job and the expected validation error")
		}
	}
}

func TestGetRejectsInvalidIDBeforeUsingDatabase(t *testing.T) {
	for _, id := range []string{"", "bad", "zzzzzzzz-0000-0000-0000-000000000000", "00000000x0000x0000x0000x000000000000"} {
		job, err := New(nil).Get(context.Background(), id)
		if !errors.Is(err, ErrInvalidID) || job.ID != "" {
			t.Fatal("invalid ID must return a zero job and ErrInvalidID")
		}
		if err := New(nil).Complete(context.Background(), id); !errors.Is(err, ErrInvalidID) {
			t.Fatal("completion must reject an invalid ID before using the database")
		}
	}
}

package session

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	rootfs "mindfs/server/internal/fs"
)

func TestLoadExchangesMergesDuplicateSeq(t *testing.T) {
	root := rootfs.NewRootInfo("test", "test", t.TempDir())
	manager := NewManager(root)
	older := time.Date(2026, 9, 27, 1, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)
	entries := []Exchange{
		{Seq: 15, Role: "user", Content: "question", Timestamp: newer},
		{Seq: 16, Role: "agent", Timestamp: older},
		{Seq: 15, Role: "user", Content: "question", Timestamp: older},
		{Seq: 16, Role: "agent", Content: "complete reply", Model: "model", Timestamp: newer},
		{Seq: 21, Role: "user", Content: "next question"},
		{Seq: 22, Role: "agent", Content: "original reply"},
		{Seq: 22, Role: "agent", Content: "later reply"},
		{Seq: 16, Role: "agent", Content: " \n\t"},
		{Seq: 23, Role: "agent"},
		{Seq: 23, Role: "agent", Model: "latest placeholder"},
	}
	want := []Exchange{entries[2], entries[3], entries[4], entries[6], entries[9]}
	var payload bytes.Buffer
	for _, entry := range entries {
		if err := json.NewEncoder(&payload).Encode(entry); err != nil {
			t.Fatal(err)
		}
	}
	path, err := manager.exchangePath("duplicates")
	if err != nil {
		t.Fatal(err)
	}
	if err := root.WriteMetaFile(path, payload.Bytes()); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name     string
		afterSeq int
		want     []Exchange
	}{
		{"full", 0, want},
		{"after user", 15, want[1:]},
		{"gap", 19, want[2:]},
		{"at end", 23, []Exchange{}},
		{"past end", 30, []Exchange{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, maxSeq, err := manager.loadExchanges("duplicates", tc.afterSeq)
			if err != nil {
				t.Fatal(err)
			}
			if maxSeq != 23 {
				t.Fatalf("max seq = %d, want 23", maxSeq)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("exchanges = %#v, want %#v", got, tc.want)
			}
		})
	}
	stored, err := root.ReadMetaFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, payload.Bytes()) {
		t.Fatal("loading changed the original log")
	}
}

func TestLoadExchangesDuplicateSeqKeepsLegacySequenceAssignment(t *testing.T) {
	root := rootfs.NewRootInfo("test", "test", t.TempDir())
	manager := NewManager(root)
	path, err := manager.exchangePath("legacy")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("{\"seq\":5,\"role\":\"agent\",\"content\":\"reply\"}\n" +
		"{\"seq\":5,\"role\":\"agent\",\"content\":\"\"}\n" +
		"invalid JSON\n\n{\"role\":\"user\",\"content\":\"legacy\"}\n")
	if err := root.WriteMetaFile(path, payload); err != nil {
		t.Fatal(err)
	}
	got, maxSeq, err := manager.loadExchanges("legacy", 0)
	if err != nil {
		t.Fatal(err)
	}
	want := []Exchange{{Seq: 5, Role: "agent", Content: "reply"}, {Seq: 6, Role: "user", Content: "legacy"}}
	if maxSeq != 6 || !reflect.DeepEqual(got, want) {
		t.Fatalf("exchanges = %#v, maxSeq = %d; want %#v, 6", got, maxSeq, want)
	}
}

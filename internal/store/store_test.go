package store

import (
	"errors"
	"testing"
)

func TestKVRoundTrip(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	kv := s.Namespace("test")
	if err := kv.Set("user1", "k", "v1"); err != nil {
		t.Fatal(err)
	}
	if v, err := kv.Get("user1", "k"); err != nil || v != "v1" {
		t.Fatalf("got %q, %v", v, err)
	}
	if err := kv.Set("user1", "k", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := kv.Get("user1", "k"); v != "v2" {
		t.Fatalf("upsert failed: %q", v)
	}
	if _, err := kv.Get("user2", "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	if err := kv.Delete("user1", "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := kv.Get("user1", "k"); !errors.Is(err, ErrNotFound) {
		t.Fatal("delete did not remove key")
	}
}

func TestNamespaceIsolation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	a, b := s.Namespace("a"), s.Namespace("b")
	a.Set("u", "k", "from-a")
	if _, err := b.Get("u", "k"); !errors.Is(err, ErrNotFound) {
		t.Fatal("namespaces leak")
	}
}

func TestListPrefix(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	kv := s.Namespace("p")
	kv.Set("u1", "r:001", "a")
	kv.Set("u1", "r:002", "b")
	kv.Set("u2", "r:003", "c")
	kv.Set("u1", "other", "d")

	all, err := kv.List("r:")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("List: want 3, got %d", len(all))
	}
	mine, err := kv.ListUser("u1", "r:")
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 2 {
		t.Fatalf("ListUser: want 2, got %d", len(mine))
	}
	// LIKE wildcards in the prefix must be literal.
	kv.Set("u1", "x_y", "e")
	kv.Set("u1", "xzy", "f")
	got, _ := kv.ListUser("u1", "x_")
	if len(got) != 1 || got[0].Key != "x_y" {
		t.Fatalf("wildcard not escaped: %+v", got)
	}
}

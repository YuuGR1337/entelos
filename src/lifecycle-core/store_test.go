package lifecycle

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Each test runs on both adapters of the storage port.
func eachStore(t *testing.T, test func(t *testing.T, s Store)) {
	t.Run("git", func(t *testing.T) {
		s, err := OpenGitStore(filepath.Join(t.TempDir(), "state.git"))
		if err != nil {
			t.Fatal(err)
		}
		test(t, s)
	})
	t.Run("memory", func(t *testing.T) { test(t, NewMemoryStore()) })
}

func code(err error) string {
	var f *Fault
	if errors.As(err, &f) {
		return f.Code
	}
	if err == nil {
		return ""
	}
	return "error:" + err.Error()
}

func root(t *testing.T, s Store) *View {
	t.Helper()
	v, err := s.Read(nil)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// write stores one record under a state field and gives the new cursor.
func write(t *testing.T, s Store, expected, key, value string) string {
	t.Helper()
	r, err := s.Transact(expected, key, Object{"value": value}, func(v *View) (any, error) {
		v.State["heads"].(Object)[key] = v.Put("source", Object{"value": value})
		return value, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return r["cursor"].(string)
}

func TestStaleWriterIsRefused(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		first := write(t, s, "", "a", "1")
		_, err := s.Transact("", "b", Object{}, func(*View) (any, error) { return nil, nil })
		if code(err) != "stale_revision" || root(t, s).Cursor != first {
			t.Fatalf("got %v", err)
		}
	})
}

func TestRetryIsIdempotentAndConflictIsRefused(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		first := write(t, s, "", "a", "1")
		second := write(t, s, first, "b", "2")
		// A lost reply: the caller retries with its old expected revision.
		r, err := s.Transact("", "a", Object{"value": "1"}, func(*View) (any, error) {
			t.Fatal("apply must not run on a retry")
			return nil, nil
		})
		if err != nil || r["cursor"] != first || r["reused"] != true || r["result"] != "1" {
			t.Fatalf("retry: %v %v", r, err)
		}
		if _, err := s.Transact(second, "a", Object{"value": "other"}, nil); code(err) != "idempotency_conflict" {
			t.Fatalf("conflict: %v", err)
		}
		if root(t, s).Cursor != second {
			t.Fatal("a retry changed the root")
		}
	})
}

func TestHistoricalReadAndSnapshotsDoNotMix(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		first := write(t, s, "", "a", "1")
		second := write(t, s, first, "b", "2")
		old, err := s.Read(&first)
		if err != nil {
			t.Fatal(err)
		}
		if _, has := old.State["heads"].(Object)["b"]; has {
			t.Fatal("a historical view shows a later write")
		}
		later := root(t, s)
		ref := later.State["heads"].(Object)["b"]
		if _, err := old.Get(ref, "source"); code(err) != "artifact_unavailable" {
			t.Fatalf("an old view reads a later object: %v", err)
		}
		if got, err := later.Get(ref, "source"); err != nil || got["value"] != "2" {
			t.Fatalf("%v %v", got, err)
		}
		history, _ := s.History(second)
		if len(history) != 2 || history[0] != second || history[1] != first {
			t.Fatalf("history %v", history)
		}
		other := strings.Repeat("a", 40)
		if _, err := s.Read(&other); err == nil {
			t.Fatal("a cursor outside history was read")
		}
		empty := ""
		if v, err := s.Read(&empty); err != nil || len(v.State["heads"].(Object)) != 0 {
			t.Fatalf("empty cursor: %v", err)
		}
	})
}

func TestFailedApplyPublishesNothing(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		first := write(t, s, "", "a", "1")
		_, err := s.Transact(first, "b", Object{}, func(v *View) (any, error) {
			v.State["heads"].(Object)["b"] = v.Put("source", Object{})
			return nil, NewFault("forbidden", "no", "")
		})
		if code(err) != "forbidden" || root(t, s).Cursor != first {
			t.Fatalf("got %v", err)
		}
		// The key stays free for a later valid request.
		write(t, s, first, "b", "2")
	})
}

func TestTwoConcurrentWritersHaveOneWinner(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		var ready, done sync.WaitGroup
		ready.Add(2)
		done.Add(2)
		errs := make([]error, 2)
		for i, key := range []string{"x", "y"} {
			go func(i int, key string) {
				defer done.Done()
				_, errs[i] = s.Transact("", key, Object{}, func(v *View) (any, error) {
					v.State["heads"].(Object)[key] = v.Put("source", Object{"key": key})
					ready.Done()
					ready.Wait() // both writers captured the same root
					return key, nil
				})
			}(i, key)
		}
		done.Wait()
		codes := map[string]int{code(errs[0]): 1}
		codes[code(errs[1])]++
		if codes[""] != 1 || codes["stale_revision"] != 1 {
			t.Fatalf("got %v and %v", errs[0], errs[1])
		}
		if heads := root(t, s).State["heads"].(Object); len(heads) != 1 {
			t.Fatalf("both writes are visible: %v", heads)
		}
	})
}

// interrupt makes the next publication fail. lost is true if the root moves
// first (a stale writer); otherwise the failure is a storage failure.
func interrupt(t *testing.T, s Store, fail error) {
	switch s := s.(type) {
	case *GitStore:
		s.Hook = func(g *GitStore, args []string, data []byte) (GitResult, error) {
			if args[0] == "update-ref" {
				g.Hook = nil
				if fail != nil {
					return GitResult{}, fail
				}
				return GitResult{Code: 1, Stderr: []byte("disk full")}, nil
			}
			return g.RunGit(args, data)
		}
	case *MemoryStore:
		s.BeforePublish = func() error {
			s.BeforePublish = nil
			if fail != nil {
				return fail
			}
			return NewFault("store_error", "disk full", "store")
		}
	}
}

func TestInterruptionBeforePublicationThenRecovery(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		first := write(t, s, "", "a", "1")
		interrupt(t, s, NewFault("store_error", "process stopped", "store"))
		if _, err := s.Transact(first, "b", Object{"value": "2"}, func(*View) (any, error) { return "2", nil }); code(err) != "store_error" {
			t.Fatalf("got %v", err)
		}
		if root(t, s).Cursor != first {
			t.Fatal("an interrupted write was published")
		}
		write(t, s, first, "b", "2")
	})
}

func TestStorageFailureIsNotAStaleWriter(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		first := write(t, s, "", "a", "1")
		interrupt(t, s, nil)
		_, err := s.Transact(first, "b", Object{}, func(*View) (any, error) { return nil, nil })
		if code(err) != "store_error" || root(t, s).Cursor != first {
			t.Fatalf("got %v", err)
		}
	})
}

func TestRequestKeyIsBounded(t *testing.T) {
	eachStore(t, func(t *testing.T, s Store) {
		for _, key := range []any{"", strings.Repeat("k", 129), 7, nil} {
			if _, err := s.Transact("", key, Object{}, nil); code(err) != "invalid_request" {
				t.Fatalf("%v: %v", key, err)
			}
		}
		write(t, s, "", strings.Repeat("é", 128), "1")
	})
}

func TestGitEnvironmentCannotRedirectObjects(t *testing.T) {
	elsewhere := t.TempDir()
	t.Setenv("GIT_OBJECT_DIRECTORY", elsewhere)
	t.Setenv("GIT_DIR", elsewhere)
	s, err := OpenGitStore(filepath.Join(t.TempDir(), "state.git"))
	if err != nil {
		t.Fatal(err)
	}
	first := write(t, s, "", "a", "1")
	entries, _ := os.ReadDir(elsewhere)
	if len(entries) != 0 {
		t.Fatalf("objects went to %s", elsewhere)
	}
	if _, err := s.Git(nil, "cat-file", "-e", first); err != nil {
		t.Fatal(err)
	}
}

func TestGitStoreRefusesANonBareRepository(t *testing.T) {
	work := filepath.Join(t.TempDir(), "work")
	if out, err := exec.Command("git", "init", "-q", work).CombinedOutput(); err != nil {
		t.Fatal(string(out))
	}
	if _, err := OpenGitStore(filepath.Join(work, ".git")); code(err) != "invalid_store" {
		t.Fatalf("got %v", err)
	}
}

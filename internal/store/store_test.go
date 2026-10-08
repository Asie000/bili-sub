package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

func TestUpsertIdempotent(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v := &Video{Bvid: "BV1xx", Cid: 100, Title: "a", IsCharging: true, State: StatePending}
	id1, err := s.UpsertVideo(v)
	if err != nil {
		t.Fatal(err)
	}
	v.Title = "b"
	id2, err := s.UpsertVideo(v)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("not idempotent: %d vs %d", id1, id2)
	}
	got, _ := s.ListVideos("")
	if len(got) != 1 || got[0].Title != "b" {
		t.Fatalf("bad list: %+v", got)
	}
	if !got[0].IsCharging {
		t.Fatalf("is_charging did not survive round trip: %+v", got[0])
	}
}

func TestSetState(t *testing.T) {
	s, _ := Open(":memory:")
	defer s.Close()
	id, _ := s.UpsertVideo(&Video{Bvid: "BV1", Cid: 1, State: StatePending})
	if err := s.SetState(id, StateChargingLocked, ""); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListVideos(StateChargingLocked)
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
}

func TestFileConcurrentReadWrite(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	const workers, iterations = 8, 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				v := &Video{Bvid: fmt.Sprintf("BV%d", w), Cid: int64(j + 1), Title: "t", State: StatePending}
				if _, err := s.UpsertVideo(v); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
				if _, err := s.ListVideos(""); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
			}
		}(w)
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("%d concurrent ops failed, first: %v", len(errs), errs[0])
	}
	got, err := s.ListVideos("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != workers*iterations {
		t.Fatalf("want %d videos, got %d", workers*iterations, len(got))
	}
}

func TestListVideosEmptyNonNil(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := s.ListVideos("nonexistent-state")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Fatalf("want 0 rows, got %d", len(got))
	}
}

func TestListVideosOrderedByID(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 5; i++ {
		if _, err := s.UpsertVideo(&Video{Bvid: fmt.Sprintf("BV%d", i), Cid: 1, State: StatePending}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.ListVideos("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("want 5, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ID > got[i].ID {
			t.Fatalf("not ordered by id: %+v", got)
		}
	}
}

func TestOpenMemoryConcurrent(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.UpsertVideo(&Video{Bvid: "BV1", Cid: 1, State: StatePending}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if _, err := s.ListVideos(""); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
					return
				}
			}
		}()
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("%d concurrent ListVideos failed, first: %v", len(errs), errs[0])
	}
}

func TestSaveTranscriptRoundTrip(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id, err := s.UpsertVideo(&Video{Bvid: "BV1", Cid: 1, State: StateFetched})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveTranscript(id, "bilibili", "zh-CN", `{"segments":[]}`); err != nil {
		t.Fatal(err)
	}
	engine, lang, irJSON, err := s.GetTranscript(id)
	if err != nil {
		t.Fatal(err)
	}
	if engine != "bilibili" || lang != "zh-CN" || irJSON != `{"segments":[]}` {
		t.Fatalf("bad round trip: %q %q %q", engine, lang, irJSON)
	}
	if err := s.SaveTranscript(id, "whisper", "zh", `{"segments":[1]}`); err != nil {
		t.Fatal(err)
	}
	engine, lang, irJSON, err = s.GetTranscript(id)
	if err != nil {
		t.Fatal(err)
	}
	if engine != "whisper" || lang != "zh" || irJSON != `{"segments":[1]}` {
		t.Fatalf("bad overwrite: %q %q %q", engine, lang, irJSON)
	}
	other, err := s.UpsertVideo(&Video{Bvid: "BV2", Cid: 2, State: StatePending})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.GetTranscript(other); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("want sql.ErrNoRows, got %v", err)
	}
}

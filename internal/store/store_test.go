package store

import (
	"database/sql"
	"errors"
	"sync"
	"testing"
)

func TestUpsertIdempotent(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v := &Video{Bvid: "BV1xx", Cid: 100, Title: "a", State: StatePending}
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

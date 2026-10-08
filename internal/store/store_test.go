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

func TestListVideosBySource(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rows := []Video{
		{Bvid: "BV1", Cid: 1, SourceKey: "111", State: StatePending},
		{Bvid: "BV2", Cid: 1, SourceKey: "222", State: StatePending},
		{Bvid: "BV3", Cid: 1, SourceKey: "111", State: StatePending},
	}
	for i := range rows {
		if _, err := s.UpsertVideo(&rows[i]); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.ListVideosBySource("111")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 rows for source 111, got %d (%+v)", len(got), got)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].ID > got[i].ID {
			t.Fatalf("not ordered by id: %+v", got)
		}
	}

	empty, err := s.ListVideosBySource("nope")
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil {
		t.Fatal("want non-nil empty slice")
	}
	if len(empty) != 0 {
		t.Fatalf("want 0 rows, got %d", len(empty))
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

func TestUpsertAidRoundTrip(t *testing.T) {
	s, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	v := &Video{Bvid: "BV1", Cid: 1, Aid: 42, State: StatePending}
	if _, err := s.UpsertVideo(v); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListVideos("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Aid != 42 {
		t.Fatalf("aid did not round trip: %+v", got)
	}

	// The ON CONFLICT update must also carry the new column.
	v.Aid = 43
	if _, err := s.UpsertVideo(v); err != nil {
		t.Fatal(err)
	}
	got, err = s.ListVideos("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Aid != 43 {
		t.Fatalf("aid did not update: %+v", got)
	}
}

// TestOpenIdempotent opens the same file-backed database twice: the second open
// re-runs the schema and the aid migration, which must treat the already-present
// column as a no-op instead of failing.
func TestOpenIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if _, err := s.UpsertVideo(&Video{Bvid: "BV1", Cid: 1, Aid: 7, State: StatePending}); err != nil {
			s.Close()
			t.Fatalf("open %d: upsert: %v", i, err)
		}
		got, err := s.ListVideos("")
		if err != nil {
			s.Close()
			t.Fatalf("open %d: list: %v", i, err)
		}
		if len(got) != 1 || got[0].Aid != 7 {
			s.Close()
			t.Fatalf("open %d: got %+v want one row with aid=7", i, got)
		}
		s.Close()
	}
}

// TestOpenMigratesLegacyVideosTable writes a database with the pre-aid schema,
// then opens it with the current code and checks the aid column was added.
func TestOpenMigratesLegacyVideosTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE videos (
		id INTEGER PRIMARY KEY,
		bvid TEXT,
		cid INTEGER,
		author TEXT,
		title TEXT,
		pubdate INTEGER,
		duration INTEGER,
		page_index INTEGER,
		part TEXT,
		is_charging INTEGER NOT NULL DEFAULT 0,
		state TEXT,
		lang TEXT,
		source_key TEXT,
		UNIQUE(bvid,cid)
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO videos (bvid,cid,author,title,pubdate,duration,page_index,part,is_charging,state,lang,source_key) VALUES ('BV1',1,'','',0,0,0,'',0,'pending','','')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open legacy db: %v", err)
	}
	defer s.Close()

	got, err := s.ListVideos("")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Aid != 0 {
		t.Fatalf("legacy row after migration=%+v want one row with aid=0", got)
	}
	if _, err := s.UpsertVideo(&Video{Bvid: "BV2", Cid: 2, Aid: 9, State: StatePending}); err != nil {
		t.Fatalf("upsert with aid after migration: %v", err)
	}
	got, err = s.ListVideos("")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, v := range got {
		if v.Bvid == "BV2" && v.Aid == 9 {
			found = true
		}
	}
	if !found {
		t.Fatalf("migrated table did not store aid: %+v", got)
	}
}

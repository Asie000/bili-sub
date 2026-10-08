package store

import (
	"database/sql"
	"strings"
)

type Video struct {
	ID         int64
	Bvid       string
	Cid        int64
	Aid        int64
	Author     string
	Title      string
	Pubdate    int64
	Duration   int
	PageIndex  int
	Part       string
	IsCharging bool
	State      string
	Lang       string
	SourceKey  string
}

func (s *Store) UpsertVideo(v *Video) (int64, error) {
	_, err := s.db.Exec(`
		INSERT INTO videos (bvid,cid,aid,author,title,pubdate,duration,page_index,part,is_charging,state,lang,source_key)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(bvid,cid) DO UPDATE SET
			aid=excluded.aid,
			author=excluded.author, title=excluded.title, pubdate=excluded.pubdate,
			duration=excluded.duration, page_index=excluded.page_index, part=excluded.part,
			is_charging=excluded.is_charging, source_key=excluded.source_key`,
		v.Bvid, v.Cid, v.Aid, v.Author, v.Title, v.Pubdate, v.Duration, v.PageIndex, v.Part, boolToInt(v.IsCharging), v.State, v.Lang, v.SourceKey)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(`SELECT id FROM videos WHERE bvid=? AND cid=?`, v.Bvid, v.Cid).Scan(&id)
	return id, err
}

func (s *Store) ListVideos(state string) ([]Video, error) {
	q := `SELECT id,bvid,cid,aid,author,title,pubdate,duration,page_index,part,is_charging,state,lang,source_key FROM videos`
	order := ` ORDER BY id`
	var rows *sql.Rows
	var err error
	if state == "" {
		rows, err = s.db.Query(q + order)
	} else {
		rows, err = s.db.Query(q+` WHERE state=?`+order, state)
	}
	if err != nil {
		return nil, err
	}
	return scanVideos(rows)
}

// ListVideosBySource returns every row belonging to one parsed source (the key
// stored in source_key), ordered by id. It is what scopes the web UI's list to
// the source the user just probed.
func (s *Store) ListVideosBySource(source string) ([]Video, error) {
	rows, err := s.db.Query(`SELECT id,bvid,cid,aid,author,title,pubdate,duration,page_index,part,is_charging,state,lang,source_key FROM videos WHERE source_key=? ORDER BY id`, source)
	if err != nil {
		return nil, err
	}
	return scanVideos(rows)
}

// ListVideosFiltered returns the rows matching the optional state and source_key
// filters (an empty filter matches every row), ordered by id, with zero rows
// normalized to a non-nil slice. It backs the paged /api/videos endpoint and the
// source-scoped CLI operations, so a caller never has to build SQL itself.
func (s *Store) ListVideosFiltered(state, source string) ([]Video, error) {
	where, args := videoFilter(state, source)
	rows, err := s.db.Query(
		`SELECT id,bvid,cid,aid,author,title,pubdate,duration,page_index,part,is_charging,state,lang,source_key FROM videos`+where+` ORDER BY id`,
		args...)
	if err != nil {
		return nil, err
	}
	return scanVideos(rows)
}

// CountVideos returns how many rows match the same optional state/source_key
// filters as ListVideosFiltered. It is the total half of a paged response, so a
// caller can report the full size without materializing every row.
func (s *Store) CountVideos(state, source string) (int, error) {
	where, args := videoFilter(state, source)
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM videos`+where, args...).Scan(&n)
	return n, err
}

// DeleteAllVideos removes every row from videos and transcripts in one
// transaction and reports how many videos were deleted. Transcripts are cleared
// first so a partial failure cannot leave orphaned subtitle rows (no foreign key
// enforces the link).
func (s *Store) DeleteAllVideos() (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM transcripts`); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`DELETE FROM videos`)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// videoFilter builds the shared optional WHERE clause for the state and
// source_key filters, so the list and count queries cannot drift.
func videoFilter(state, source string) (string, []any) {
	var clauses []string
	var args []any
	if state != "" {
		clauses = append(clauses, "state=?")
		args = append(args, state)
	}
	if source != "" {
		clauses = append(clauses, "source_key=?")
		args = append(args, source)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

// scanVideos drains a videos query into a slice, normalizing the zero-row case
// so callers (e.g. JSON API handlers) get an empty list instead of a nil slice
// that marshals to null.
func scanVideos(rows *sql.Rows) ([]Video, error) {
	defer rows.Close()
	var out []Video
	for rows.Next() {
		var v Video
		if err := rows.Scan(&v.ID, &v.Bvid, &v.Cid, &v.Aid, &v.Author, &v.Title, &v.Pubdate, &v.Duration, &v.PageIndex, &v.Part, &v.IsCharging, &v.State, &v.Lang, &v.SourceKey); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if out == nil {
		out = []Video{}
	}
	return out, nil
}

func (s *Store) SetState(id int64, state, lang string) error {
	_, err := s.db.Exec(`UPDATE videos SET state=?, lang=? WHERE id=?`, state, lang, id)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

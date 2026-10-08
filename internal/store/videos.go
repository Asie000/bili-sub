package store

import "database/sql"

type Video struct {
	ID         int64
	Bvid       string
	Cid        int64
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
		INSERT INTO videos (bvid,cid,author,title,pubdate,duration,page_index,part,is_charging,state,lang,source_key)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(bvid,cid) DO UPDATE SET
			author=excluded.author, title=excluded.title, pubdate=excluded.pubdate,
			duration=excluded.duration, page_index=excluded.page_index, part=excluded.part,
			is_charging=excluded.is_charging, source_key=excluded.source_key`,
		v.Bvid, v.Cid, v.Author, v.Title, v.Pubdate, v.Duration, v.PageIndex, v.Part, boolToInt(v.IsCharging), v.State, v.Lang, v.SourceKey)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.db.QueryRow(`SELECT id FROM videos WHERE bvid=? AND cid=?`, v.Bvid, v.Cid).Scan(&id)
	return id, err
}

func (s *Store) ListVideos(state string) ([]Video, error) {
	q := `SELECT id,bvid,cid,author,title,pubdate,duration,page_index,part,is_charging,state,lang,source_key FROM videos`
	var rows *sql.Rows
	var err error
	if state == "" {
		rows, err = s.db.Query(q)
	} else {
		rows, err = s.db.Query(q+` WHERE state=?`, state)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Video
	for rows.Next() {
		var v Video
		if err := rows.Scan(&v.ID, &v.Bvid, &v.Cid, &v.Author, &v.Title, &v.Pubdate, &v.Duration, &v.PageIndex, &v.Part, &v.IsCharging, &v.State, &v.Lang, &v.SourceKey); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
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

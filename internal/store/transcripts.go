package store

import "time"

// SaveTranscript inserts or overwrites the canonical IR JSON of a video's
// transcript; a video has at most one transcript row (UNIQUE video_id).
func (s *Store) SaveTranscript(videoID int64, engine, lang, irJSON string) error {
	_, err := s.db.Exec(`
		INSERT INTO transcripts (video_id,engine,lang,ir_json,created_at)
		VALUES (?,?,?,?,?)
		ON CONFLICT(video_id) DO UPDATE SET
			engine=excluded.engine, lang=excluded.lang, ir_json=excluded.ir_json,
			created_at=excluded.created_at`,
		videoID, engine, lang, irJSON, time.Now().Unix())
	return err
}

// GetTranscript returns sql.ErrNoRows when the video has no stored transcript.
func (s *Store) GetTranscript(videoID int64) (engine, lang, irJSON string, err error) {
	err = s.db.QueryRow(
		`SELECT engine,lang,ir_json FROM transcripts WHERE video_id=?`, videoID).
		Scan(&engine, &lang, &irJSON)
	return engine, lang, irJSON, err
}

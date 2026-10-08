package store

const schema = `
CREATE TABLE IF NOT EXISTS videos (
	id INTEGER PRIMARY KEY,
	bvid TEXT,
	cid INTEGER,
	author TEXT,
	title TEXT,
	pubdate INTEGER,
	duration INTEGER,
	page_index INTEGER,
	part TEXT,
	is_charging INTEGER,
	state TEXT,
	lang TEXT,
	source_key TEXT,
	UNIQUE(bvid,cid)
);

CREATE TABLE IF NOT EXISTS transcripts (
	id INTEGER PRIMARY KEY,
	video_id INTEGER NOT NULL UNIQUE,
	engine TEXT,
	lang TEXT,
	ir_json TEXT,
	created_at INTEGER
);
`

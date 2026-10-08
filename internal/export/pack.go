package export

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"bilibili-sub/internal/ir"
	"bilibili-sub/internal/util"
)

// Pack renders every document in every format into one zip inside outDir.
// The archive is named YYYYMMDD-N.zip where N is the number of files written,
// and every entry is nested under a folder named exactly like the archive base
// so that unzipping cannot scatter files into the current directory.
func Pack(outDir string, now time.Time, docs map[string]*ir.Document, formats []string) (string, error) {
	base := now.Format("20060102") + "-" + strconv.Itoa(len(docs)*len(formats))
	zipPath := filepath.Join(outDir, base+".zip")

	f, err := os.Create(zipPath)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", zipPath, err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for _, d := range sortedDocs(docs) {
		stem := util.SanitizeFilename(d.Title, d.Bvid)
		for _, format := range formats {
			body, err := Render(d, format)
			if err != nil {
				return "", fmt.Errorf("%s.%s: %w", stem, format, err)
			}
			w, err := zw.Create(base + "/" + stem + "." + format)
			if err != nil {
				return "", fmt.Errorf("zip entry %s.%s: %w", stem, format, err)
			}
			if _, err := w.Write(body); err != nil {
				return "", fmt.Errorf("zip entry %s.%s: %w", stem, format, err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("close %s: %w", zipPath, err)
	}
	return zipPath, nil
}

// sortedDocs returns the documents ordered by map key so repeated exports of the
// same set produce identical archives.
func sortedDocs(docs map[string]*ir.Document) []*ir.Document {
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]*ir.Document, 0, len(keys))
	for _, k := range keys {
		out = append(out, docs[k])
	}
	return out
}

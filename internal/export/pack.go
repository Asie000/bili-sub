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
//
// A failed export leaves nothing behind: the partially written archive is
// closed and removed before the error is returned.
func Pack(outDir string, now time.Time, docs map[string]*ir.Document, formats []string) (zipPath string, err error) {
	base := now.Format("20060102") + "-" + strconv.Itoa(len(docs)*len(formats))
	zipPath = filepath.Join(outDir, base+".zip")

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", fmt.Errorf("create out dir %s: %w", outDir, err)
	}

	f, err := os.Create(zipPath)
	if err != nil {
		return "", fmt.Errorf("create %s: %w", zipPath, err)
	}
	// The deferred cleanup reads this copy: the named zipPath is zeroed by the
	// failing return statements, while cleanupPath keeps the on-disk location.
	cleanupPath := zipPath
	committed := false
	defer func() {
		if !committed {
			f.Close()
			os.Remove(cleanupPath)
		}
	}()

	zw := zip.NewWriter(f)
	written := make(map[string]struct{}, len(docs)*len(formats))
	for _, key := range sortedKeys(docs) {
		d := docs[key]
		if d == nil {
			return "", fmt.Errorf("document %q: nil document", key)
		}
		stem := util.SanitizeFilename(d.Title, d.Bvid)
		for _, format := range formats {
			entry := base + "/" + stem + "." + format
			if _, dup := written[entry]; dup {
				return "", fmt.Errorf("duplicate zip entry %q for stem %q: two documents share the same title and bvid", entry, stem)
			}
			body, err := Render(d, format)
			if err != nil {
				return "", fmt.Errorf("%s.%s: %w", stem, format, err)
			}
			w, err := zw.Create(entry)
			if err != nil {
				return "", fmt.Errorf("zip entry %s.%s: %w", stem, format, err)
			}
			if _, err := w.Write(body); err != nil {
				return "", fmt.Errorf("zip entry %s.%s: %w", stem, format, err)
			}
			written[entry] = struct{}{}
		}
	}
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("close zip %s: %w", zipPath, err)
	}
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("close %s: %w", zipPath, err)
	}
	committed = true
	return zipPath, nil
}

// sortedKeys returns the document keys ordered ascending so repeated exports of
// the same set produce identical archives.
func sortedKeys(docs map[string]*ir.Document) []string {
	keys := make([]string, 0, len(docs))
	for k := range docs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

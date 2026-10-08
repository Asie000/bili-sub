package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"bilibili-sub/internal/export"
	"bilibili-sub/internal/ir"
	"bilibili-sub/internal/store"
)

func newExportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "export",
		Short: "把已抓取的字幕打包成 zip",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			format, err := cmd.Flags().GetString("format")
			if err != nil {
				return err
			}
			// Validate before building the runtime so a typo cannot create a
			// database as a side effect.
			formats, err := parseFormats(format)
			if err != nil {
				return err
			}
			source, err := cmd.Flags().GetString("source")
			if err != nil {
				return err
			}
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			_, err = rt.export(cmd.OutOrStdout(), cmd.ErrOrStderr(), formats, source, nil)
			return err
		},
	}
	cmd.Flags().String("format", "srt,txt,json,md", "导出格式，逗号分隔：srt,txt,md,json")
	cmd.Flags().String("source", "", "只导出该来源（source_key）的字幕，空则导出全部")
	return cmd
}

// export packs the fetched/transcribed subtitles and returns the zip path (for
// the HTTP /api/export response). out receives the summary line, warn the
// per-video skip notices.
func (r *runtime) export(out, warn io.Writer, formats []string, source string, onProgress func(done, total int)) (string, error) {
	st, err := r.store()
	if err != nil {
		return "", err
	}
	// transcribed is included so ASR output (Task 15) is exported too, even
	// though nothing writes that state yet. A video cannot be in both states at
	// once, but the merge still dedupes by id so a future overlap cannot pack
	// one video twice.
	var videos []store.Video
	seen := map[int64]bool{}
	for _, state := range []string{store.StateFetched, store.StateTranscribed} {
		list, err := st.ListVideosFiltered(state, source)
		if err != nil {
			return "", err
		}
		for _, v := range list {
			if seen[v.ID] {
				continue
			}
			seen[v.ID] = true
			videos = append(videos, v)
		}
	}
	emit := func(done, total int) {
		if onProgress != nil {
			onProgress(done, total)
		}
	}
	if len(videos) == 0 {
		emit(0, 0)
		return "", errors.New("export: 没有已抓取的字幕，先运行 download")
	}
	// total is every row being packaged; a row whose IR is missing or corrupt is
	// still counted, so the final tick always reports the same total.
	emit(0, len(videos))
	tick := newProgressEmitter(len(videos), onProgress)
	doneCount := 0

	docs := make(map[string]*ir.Document, len(videos))
	for _, v := range videos {
		_, _, irJSON, err := st.GetTranscript(v.ID)
		// A locked or half-written row must not abort the whole archive: warn
		// with the bvid and skip that one video.
		if errors.Is(err, sql.ErrNoRows) {
			fmt.Fprintf(warn, "export: 跳过 %s：没有字幕记录\n", v.Bvid)
			doneCount++
			tick(doneCount)
			continue
		}
		if err != nil {
			return "", fmt.Errorf("export %s: 读取 IR: %w", v.Bvid, err)
		}
		var doc ir.Document
		if err := json.Unmarshal([]byte(irJSON), &doc); err != nil {
			fmt.Fprintf(warn, "export: 跳过 %s：IR 解析失败: %v\n", v.Bvid, err)
			doneCount++
			tick(doneCount)
			continue
		}
		// Multi-P videos share a bvid and title, so both the map key and the
		// document title need the page suffix; otherwise Pack rejects the
		// duplicate stems.
		key := v.Bvid
		if v.PageIndex > 1 {
			suffix := "_P" + strconv.Itoa(v.PageIndex)
			key += suffix
			doc.Title += suffix
		}
		docs[key] = &doc
		doneCount++
		tick(doneCount)
	}
	emit(len(videos), len(videos))
	if len(docs) == 0 {
		return "", errors.New("export: 没有可导出的字幕，所有视频都缺少有效 IR")
	}

	outDir := filepath.Join(r.cfg.DataDir, "exports")
	zipPath, err := export.Pack(outDir, time.Now(), docs, formats)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(out, "导出完成：%s（%d 个视频 × %d 种格式）\n", zipPath, len(docs), len(formats))
	return zipPath, nil
}

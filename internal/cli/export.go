package cli

import (
	"context"
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
		Short: "把尚未导出的字幕打包成 zip",
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
			includeExported, err := cmd.Flags().GetBool("include-exported")
			if err != nil {
				return err
			}
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			_, err = rt.export(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), formats, includeExported, nil)
			return err
		},
	}
	cmd.Flags().String("format", "txt", "导出格式，逗号分隔：txt,srt,md,json（默认只导出 txt）")
	cmd.Flags().Bool("include-exported", false, "连同已导出的字幕一起打包（默认只导出尚未导出的）")
	return cmd
}

// export packs the not-yet-exported subtitles and returns the zip path (for the
// HTTP /api/export response). out receives the summary line, warn the per-video
// skip notices. The exported state records what an earlier run already
// delivered; includeExported opts back into re-packaging those rows, e.g. to
// rebuild a lost archive. On success every packaged video is marked exported.
func (r *runtime) export(ctx context.Context, out, warn io.Writer, formats []string, includeExported bool, onProgress func(done, total int)) (string, error) {
	st, err := r.store()
	if err != nil {
		return "", err
	}
	// transcribed is included so the ASR leg's output is exported too; the
	// transcribe command is what writes that state. A video cannot be in two
	// states at once, but the merge still dedupes by id so an overlap can never
	// pack one video twice.
	states := []string{store.StateFetched, store.StateTranscribed}
	if includeExported {
		states = append(states, store.StateExported)
	}
	var videos []store.Video
	seen := map[int64]bool{}
	for _, state := range states {
		list, err := st.ListVideosFiltered(state, "")
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
		fmt.Fprintln(out, "没有尚未导出的字幕（如需重新导出，请勾选「包含已导出的」或用 --include-exported）。")
		return "", nil
	}
	// total is every row being packaged; a row whose IR is missing or corrupt is
	// still counted, so the final tick always reports the same total.
	emit(0, len(videos))
	tick := newProgressEmitter(len(videos), onProgress)
	doneCount := 0

	docs := make(map[string]*ir.Document, len(videos))
	stems := make(map[string]string, len(videos))
	keyIDs := make(map[string]int64, len(videos))
	for _, v := range videos {
		// Packing is local work, but a big archive still takes long enough that a
		// Ctrl+C should stop it rather than let it finish writing a zip the user
		// no longer wants.
		if err := ctx.Err(); err != nil {
			return "", err
		}
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
		// The IR is the subtitle's own record; a transcript written before the
		// author was stored can carry an empty Author, so fall back to the
		// video row's author to keep the UP主-prefixed file name meaningful.
		if doc.Author == "" {
			doc.Author = v.Author
		}
		// Multi-P videos share a bvid and title. The map key keeps them apart,
		// and the archive stem is passed in explicitly as pageStem(v): it puts
		// _P{n} after the bvid tag, where SanitizeSubtitleName's truncation
		// cannot drop it, and it matches the name the download leg wrote.
		key := v.Bvid
		if v.PageIndex > 1 {
			key += "_P" + strconv.Itoa(v.PageIndex)
		}
		docs[key] = &doc
		stems[key] = pageStem(v)
		keyIDs[key] = v.ID
		doneCount++
		tick(doneCount)
	}
	emit(len(videos), len(videos))
	if len(docs) == 0 {
		return "", errors.New("export: 没有可导出的字幕，所有视频都缺少有效 IR")
	}

	outDir := filepath.Join(r.cfg.DataDir, "exports")
	zipPath, err := export.Pack(outDir, time.Now(), docs, stems, formats)
	if err != nil {
		return "", err
	}
	// Only after the archive is safely on disk: marking first would lose the
	// video from a later export if Pack then failed.
	packedIDs := make([]int64, 0, len(keyIDs))
	for _, id := range keyIDs {
		packedIDs = append(packedIDs, id)
	}
	if err := st.MarkExported(packedIDs); err != nil {
		return "", fmt.Errorf("export: 标记已导出: %w", err)
	}
	fmt.Fprintf(out, "导出完成：%s（%d 个视频 × %d 种格式）\n", zipPath, len(docs), len(formats))
	return zipPath, nil
}

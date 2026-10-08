package cli

import (
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
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			return rt.export(cmd.OutOrStdout(), formats)
		},
	}
	cmd.Flags().String("format", "srt,txt,json,md", "导出格式，逗号分隔：srt,txt,md,json")
	return cmd
}

func (r *runtime) export(out io.Writer, formats []string) error {
	st, err := r.store()
	if err != nil {
		return err
	}
	// transcribed is included so ASR output (Task 15) is exported too, even
	// though nothing writes that state yet.
	var videos []store.Video
	for _, state := range []string{store.StateFetched, store.StateTranscribed} {
		list, err := st.ListVideos(state)
		if err != nil {
			return err
		}
		videos = append(videos, list...)
	}
	if len(videos) == 0 {
		return errors.New("export: 没有已抓取的字幕，先运行 download")
	}

	docs := make(map[string]*ir.Document, len(videos))
	for _, v := range videos {
		_, _, irJSON, err := st.GetTranscript(v.ID)
		if err != nil {
			return fmt.Errorf("export %s: 读取 IR: %w", v.Bvid, err)
		}
		var doc ir.Document
		if err := json.Unmarshal([]byte(irJSON), &doc); err != nil {
			return fmt.Errorf("export %s: 解析 IR: %w", v.Bvid, err)
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
	}

	outDir := filepath.Join(r.cfg.DataDir, "exports")
	zipPath, err := export.Pack(outDir, time.Now(), docs, formats)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "导出完成：%s（%d 个视频 × %d 种格式）\n", zipPath, len(docs), len(formats))
	return nil
}

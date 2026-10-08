package cli

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/export"
	"bilibili-sub/internal/server"
)

// loginCheckTimeout bounds the SESSDATA validity check POST /api/login/sessdata
// performs after saving the value.
const loginCheckTimeout = 30 * time.Second

func newServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "启动 Web 界面（HTTP API + SSE + 内嵌页面）",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			addr, err := cmd.Flags().GetString("addr")
			if err != nil {
				return err
			}
			rt, err := newRuntime(cmd)
			if err != nil {
				return err
			}
			defer rt.close()
			return rt.serve(cmd.OutOrStdout(), addr)
		},
	}
	cmd.Flags().String("addr", ":8080", "监听地址")
	return cmd
}

// serve wires the store and the pipeline seams into the HTTP server. The seams
// reuse the CLI helpers, so the browser runs exactly the same pipeline as
// `bilibili-sub probe|download|export`.
func (r *runtime) serve(out io.Writer, addr string) error {
	st, err := r.store()
	if err != nil {
		return err
	}
	hub := server.NewHub()

	srv := server.New(server.Options{
		Store: st,
		Hub:   hub,
		Probe: func(ctx context.Context, rawURL string) (server.ProbeSummary, error) {
			stats, err := r.probe(ctx, io.Discard, rawURL)
			if err != nil {
				return server.ProbeSummary{}, err
			}
			return server.ProbeSummary{
				Probed:         stats.Total,
				Downloadable:   stats.Downloadable,
				NoSubtitle:     stats.NoSubtitle,
				ChargingLocked: stats.ChargingLocked,
				Failed:         stats.Failed,
			}, nil
		},
		Download: func(ctx context.Context, lang string, transcribe bool) (server.DownloadSummary, error) {
			// The downloader owns the .srt; export renders the other formats
			// from the stored IR, so only srt is written here.
			stats, err := r.download(ctx, io.Discard, lang, []string{export.FormatSRT}, transcribe)
			if err != nil {
				return server.DownloadSummary{}, err
			}
			return server.DownloadSummary{Fetched: stats.Fetched, Failed: stats.Failed}, nil
		},
		Export: func(_ context.Context, formats []string) (string, error) {
			return r.export(io.Discard, io.Discard, formats)
		},
		Login:   r.saveSessdata,
		QRLogin: r.qrLogin(hub),
	})

	display := addr
	if strings.HasPrefix(display, ":") {
		display = "localhost" + display
	}
	fmt.Fprintf(out, "Web 界面已启动：http://%s（Ctrl+C 退出）\n", display)
	return http.ListenAndServe(addr, srv.Handler())
}

// saveSessdata persists a SESSDATA and verifies it before reporting success, so
// a typo is reported instead of silently stored.
func (r *runtime) saveSessdata(sessdata string) error {
	if err := bili.SaveSESSDATA(r.sessionPath(), sessdata); err != nil {
		return err
	}
	client, err := r.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), loginCheckTimeout)
	defer cancel()
	ok, err := client.CheckLogin(ctx)
	if err != nil {
		return fmt.Errorf("login: 校验失败: %w", err)
	}
	if !ok {
		return fmt.Errorf("login: SESSDATA 已保存，但校验未通过（未登录）")
	}
	return nil
}

// qrLogin builds the QRLogin seam. GET /api/login/qrcode only returns the code's
// URL, so the poll that completes the scan runs in the background and reports
// the result over SSE.
func (r *runtime) qrLogin(hub *server.Hub) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		client := bili.NewQRLoginClient()
		qrURL := make(chan string, 1)
		go func() {
			sessdata, err := client.Login(context.Background(), func(url string) {
				select {
				case qrURL <- url:
				default:
				}
			})
			if err == nil {
				err = bili.SaveSESSDATA(r.sessionPath(), sessdata)
			}
			if err != nil {
				hub.Broadcast(server.Event{Type: "login", Data: map[string]string{"error": err.Error()}})
				return
			}
			hub.Broadcast(server.Event{Type: "login", Data: map[string]bool{"ok": true}})
		}()
		select {
		case url := <-qrURL:
			return url, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

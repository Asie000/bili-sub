package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"bilibili-sub/internal/bili"
	"bilibili-sub/internal/export"
	"bilibili-sub/internal/server"
	"bilibili-sub/internal/store"
)

// loginCheckTimeout bounds the SESSDATA validity check POST /api/login/sessdata
// performs after saving the value.
const loginCheckTimeout = 30 * time.Second

// serveShutdownTimeout bounds the wait for in-flight requests after Ctrl+C.
const serveShutdownTimeout = 5 * time.Second

// serveReadHeaderTimeout caps how long a client may take to send its headers,
// so one stalled connection cannot hold a handler slot forever.
const serveReadHeaderTimeout = 10 * time.Second

// qrLoginTimeout bounds the background QR poll: a code that is scanned starts
// the login within seconds and one that is not expires on its own, so this is
// only a backstop that keeps a wedged poll from leaking its goroutine.
const qrLoginTimeout = 5 * time.Minute

// errOperationBusy is returned by a seam when another pipeline operation is
// still running. Probe, download and export all fetch through the same client,
// so overlap would multiply the request rate BILI_QPS is there to cap.
var errOperationBusy = errors.New("已有任务在执行，请等它完成后再试")

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
			return rt.serve(cmd.Context(), cmd.OutOrStdout(), addr)
		},
	}
	cmd.Flags().String("addr", ":8080", "监听地址")
	return cmd
}

// serve wires the store and the pipeline seams into the HTTP server. It returns
// when the listener fails or when the process is asked to stop (ctx, which
// main cancels on Ctrl+C).
//
// Stopping on request is why this no longer calls http.ListenAndServe directly:
// once Ctrl+C cancels a context instead of killing the process, a bare
// ListenAndServe would leave a server that ignores the very key its own startup
// message tells the user to press.
func (r *runtime) serve(ctx context.Context, out io.Writer, addr string) error {
	st, err := r.store()
	if err != nil {
		return err
	}
	srv := server.New(r.serveOptions(st, server.NewHub()))

	display := addr
	if strings.HasPrefix(display, ":") {
		display = "localhost" + display
	}
	fmt.Fprintf(out, "Web 界面已启动：http://%s（Ctrl+C 退出）\n", display)

	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: serveReadHeaderTimeout,
	}
	// ListenAndServe blocks, so the shutdown path below needs the error value
	// in hand before it can pick it up.
	errCh := make(chan error, 1)
	go func() { errCh <- httpSrv.ListenAndServe() }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// Let the request in flight finish, then stop. The wait is short
		// because a download leg can take minutes and a second Ctrl+C (main
		// restores the default handler on the first one) still exits
		// immediately.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), serveShutdownTimeout)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			_ = httpSrv.Close()
			return err
		}
		// Unlike probe/download/transcribe, where a cancellation must not look
		// like a clean run, stopping this server is the documented, normal way
		// out, so it is not reported as an error.
		fmt.Fprintln(out, "Web 界面已停止。")
		return nil
	}
}

// serveOptions builds the server's seams. They reuse the CLI helpers, so the
// browser runs exactly the same pipeline as `bilibili-sub probe|download|export`.
// The three long-running seams share r.opMu and report errOperationBusy instead
// of running alongside each other.
func (r *runtime) serveOptions(st *store.Store, hub *server.Hub) server.Options {
	return server.Options{
		Store: st,
		Hub:   hub,
		Probe: func(ctx context.Context, rawURL, kind string, force bool, onProgress func(server.Progress)) (server.ProbeSummary, error) {
			if !r.opMu.TryLock() {
				return server.ProbeSummary{}, errOperationBusy
			}
			defer r.opMu.Unlock()
			var progress func(done, total int)
			if onProgress != nil {
				progress = func(done, total int) {
					onProgress(server.Progress{Op: "probe", Done: done, Total: total})
				}
			}
			stats, err := r.probe(ctx, io.Discard, rawURL, kind, force, progress)
			if err != nil {
				return server.ProbeSummary{}, err
			}
			return server.ProbeSummary{
				Probed:         stats.Total,
				Downloadable:   stats.Downloadable,
				NoSubtitle:     stats.NoSubtitle,
				ChargingLocked: stats.ChargingLocked,
				Failed:         stats.Failed,
				Skipped:        stats.Skipped,
			}, nil
		},
		Download: func(ctx context.Context, lang string, transcribe bool, asrLang, model, source string, onProgress func(server.Progress)) (server.DownloadSummary, error) {
			if !r.opMu.TryLock() {
				return server.DownloadSummary{}, errOperationBusy
			}
			defer r.opMu.Unlock()
			var progress func(done, total int)
			if onProgress != nil {
				progress = func(done, total int) {
					onProgress(server.Progress{Op: "download", Done: done, Total: total})
				}
			}
			// The downloader owns the .srt; export renders the other formats
			// from the stored IR, so only srt is written here. The language and
			// model come from the page, falling back to the config inside
			// runtime.transcribe.
			stats, err := r.download(ctx, io.Discard, lang, []string{export.FormatSRT}, transcribe, asrLang, model, source, progress)
			// The counts go back even when the run failed part-way: the handler
			// decides whether that is a partial success or a real failure.
			return server.DownloadSummary{
				Fetched:          stats.Fetched,
				Failed:           stats.Failed,
				Transcribed:      stats.Transcribed,
				TranscribeFailed: stats.TranscribeFailed,
			}, err
		},
		Export: func(_ context.Context, formats []string, includeExported bool, onProgress func(server.Progress)) (string, error) {
			if !r.opMu.TryLock() {
				return "", errOperationBusy
			}
			defer r.opMu.Unlock()
			var progress func(done, total int)
			if onProgress != nil {
				progress = func(done, total int) {
					onProgress(server.Progress{Op: "export", Done: done, Total: total})
				}
			}
			return r.export(io.Discard, io.Discard, formats, includeExported, progress)
		},
		Login:       r.saveSession,
		LoginStatus: r.loginStatus,
		QRLogin:     r.qrLogin(hub),
	}
}

// loginStatus reports whether the stored SESSDATA is still accepted and the
// account name, so a refresh can restore the header instead of showing nothing.
func (r *runtime) loginStatus(ctx context.Context) (server.LoginState, error) {
	client, err := r.client()
	if err != nil {
		return server.LoginState{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, loginCheckTimeout)
	defer cancel()
	ok, uname, err := client.NavStatus(ctx)
	if err != nil {
		return server.LoginState{}, err
	}
	return server.LoginState{LoggedIn: ok, Uname: uname}, nil
}

// saveSession verifies a SESSDATA/full Cookie BEFORE persisting it, so a typo
// or a stale value cannot clobber a working login.
func (r *runtime) saveSession(sessdata, cookie string) error {
	client, err := r.client()
	if err != nil {
		return err
	}
	client.SESSDATA = sessdata
	client.Cookie = cookie

	ctx, cancel := context.WithTimeout(context.Background(), loginCheckTimeout)
	defer cancel()
	ok, err := client.CheckLogin(ctx)
	if err != nil {
		return fmt.Errorf("login: 校验失败: %w", err)
	}
	if !ok {
		return fmt.Errorf("login: 校验未通过（未登录），已保留原有登录信息")
	}
	return bili.SaveSession(r.sessionPath(), sessdata, cookie)
}

// qrResult is the single outcome the QR goroutine reports back to its request:
// the code's url once it exists, or the error that stopped the flow before that.
type qrResult struct {
	url string
	err error
}

// newQRLoginClient builds the QR login client. It is a variable so tests can
// point the flow at a local server.
var newQRLoginClient = bili.NewQRLoginClient

// qrLogin builds the QRLogin seam. GET /api/login/qrcode returns only the
// code's url, so the scan-completing poll runs in a background goroutine and
// reports its result over SSE. The goroutine always sends exactly one result (a
// url or an error) before returning, so the request can never wait forever for
// a url that will not arrive.
func (r *runtime) qrLogin(hub *server.Hub) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		client := newQRLoginClient()
		// The poll outlives this request: the browser displays the url and the
		// scan is confirmed later. It therefore runs on its own context rather
		// than the request's (net/http cancels the request context as soon as
		// ServeHTTP returns), bounded by qrLoginTimeout.
		pollCtx, cancelPoll := context.WithTimeout(context.Background(), qrLoginTimeout)
		done := make(chan qrResult, 1)
		go func() {
			defer cancelPoll()
			var urlSent bool
			sessdata, err := client.Login(pollCtx, func(url string) {
				urlSent = true
				// Buffered, so the send never blocks even if the request is gone.
				done <- qrResult{url: url}
			})
			if err == nil {
				err = bili.SaveSession(r.sessionPath(), sessdata, "")
			}
			switch {
			case err == nil:
				hub.Broadcast(server.Event{Type: "login", Data: map[string]bool{"ok": true}})
			case errors.Is(err, context.Canceled):
				// The request gave up and cancelled the poll; it already answered
				// with that error, so there is nothing to report.
			case urlSent:
				hub.Broadcast(server.Event{Type: "login", Data: map[string]string{"error": err.Error()}})
			default:
				done <- qrResult{err: err}
			}
		}()
		select {
		case res := <-done:
			if res.err != nil {
				cancelPoll()
				return "", res.err
			}
			return res.url, nil
		case <-ctx.Done():
			// A client that disconnected before the url arrived stops the poll.
			cancelPoll()
			return "", ctx.Err()
		}
	}
}

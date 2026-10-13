package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"bilibili-sub/internal/cli"
)

func main() {
	// Ctrl+C cancels the command's context instead of killing the process, so
	// the pipeline gets to clean up after itself: the temporary dash audio is
	// deleted and no video is marked failed by a run the user interrupted. The
	// commands already guard on ctx.Err() for exactly this; what was missing was
	// anyone turning the key into a cancellation.
	//
	// Installing the handler costs the escape hatch of "just kill it", so the
	// registration is dropped again as soon as the first signal lands: a second
	// Ctrl+C gets the default behaviour and exits at once, which is what an
	// unresponsive run needs.
	//
	// SIGTERM is here for the non-Windows case: `docker stop`, systemd and timeout
	// all send it, and without this they would keep killing the process outright --
	// leaving the same half-finished state Ctrl+C used to leave. Windows never
	// delivers it, registering it there costs nothing.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop()
	}()

	if err := cli.NewRootCmd().ExecuteContext(ctx); err != nil {
		os.Exit(1)
	}
}

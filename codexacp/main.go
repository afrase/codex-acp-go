package codexacp

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
)

// Main runs the agent on stdin and stdout, logging to stderr (cfg.Log, if
// set, takes its place). The codex it runs is cfg.CodexPath, else
// $CODEX_PATH (the variable codex-acp reads too), else codex on PATH, and
// cfg.Spawn starts it (nil: appserver.Exec).
//
// With appserver.Exec the app-server stays in the agent's process group,
// as codex-acp's does, so a client that kills the agent's group kills the
// app-server with it. SIGTERM and SIGINT end the agent as the end of stdin
// does: the app-server is stopped; a second signal has its default effect.
//
// Main returns nil when the client went or a signal ended the agent, and
// otherwise what broke the connection (already logged), so that a
// command can exit with an error.
func Main(cfg Config) error {
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	log := cfg.Log
	if cfg.CodexPath == "" {
		cfg.CodexPath = os.Getenv("CODEX_PATH")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)
	go func() {
		select {
		case sig := <-sigs:
			signal.Stop(sigs) // the next one has its default effect
			log.Info("stopping on a signal", "signal", sig)
			cancel()
		case <-ctx.Done():
		}
	}()
	err := Serve(ctx, os.Stdin, os.Stdout, cfg)
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		log.Error("serving the client", "err", err)
	}
	return err
}

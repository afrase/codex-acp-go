package codexacp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"

	"github.com/afrase/acp-go/steering"
	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver"
)

// Config is how the agent runs Codex.
type Config struct {
	CodexPath string            // "" = codex, looked up on PATH
	Env       []string          // codex's environment; nil = this process's
	Spawn     appserver.Spawner // nil = appserver.Exec
	Log       *slog.Logger      // nil = no log; the agent never logs a prompt or a token
}

// readLimit caps a message from the client: a prompt with images can run
// to tens of MiB.
const readLimit = 64 << 20

// agent is the ACP agent: the client's connection, one app-server, and
// the sessions open on it, each a Codex thread.
type agent struct {
	acp.UnimplementedAgent
	cfg    Config
	log    *slog.Logger
	client *acp.ClientConn
	out    *afterAnswer
	ctx    context.Context //nolint:containedctx // ends with the client's connection

	mu       sync.Mutex
	server   *appserver.Conn
	proc     appserver.Proc
	forms    bool // the client answers form elicitations
	stopping bool // stop has begun: the app-server's exit is the agent's doing
	sessions map[string]*session
}

// Serve runs the agent on r and w, the client's streams, until the client
// goes, ctx ends or the app-server exits (the client's connection is then
// closed: the agent quit); then the app-server is stopped.
// It returns nil once the client has gone, ctx's error when ctx ended
// first, and otherwise what broke the connection.
func Serve(ctx context.Context, r io.Reader, w io.Writer, cfg Config) error {
	a := &agent{cfg: cfg, log: cfg.Log, sessions: map[string]*session{}}
	if a.log == nil {
		a.log = slog.New(slog.DiscardHandler)
	}
	actx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()
	a.ctx = actx
	a.out = &afterAnswer{w: w}
	conn := acp.NewClientConn(r, a.out, func(c *acp.ClientConn) acp.Agent {
		a.client = c
		return a
	}, acp.WithReadLimit(readLimit), acp.WithLogger(a.log))
	select {
	case <-conn.Done():
	case <-ctx.Done():
		_ = conn.Close()
		<-conn.Done()
	}
	a.stop()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := errors.Unwrap(conn.Err()); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("codexacp: the client's connection: %w", err)
	}
	return nil
}

// stop ends the app-server and every session's turn.
func (a *agent) stop() {
	a.mu.Lock()
	a.stopping = true
	server, proc := a.server, a.proc
	open := make([]*session, 0, len(a.sessions))
	for _, s := range a.sessions {
		open = append(open, s)
	}
	a.mu.Unlock()
	for _, s := range open {
		s.end(errors.New("the agent stopped"))
	}
	if server != nil {
		_ = server.Close()
		proc.Kill()
	}
}

// startServer starts the app-server and initializes it, once.
func (a *agent) startServer(ctx context.Context, info *acp.Implementation) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.server != nil {
		return nil
	}
	spawn := a.cfg.Spawn
	if spawn == nil {
		spawn = appserver.Exec
	}
	path := a.cfg.CodexPath
	if path == "" {
		path = "codex"
	}
	proc, err := spawn(appserver.Spec{Path: path, Args: []string{"app-server"}, Env: a.cfg.Env})
	if err != nil {
		return fmt.Errorf("start codex app-server: %w", err)
	}
	server := appserver.NewConn(proc.Out, proc.In, router{a}, a.log)
	client := appserver.ClientInfo{Name: Name, Title: "Codex", Version: Version}
	if info != nil && info.Name != "" {
		client = appserver.ClientInfo{Name: info.Name, Title: info.Title, Version: info.Version}
	}
	if client.Version == "" {
		client.Version = Version
	}
	err = server.Call(ctx, "initialize", appserver.InitializeParams{ClientInfo: client,
		Capabilities: &appserver.InitializeCapabilities{ExperimentalAPI: true}}, nil)
	if err != nil {
		_ = server.Close()
		proc.Kill()
		if said := proc.Said(); said != "" {
			return fmt.Errorf("codex app-server: %w (%s)", err, said)
		}
		return fmt.Errorf("codex app-server: %w", err)
	}
	a.server, a.proc = server, proc
	go func() { // Codex gone, the agent goes: the client says the agent quit
		<-server.Done()
		a.mu.Lock()
		stopping := a.stopping
		a.mu.Unlock()
		if !stopping {
			a.log.Warn("codex app-server exited", "said", proc.Said())
		}
		_ = a.client.Close()
	}()
	return nil
}

// call calls the app-server.
func (a *agent) call(ctx context.Context, method string, params, result any) error {
	a.mu.Lock()
	server := a.server
	a.mu.Unlock()
	if server == nil {
		return &acp.Error{Code: acp.ErrorCodeInternalError, Message: "Codex isn't running: initialize first"}
	}
	err := server.Call(ctx, method, params, result)
	if e, ok := errors.AsType[*appserver.Error](err); ok {
		// As the ACP SDK under codex-acp answers an error thrown: Internal
		// error, the app-server's words in data.details. A client reads a
		// session Codex no longer has from them ("no rollout found").
		return &acp.Error{Code: acp.ErrorCodeInternalError, Message: "Internal error", Data: raw(map[string]any{"details": e.Message})}
	}
	return err
}

func (a *agent) Initialize(ctx context.Context, req *acp.InitializeRequest) (*acp.InitializeResponse, error) {
	if err := a.startServer(ctx, req.ClientInfo); err != nil {
		return nil, err
	}
	a.mu.Lock()
	c := req.ClientCapabilities
	a.forms = c != nil && c.Elicitation != nil && c.Elicitation.Form != nil
	a.mu.Unlock()
	return &acp.InitializeResponse{
		ProtocolVersion: acp.LatestProtocolVersion,
		AgentInfo:       &acp.Implementation{Name: Name, Title: "Codex", Version: Version},
		AgentCapabilities: &acp.AgentCapabilities{
			LoadSession:        true,
			PromptCapabilities: &acp.PromptCapabilities{Image: true, EmbeddedContext: true},
			MCPCapabilities:    &acp.MCPCapabilities{HTTP: true},
			SessionCapabilities: &acp.SessionCapabilities{
				Close:  &acp.SessionCloseCapabilities{},
				Resume: &acp.SessionResumeCapabilities{},
				Fork:   &acp.SessionForkCapabilities{},
				List:   &acp.SessionListCapabilities{},
				Delete: &acp.SessionDeleteCapabilities{},
			},
		},
		Meta: steering.Advertise(nil),
	}, nil
}

// session is the open session id, or an Invalid params error.
func (a *agent) session(id acp.SessionID) (*session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if s := a.sessions[string(id)]; s != nil {
		return s, nil
	}
	return nil, &acp.Error{Code: acp.ErrorCodeInvalidParams, Message: fmt.Sprintf("no open session %q", id)}
}

// update sends the client a session's update.
func (a *agent) update(sid string, u acp.SessionUpdate) {
	if err := a.client.SessionUpdate(a.ctx, &acp.SessionNotification{SessionID: acp.SessionID(sid), Update: u}); err != nil {
		a.log.Debug("update not sent", "session", sid, "err", err)
	}
}

// router hands what the app-server sends to the session of its thread.
type router struct{ a *agent }

// threadOf is the threadId of a message's params, "" without one.
func threadOf(params json.RawMessage) string {
	var p struct {
		ThreadID *string `json:"threadId"`
	}
	_ = json.Unmarshal(params, &p)
	if p.ThreadID == nil {
		return ""
	}
	return *p.ThreadID
}

func (r router) Notified(method string, params json.RawMessage) {
	tid := threadOf(params)
	r.a.mu.Lock()
	s := r.a.sessions[tid]
	var all []*session
	if tid == "" {
		for _, s := range r.a.sessions {
			all = append(all, s)
		}
	}
	r.a.mu.Unlock()
	switch {
	case s != nil:
		s.notified(method, params)
	case tid == "":
		for _, s := range all { // a warning of no thread: the sessions with a turn running
			s.unthreaded(method, params)
		}
	}
}

func (r router) Asked(ctx context.Context, method string, params json.RawMessage) (any, error) {
	r.a.mu.Lock()
	s := r.a.sessions[threadOf(params)]
	r.a.mu.Unlock()
	if s == nil {
		return refusal(method), nil
	}
	return s.asked(ctx, method, params)
}

// refusal is the answer to a request of a thread with no session, as
// codex-acp's: cancel.
func refusal(method string) any {
	switch method {
	case "item/tool/requestUserInput":
		return map[string]any{"answers": map[string]any{}}
	case "mcpServer/elicitation/request":
		return map[string]any{"action": "cancel", "content": nil, "_meta": nil}
	case "item/permissions/requestApproval":
		return map[string]any{"permissions": map[string]any{}, "scope": "turn", "strictAutoReview": false}
	}
	return appserver.Decision{Decision: "cancel"}
}

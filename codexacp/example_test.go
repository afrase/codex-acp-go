package codexacp_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver/appservertest"
	"github.com/afrase/codex-acp-go/codexacp"
)

// printer is an ACP client that keeps the agent's text.
type printer struct {
	acp.UnimplementedClient
	mu   sync.Mutex
	text strings.Builder
}

func (p *printer) SessionUpdate(_ context.Context, n *acp.SessionNotification) error {
	if c := n.Update.AgentMessageChunk; c != nil && c.Content.Text != nil {
		p.mu.Lock()
		p.text.WriteString(c.Content.Text.Text)
		p.mu.Unlock()
	}
	return nil
}

// fakeCodex is a scripted app-server: signed in, and answering a turn
// with one streamed message.
func fakeCodex() *appservertest.Fake {
	f := appservertest.New()
	f.Codex()
	f.Turn("turn_1", "completed", appservertest.Note{Method: "item/agentMessage/delta",
		Params: map[string]any{"threadId": "thr_1", "turnId": "turn_1", "itemId": "msg_1", "delta": "Hello from Codex"}})
	return f
}

// Serve runs the agent on a client's streams: here an ACP client in the
// same process, and a fake app-server in place of the real Codex (leave
// Spawn nil to run codex). A program that only serves the agent on stdin
// and stdout calls Main.
func ExampleServe() {
	ctx := context.Background()
	toAgentR, toAgentW := io.Pipe()
	fromAgentR, fromAgentW := io.Pipe()
	served := make(chan error, 1)
	go func() {
		served <- codexacp.Serve(ctx, toAgentR, fromAgentW, codexacp.Config{Spawn: fakeCodex().Spawn})
		_ = fromAgentW.Close()
	}()
	client := &printer{}
	conn := acp.NewAgentConn(fromAgentR, toAgentW, func(*acp.AgentConn) acp.Client { return client })

	init, err := conn.Initialize(ctx, &acp.InitializeRequest{ProtocolVersion: acp.LatestProtocolVersion})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("agent:", init.AgentInfo.Name)
	s, err := conn.NewSession(ctx, &acp.NewSessionRequest{Cwd: "/work/project", MCPServers: []acp.MCPServer{}})
	if err != nil {
		fmt.Println(err)
		return
	}
	r, err := conn.Prompt(ctx, &acp.PromptRequest{SessionID: s.SessionID, Prompt: []acp.ContentBlock{{Text: &acp.TextContent{Text: "Say hello"}}}})
	if err != nil {
		fmt.Println(err)
		return
	}
	client.mu.Lock()
	fmt.Println("codex:", client.text.String())
	client.mu.Unlock()
	fmt.Println("stop:", r.StopReason)

	_ = toAgentW.Close() // the client goes: the agent stops the app-server
	fmt.Println("served:", <-served)
	// Output:
	// agent: codexacp
	// codex: Hello from Codex
	// stop: end_turn
	// served: <nil>
}

// Main serves one ACP client on stdin and stdout, as a program an editor
// starts. Here the program names the codex to run and logs as JSON; a
// Spawn of its own could start codex another way (in a container, say).
func ExampleMain() { //nolint:testableexamples // Main serves this process's stdin: it compiles, but a test can't run it
	cfg := codexacp.Config{
		CodexPath: "/opt/homebrew/bin/codex",
		Log:       slog.New(slog.NewJSONHandler(os.Stderr, nil)),
	}
	if err := codexacp.Main(cfg); err != nil {
		os.Exit(1)
	}
}

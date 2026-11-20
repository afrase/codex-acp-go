package appserver_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/afrase/codex-acp-go/appserver"
	"github.com/afrase/codex-acp-go/appserver/appservertest"
)

// ignore is a Handler that takes the server's notifications and declines
// its requests.
type ignore struct{}

func (ignore) Notified(string, json.RawMessage) {}

func (ignore) Asked(context.Context, string, json.RawMessage) (any, error) {
	return appserver.Decision{Decision: "decline"}, nil
}

// A client of the app-server: initialize, then a call. Exec in place of
// the fake's Spawn runs the real `codex app-server`.
func ExampleConn() {
	ctx := context.Background()
	fake := appservertest.New()
	fake.Codex()
	proc, err := fake.Spawn(appserver.Spec{Path: "codex", Args: []string{"app-server"}})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer proc.Kill()
	conn := appserver.NewConn(proc.Out, proc.In, ignore{}, nil)
	defer func() { _ = conn.Close() }()

	err = conn.Call(ctx, "initialize", appserver.InitializeParams{
		ClientInfo:   appserver.ClientInfo{Name: "example", Version: "1.0.0"},
		Capabilities: &appserver.InitializeCapabilities{ExperimentalAPI: true},
	}, nil)
	if err != nil {
		fmt.Println(err)
		return
	}
	var models appserver.ModelListResponse
	if err := conn.Call(ctx, "model/list", appserver.ModelListParams{}, &models); err != nil {
		fmt.Println(err)
		return
	}
	for _, m := range models.Data {
		fmt.Println(m.ID, m.IsDefault)
	}
	// Output:
	// fake-model-1 true
	// fake-model-2 false
}

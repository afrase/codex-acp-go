package appservertest

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/afrase/codex-acp-go/appserver"
)

type client struct {
	notes chan string
}

func (c client) Notified(method string, _ json.RawMessage) { c.notes <- method }

func (c client) Asked(context.Context, string, json.RawMessage) (any, error) {
	return map[string]string{"decision": "accept"}, nil
}

// The fake answers by its script, sends a turn's notifications after
// turn/start's answer, keeps the calls, asks the client, and exits.
func TestFake(t *testing.T) {
	ctx := t.Context()
	f := New()
	f.Codex()
	f.Turn("turn_1", "completed", Note{"item/agentMessage/delta", map[string]any{"threadId": "thr_1", "itemId": "msg_1", "delta": "hi"}})
	p, err := f.Spawn(appserver.Spec{Path: "/fake/codex", Args: []string{"app-server"}})
	if err != nil {
		t.Fatal(err)
	}
	c := client{notes: make(chan string, 8)}
	conn := appserver.NewConn(p.Out, p.In, c, nil)
	var opened appserver.ThreadOpened
	if err := conn.Call(ctx, "thread/start", appserver.ThreadStartParams{Cwd: "/w/proj"}, &opened); err != nil || opened.Thread.ID != "thr_1" || opened.Thread.Cwd != "/w/proj" {
		t.Fatalf("thread/start: %+v, %v", opened, err)
	}
	var e *appserver.Error
	if err := conn.Call(ctx, "nothing", nil, nil); !errors.As(err, &e) || e.Code != appserver.CodeMethodNotFound {
		t.Errorf("unscripted: %v", err)
	}
	if err := conn.Call(ctx, "turn/start", appserver.TurnStartParams{ThreadID: "thr_1"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"turn/started", "item/agentMessage/delta", "turn/completed"} {
		if got := <-c.notes; got != want {
			t.Errorf("notification %s, want %s", got, want)
		}
	}
	if got, err := f.Ask(ctx, "item/fileChange/requestApproval", map[string]any{"threadId": "thr_1"}); err != nil || string(got) != `{"decision":"accept"}` {
		t.Errorf("ask: %s, %v", got, err)
	}
	if err := conn.Notify("initialized", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Await(ctx, "initialized", 0); err != nil {
		t.Fatal(err)
	}
	if n := len(f.Calls("thread/start")); n != 1 || f.Spec().Args[0] != "app-server" {
		t.Errorf("%d thread/start calls, spec %v", n, f.Spec())
	}
	p.Kill()
	<-f.Exited()
	<-conn.Done()
}

package appservertest

import (
	"encoding/json"
	"strconv"
	"sync"
)

// Made-up data a scripted Codex answers with: its models, its folder.
const (
	Model  = "fake-model-1"
	Model2 = "fake-model-2"
)

// Models is model/list's page: two models, the first the default, with
// low and medium effort; the second takes images too.
func Models() map[string]any {
	efforts := []map[string]any{{"reasoningEffort": "low", "description": "Quick"}, {"reasoningEffort": "medium", "description": "Balanced"}}
	model := func(id, name string, def bool, modalities ...string) map[string]any {
		return map[string]any{"id": id, "model": id, "displayName": name, "description": name + " model", "hidden": false,
			"supportedReasoningEfforts": efforts, "defaultReasoningEffort": "medium", "inputModalities": modalities,
			"isDefault": def, "serviceTiers": []any{}, "additionalSpeedTiers": []any{}}
	}
	return map[string]any{"data": []any{model(Model, "Fake One", true, "text"), model(Model2, "Fake Two", false, "text", "image")}, "nextCursor": nil}
}

// Thread is a thread as the app-server sends it, made up.
func Thread(id, cwd string) map[string]any {
	return map[string]any{"id": id, "sessionId": id, "preview": "", "name": nil, "cwd": cwd, "createdAt": 1_700_000_000,
		"updatedAt": 1_700_000_000, "historyMode": "paginated", "model": Model, "modelProvider": "fake", "reasoningEffort": "medium",
		"ephemeral": false, "status": map[string]any{"type": "idle"}, "turns": []any{}, "source": "appServer", "cliVersion": "0.163.0"}
}

// Opened is thread/start's (and resume's, fork's) answer for thread.
func Opened(thread map[string]any) map[string]any {
	return map[string]any{"thread": thread, "model": Model, "modelProvider": "fake", "serviceTier": nil,
		"reasoningEffort": "medium", "cwd": thread["cwd"], "approvalPolicy": "on-request", "approvalsReviewer": "user",
		"sandbox": map[string]any{"type": "workspaceWrite"}, "itemsBackwardsCursor": nil, "collaborationMode": nil}
}

// Codex scripts f as a signed-in Codex: account/read, model/list,
// skills/list, and thread/start opening threads thr_1, thr_2… in the
// folder asked for.
func (f *Fake) Codex() {
	f.On("account/read", Reply(map[string]any{"account": map[string]any{"type": "apiKey"}, "requiresOpenaiAuth": true}))
	f.On("model/list", Reply(Models()))
	f.On("skills/list", Reply(map[string]any{"data": []any{}}))
	f.On("thread/unsubscribe", Reply(map[string]any{"status": "unsubscribed"}))
	var mu sync.Mutex
	n := 0
	f.On("thread/start", func(params json.RawMessage) (any, error) {
		var p struct {
			Cwd string `json:"cwd"`
		}
		_ = json.Unmarshal(params, &p)
		mu.Lock()
		n++
		id := "thr_" + strconv.Itoa(n)
		mu.Unlock()
		return Opened(Thread(id, p.Cwd)), nil
	})
}

// Note is a notification a script sends.
type Note struct {
	Method string
	Params any
}

// Turn scripts turn/start: it answers with turn id (in progress), then
// sends turn/started, notes and, unless end is "", turn/completed with
// that status.
func (f *Fake) Turn(id, end string, notes ...Note) {
	f.On("turn/start", func(json.RawMessage) (any, error) {
		return map[string]any{"turn": TurnOf(id, "inProgress")}, nil
	})
	f.Then("turn/start", func(params json.RawMessage) {
		var p struct {
			ThreadID string `json:"threadId"`
		}
		_ = json.Unmarshal(params, &p)
		f.Notify("turn/started", map[string]any{"threadId": p.ThreadID, "turn": TurnOf(id, "inProgress")})
		for _, n := range notes {
			f.Notify(n.Method, n.Params)
		}
		if end != "" {
			f.Notify("turn/completed", map[string]any{"threadId": p.ThreadID, "turn": TurnOf(id, end)})
		}
	})
}

// TurnOf is a turn with no items, in status.
func TurnOf(id, status string) map[string]any {
	return map[string]any{"id": id, "items": []any{}, "itemsView": "notLoaded", "status": status, "error": nil,
		"startedAt": nil, "completedAt": nil, "durationMs": nil}
}

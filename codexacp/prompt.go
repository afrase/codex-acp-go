package codexacp

import (
	"context"
	"encoding/json"
	"math"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	acp "github.com/afrase/acp-go/unstable"

	"github.com/afrase/codex-acp-go/appserver"
)

// turn is a prompt's Codex turn: its id once Codex says it, how it ended,
// and what it used.
type turn struct {
	ctx    context.Context //nolint:containedctx // ends with the turn: its questions to the user go with it
	cancel context.CancelFunc
	gone   chan struct{} // closed when the session closes or the agent stops
	once   sync.Once
	known  chan struct{} // closed once Codex says the turn's id
	done   chan struct{} // closed when the prompt returns
	// stop sends turn/interrupt once, on a goroutine of its own: Cancel
	// runs on the goroutine reading the client, which must never wait on
	// Codex. stopped is closed once Codex has answered it, or
	// interruptGrace after it was sent, for a caller whose next call must
	// come after it.
	stop    sync.Once
	stopped chan struct{}
	// late is closed interruptGrace after the turn was cancelled: Codex
	// hasn't ended it, and the prompt ends without its turn/completed.
	late     chan struct{}
	lateOnce sync.Once
	lateStop *time.Timer

	mu        sync.Mutex
	id        string
	cancelled bool                      // the client cancelled it
	ended     chan appserver.Turn       // turn/completed's turn
	early     map[string]appserver.Turn // turns completed before their id was known
	failure   error                     // what the prompt fails with, from an error notification
	usage     tokens
}

// Prompt runs a turn: the prompt as Codex input, in the session's mode,
// model and effort, until turn/completed.
func (a *agent) Prompt(ctx context.Context, req *acp.PromptRequest) (*acp.PromptResponse, error) {
	return a.prompt(ctx, sequence(ctx), req)
}

// sequence is where the message ctx is for came among the client's
// (acp.SequenceFromContext); one with none comes after every cancel.
func sequence(ctx context.Context) uint64 {
	if seq, ok := acp.SequenceFromContext(ctx); ok {
		return seq
	}
	return math.MaxUint64
}

// prompt runs a turn for the seq-th of the client's messages: a
// session/cancel that came after it, while it was still setting up or
// waiting for the turn before it, ends it cancelled before it starts, as
// does the session's close or a load of it again.
func (a *agent) prompt(ctx context.Context, seq uint64, req *acp.PromptRequest) (*acp.PromptResponse, error) {
	s, err := a.session(req.SessionID)
	if err != nil {
		return nil, err
	}
	input := promptInput(req.Prompt)
	if len(input) == 0 {
		return nil, invalidParams("an empty prompt")
	}
	if ok, err := s.waitTurn(ctx, seq); !ok {
		if err != nil {
			return nil, err
		}
		return &acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	defer func() { <-s.prompting }()

	s.mu.Lock()
	if s.closed || seq < s.cancelSeq {
		s.mu.Unlock()
		return &acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	st := s.settings
	if hasImage(input) {
		if m, ok := st.current(); ok && !takesImages(m) {
			s.mu.Unlock()
			return nil, &acp.Error{Code: acp.ErrorCodeInvalidRequest, Message: "The current model does not support image input"}
		}
	}
	tctx, cancel := context.WithCancel(a.ctx)
	t := &turn{ctx: tctx, cancel: cancel, gone: make(chan struct{}), known: make(chan struct{}), done: make(chan struct{}), late: make(chan struct{}),
		stopped: make(chan struct{}), ended: make(chan appserver.Turn, 1), early: map[string]appserver.Turn{}, usage: tokens{start: s.total}}
	s.turn = t
	s.mu.Unlock()
	defer func() {
		cancel()
		t.mu.Lock()
		if t.lateStop != nil {
			t.lateStop.Stop()
		}
		t.mu.Unlock()
		close(t.done)
		s.mu.Lock()
		if s.turn == t {
			s.turn = nil
		}
		s.mu.Unlock()
	}()

	m, _ := modeByID(st.mode)
	params := appserver.TurnStartParams{ThreadID: s.id, Input: input, ApprovalPolicy: m.approval, ApprovalsReviewer: m.reviewer,
		SandboxPolicy: m.sandbox, Model: st.model, Effort: st.effort, Summary: "auto"}
	var answer json.RawMessage
	if err := a.call(ctx, "turn/start", params, &answer); err != nil {
		return nil, err
	}
	var started appserver.TurnStartResponse
	if err := json.Unmarshal(answer, &started); err != nil {
		return nil, s.unreadStart(ctx, t, answer, err)
	}
	s.started(t, started.Turn.ID)

	var end appserver.Turn
	closed := false
	select {
	case end = <-t.ended:
	case <-ctx.Done():
		s.interrupt(t)
		if a.client.Err() == nil { // withdrawn: the next prompt's turn/start comes after the interrupt
			t.interruptSent(context.WithoutCancel(ctx)) // bounded by the grace
		}
		return nil, ctx.Err()
	case <-t.gone: // closed mid-turn (interrupted first): cancelled, as codex-acp ends it
		closed = true
	case <-t.late:
		a.log.Warn("an interrupted turn didn't end: the prompt ends cancelled", "session", s.id, "after", a.grace)
		closed = true
	}
	t.mu.Lock()
	cancelled, failure, usage := t.cancelled, t.failure, t.usage.used()
	t.mu.Unlock()
	switch {
	case closed || end.Status == "interrupted" || cancelled:
		return &acp.PromptResponse{StopReason: acp.StopReasonCancelled, Usage: usage}, nil
	case end.Status == "failed" && end.Error.ErrorInfo() == "unauthorized":
		return nil, &acp.Error{Code: acp.ErrorCodeAuthenticationRequired, Message: "Authentication required"}
	case failure != nil:
		return nil, failure
	}
	s.commands(ctx, true) // a turn may have added a skill
	s.promptTitle(req.Prompt)
	return &acp.PromptResponse{StopReason: acp.StopReasonEndTurn, Usage: usage}, nil
}

// waitTurn takes the session's one turn for the seq-th of the client's
// messages, once the turn before it has ended: false, with no error, when
// a session/cancel that came after it, or the session's close, ends it
// first.
func (s *session) waitTurn(ctx context.Context, seq uint64) (bool, error) {
	waited := false
	for {
		s.mu.Lock()
		cancelled, wake := s.closed || seq < s.cancelSeq, s.wake
		s.mu.Unlock()
		if cancelled {
			return false, nil
		}
		select {
		case s.prompting <- struct{}{}:
			return true, nil
		default:
		}
		if !waited {
			waited = true
			s.a.log.Debug("a prompt waits for the turn before it", "session", s.id)
		}
		select {
		case s.prompting <- struct{}{}:
			return true, nil
		case <-wake:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
}

// unreadStart ends a prompt whose turn/start answer doesn't decode: the
// turn Codex may be running is interrupted (by the id the answer or
// turn/started gives) and the prompt fails once it has ended, or
// interruptGrace after.
func (s *session) unreadStart(ctx context.Context, t *turn, answer json.RawMessage, err error) error {
	s.a.log.Warn("turn/start's answer doesn't decode: the turn is interrupted", "session", s.id, "err", err)
	var id struct {
		Turn struct {
			ID string `json:"id"`
		} `json:"turn"`
	}
	_ = json.Unmarshal(answer, &id)
	s.interrupt(t)
	s.started(t, id.Turn.ID)
	select {
	case <-t.ended:
	case <-t.late:
	case <-t.gone:
	case <-ctx.Done():
	}
	return &acp.Error{Code: acp.ErrorCodeInternalError, Message: "Internal error", Data: raw(map[string]any{"details": "Codex's answer to turn/start couldn't be read"})}
}

// promptTitle titles a session that has no title yet after its prompt's
// text, once the turn has ended, as codex-acp does.
func (s *session) promptTitle(prompt []acp.ContentBlock) {
	var texts []string
	for _, b := range prompt {
		if b.Text != nil {
			texts = append(texts, b.Text.Text)
		}
	}
	name := title(strings.Join(texts, " "))
	s.mu.Lock()
	send := !s.titled && name != ""
	s.titled = s.titled || send
	s.mu.Unlock()
	if send {
		s.update(titleUpdate(name))
	}
}

// started records the turn's id from turn/start's answer or turn/started,
// and ends it if Codex said it ended first; a turn cancelled before its
// id was known is interrupted now.
func (s *session) started(t *turn, id string) {
	t.mu.Lock()
	if t.id != "" || id == "" {
		t.mu.Unlock()
		return
	}
	t.id = id
	close(t.known)
	cancelled := t.cancelled
	end, early := t.early[id]
	t.mu.Unlock()
	if early {
		t.ended <- end
		return
	}
	if cancelled {
		s.interrupt(t)
	}
}

// completed takes turn/completed: the prompt's turn ends; one whose id
// isn't known yet waits for it.
func (s *session) completed(end appserver.Turn) {
	s.mu.Lock()
	t := s.turn
	s.mu.Unlock()
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	switch t.id {
	case end.ID:
		select {
		case t.ended <- end:
		default:
		}
	case "":
		t.early[end.ID] = end
	}
}

// Cancel stops the session's prompts: the one running, and those from
// before the cancel still setting up or waiting for it.
func (a *agent) Cancel(ctx context.Context, n *acp.CancelNotification) error {
	s, err := a.session(n.SessionID)
	if err != nil {
		return nil //nolint:nilerr // a cancel of a session gone is done
	}
	seq, _ := acp.SequenceFromContext(ctx)
	s.mu.Lock()
	s.cancelSeq = max(s.cancelSeq, seq)
	s.wakeLocked()
	t := s.turn
	s.mu.Unlock()
	if t != nil {
		s.interrupt(t)
	}
	return nil
}

// interrupt cancels t: its questions to the user are withdrawn (Codex is
// told cancel), and Codex interrupts the turn once its id is known; the
// prompt then ends cancelled at turn/completed, or interruptGrace after
// the cancel if that never comes. It doesn't wait for Codex: turn/interrupt
// goes on a goroutine of its own, and t.stopped is closed once Codex has
// answered it or interruptGrace has passed.
//
//nolint:contextcheck // turn/interrupt is bound to the agent's life and the grace, not to the caller's request
func (s *session) interrupt(t *turn) {
	t.mu.Lock()
	t.cancelled = true
	id := t.id
	t.mu.Unlock()
	t.cancel()
	if id != "" {
		s.mu.Lock()
		if s.stale == nil {
			s.stale = map[string]bool{}
		}
		s.stale[id] = true
		s.mu.Unlock()
	}
	t.lateOnce.Do(func() {
		timer := time.AfterFunc(s.a.grace, func() { close(t.late) })
		t.mu.Lock()
		t.lateStop = timer
		t.mu.Unlock()
	})
	if id == "" {
		return
	}
	t.stop.Do(func() {
		go func() {
			defer close(t.stopped)
			ctx, cancel := context.WithTimeout(s.a.ctx, s.a.grace)
			defer cancel()
			if err := s.a.call(ctx, "turn/interrupt", appserver.TurnInterruptParams{ThreadID: s.id, TurnID: id}, nil); err != nil {
				s.a.log.Warn("turn not interrupted", "session", s.id, "err", err)
			}
		}()
	})
}

// interruptSent waits until Codex has answered t's turn/interrupt, or
// given up on it; a turn whose id isn't known has sent none.
func (t *turn) interruptSent(ctx context.Context) {
	select {
	case <-t.known:
	default:
		return
	}
	select {
	case <-t.stopped:
	case <-ctx.Done():
	}
}

// closeLocked marks the session closed: the prompts waiting end
// cancelled, and none starts a turn; s.mu is held.
func (s *session) closeLocked() {
	if !s.closed {
		s.closed = true
		s.wakeLocked()
	}
}

// wakeLocked has the prompts waiting for the session's turn look again;
// s.mu is held.
func (s *session) wakeLocked() {
	close(s.wake)
	s.wake = make(chan struct{})
}

// interrupted says the turn id was interrupted.
func (s *session) interrupted(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return id != "" && s.stale[id]
}

// end ends the session's prompt, if one runs, and the prompts waiting:
// the session closed or was opened again, or the agent stopped.
func (s *session) end(error) {
	s.mu.Lock()
	s.closeLocked()
	t := s.turn
	s.mu.Unlock()
	if t != nil {
		t.cancel()
		t.once.Do(func() { close(t.gone) })
	}
}

// promptInput is an ACP prompt as Codex input, as codex-acp makes it:
// text as text; an image as its URL (http, https or data:), else a data:
// URL of its bytes; a resource link as `[@name](uri)`; an embedded text
// resource as that link and the text in a <context> tag; an embedded
// image as a data: URL; other embedded bytes in a <context> tag, base64.
// Audio is dropped.
func promptInput(blocks []acp.ContentBlock) []appserver.UserInput {
	var out []appserver.UserInput
	text := func(s string) { out = append(out, appserver.UserInput{Type: "text", Text: s}) }
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			text(b.Text.Text)
		case b.Image != nil:
			u := b.Image.URI
			if !imageURL(u) {
				u = "data:" + b.Image.MimeType + ";base64," + b.Image.Data
			}
			out = append(out, appserver.UserInput{Type: "image", URL: u})
		case b.ResourceLink != nil:
			text(link(b.ResourceLink.Name, b.ResourceLink.URI))
		case b.Resource != nil && b.Resource.Resource.TextResourceContents != nil:
			r := b.Resource.Resource.TextResourceContents
			text(link("", r.URI) + "\n<context ref=\"" + r.URI + "\">\n" + r.Text + "\n</context>")
		case b.Resource != nil && b.Resource.Resource.BlobResourceContents != nil:
			r := b.Resource.Resource.BlobResourceContents
			if strings.HasPrefix(r.MimeType, "image/") {
				out = append(out, appserver.UserInput{Type: "image", URL: "data:" + r.MimeType + ";base64," + r.Blob})
				continue
			}
			mime := r.MimeType
			if mime == "" {
				mime = "application/octet-stream"
			}
			text(link("", r.URI) + "\n<context ref=\"" + r.URI + "\" mimeType=\"" + mime + "\" encoding=\"base64\">\n" + r.Blob + "\n</context>")
		}
	}
	return out
}

// link is a mention of uri: `[@name](uri)`, a file's own name when name
// is "", the bare uri for anything else.
func link(name, uri string) string {
	if name != "" {
		return "[@" + name + "](" + uri + ")"
	}
	if p, ok := strings.CutPrefix(uri, "file://"); ok {
		return "[@" + path.Base(p) + "](" + uri + ")"
	}
	return uri
}

func imageURL(u string) bool {
	parsed, err := url.Parse(u)
	return u != "" && err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https" || parsed.Scheme == "data")
}

func hasImage(in []appserver.UserInput) bool {
	for _, i := range in {
		if i.Type == "image" {
			return true
		}
	}
	return false
}

func takesImages(m appserver.Model) bool {
	return len(m.InputModalities) == 0 || slices.Contains(m.InputModalities, "image") // none: a server that doesn't say
}

// tokens is a prompt's usage, from the thread's running totals, as
// codex-acp counts it: the total at its end less the total at its start,
// or, when the total went back (a compaction), the sum of each request's.
// A thread whose total isn't known (one resumed) takes its first total
// before the model's first output as the start.
type tokens struct {
	start, latest, summed *appserver.TokenCount
	wentBack, output      bool
}

// observe takes a thread/tokenUsage/updated: the thread's total and its
// last request's.
func (u *tokens) observe(total, last appserver.TokenCount) {
	prev := u.latest
	if prev == nil {
		prev = u.start
	}
	if prev != nil && *prev == total {
		return
	}
	if prev == nil && !u.output {
		u.start = &total
		return
	}
	if prev != nil && (total.TotalTokens < prev.TotalTokens || total.InputTokens < prev.InputTokens || total.OutputTokens < prev.OutputTokens) {
		u.wentBack = true
	}
	if u.summed == nil {
		u.summed = &appserver.TokenCount{}
	}
	*u.summed = add(*u.summed, last, 1)
	u.latest = &total
}

func (u *tokens) used() *acp.Usage {
	if u.latest == nil {
		return nil
	}
	c := *u.summed
	if u.start != nil && !u.wentBack {
		c = add(*u.latest, *u.start, -1)
	}
	return acpUsage(c)
}

func add(a, b appserver.TokenCount, sign int64) appserver.TokenCount {
	return appserver.TokenCount{TotalTokens: a.TotalTokens + sign*b.TotalTokens, InputTokens: a.InputTokens + sign*b.InputTokens,
		CachedInputTokens: a.CachedInputTokens + sign*b.CachedInputTokens, CacheWriteInputTokens: a.CacheWriteInputTokens + sign*b.CacheWriteInputTokens,
		OutputTokens: a.OutputTokens + sign*b.OutputTokens, ReasoningOutputTokens: a.ReasoningOutputTokens + sign*b.ReasoningOutputTokens}
}

// acpUsage is c as ACP's usage: input without what came from the cache.
func acpUsage(c appserver.TokenCount) *acp.Usage {
	u := func(n int64) uint64 { return uint64(max(n, 0)) }
	cached, written, thought := u(c.CachedInputTokens), u(c.CacheWriteInputTokens), u(c.ReasoningOutputTokens)
	return &acp.Usage{TotalTokens: u(c.TotalTokens), InputTokens: u(c.InputTokens - c.CachedInputTokens - c.CacheWriteInputTokens),
		OutputTokens: u(c.OutputTokens), CachedReadTokens: &cached, CachedWriteTokens: &written, ThoughtTokens: &thought}
}

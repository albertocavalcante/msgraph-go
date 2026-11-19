package msgraph

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
)

// recorder is a minimal in-memory TracerProvider, so the tests need only the
// OpenTelemetry API and never the SDK.
type recorder struct {
	embedded.TracerProvider

	mu     sync.Mutex
	spans  []*recordedSpan
	nextID uint64
}

type recordedSpan struct {
	embedded.Span

	r        *recorder
	scope    string
	name     string
	kind     trace.SpanKind
	sc       trace.SpanContext
	parent   trace.SpanID
	attrs    []attribute.KeyValue
	events   []recordedEvent
	status   codes.Code
	statusOf string
	ended    bool
}

type recordedEvent struct {
	name  string
	attrs []attribute.KeyValue
}

type recordingTracer struct {
	embedded.Tracer

	r     *recorder
	scope string
}

func (r *recorder) Tracer(name string, _ ...trace.TracerOption) trace.Tracer {
	return recordingTracer{r: r, scope: name}
}

func (t recordingTracer) Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	cfg := trace.NewSpanStartConfig(opts...)
	parent := trace.SpanContextFromContext(ctx)
	t.r.mu.Lock()
	defer t.r.mu.Unlock()
	t.r.nextID++
	var spanID trace.SpanID
	binary.BigEndian.PutUint64(spanID[:], t.r.nextID)
	traceID := parent.TraceID()
	if !traceID.IsValid() {
		traceID = trace.TraceID{1}
	}
	span := &recordedSpan{
		r:      t.r,
		scope:  t.scope,
		name:   name,
		kind:   cfg.SpanKind(),
		parent: parent.SpanID(),
		attrs:  cfg.Attributes(),
		sc: trace.NewSpanContext(trace.SpanContextConfig{
			TraceID:    traceID,
			SpanID:     spanID,
			TraceFlags: trace.FlagsSampled,
		}),
	}
	t.r.spans = append(t.r.spans, span)
	return trace.ContextWithSpan(ctx, span), span
}

func (s *recordedSpan) End(...trace.SpanEndOption) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	s.ended = true
}

func (s *recordedSpan) AddEvent(name string, opts ...trace.EventOption) {
	cfg := trace.NewEventConfig(opts...)
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	s.events = append(s.events, recordedEvent{name: name, attrs: cfg.Attributes()})
}

func (s *recordedSpan) AddLink(trace.Link)                   {}
func (s *recordedSpan) IsRecording() bool                    { return true }
func (s *recordedSpan) SpanContext() trace.SpanContext       { return s.sc }
func (s *recordedSpan) TracerProvider() trace.TracerProvider { return s.r }
func (s *recordedSpan) RecordError(error, ...trace.EventOption) {
	panic("RecordError would record the error message")
}

func (s *recordedSpan) SetStatus(code codes.Code, description string) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	s.status, s.statusOf = code, description
}

func (s *recordedSpan) SetName(name string) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	s.name = name
}

func (s *recordedSpan) SetAttributes(kv ...attribute.KeyValue) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	s.attrs = append(s.attrs, kv...)
}

// root starts a test span with the recorder, which is how a caller turns
// tracing on: the client takes its tracer from the context.
func (r *recorder) root(t *testing.T) (context.Context, *recordedSpan) {
	t.Helper()
	ctx, span := r.Tracer("test").Start(context.Background(), "test")
	return ctx, span.(*recordedSpan)
}

// children returns the spans whose parent is p, in start order.
func (r *recorder) children(p *recordedSpan) []*recordedSpan {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*recordedSpan
	for _, s := range r.spans {
		if s.parent == p.sc.SpanID() {
			out = append(out, s)
		}
	}
	return out
}

// tree renders the spans below p as indented names, for whole-tree asserts.
func (r *recorder) tree(p *recordedSpan) string {
	var b strings.Builder
	var walk func(*recordedSpan, int)
	walk = func(s *recordedSpan, depth int) {
		for _, child := range r.children(s) {
			fmt.Fprintf(&b, "%s%s\n", strings.Repeat("  ", depth), child.name)
			walk(child, depth+1)
		}
	}
	walk(p, 0)
	return b.String()
}

// dump renders everything recorded, for redaction checks.
func (r *recorder) dump() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, s := range r.spans {
		fmt.Fprintf(&b, "span %q status=%v %q\n", s.name, s.status, s.statusOf)
		for _, kv := range s.attrs {
			fmt.Fprintf(&b, "  %s=%s\n", kv.Key, kv.Value.String())
		}
		for _, e := range s.events {
			fmt.Fprintf(&b, "  event %q\n", e.name)
			for _, kv := range e.attrs {
				fmt.Fprintf(&b, "    %s=%s\n", kv.Key, kv.Value.String())
			}
		}
	}
	return b.String()
}

func (s *recordedSpan) attr(key attribute.Key) (attribute.Value, bool) {
	s.r.mu.Lock()
	defer s.r.mu.Unlock()
	return lookup(s.attrs, key)
}

func lookup(attrs []attribute.KeyValue, key attribute.Key) (attribute.Value, bool) {
	var found attribute.Value
	ok := false
	for _, kv := range attrs {
		if kv.Key == key {
			found, ok = kv.Value, true
		}
	}
	return found, ok
}

func (s *recordedSpan) wantAttr(t *testing.T, key attribute.Key, want any) {
	t.Helper()
	got, ok := s.attr(key)
	if !ok {
		t.Errorf("span %q: %s missing, want %v", s.name, key, want)
		return
	}
	if got.AsInterface() != want {
		t.Errorf("span %q: %s = %v (%T), want %v (%T)", s.name, key, got.AsInterface(), got.AsInterface(), want, want)
	}
}

func (s *recordedSpan) wantNoAttr(t *testing.T, key attribute.Key) {
	t.Helper()
	if got, ok := s.attr(key); ok {
		t.Errorf("span %q: %s = %v, want it absent", s.name, key, got.AsInterface())
	}
}

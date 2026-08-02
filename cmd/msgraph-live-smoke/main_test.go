package main

import (
	"reflect"
	"testing"
	"unicode/utf8"
)

func TestMessageSelectFieldsRedactsDetailsByDefault(t *testing.T) {
	got := messageSelectFields(false)
	want := []string{"id", "receivedDateTime"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestMessageSelectFieldsIncludesDetailsWhenRequested(t *testing.T) {
	got := messageSelectFields(true)
	want := []string{"id", "subject", "from", "receivedDateTime"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestTruncatePreservesUTF8(t *testing.T) {
	got := truncate("olá mundo", 5)
	if !utf8.ValidString(got) {
		t.Fatalf("truncate returned invalid UTF-8: %q", got)
	}
	if got != "ol..." {
		t.Fatalf("got %q, want %q", got, "ol...")
	}
}

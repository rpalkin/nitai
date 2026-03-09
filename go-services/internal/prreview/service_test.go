package prreview

import (
	"testing"
	"time"
)

func TestDebounceTimeoutFromConfig(t *testing.T) {
	// Create PRReview with a custom debounce timeout
	svc := New(nil, WithDebounceTimeout(5*time.Second))
	if svc.debounceTimeout != 5*time.Second {
		t.Errorf("expected 5s, got %v", svc.debounceTimeout)
	}
}

func TestDebounceTimeoutDefault(t *testing.T) {
	svc := New(nil)
	if svc.debounceTimeout != 3*time.Minute {
		t.Errorf("expected 3m default, got %v", svc.debounceTimeout)
	}
}

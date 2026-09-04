package main

import (
	"context"
	"strings"
	"testing"
)

func TestRunRejectsConflictingModesBeforeStartup(t *testing.T) {
	err := run(context.Background(), []string{"--web-only", "--bridge-only"})
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("run() error = %v, want conflicting mode error", err)
	}
}

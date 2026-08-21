package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRunReturnsConfigErrForMissingConfigFile covers the review finding for §8.7: a missing or
// misconfigured ZORGSCOPE_CONFIG path — the most likely operator mistake in a container with a bad
// mount — must be reported the same way as a bad YAML file (main's exit(2) branch), not fall
// through to the generic exit(1) path. config.Load fails and returns before run() does any other
// work (no store, no server, no goroutines), so calling run() directly here is side-effect free.
func TestRunReturnsConfigErrForMissingConfigFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist.yaml")
	t.Setenv("ZORGSCOPE_CONFIG", missing)

	err := run()
	if err == nil {
		t.Fatal("expected an error for a missing config file")
	}
	var ce *configErr
	if !errors.As(err, &ce) {
		t.Fatalf("expected *configErr, got %T: %v", err, err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected errors.Is(err, os.ErrNotExist) to hold through configErr's Unwrap, got: %v", err)
	}
}

// TestRunReturnsConfigErrForInvalidConfig covers the other configErr source: a config.ValidationError
// (here, an unknown YAML key) must still reach main's exit(2) branch with its precise "key: message"
// text intact, unchanged by wrapping it in configErr.
func TestRunReturnsConfigErrForInvalidConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("bogus_top_level_key: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZORGSCOPE_CONFIG", path)

	err := run()
	if err == nil {
		t.Fatal("expected an error for an invalid config file")
	}
	var ce *configErr
	if !errors.As(err, &ce) {
		t.Fatalf("expected *configErr, got %T: %v", err, err)
	}
}

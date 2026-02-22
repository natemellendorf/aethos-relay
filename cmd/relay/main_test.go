package main

import (
	"reflect"
	"testing"
)

func TestNormalizeBoolFlagArgs(t *testing.T) {
	boolFlags := map[string]struct{}{
		"dev-mode":            {},
		"auto-peer-discovery": {},
	}

	args := []string{"relay", "-ws-addr", "127.0.0.1:8082", "-dev-mode", "true", "-auto-peer-discovery", "false", "-relay-id", "nine"}
	got := normalizeBoolFlagArgs(args, boolFlags)
	want := []string{"relay", "-ws-addr", "127.0.0.1:8082", "-dev-mode=true", "-auto-peer-discovery=false", "-relay-id", "nine"}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeBoolFlagArgs() = %v, want %v", got, want)
	}
}

func TestNormalizeBoolFlagArgsPreservesExistingForms(t *testing.T) {
	boolFlags := map[string]struct{}{
		"dev-mode": {},
	}

	args := []string{"relay", "-dev-mode=false", "-peer", "ws://127.0.0.1:8082/federation/ws", "--", "-dev-mode", "true"}
	got := normalizeBoolFlagArgs(args, boolFlags)

	if !reflect.DeepEqual(got, args) {
		t.Fatalf("normalizeBoolFlagArgs() modified args unexpectedly: got %v want %v", got, args)
	}
}

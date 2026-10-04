package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVerifyReceiptRootBindingUsesNativeObserver(t *testing.T) {
	t.Parallel()
	command := newVerifyReceiptCmd()
	command.SilenceErrors = true
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&bytes.Buffer{})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "--local-check is required") {
		t.Fatalf("native receipt binding error=%v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("missing evidence emitted receipt %q", output.String())
	}
	child, _, err := command.Find([]string{"remote-target"})
	if err != nil || child == command {
		t.Fatalf("remote target binding child=%v err=%v", child, err)
	}
}

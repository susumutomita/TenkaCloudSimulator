package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestTokenDisplayRejectsRedirectsAndCI(t *testing.T) {
	t.Setenv("CI", "restore-at-cleanup")
	if err := os.Unsetenv("CI"); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(t.TempDir(), "redirect")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	for _, output := range []io.Writer{io.Discard, &bytes.Buffer{}, file, null} {
		if interactiveTokenOutput(output) {
			t.Fatal("nonterminal output accepted for secret display")
		}
	}
	for _, ci := range []string{"", "true", "false"} {
		t.Setenv("CI", ci)
		if interactiveTokenOutput(file) {
			t.Fatal("CI output accepted for secret display")
		}
	}
}

func TestTokenDisplayCannotDisableAuthentication(t *testing.T) {
	path := t.TempDir() + "/operator"
	var output bytes.Buffer
	err := run(context.Background(), []string{"--listen=127.0.0.1:0", "--token-file=" + path, "--show-token", "--unsafe-no-auth"}, &output)
	if err == nil || !strings.Contains(err.Error(), "requires authentication") {
		t.Fatal("meaningless unauthenticated display request accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("rejected flags still created a token")
	}
}

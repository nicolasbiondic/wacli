package main

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
)

const envWebhookSecret = "WACLI_WEBHOOK_SECRET"

// resolveWebhookSecret returns the HMAC secret for sync webhooks. An argument
// shows up in the process list, so the secret can also come from a file
// (--webhook-secret-file) or from WACLI_WEBHOOK_SECRET. The flag keeps its
// meaning, the file wins over the environment, and the environment is only
// read when a webhook is configured, so an exported variable never turns a
// plain `wacli sync` into an error. For a file it also returns the file's
// permission bits (0 on Windows, where they mean nothing) so sync can warn about
// a secret that group or others can read.
func resolveWebhookSecret(secret, secretFile, webhookURL string) (string, os.FileMode, error) {
	fromEnv := os.Getenv(envWebhookSecret)
	// Delegated sends run ffmpeg and ffprobe from this process: keep the secret
	// out of their environment.
	_ = os.Unsetenv(envWebhookSecret)

	if secret != "" && secretFile != "" {
		return "", 0, fmt.Errorf("--webhook-secret and --webhook-secret-file are mutually exclusive")
	}
	if secretFile != "" {
		if webhookURL == "" {
			return "", 0, fmt.Errorf("--webhook-secret-file requires --webhook")
		}
		return readWebhookSecretFile(secretFile)
	}
	if secret != "" {
		if webhookURL == "" {
			return "", 0, fmt.Errorf("--webhook-secret requires --webhook")
		}
		return secret, 0, nil
	}
	if webhookURL == "" {
		return "", 0, nil
	}
	return fromEnv, 0, nil
}

func readWebhookSecretFile(path string) (string, os.FileMode, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("read --webhook-secret-file: %w", err)
	}
	defer f.Close()
	// Stat the open file, not the path, so the mode is the one of what is read.
	info, err := f.Stat()
	if err != nil {
		return "", 0, fmt.Errorf("read --webhook-secret-file: %w", err)
	}
	raw, err := io.ReadAll(f)
	if err != nil {
		return "", 0, fmt.Errorf("read --webhook-secret-file: %w", err)
	}
	secret := strings.TrimRight(string(raw), "\r\n")
	if secret == "" {
		return "", 0, fmt.Errorf("--webhook-secret-file %s is empty", path)
	}
	if runtime.GOOS == "windows" {
		return secret, 0, nil
	}
	return secret, info.Mode().Perm(), nil
}

package main

import (
	"fmt"
	"os"
	"strings"
)

const envWebhookSecret = "WACLI_WEBHOOK_SECRET"

// resolveWebhookSecret returns the HMAC secret for sync webhooks. An argument
// shows up in the process list, so the secret can also come from a file
// (--webhook-secret-file) or from WACLI_WEBHOOK_SECRET. The flag keeps its
// meaning, the file wins over the environment, and the environment is only
// read when a webhook is configured, so an exported variable never turns a
// plain `wacli sync` into an error.
func resolveWebhookSecret(secret, secretFile, webhookURL string) (string, error) {
	fromEnv := os.Getenv(envWebhookSecret)
	// Delegated sends run ffmpeg and ffprobe from this process: keep the secret
	// out of their environment.
	_ = os.Unsetenv(envWebhookSecret)

	if secret != "" && secretFile != "" {
		return "", fmt.Errorf("--webhook-secret and --webhook-secret-file are mutually exclusive")
	}
	if secretFile != "" {
		if webhookURL == "" {
			return "", fmt.Errorf("--webhook-secret-file requires --webhook")
		}
		return readWebhookSecretFile(secretFile)
	}
	if secret != "" {
		if webhookURL == "" {
			return "", fmt.Errorf("--webhook-secret requires --webhook")
		}
		return secret, nil
	}
	if webhookURL == "" {
		return "", nil
	}
	return fromEnv, nil
}

func readWebhookSecretFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read --webhook-secret-file: %w", err)
	}
	secret := strings.TrimRight(string(raw), "\r\n")
	if secret == "" {
		return "", fmt.Errorf("--webhook-secret-file %s is empty", path)
	}
	return secret, nil
}

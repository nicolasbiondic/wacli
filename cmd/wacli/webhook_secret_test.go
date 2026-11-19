package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeSecretFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "webhook-secret")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveWebhookSecret(t *testing.T) {
	const url = "https://example.test/hook"
	tests := []struct {
		name    string
		env     string
		secret  string
		file    string // file content; "-" means no file
		url     string
		want    string
		wantErr string
	}{
		{name: "flag keeps working", secret: "from-flag", file: "-", url: url, want: "from-flag"},
		{name: "environment", env: "from-env", file: "-", url: url, want: "from-env"},
		{name: "file", file: "from-file", url: url, want: "from-file"},
		{name: "file drops the trailing newline", file: "from-file\n", url: url, want: "from-file"},
		{name: "file drops a trailing CRLF", file: "from-file\r\n", url: url, want: "from-file"},
		{name: "file wins over the environment", env: "from-env", file: "from-file", url: url, want: "from-file"},
		{name: "flag wins over the environment", env: "from-env", secret: "from-flag", file: "-", url: url, want: "from-flag"},
		{name: "no secret at all", file: "-", url: url, want: ""},
		{name: "environment without a webhook is ignored", env: "from-env", file: "-", want: ""},
		{name: "flag and file together", secret: "from-flag", file: "from-file", url: url, wantErr: "mutually exclusive"},
		{name: "flag without a webhook", secret: "from-flag", file: "-", wantErr: "--webhook-secret requires --webhook"},
		{name: "file without a webhook", file: "from-file", wantErr: "--webhook-secret-file requires --webhook"},
		{name: "empty file", file: "\n", url: url, wantErr: "is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(envWebhookSecret, tt.env)
			path := ""
			if tt.file != "-" {
				path = writeSecretFile(t, tt.file)
			}

			got, _, err := resolveWebhookSecret(tt.secret, path, tt.url)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("secret = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveWebhookSecretMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent")

	_, _, err := resolveWebhookSecret("", path, "https://example.test/hook")
	if err == nil || !strings.Contains(err.Error(), "--webhook-secret-file") || !strings.Contains(err.Error(), path) {
		t.Fatalf("expected an error naming the file, got %v", err)
	}
}

func TestResolveWebhookSecretKeepsSecretsOutOfErrors(t *testing.T) {
	t.Setenv(envWebhookSecret, "env-secret-value")

	_, _, err := resolveWebhookSecret("flag-secret-value", writeSecretFile(t, "file-secret-value"), "https://example.test/hook")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, secret := range []string{"env-secret-value", "flag-secret-value", "file-secret-value"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaks a secret: %v", err)
		}
	}
}

func TestResolveWebhookSecretRemovesEnvironmentVariable(t *testing.T) {
	t.Setenv(envWebhookSecret, "from-env")

	if _, _, err := resolveWebhookSecret("", "", "https://example.test/hook"); err != nil {
		t.Fatal(err)
	}
	if _, ok := os.LookupEnv(envWebhookSecret); ok {
		t.Fatalf("%s must not stay in the environment that ffmpeg and ffprobe inherit", envWebhookSecret)
	}
}

func TestSyncCommandExposesWebhookSecretFileFlag(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	if cmd.Flags().Lookup("webhook-secret-file") == nil {
		t.Fatal("missing --webhook-secret-file flag")
	}
}

func TestSyncCommandRequiresWebhookForSecretFile(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	cmd.SetArgs([]string{"--webhook-secret-file", writeSecretFile(t, "secret")})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "--webhook-secret-file requires --webhook") {
		t.Fatalf("expected webhook-secret-file validation error, got %v", err)
	}
}

func TestSyncCommandRejectsSecretAndSecretFile(t *testing.T) {
	cmd := newSyncCmd(&rootFlags{})
	cmd.SetArgs([]string{
		"--webhook", "https://example.test/hook",
		"--webhook-secret", "secret",
		"--webhook-secret-file", writeSecretFile(t, "secret"),
	})

	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("expected mutual exclusion error, got %v", err)
	}
}

func TestSyncCommandIgnoresWebhookSecretEnvironmentWithoutWebhook(t *testing.T) {
	t.Setenv(envWebhookSecret, "from-env")
	cmd := newSyncCmd(&rootFlags{})
	cmd.SetArgs([]string{"--webhook-events", "receipt"})

	// Fails on the next validation, not on the secret: the variable alone must
	// not make `wacli sync` stricter.
	err := cmd.Execute()
	if err == nil || strings.Contains(err.Error(), "WACLI_WEBHOOK_SECRET") || strings.Contains(err.Error(), "--webhook-secret") {
		t.Fatalf("expected the webhook-events validation error, got %v", err)
	}
}

func TestResolveWebhookSecretReportsTheFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits mean nothing on Windows")
	}
	const url = "https://example.test/hook"
	for _, mode := range []os.FileMode{0o600, 0o400, 0o640, 0o644, 0o660, 0o604} {
		path := writeSecretFile(t, "from-file")
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}

		secret, got, err := resolveWebhookSecret("", path, url)
		if err != nil {
			t.Fatalf("mode %04o: %v", mode, err)
		}
		if secret != "from-file" || got != mode {
			t.Fatalf("mode %04o: got secret %q, mode %04o", mode, secret, got)
		}
	}
}

func TestResolveWebhookSecretReportsNoModeForFlagOrEnvironment(t *testing.T) {
	t.Setenv(envWebhookSecret, "from-env")
	for _, secret := range []string{"", "from-flag"} {
		_, mode, err := resolveWebhookSecret(secret, "", "https://example.test/hook")
		if err != nil || mode != 0 {
			t.Fatalf("secret %q: mode %04o, err %v", secret, mode, err)
		}
	}
}

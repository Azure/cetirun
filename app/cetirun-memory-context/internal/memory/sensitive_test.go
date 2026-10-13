// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package memory

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// syntheticToken builds a token-shaped value at run time, so that this source
// holds no string in a credential format.
func syntheticToken(prefix string, length int) string {
	return prefix + strings.Repeat("Synthetic0", length/10+1)[:length]
}

func TestSecretDetection(t *testing.T) {
	segment := func(part string) string { return base64.RawURLEncoding.EncodeToString([]byte(part)) }
	refused := map[string]string{
		"private key":         "-----BEGIN " + "PRIVATE KEY-----",
		"bearer token":        "Authorization: Bearer " + syntheticToken("", 24),
		"OpenAI key":          syntheticToken("sk-"+"proj-", 32),
		"GitHub token":        syntheticToken("gh"+"p_", 36),
		"GitHub OAuth token":  syntheticToken("gh"+"o_", 36),
		"GitHub fine-grained": syntheticToken("github"+"_pat_", 40),
		"GitLab token":        syntheticToken("gl"+"pat-", 20),
		"Slack token":         "xo" + "xb-1234567890-" + syntheticToken("", 24),
		"AWS key ID":          "AK" + "IA" + "SYNTHETIC0123456",
		"Google API key":      syntheticToken("AI"+"za", 35),
		"npm token":           syntheticToken("np"+"m_", 36),
		"JSON Web Token":      segment(`{"alg":"none"}`) + "." + segment(`{"sub":"synthetic"}`) + "." + segment("synthetic signature"),
		"URL credentials":     "https://x-access-token:" + syntheticToken("", 20) + "@git.example.com/repo.git",
		"signed URL":          "https://storage.example.com/container/blob?sv=2025-01-01&s" + "ig=" + syntheticToken("", 20),
		"variable":            "OPENAI_API" + "_KEY=" + syntheticToken("", 32),
		"exported token":      "export GITHUB" + "_TOKEN=" + syntheticToken("", 32),
		"JSON token":          `{"gateway":{"auth":{"token":"` + syntheticToken("", 32) + `"}}}`,
		"YAML token":          "github.com:\n    oauth_token: " + syntheticToken("", 32),
		"header":              "curl -H 'api-key: " + strings.Repeat("0123456789abcdef", 2) + "' https://api.example.com",
		"connection string":   "DefaultEndpointsProtocol=https;AccountName=example;Account" + "Key=" + syntheticToken("", 40) + "==",
		"password":            "password = " + syntheticToken("", 20),
	}
	for name, text := range refused {
		if validText(text, 32<<10) {
			t.Errorf("%s: accepted %q", name, text)
		}
	}
	for _, text := range []string{
		"The owner prefers green tea in the morning.",
		"max_tokens: 4096; token budget: 128000 tokens per day",
		"tokenizer: o200k_harmony_extended",
		"password: at least 12 characters, rotated every 90 days",
		"OPENAI_API_KEY=${OPENAI_API_KEY}",
		"credentials_file: /home/vibe/.config/example/credentials.json",
		"token: https://login.example.com/oauth2/v2.0/token",
		"secret: 2026-10-13T09:20:57Z",
		"ssh://git@git.example.com:22/example/repo.git and http://localhost:8080/api",
		"commit 0123456789abcdef0123456789abcdef01234567, digest sha256:" + strings.Repeat("0a", 32),
		"The Wi-Fi password is on the fridge; the API key is in the team vault.",
		"会议定在 2026-10-20，地点在上海。",
	} {
		if !validText(text, 32<<10) {
			t.Errorf("refused ordinary text %q", text)
		}
	}
}

func TestImportRefusesCredentialFiles(t *testing.T) {
	f := newFixture(t)
	write := func(rel, content string) string {
		t.Helper()
		name := filepath.Join(f.home, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(name), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		return name
	}
	importPlan := func(name string) (Response, int) {
		return f.store.Execute(context.Background(), "import-plan", f.request(t, importInput{Files: []string{name}}))
	}
	// Plain content: these files are refused for where they are, not for what
	// they hold.
	for _, rel := range []string{
		".ssh/id_ed25519", ".ssh/config", ".gnupg/gpg.conf", ".aws/credentials", ".azure/msal_token_cache.json",
		".kube/config", ".docker/config.json", ".config/gh/hosts.yml", ".config/github-copilot/apps.json",
		".git-credentials", ".netrc", ".npmrc", ".env", ".hermes/.env", "project/.env.local", ".bash_history",
		".local/share/fish/fish_history", ".hermes/auth.json", ".hermes/config.yaml", ".openclaw/openclaw.json",
		".openclaw/openclaw.json.bak", ".openclaw/credentials/channel.json", ".openclaw/agents/main/agent/auth-profiles.json",
		".openclaw/agents/main/agent/models.json", "certificates/server.pem", "certificates/server.key",
		"Downloads/client_secret_example.json", "kubeconfig",
	} {
		if response, code := importPlan(write(rel, "Synthetic plain text.")); code != 2 || response.Reason != "SensitiveContent" {
			t.Errorf("%s: code %d, reason %s; want SensitiveContent", rel, code, response.Reason)
		}
	}
	// The harnesses' own memory files and other notes in HOME are imported.
	files := []string{}
	for _, rel := range []string{".openclaw/workspace/MEMORY.md", ".openclaw/workspace/memory/2026-10-01.md",
		".hermes/memories/MEMORY.md", "notes/environment.md", "notes/keyboard-shortcuts.md"} {
		files = append(files, write(rel, "Synthetic note: the owner prefers green tea."))
	}
	plan := f.call(t, "import-plan", importInput{Files: files}).Data.(ImportPlan)
	if refs := f.call(t, "import-apply", importApplyInput{Plan: plan}).Data.(writeResult).Refs; len(refs) != len(files) {
		t.Fatalf("imported %d of %d note files", len(refs), len(files))
	}
	// Elsewhere, text with a credential is refused.
	if response, code := importPlan(write("notes/setup.md", "export GITHUB"+"_TOKEN="+syntheticToken("", 32))); code != 2 || response.Reason != "SensitiveContent" {
		t.Fatalf("a note with a token: code %d, reason %s; want SensitiveContent", code, response.Reason)
	}
	// Apply rechecks both path and content even for an internally consistent
	// plan supplied by a caller. A later refused file must not commit earlier
	// files from the same batch.
	for _, rel := range []string{".env", "notes/assignment.md"} {
		body := "API_KEY=" + syntheticToken("", 32)
		if rel == ".env" {
			body = "Synthetic plain text."
		}
		name := write(rel, body)
		plan := f.call(t, "import-plan", importInput{Files: []string{files[0]}}).Data.(ImportPlan)
		plan.Files = append(plan.Files, ImportFile{Path: name, Digest: digest([]byte(body))})
		plan.Digest = plan.hash()
		request := f.request(t, importApplyInput{Plan: plan})
		before := *request.ExpectedGeneration
		response, code := f.store.Execute(context.Background(), "import-apply", request)
		if code != 2 || response.Reason != "SensitiveContent" {
			t.Fatalf("apply %s: code %d, reason %s", rel, code, response.Reason)
		}
		if after := *f.request(t, struct{}{}).ExpectedGeneration; after != before {
			t.Fatalf("refused batch changed generation: %d -> %d", before, after)
		}
	}
}

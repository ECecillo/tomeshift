package llm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ECecillo/tomeshift/internal/llm"
)

// fakeCLI writes an executable standing in for `claude`, printing body on
// stdout and recording its argv and stdin so the invocation can be asserted.
// It exercises the real exec path rather than a stubbed runner: the argument
// wiring is most of what this adapter does.
func fakeCLI(t *testing.T, body string, exitCode int) (bin, argsFile, stdinFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake CLI is a POSIX shell script")
	}

	dir := t.TempDir()
	bin = filepath.Join(dir, "fake-claude")
	argsFile = filepath.Join(dir, "args")
	stdinFile = filepath.Join(dir, "stdin")

	// Each argument on its own line, so an argument containing spaces or
	// newlines stays one record.
	script := fmt.Sprintf(`#!/bin/sh
: > %[1]q
for a in "$@"; do printf '%%s\n---ARG---\n' "$a" >> %[1]q; done
cat > %[2]q
cat <<'CLI_EOF'
%[3]s
CLI_EOF
exit %[4]d
`, argsFile, stdinFile, body, exitCode)

	require.NoError(t, os.WriteFile(bin, []byte(script), 0o755))
	return bin, argsFile, stdinFile
}

func readArgs(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	// Every record is terminated, so the split leaves one trailing empty piece.
	// Only that one is dropped: an empty argument is meaningful here — `--tools
	// ""` is how all tools are disabled, and discarding it would shift every
	// following argument by one.
	parts := strings.Split(string(raw), "\n---ARG---\n")
	if n := len(parts); n > 0 && parts[n-1] == "" {
		parts = parts[:n-1]
	}
	return parts
}

func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

const cliSuccessBody = `{"type":"result","subtype":"success","is_error":false,
"stop_reason":"end_turn","total_cost_usd":0.0126,
"usage":{"input_tokens":7,"output_tokens":28,
"cache_read_input_tokens":2027,"cache_creation_input_tokens":11},
"result":"{\"segments\":[{\"id\":\"a\",\"target_text\":\"bonjour\"}]}"}`

func TestCLIChatClient_MapsInvocationAndUsage(t *testing.T) {
	t.Parallel()

	bin, argsFile, stdinFile := fakeCLI(t, cliSuccessBody, 0)
	client := llm.NewCLIChatClient(bin)

	resp, err := client.Chat(context.Background(), llm.ChatRequest{
		Model: "opus",
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "GUIDE_DE_STYLE: sobre"},
			{Role: "user", Content: "SEGMENTS_DU_CHAPITRE_JSON: {}"},
		},
	})
	require.NoError(t, err)

	// The reply text and every token counter survive the round trip: the run
	// report and metrics.json are derived from them, and on a subscription seat
	// they are the only signal of what was consumed.
	require.JSONEq(t, `{"segments":[{"id":"a","target_text":"bonjour"}]}`, resp.Text)
	require.Equal(t, 7, resp.InputTokens)
	require.Equal(t, 28, resp.OutputTokens)
	require.Equal(t, 2027, resp.CacheReadInputTokens)
	require.Equal(t, 11, resp.CacheCreationInputTokens)
	require.Equal(t, llm.StopReasonEndTurn, resp.StopReason)

	args := readArgs(t, argsFile)
	require.Contains(t, args, "-p")
	format, ok := argValue(args, "--output-format")
	require.True(t, ok)
	require.Equal(t, "json", format)

	model, ok := argValue(args, "--model")
	require.True(t, ok)
	require.Equal(t, "opus", model)

	// Tools are disabled: this is a completion, not an agent with file access.
	tools, ok := argValue(args, "--tools")
	require.True(t, ok)
	require.Empty(t, tools)

	// --system-prompt REPLACES the agent's default prompt, which is what keeps
	// the per-call overhead at ~2k tokens instead of ~49k.
	system, ok := argValue(args, "--system-prompt")
	require.True(t, ok)
	require.Equal(t, "GUIDE_DE_STYLE: sobre", system)

	// The user turn travels on stdin, so a chapter-sized prompt never meets
	// ARG_MAX.
	stdin, err := os.ReadFile(stdinFile)
	require.NoError(t, err)
	require.Equal(t, "SEGMENTS_DU_CHAPITRE_JSON: {}", string(stdin))
}

func TestCLIChatClient_InlinesTheSchemaItCannotEnforce(t *testing.T) {
	t.Parallel()

	bin, argsFile, _ := fakeCLI(t, cliSuccessBody, 0)

	_, err := llm.NewCLIChatClient(bin).Chat(context.Background(), llm.ChatRequest{
		Messages:   []llm.ChatMessage{{Role: "system", Content: "sys"}, {Role: "user", Content: "u"}},
		JSONSchema: json.RawMessage(`{"type":"object"}`),
	})
	require.NoError(t, err)

	// The CLI exposes no structured-output flag, so the schema has to become an
	// instruction; dropping it silently would leave the model with no contract
	// at all.
	system, ok := argValue(readArgs(t, argsFile), "--system-prompt")
	require.True(t, ok)
	require.Contains(t, system, `{"type":"object"}`)
}

func TestCLIChatClient_HugeSystemPromptFallsBackToStdin(t *testing.T) {
	t.Parallel()

	bin, argsFile, stdinFile := fakeCLI(t, cliSuccessBody, 0)
	huge := strings.Repeat("x", 300<<10)

	_, err := llm.NewCLIChatClient(bin).Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "system", Content: huge}, {Role: "user", Content: "u"}},
	})
	require.NoError(t, err)

	// ARG_MAX is shared by arguments and environment; past the bound the prompt
	// moves to stdin rather than failing the call.
	_, ok := argValue(readArgs(t, argsFile), "--system-prompt")
	require.False(t, ok)

	stdin, err := os.ReadFile(stdinFile)
	require.NoError(t, err)
	require.Contains(t, string(stdin), huge)
	require.Contains(t, string(stdin), "u")
}

func TestCLIChatClient_ReportsFailures(t *testing.T) {
	t.Parallel()

	t.Run("cli reports an error result", func(t *testing.T) {
		t.Parallel()
		bin, _, _ := fakeCLI(t, `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"rate limit reached"}`, 0)

		_, err := llm.NewCLIChatClient(bin).Chat(context.Background(), llm.ChatRequest{
			Messages: []llm.ChatMessage{{Role: "user", Content: "u"}},
		})
		// A quota refusal must not read as a malformed reply: the literary
		// batcher would keep halving batches against a wall.
		require.ErrorIs(t, err, llm.ErrProviderStatus)
		require.Contains(t, err.Error(), "rate limit reached")
	})

	t.Run("non-zero exit", func(t *testing.T) {
		t.Parallel()
		bin, _, _ := fakeCLI(t, "boom", 1)

		_, err := llm.NewCLIChatClient(bin).Chat(context.Background(), llm.ChatRequest{
			Messages: []llm.ChatMessage{{Role: "user", Content: "u"}},
		})
		require.Error(t, err)
	})

	t.Run("output is not the expected json", func(t *testing.T) {
		t.Parallel()
		bin, _, _ := fakeCLI(t, "not json at all", 0)

		_, err := llm.NewCLIChatClient(bin).Chat(context.Background(), llm.ChatRequest{
			Messages: []llm.ChatMessage{{Role: "user", Content: "u"}},
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "not json at all")
	})

	t.Run("empty result still reports its usage", func(t *testing.T) {
		t.Parallel()
		bin, _, _ := fakeCLI(t, `{"type":"result","subtype":"success","result":"   ",
			"usage":{"input_tokens":11,"output_tokens":16000,"cache_read_input_tokens":900}}`, 0)

		resp, err := llm.NewCLIChatClient(bin).Chat(context.Background(), llm.ChatRequest{
			Messages: []llm.ChatMessage{{Role: "user", Content: "u"}},
		})
		// Unusable content, but the tokens were generated and billed — and the
		// caller retries this on halved batches rather than failing the chapter.
		require.ErrorIs(t, err, llm.ErrEmptyResponse)
		require.Equal(t, 11, resp.InputTokens)
		require.Equal(t, 16000, resp.OutputTokens)
		require.Equal(t, 900, resp.CacheReadInputTokens)
	})
}

func TestCLIChatClient_CancellationStops(t *testing.T) {
	t.Parallel()

	bin, _, _ := fakeCLI(t, cliSuccessBody, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := llm.NewCLIChatClient(bin).Chat(ctx, llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "u"}},
	})
	// A cancelled run must surface as cancellation so the pipeline maps it to
	// the "aborted" exit code instead of "provider failed".
	require.ErrorIs(t, err, context.Canceled)
}

func TestNewChatClient_SelectsTheCLIAdapter(t *testing.T) {
	t.Parallel()

	client := llm.NewChatClient(llm.ChatClientConfig{
		Provider: llm.ChatProviderClaudeCLI, CLIBin: "/usr/local/bin/claude",
	})
	cli, ok := client.(*llm.CLIChatClient)
	require.True(t, ok)
	require.Equal(t, "/usr/local/bin/claude", cli.Bin)
}

func TestCLIChatClient_RateLimitIsNamed(t *testing.T) {
	t.Parallel()

	// What the CLI prints when its own upstream request was refused for rate:
	// a successful process, an error subtype, and the upstream status in
	// api_error_status — the only field that tells the two apart.
	body := `{"type":"result","subtype":"error_during_execution","is_error":true,` +
		`"api_error_status":429,"result":"rate limit reached"}`
	bin, _, _ := fakeCLI(t, body, 0)

	_, err := llm.NewCLIChatClient(bin).Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "hi"}},
	})

	require.ErrorIs(t, err, llm.ErrRateLimited)
	// Still a provider status, so a caller that only cares that the provider
	// said no keeps matching.
	require.ErrorIs(t, err, llm.ErrProviderStatus)
	require.Contains(t, err.Error(), "429")
}

func TestCLIChatClient_OtherFailuresAreNotRateLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "a server error carries its own status",
			body: `{"type":"result","subtype":"error_during_execution","is_error":true,` +
				`"api_error_status":500,"result":"boom"}`,
		},
		{
			name: "a failure with no upstream status at all",
			body: `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			bin, _, _ := fakeCLI(t, tt.body, 0)
			_, err := llm.NewCLIChatClient(bin).Chat(context.Background(), llm.ChatRequest{
				Messages: []llm.ChatMessage{{Role: "user", Content: "hi"}},
			})

			require.ErrorIs(t, err, llm.ErrProviderStatus)
			require.NotErrorIs(t, err, llm.ErrRateLimited,
				"only a 429 may be treated as the end of the road")
		})
	}
}

func TestOpenAICompatibleClient_RateLimitIsNamed(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)

	_, err := llm.NewOpenAICompatibleClient(srv.URL+"/v1", "k").Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "hi"}},
	})

	require.ErrorIs(t, err, llm.ErrRateLimited)
	require.ErrorIs(t, err, llm.ErrProviderStatus)
}

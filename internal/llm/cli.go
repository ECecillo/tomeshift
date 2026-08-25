package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// ChatProviderClaudeCLI routes chat completions through the locally installed
// Claude Code CLI instead of the HTTP API, so a run is billed against an
// existing subscription seat rather than an API key.
//
// It is an EXPERIMENT, and the two things it gives up are worth knowing before
// a volume is started:
//
//   - The budget guardrail loses its meaning. A subscription has no marginal
//     dollar, so the configured rates — and therefore the per-book cap the whole
//     cost model exists to enforce — no longer describe what a run consumes.
//     Setting the rates to the model's published API price restores a usable
//     proxy: the cap then bounds "how much model would this have bought", which
//     is the closest stand-in for quota this pipeline can compute.
//   - There is no cache_control. The Anthropic adapter marks the stable prefix
//     explicitly; here the CLI decides. The prefix is still byte-identical
//     across a chapter's batches, so the provider's content-keyed cache may hit
//     on its own — CacheReadInputTokens in report.json is what says whether it
//     did, and it is the first number to look at after a real run.
const ChatProviderClaudeCLI = "claude-cli"

const defaultCLIBin = "claude"

// maxSystemPromptArg bounds what is passed as an argv element. The stable
// prefix carries a bible plus a whole chapter's source, and ARG_MAX is shared
// by the arguments and the environment; past this the prompt is folded into
// stdin instead, which has no such limit.
const maxSystemPromptArg = 256 << 10

// CLIChatClient runs one chat completion per subprocess invocation.
//
// Every call is an independent session: nothing is resumed, so the adapter
// stays as stateless as the HTTP ones and the SQLite completion cache keeps
// working unchanged (it keys on model plus messages, not on transport).
type CLIChatClient struct {
	// Bin is the executable to run. Empty uses "claude" from PATH.
	Bin string
	// Args are extra arguments inserted before the generated ones, for
	// per-setup needs (e.g. --settings). Rarely needed.
	Args []string
}

// NewCLIChatClient builds an adapter driving bin. An empty bin uses "claude".
func NewCLIChatClient(bin string) *CLIChatClient {
	return &CLIChatClient{Bin: bin}
}

// cliResult is the shape `claude -p --output-format json` prints. Only the
// fields this adapter maps are declared; the CLI emits many more and adds to
// them between versions, so decoding stays lenient by design.
type cliResult struct {
	Type       string `json:"type"`
	Subtype    string `json:"subtype"`
	IsError    bool   `json:"is_error"`
	Result     string `json:"result"`
	StopReason string `json:"stop_reason"`
	// APIErrorStatus is the upstream HTTP status when the CLI's own request
	// failed, and null otherwise. It is the only field that separates "the model
	// declined this batch" from "the seat is out of quota" — a distinction the
	// literary pass needs, because it degrades the first and must stop the book
	// on the second.
	APIErrorStatus *int `json:"api_error_status"`
	// TotalCostUSD is what the same work would have cost at API rates. It is
	// deliberately NOT used as the run's cost: on a subscription seat nothing
	// was billed, and reporting it would make report.json claim spend that
	// never happened. The token counters below are the real signal.
	TotalCostUSD float64 `json:"total_cost_usd"`
	Usage        struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// Chat renders req into one CLI invocation and maps the reply back.
func (c *CLIChatClient) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	system, prompt := splitMessages(req)
	if req.JSONSchema != nil {
		// The CLI exposes no structured-output flag, so the schema becomes an
		// instruction. The literary decoder already tolerates prose around the
		// object and retries on halved batches, which is what absorbs the
		// difference in reliability.
		system = strings.TrimSpace(system + "\n\nRéponds UNIQUEMENT avec un objet JSON conforme à ce schéma, sans texte avant ni après :\n" + string(req.JSONSchema))
	}

	args := append([]string{}, c.Args...)
	args = append(args, "-p", "--output-format", "json",
		// No tools: this is a completion, not an agent. It also keeps the
		// tool definitions out of the prompt.
		"--tools", "")
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	// --system-prompt REPLACES the agent's default system prompt. That is the
	// whole reason this adapter is affordable: measured on 2026-08-08, the
	// default prompt costs ~48,900 input tokens per invocation against ~2,000
	// with this flag — 24x, on every single call.
	if system != "" && len(system) <= maxSystemPromptArg {
		args = append(args, "--system-prompt", system)
	} else if system != "" {
		prompt = system + "\n\n" + prompt
	}

	// A working directory of its own, so the CLI's project-file discovery
	// cannot fold this repository's CLAUDE.md into a literary translation.
	dir, err := os.MkdirTemp("", "epub-translator-cli-")
	if err != nil {
		return ChatResponse{}, fmt.Errorf("unable to create cli working dir, err: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()

	cmd := exec.CommandContext(ctx, c.bin(), args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(prompt)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ChatResponse{}, ctxErr
		}
		return ChatResponse{}, fmt.Errorf("unable to run %s (%s), err: %w",
			c.bin(), truncate(stderr.String(), 200), err)
	}

	var out cliResult
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return ChatResponse{}, fmt.Errorf("unable to decode %s output (%s), err: %w",
			c.bin(), truncate(stdout.String(), 200), err)
	}
	if out.IsError || (out.Subtype != "" && out.Subtype != "success") {
		status := 0
		if out.APIErrorStatus != nil {
			status = *out.APIErrorStatus
		}
		return ChatResponse{}, statusError(status, "unable to complete via %s (subtype=%q, status=%d): %s",
			c.bin(), out.Subtype, status, truncate(out.Result, 200))
	}
	usage := ChatResponse{
		InputTokens:              out.Usage.InputTokens,
		OutputTokens:             out.Usage.OutputTokens,
		CacheReadInputTokens:     out.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: out.Usage.CacheCreationInputTokens,
		StopReason:               out.StopReason,
	}

	// The usage rides along with the error, as it does on the HTTP adapters: an
	// empty reply was still generated and still billed, and the caller treats
	// this as a retry-on-halves case rather than a fatal one.
	if strings.TrimSpace(out.Result) == "" {
		return usage, ErrEmptyResponse
	}

	usage.Text = out.Result
	return usage, nil
}

func (c *CLIChatClient) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	return defaultCLIBin
}

// splitMessages folds the conversation into the CLI's two inputs: one system
// prompt and one prompt on stdin. Assistant turns are labelled rather than
// dropped, so a caller that sends a multi-turn history keeps it.
func splitMessages(req ChatRequest) (system, prompt string) {
	var sys, body strings.Builder
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			if sys.Len() > 0 {
				sys.WriteString("\n\n")
			}
			sys.WriteString(m.Content)
		case "assistant":
			if body.Len() > 0 {
				body.WriteString("\n\n")
			}
			body.WriteString("[assistant]\n" + m.Content)
		default:
			if body.Len() > 0 {
				body.WriteString("\n\n")
			}
			body.WriteString(m.Content)
		}
	}
	return sys.String(), body.String()
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

var _ ChatClient = (*CLIChatClient)(nil)

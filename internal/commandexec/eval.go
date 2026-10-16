package commandexec

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ECecillo/tomeshift/internal/cli"
	"github.com/ECecillo/tomeshift/internal/config"
	"github.com/ECecillo/tomeshift/internal/eval"
	"github.com/ECecillo/tomeshift/internal/exitcode"
)

// runEval compares a candidate translation against a human reference EPUB using
// chrF and writes eval-report.json beside the candidate.
func runEval(stdout, stderr io.Writer, cmd cli.EvalCommand, cfg config.Config) int {
	if cmd.Segments != "" {
		return runEvalQE(stdout, stderr, cmd, cfg)
	}

	ctx := context.Background()

	candText, err := bookPlainText(ctx, cmd.Candidate)
	if err != nil {
		writef(stderr, "unable to read candidate: %v\n", err)
		return exitCodeForError(err)
	}
	refText, err := bookPlainText(ctx, cmd.Reference)
	if err != nil {
		writef(stderr, "unable to read reference: %v\n", err)
		return exitCodeForError(err)
	}

	score := eval.ChrF(candText, refText, eval.DefaultOrder, eval.DefaultBeta)

	report := map[string]any{
		"candidate":       cmd.Candidate,
		"reference":       cmd.Reference,
		"metric":          "chrf",
		"score":           score,
		"candidate_chars": len([]rune(candText)),
		"reference_chars": len([]rune(refText)),
	}
	if data, err := json.MarshalIndent(report, "", "  "); err == nil {
		out := filepath.Join(filepath.Dir(cmd.Candidate), "eval-report.json")
		if werr := os.WriteFile(out, append(data, '\n'), 0o644); werr != nil {
			writef(stderr, "unable to write eval report: %v\n", werr)
		}
	}

	writef(stdout, "chrF = %.4f (candidate %d chars vs reference %d chars)\n",
		score, len([]rune(candText)), len([]rune(refText)))
	return exitcode.Success
}

// evalTokenPattern strips inline (<x id="N"/>) and glossary (<g id="N"/>)
// placeholder tokens so the QE model scores natural text only.
var evalTokenPattern = regexp.MustCompile(`<[xg] id="\d+"\s*/?>(?:\s*</[xg]>)?`)

func stripPlaceholderTokens(s string) string {
	return strings.TrimSpace(evalTokenPattern.ReplaceAllString(s, " "))
}

// runEvalQE scores a run's segments (source vs target) with reference-free
// COMET-QE. It reads segments.jsonl, strips placeholder tokens, and reports the
// mean/min score. Requires a COMET service (TOMESHIFT_COMET_ENDPOINT).
func runEvalQE(stdout, stderr io.Writer, cmd cli.EvalCommand, cfg config.Config) int {
	if cfg.CometEndpoint == "" {
		writef(stderr, "QE eval needs a COMET service: set TOMESHIFT_COMET_ENDPOINT\n")
		return exitcode.InvalidInputConfig
	}

	f, err := os.Open(cmd.Segments)
	if err != nil {
		writef(stderr, "unable to open segments file: %v\n", err)
		return exitcode.InvalidInputConfig
	}
	defer func() { _ = f.Close() }()

	var samples []eval.Sample
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<23) // tolerate long lines
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var rec struct {
			SourceText string `json:"source_text"`
			TargetText string `json:"target_text"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			writef(stderr, "unable to parse segments line: %v\n", err)
			return exitcode.InvalidInputConfig
		}
		src, mt := stripPlaceholderTokens(rec.SourceText), stripPlaceholderTokens(rec.TargetText)
		if src == "" || mt == "" {
			continue
		}
		samples = append(samples, eval.Sample{Src: src, MT: mt})
	}
	if err := sc.Err(); err != nil {
		writef(stderr, "unable to read segments file: %v\n", err)
		return exitcode.InvalidInputConfig
	}
	if len(samples) == 0 {
		writef(stderr, "no scorable segments in %s\n", cmd.Segments)
		return exitcode.InvalidInputConfig
	}

	scores, err := eval.NewCometClient(cfg.CometEndpoint, cfg.CometAPIKey).Score(context.Background(), samples)
	if err != nil {
		writef(stderr, "unable to score with COMET-QE: %v\n", err)
		return exitCodeForError(err)
	}

	sum, minScore := 0.0, scores[0]
	for _, s := range scores {
		sum += s
		if s < minScore {
			minScore = s
		}
	}
	avg := sum / float64(len(scores))

	report := map[string]any{
		"metric":        "comet-qe",
		"segments":      len(scores),
		"avg_score":     avg,
		"min_score":     minScore,
		"segments_file": cmd.Segments,
	}
	if data, mErr := json.MarshalIndent(report, "", "  "); mErr == nil {
		out := filepath.Join(filepath.Dir(cmd.Segments), "qe-eval-report.json")
		if wErr := os.WriteFile(out, append(data, '\n'), 0o644); wErr != nil {
			writef(stderr, "unable to write qe-eval report: %v\n", wErr)
		}
	}

	writef(stdout, "COMET-QE avg = %.4f (min %.4f over %d segments)\n", avg, minScore, len(scores))
	return exitcode.Success
}

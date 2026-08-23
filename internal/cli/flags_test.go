package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestParseRunArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		args            []string
		want            RunCommand
		wantErr         error
		wantErrContains string
	}{
		{
			name: "happy path with optional flags",
			args: []string{
				"--input", "book.epub",
				"--output-dir", "./runs",
				"--source-lang", "en",
				"--target-lang", "fr",
				"--glossary", "glossary.csv",
				"--run-id", "run-001",
				"--resume",
			},
			want: RunCommand{
				Input:      "book.epub",
				OutputDir:  "./runs",
				SourceLang: "en",
				TargetLang: "fr",
				Glossary:   "glossary.csv",
				RunID:      "run-001",
				Mode:       "nmt",
				Resume:     true,
			},
		},
		{
			name: "progress bar opted out",
			args: []string{
				"--input", "book.epub",
				"--output-dir", "./runs",
				"--source-lang", "en",
				"--target-lang", "fr",
				"--no-progress",
			},
			want: RunCommand{
				Input:      "book.epub",
				OutputDir:  "./runs",
				SourceLang: "en",
				TargetLang: "fr",
				Mode:       "nmt",
				NoProgress: true,
			},
		},
		{
			name: "literary mode",
			args: []string{
				"--input", "book.epub",
				"--output-dir", "./runs",
				"--source-lang", "en",
				"--target-lang", "fr",
				"--mode", "literary",
				"--series", "series-a",
			},
			want: RunCommand{
				Input:      "book.epub",
				OutputDir:  "./runs",
				SourceLang: "en",
				TargetLang: "fr",
				Mode:       "literary",
				Series:     "series-a",
			},
		},
		{
			name:            "flag parse error",
			args:            []string{"--unknown"},
			wantErrContains: "unable to parse cli flags",
		},
		{
			name:            "unexpected positional arg",
			args:            []string{"--input", "book.epub", "--output-dir", "./runs", "--source-lang", "en", "--target-lang", "fr", "extra"},
			wantErr:         ErrUnexpectedPositionalArgs,
			wantErrContains: "unable to parse cli flags",
		},
		{
			name:    "missing input",
			args:    []string{"--output-dir", "./runs", "--source-lang", "en", "--target-lang", "fr"},
			wantErr: ErrInputRequired,
		},
		{
			name:    "missing output-dir",
			args:    []string{"--input", "book.epub", "--source-lang", "en", "--target-lang", "fr"},
			wantErr: ErrOutputDirRequired,
		},
		{
			name:    "missing source-lang",
			args:    []string{"--input", "book.epub", "--output-dir", "./runs", "--target-lang", "fr"},
			wantErr: ErrSourceLangRequired,
		},
		{
			name:    "missing target-lang",
			args:    []string{"--input", "book.epub", "--output-dir", "./runs", "--source-lang", "en"},
			wantErr: ErrTargetLangRequired,
		},
		{
			name:            "unsupported source-lang",
			args:            []string{"--input", "book.epub", "--output-dir", "./runs", "--source-lang", "es", "--target-lang", "fr"},
			wantErr:         ErrUnsupportedSourceLang,
			wantErrContains: "unable to validate run args",
		},
		{
			name:            "unsupported target-lang",
			args:            []string{"--input", "book.epub", "--output-dir", "./runs", "--source-lang", "en", "--target-lang", "de"},
			wantErr:         ErrUnsupportedTargetLang,
			wantErrContains: "unable to validate run args",
		},
		{
			name:            "unsupported mode",
			args:            []string{"--input", "book.epub", "--output-dir", "./runs", "--source-lang", "en", "--target-lang", "fr", "--mode", "fast"},
			wantErr:         ErrUnsupportedMode,
			wantErrContains: "unable to validate run args",
		},
		{
			name:    "literary missing series",
			args:    []string{"--input", "book.epub", "--output-dir", "./runs", "--source-lang", "en", "--target-lang", "fr", "--mode", "literary"},
			wantErr: ErrSeriesRequired,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stderr := &bytes.Buffer{}
			got, err := ParseRunArgs(tt.args, stderr)

			if tt.wantErr == nil && tt.wantErrContains == "" {
				if err != nil {
					t.Fatalf("ParseRunArgs() unexpected error = %v", err)
				}
				if got != tt.want {
					t.Fatalf("ParseRunArgs() got = %+v, want %+v", got, tt.want)
				}
				return
			}

			if err == nil {
				t.Fatalf("ParseRunArgs() expected error, got nil")
			}
			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseRunArgs() error = %v, want errors.Is(..., %v)", err, tt.wantErr)
			}
			if tt.wantErrContains != "" && !strings.Contains(err.Error(), tt.wantErrContains) {
				t.Fatalf("ParseRunArgs() error = %q, want substring %q", err.Error(), tt.wantErrContains)
			}
		})
	}
}

func TestPrintRootUsage(t *testing.T) {
	t.Parallel()

	buf := &bytes.Buffer{}
	PrintRootUsage(buf)

	out := buf.String()
	if !strings.Contains(out, "Usage:") {
		t.Fatalf("PrintRootUsage() missing Usage header, got %q", out)
	}
	if !strings.Contains(out, "epub-translator run --input") {
		t.Fatalf("PrintRootUsage() missing command synopsis, got %q", out)
	}
	if !strings.Contains(out, "epub-translator style-profile --input") {
		t.Fatalf("PrintRootUsage() missing style-profile synopsis, got %q", out)
	}
}

func TestParseStyleProfileArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		want    StyleProfileCommand
		wantErr error
	}{
		{
			name: "happy path",
			args: []string{"--input", "book.epub", "--series", "series-a", "--samples", "4"},
			want: StyleProfileCommand{Input: "book.epub", Series: "series-a", Samples: 4},
		},
		{
			name:    "missing input",
			args:    []string{"--series", "series-a"},
			wantErr: ErrInputRequired,
		},
		{
			name:    "missing series",
			args:    []string{"--input", "book.epub"},
			wantErr: ErrSeriesRequired,
		},
		{
			name: "clamps samples",
			args: []string{"--input", "book.epub", "--series", "series-a", "--samples", "0"},
			want: StyleProfileCommand{Input: "book.epub", Series: "series-a", Samples: 1},
		},
		{
			name:    "unexpected positional",
			args:    []string{"--input", "book.epub", "--series", "series-a", "extra"},
			wantErr: ErrUnexpectedPositionalArgs,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseStyleProfileArgs(tt.args, &bytes.Buffer{})
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("ParseStyleProfileArgs() unexpected error = %v", err)
				}
				if got != tt.want {
					t.Fatalf("ParseStyleProfileArgs() got = %+v, want %+v", got, tt.want)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseStyleProfileArgs() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func FuzzParseRunArgs_NoPanic(f *testing.F) {
	seedInputs := []string{
		"",
		"--input\x00book.epub\x00--output-dir\x00./runs\x00--source-lang\x00en\x00--target-lang\x00fr",
		"--unknown",
		"--input\x00book.epub\x00--output-dir\x00./runs\x00--source-lang\x00es\x00--target-lang\x00fr",
		"--input\x00book.epub\x00--output-dir\x00./runs\x00--source-lang\x00en\x00--target-lang\x00de",
	}
	for _, seed := range seedInputs {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		args := strings.Split(raw, "\x00")
		stderr := &bytes.Buffer{}

		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("ParseRunArgs panicked with args=%q panic=%v", args, r)
			}
		}()

		_, _ = ParseRunArgs(args, stderr)
	})
}

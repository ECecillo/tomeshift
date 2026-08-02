package cli

import (
	"bytes"
	"errors"
	"testing"
)

func TestParseSeriesBibleArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		want    SeriesBibleCommand
		wantErr error
	}{
		{
			name: "happy path",
			args: []string{"--input-dir", "./books/lumen", "--series", "lumen", "--samples", "8", "--merge"},
			want: SeriesBibleCommand{InputDir: "./books/lumen", Series: "lumen", Samples: 8, Merge: true},
		},
		{
			name: "defaults",
			args: []string{"--input-dir", "./books", "--series", "lumen"},
			want: SeriesBibleCommand{InputDir: "./books", Series: "lumen", Samples: 3},
		},
		{
			name: "samples floor",
			args: []string{"--input-dir", "./books", "--series", "lumen", "--samples", "0"},
			want: SeriesBibleCommand{InputDir: "./books", Series: "lumen", Samples: 1},
		},
		{
			name:    "input-dir required",
			args:    []string{"--series", "lumen"},
			wantErr: ErrInputDirRequired,
		},
		{
			name:    "series required",
			args:    []string{"--input-dir", "./books"},
			wantErr: ErrSeriesRequired,
		},
		{
			name:    "positional args rejected",
			args:    []string{"--input-dir", "./books", "--series", "lumen", "extra"},
			wantErr: ErrUnexpectedPositionalArgs,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stderr bytes.Buffer
			got, err := ParseSeriesBibleArgs(tt.args, &stderr)

			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

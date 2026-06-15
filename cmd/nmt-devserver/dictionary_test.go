package main

import "testing"

func TestTranslate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"known words", "hello world", "bonjour monde"},
		{"case preserved", "Hello World", "Bonjour Monde"},
		{"unknown words pass through", "hello Frodo", "bonjour Frodo"},
		{"placeholder token preserved", `a <x id="1"/> claim`, `un <x id="1"/> affirmation`},
		{"punctuation preserved", "the book.", "le livre."},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := translate(tt.in); got != tt.want {
				t.Fatalf("translate(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

package segment

import (
	"strings"
	"testing"
)

const nbsp = "\u00a0"

func TestFrenchTypography(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// Rule 1: ellipsis.
		{"ellipsis three dots", "Trois points...", "Trois points…"},
		{"ellipsis four dots", "Quatre....", "Quatre…"},
		{"ellipsis two dots kept", "Deux..", "Deux.."},
		{"ellipsis single dot kept", "Un.", "Un."},
		{"ellipsis existing not collapsed", "Déjà……", "Déjà……"},
		{"ellipsis repeated runs", "a...b...c", "a…b…c"},

		// Rule 2: guillemets.
		{"guillemets tight", "«Bonjour»", "«" + nbsp + "Bonjour" + nbsp + "»"},
		{"guillemets spaced", "« Bonjour »", "«" + nbsp + "Bonjour" + nbsp + "»"},
		{"guillemets double spaced", "«  Bonjour  »", "«" + nbsp + "Bonjour" + nbsp + "»"},
		{"guillemets narrow nbsp", "«\u202fBonjour\u202f»", "«" + nbsp + "Bonjour" + nbsp + "»"},
		{"guillemets already nbsp", "«\u00a0Bonjour\u00a0»", "«" + nbsp + "Bonjour" + nbsp + "»"},

		// Rule 3: high punctuation.
		{"question tight", "Vraiment?", "Vraiment" + nbsp + "?"},
		{"question spaced", "Vraiment ?", "Vraiment" + nbsp + "?"},
		{"bang", "Attends!", "Attends" + nbsp + "!"},
		{"colon", "Deux points: trois", "Deux points" + nbsp + ": trois"},
		{"semicolon", "Point-virgule; suite", "Point-virgule" + nbsp + "; suite"},
		{"run question bang", "Quoi?!", "Quoi" + nbsp + "?!"},
		{"run double bang", "Non!!", "Non" + nbsp + "!!"},
		{"run triple question", "Hein???", "Hein" + nbsp + "???"},
		{"time colon untouched", "Rendez-vous 10:30", "Rendez-vous 10:30"},
		{"title colon untouched", "Re:Zero !", "Re:Zero" + nbsp + "!"},
		{"url colon untouched", "https://example.com", "https://example.com"},
		{"no space at start", "?!", "?!"},
		{"no space after open guillemet", "«?", "«" + nbsp + "?"},
		{"speaker label name", "Ram: « Viens", "Ram" + nbsp + ": «" + nbsp + "Viens"},
		{"speaker label unknown", "???: « Viens", "???" + nbsp + ": «" + nbsp + "Viens"},
		{"existing space after symbol", "■■■■ : « Viens", "■■■■" + nbsp + ": «" + nbsp + "Viens"},

		// Rule 4: em dash.
		{"dash between words both spaced", "mot — mot", "mot—mot"},
		{"dash between words left spaced", "mot— mot", "mot—mot"},
		{"dash between words right spaced", "mot —mot", "mot—mot"},
		{"dash between words tight", "mot—mot", "mot—mot"},
		{"dash after open guillemet spaced", "« — Viens", "«" + nbsp + "—Viens"},
		{"dash after open guillemet tight", "« —Viens", "«" + nbsp + "—Viens"},
		{"dash after bang", "faire ! — Viens", "faire" + nbsp + "! —Viens"},
		{"dash after period kept space", "fin. —Suite", "fin. —Suite"},
		{"dash after period collapse space", "fin. — Suite", "fin. —Suite"},
		{"double dash spaced", "mot —— mot", "mot——mot"},
		{"double dash tight", "mot——mot", "mot——mot"},
		{"dash before period glued", "la fin — .", "la fin—."},
		{"silence dash after open guillemet", "« — . On", "«" + nbsp + "—. On"},
		{"dash before comma glued", "sa peine — , il", "sa peine—, il"},
		{"dash before closing guillemet", "dit-il — »", "dit-il —" + nbsp + "»"},

		// Tokens: zero-width for context, preserved byte-for-byte.
		{"token before question", `mot<x id="2"/>?`, `mot<x id="2"/>` + nbsp + "?"},
		{"token hides start before run", `<x id="1"/>??? : «`, `<x id="1"/>???` + nbsp + ": «" + nbsp},
		{"glossary token zero width dash", `mot<g id="3"/> — autre`, `mot<g id="3"/>—autre`},
		{"token not modified inside", `<x id="10"/>`, `<x id="10"/>`},

		// Combined, realistic dialogue.
		{
			"dialogue combined",
			"«Ram: Attends...moi!»",
			"«" + nbsp + "Ram" + nbsp + ": Attends…moi" + nbsp + "!" + nbsp + "»",
		},

		// No-ops.
		{"plain sentence", "Bonjour le monde.", "Bonjour le monde."},
		{"empty", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FrenchTypography(tc.in)
			if got != tc.want {
				t.Errorf("FrenchTypography(%q)\n got = %q\nwant = %q", tc.in, got, tc.want)
			}
			// Idempotence: f(f(x)) == f(x).
			if again := FrenchTypography(got); again != got {
				t.Errorf("not idempotent for %q:\n first = %q\nsecond = %q", tc.in, got, again)
			}
		})
	}
}

func TestFrenchTypographyPreservesTokens(t *testing.T) {
	in := `D'abord<x id="1"/>, puis<g id="2"/>... «<x id="3"/>» !`
	got := FrenchTypography(in)
	for _, tok := range []string{`<x id="1"/>`, `<g id="2"/>`, `<x id="3"/>`} {
		if !strings.Contains(got, tok) {
			t.Errorf("token %q dropped from %q", tok, got)
		}
	}
}

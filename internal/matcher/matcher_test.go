package matcher

import (
	"testing"

	"github.com/bateau84/yt2sp/internal/domain"
)

func TestNormalize(t *testing.T) {
	m := NewMatcher()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "removes known bracketed and parenthesized suffixes",
			input: "The Weeknd - Blinding Lights [Official Video] (HD)",
			want:  "the weeknd blinding lights",
		},
		{
			name:  "removes common noise phrases",
			input: "Daft Punk - One More Time (Remastered) HQ High Quality Audio",
			want:  "daft punk one more time",
		},
		{
			name:  "normalizes separators and trims whitespace",
			input: "Adele — Hello | Official Music Video",
			want:  "adele hello",
		},
		{
			name:  "handles lyric and visualizer markers",
			input: "Imagine Dragons - Believer (Lyrics) (Visualizer)",
			want:  "imagine dragons believer",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := m.Normalize(tc.input)
			if got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestScore(t *testing.T) {
	m := NewMatcher()

	t.Run("high score when artist and title both match", func(t *testing.T) {
		score := m.Score(
			"The Weeknd - Blinding Lights [Official Music Video]",
			"Blinding Lights",
			"The Weeknd",
		)

		if score <= 0.8 {
			t.Fatalf("expected score > 0.8, got %.4f", score)
		}
	})

	t.Run("medium score for partial similarity (remix)", func(t *testing.T) {
		score := m.Score(
			"The Weeknd - Blinding Lights Remix ft. Rosalia",
			"Blinding Lights",
			"The Weeknd",
		)

		if score <= 0.4 || score > 0.8 {
			t.Fatalf("expected score in (0.4, 0.8], got %.4f", score)
		}
	})

	t.Run("low score for unrelated track", func(t *testing.T) {
		score := m.Score(
			"Rick Astley - Never Gonna Give You Up",
			"Smells Like Teen Spirit",
			"Nirvana",
		)

		if score >= 0.4 {
			t.Fatalf("expected score < 0.4, got %.4f", score)
		}
	})
}

func TestClassify(t *testing.T) {
	m := NewMatcher()

	tests := []struct {
		name  string
		score float64
		want  domain.MatchDecision
	}{
		{name: "auto above threshold", score: 0.81, want: domain.MatchAuto},
		{name: "pending lower bound inclusive", score: 0.4, want: domain.MatchPending},
		{name: "pending upper bound inclusive", score: 0.8, want: domain.MatchPending},
		{name: "rejected below threshold", score: 0.39, want: domain.MatchRejected},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := m.Classify(tc.score)
			if got != tc.want {
				t.Fatalf("Classify(%.2f) = %q, want %q", tc.score, got, tc.want)
			}
		})
	}
}

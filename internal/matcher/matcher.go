package matcher

import (
	"regexp"
	"strings"

	"github.com/bateau84/yt2sp/internal/domain"
)

var (
	knownBracketedNoise = []string{
		"official video",
		"lyrics",
		"hd",
		"official music video",
		"official audio",
		"visualizer",
	}

	commonNoisePhrases = []string{
		"official music video",
		"audio",
		"visualizer",
		"remastered",
		"hq",
		"high quality",
	}

	spaceRegex = regexp.MustCompile(`\s+`)
	punctuationRegex = regexp.MustCompile(`[^\p{L}\p{N}\s]+`)
)

type Matcher struct{}

func NewMatcher() *Matcher {
	return &Matcher{}
}

func (m *Matcher) Normalize(title string) string {
	normalized := strings.ToLower(title)

	for _, phrase := range knownBracketedNoise {
		normalized = strings.ReplaceAll(normalized, "["+phrase+"]", " ")
		normalized = strings.ReplaceAll(normalized, "("+phrase+")", " ")
	}

	normalized = strings.NewReplacer("-", " ", "—", " ", "|", " ").Replace(normalized)
	normalized = strings.NewReplacer("[", " ", "]", " ", "(", " ", ")", " ").Replace(normalized)

	for _, phrase := range commonNoisePhrases {
		normalized = strings.ReplaceAll(normalized, phrase, " ")
	}

	normalized = punctuationRegex.ReplaceAllString(normalized, " ")

	normalized = spaceRegex.ReplaceAllString(normalized, " ")
	return strings.TrimSpace(normalized)
}

func (m *Matcher) Score(videoTitle string, candidateTitle string, artist string) float64 {
	normalizedVideo := m.Normalize(videoTitle)
	normalizedCandidate := m.Normalize(candidateTitle)
	normalizedArtist := m.Normalize(artist)

	if normalizedVideo == "" || normalizedCandidate == "" {
		return 0
	}

	videoTokens := tokenSet(normalizedVideo)
	expectedQuery := normalizedCandidate
	if normalizedArtist != "" {
		expectedQuery += " " + normalizedArtist
	}
	expectedTokens := tokenSet(expectedQuery)

	precision := overlapRatio(expectedTokens, videoTokens)
	recall := overlapRatio(videoTokens, expectedTokens)

	score := f1Score(precision, recall)

	hasCandidatePhrase := strings.Contains(normalizedVideo, normalizedCandidate)
	hasArtist := normalizedArtist != "" && strings.Contains(normalizedVideo, normalizedArtist)

	if hasCandidatePhrase {
		score += 0.02
	}
	if hasArtist {
		score += 0.02
	}
	if hasCandidatePhrase && hasArtist {
		score += 0.03
	}

	if score > 1 {
		return 1
	}

	return score
}

func (m *Matcher) Classify(score float64) domain.MatchDecision {
	if score > 0.8 {
		return domain.MatchAuto
	}

	if score < 0.4 {
		return domain.MatchRejected
	}

	return domain.MatchPending
}

func tokenSet(input string) map[string]struct{} {
	tokens := strings.Fields(input)
	set := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		set[token] = struct{}{}
	}

	return set
}

func overlapRatio(videoTokens map[string]struct{}, candidateTokens map[string]struct{}) float64 {
	if len(candidateTokens) == 0 {
		return 0
	}

	common := 0
	for token := range candidateTokens {
		if _, ok := videoTokens[token]; ok {
			common++
		}
	}

	return float64(common) / float64(len(candidateTokens))
}

func f1Score(precision float64, recall float64) float64 {
	if precision <= 0 || recall <= 0 {
		return 0
	}

	return 2 * precision * recall / (precision + recall)
}

package tessa

import (
	"embed"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode"
)

//go:embed help/*.json
var helpFiles embed.FS

type HelpSection struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Tags    []string `json:"tags"`
	Content string   `json:"content"`
	RouteID string   `json:"route_id,omitempty"`
}

type HelpManifest struct {
	Revision string        `json:"revision"`
	Sections []HelpSection `json:"sections"`
}

type HelpMatch struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
	RouteID string `json:"route_id,omitempty"`
}

type HelpIndex struct {
	revision string
	sections []indexedHelpSection
}

type indexedHelpSection struct {
	section HelpSection
	terms   map[string]int
}

func LoadHelpIndex() (*HelpIndex, error) {
	payload, err := helpFiles.ReadFile("help/content.json")
	if err != nil {
		return nil, err
	}
	var manifest HelpManifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return nil, err
	}
	manifest.Revision = strings.TrimSpace(manifest.Revision)
	if manifest.Revision == "" || len(manifest.Sections) == 0 {
		return nil, errors.New("Tessa help manifest is empty")
	}
	index := &HelpIndex{revision: manifest.Revision, sections: make([]indexedHelpSection, 0, len(manifest.Sections))}
	seen := make(map[string]struct{}, len(manifest.Sections))
	for _, section := range manifest.Sections {
		section.ID = strings.TrimSpace(section.ID)
		section.Title = strings.TrimSpace(section.Title)
		section.Content = strings.TrimSpace(section.Content)
		section.RouteID = strings.TrimSpace(section.RouteID)
		if section.ID == "" || section.Title == "" || section.Content == "" || len(section.Content) > 1600 {
			return nil, errors.New("Tessa help section is invalid")
		}
		if _, duplicate := seen[section.ID]; duplicate {
			return nil, errors.New("Tessa help section ID is duplicated")
		}
		seen[section.ID] = struct{}{}
		terms := tokenize(section.Title + " " + strings.Join(section.Tags, " ") + " " + section.Content)
		index.sections = append(index.sections, indexedHelpSection{section: section, terms: termCounts(terms)})
	}
	return index, nil
}

func (index *HelpIndex) Revision() string {
	if index == nil {
		return ""
	}
	return index.revision
}

func (index *HelpIndex) Search(query string, limit int) []HelpMatch {
	if index == nil {
		return []HelpMatch{}
	}
	if limit < 1 || limit > 3 {
		limit = 3
	}
	queryTerms := tokenize(query)
	type scored struct {
		position int
		score    int
	}
	scores := make([]scored, 0, len(index.sections))
	for position, section := range index.sections {
		score := 0
		for _, term := range queryTerms {
			if count := section.terms[term]; count > 0 {
				score += 2 + min(count, 3)
			}
		}
		if score > 0 {
			scores = append(scores, scored{position: position, score: score})
		}
	}
	sort.SliceStable(scores, func(i, j int) bool {
		if scores[i].score != scores[j].score {
			return scores[i].score > scores[j].score
		}
		return index.sections[scores[i].position].section.ID < index.sections[scores[j].position].section.ID
	})
	if len(scores) > limit {
		scores = scores[:limit]
	}
	result := make([]HelpMatch, 0, len(scores))
	for _, match := range scores {
		section := index.sections[match.position].section
		result = append(result, HelpMatch{ID: section.ID, Title: section.Title, Content: section.Content, RouteID: section.RouteID})
	}
	return result
}

func tokenize(value string) []string {
	return strings.FieldsFunc(strings.ToLower(value), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
}

func termCounts(terms []string) map[string]int {
	counts := make(map[string]int, len(terms))
	for _, term := range terms {
		if len(term) >= 2 {
			counts[term]++
		}
	}
	return counts
}

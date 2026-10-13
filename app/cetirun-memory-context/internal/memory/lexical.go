// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package memory

import (
	"context"
	"math"
	"sort"
	"strings"
	"unicode"
)

// Text search ranks notes that share words with the query when no title,
// alias or keyword equals a query term. It reads only notes already in the
// bounded corpus and keeps nothing on disk, so a forgotten note leaves no
// searchable copy behind.

var stopWords = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an and are as at be been but by can could did do does for from had has have how i if in into is it its me my of on or our so than that the their them then there these they this those to us was we were what when where which who whom why will with would you your about any some just also very`) {
		stopWords[w] = true
	}
}

// words splits text into lower-case letter and digit runs, folds common
// English inflections, splits Han text into overlapping character pairs and
// drops function words.
func words(text string) []string {
	out := []string{}
	var run []rune
	flush := func() {
		if len(run) == 0 {
			return
		}
		if unicode.Is(unicode.Han, run[0]) {
			if len(run) == 1 {
				out = append(out, string(run))
			}
			for i := 0; i+1 < len(run); i++ {
				out = append(out, string(run[i:i+2]))
			}
		} else if w := string(run); !stopWords[w] && (len(run) > 1 || unicode.IsDigit(run[0])) {
			out = append(out, stem(w))
		}
		run = run[:0]
	}
	for _, r := range strings.ToLower(text) {
		switch {
		case unicode.Is(unicode.Han, r):
			if len(run) > 0 && !unicode.Is(unicode.Han, run[0]) {
				flush()
			}
			run = append(run, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if len(run) > 0 && unicode.Is(unicode.Han, run[0]) {
				flush()
			}
			run = append(run, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

type textHit struct {
	item    corpusNote
	score   float64
	matched []string
}

// Field weights make a word in a title or alias count more than the same word
// in the body.
var fieldWeights = []float64{3, 2.5, 2, 1}

func noteFields(n Note) [4][]string {
	var fields [4][]string
	fields[0] = words(n.Title)
	for _, alias := range n.Aliases {
		fields[1] = append(fields[1], words(alias)...)
	}
	for _, tag := range append(append([]string{}, n.Keywords...), n.Tags...) {
		fields[2] = append(fields[2], words(tag)...)
	}
	fields[3] = words(n.Body)
	return fields
}

// rankText scores in-scope notes with BM25 over weighted fields. A note must
// share at least one query word. Ties fall back to the exact-stage order:
// scope specificity, evidence recency, then ID. It stops when ctx ends.
func rankText(ctx context.Context, corpus []corpusNote, query []string, scope Scope) ([]textHit, int, error) {
	wanted := map[string]bool{}
	for _, term := range query {
		for _, w := range words(term) {
			wanted[w] = true
		}
	}
	if len(wanted) == 0 {
		return nil, 0, nil
	}
	type doc struct {
		item   corpusNote
		tf     map[string]float64
		length float64
	}
	docs := []doc{}
	df := map[string]int{}
	total, work := 0.0, 0
	for _, item := range corpus {
		if err := ctx.Err(); err != nil {
			return nil, work, err
		}
		if !scopeMatches(item.Note.Scope, scope) {
			continue
		}
		d := doc{item: item, tf: map[string]float64{}}
		for i, field := range noteFields(item.Note) {
			for _, w := range field {
				work++
				d.length += fieldWeights[i]
				if wanted[w] {
					d.tf[w] += fieldWeights[i]
				}
			}
		}
		for w := range d.tf {
			df[w]++
		}
		total += d.length
		docs = append(docs, d)
	}
	if len(docs) == 0 {
		return nil, work, nil
	}
	const k1, b = 1.2, 0.75
	average := total / float64(len(docs))
	if average == 0 {
		average = 1
	}
	hits := []textHit{}
	for _, d := range docs {
		if len(d.tf) == 0 {
			continue
		}
		hit := textHit{item: d.item}
		for w := range d.tf {
			hit.matched = append(hit.matched, w)
		}
		// Summing in a fixed word order gives notes with equal statistics equal
		// scores, so they reach the tie-break instead of differing by rounding.
		sort.Strings(hit.matched)
		for _, w := range hit.matched {
			tf := d.tf[w]
			idf := math.Log(1 + (float64(len(docs))-float64(df[w])+0.5)/(float64(df[w])+0.5))
			hit.score += idf * tf * (k1 + 1) / (tf + k1*(1-b+b*d.length/average))
		}
		hits = append(hits, hit)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		a, c := hits[i], hits[j]
		if a.score != c.score {
			return a.score > c.score
		}
		if len(a.item.Note.Scope) != len(c.item.Note.Scope) {
			return len(a.item.Note.Scope) > len(c.item.Note.Scope)
		}
		if evidenceTime(a.item.Note) != evidenceTime(c.item.Note) {
			return evidenceTime(a.item.Note) > evidenceTime(c.item.Note)
		}
		return a.item.Ref.ID < c.item.Ref.ID
	})
	return hits, work, nil
}

package tessa

import "testing"

func TestHelpSearchIsBoundedAndDeterministic(t *testing.T) {
	index, err := LoadHelpIndex()
	if err != nil {
		t.Fatal(err)
	}
	first := index.Search("availability business hours booking", 2)
	second := index.Search("availability business hours booking", 2)
	if len(first) == 0 || len(first) > 2 || len(second) != len(first) {
		t.Fatalf("unexpected search results: first=%+v second=%+v", first, second)
	}
	for position := range first {
		if first[position].ID != second[position].ID {
			t.Fatalf("search order changed: first=%+v second=%+v", first, second)
		}
	}
}

func TestHelpSearchDoesNotReturnUnrelatedContent(t *testing.T) {
	index, err := LoadHelpIndex()
	if err != nil {
		t.Fatal(err)
	}
	if matches := index.Search("quantum astrophysics", 3); len(matches) != 0 {
		t.Fatalf("unrelated query returned %+v", matches)
	}
}

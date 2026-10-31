package apcupsdexporter

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

var (
	catalogueRow  = regexp.MustCompile("(?m)^\\|\\s*`(apcupsd_[a-zA-Z0-9_]+)`\\s*\\|")
	catalogueName = regexp.MustCompile(`\bapcupsd_[a-zA-Z0-9_]+`)
)

func TestMetricsCatalogueMatchesDescribe(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("docs", "metrics.md"))
	if err != nil {
		t.Fatalf("failed to read catalogue: %v", err)
	}

	described := describedNames(t, New(nil))

	var rows []string
	for _, m := range catalogueRow.FindAllSubmatch(doc, -1) {
		rows = append(rows, string(m[1]))
	}

	for _, name := range described {
		if !slices.Contains(rows, name) {
			t.Errorf("%s is described but has no row in docs/metrics.md", name)
		}
	}

	for _, name := range catalogueName.FindAll(doc, -1) {
		if !slices.Contains(described, string(name)) {
			t.Errorf("docs/metrics.md names %s, which Exporter.Describe does not send", name)
		}
	}
}

package kube

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

type filterFixture struct {
	Rows []struct {
		ID         string            `json:"id"`
		Context    string            `json:"context"`
		Namespace  string            `json:"namespace"`
		Name       string            `json:"name"`
		Status     string            `json:"status"`
		Health     Health            `json:"health"`
		AgeSeconds int               `json:"ageSeconds"`
		Cells      map[string]string `json:"cells"`
		Labels     map[string]string `json:"labels"`
	} `json:"rows"`
	Cases []struct {
		Filter string   `json:"filter"`
		Match  []string `json:"match"`
	} `json:"cases"`
}

func TestFilterLanguage(t *testing.T) {
	data, err := os.ReadFile("testdata/filter_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx filterFixture
	if err := json.Unmarshal(data, &fx); err != nil {
		t.Fatal(err)
	}
	for _, c := range fx.Cases {
		f := ParseFilter(c.Filter)
		got := []string{}
		for _, r := range fx.Rows {
			row := Row{Namespace: r.Namespace, Name: r.Name, Status: r.Status, Health: r.Health,
				Cells: r.Cells, Labels: r.Labels, Created: time.Now().Add(-time.Duration(r.AgeSeconds) * time.Second)}
			if f.Match(r.Context, row) {
				got = append(got, r.ID)
			}
		}
		if !reflect.DeepEqual(got, c.Match) {
			t.Errorf("filter %q: got %v, want %v", c.Filter, got, c.Match)
		}
	}
}

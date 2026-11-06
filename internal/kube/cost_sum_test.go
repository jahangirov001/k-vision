package kube

import (
	"fmt"
	"testing"
)

func TestSumAllocs(t *testing.T) {
	d1 := map[string]ocAlloc{"a": {TotalCost: 2, CPUCost: 1, RAMCost: 1, CPUEfficiency: 0.5, RAMEfficiency: 1}}
	d2 := map[string]ocAlloc{"a": {TotalCost: 4, CPUCost: 3, RAMCost: 1, CPUEfficiency: 0.1, RAMEfficiency: 0.5}, "b": {TotalCost: 1}}
	s := sumAllocs([]map[string]ocAlloc{d1, d2})
	if s["a"].TotalCost != 6 || s["b"].TotalCost != 1 {
		t.Fatalf("%v", s)
	}
	if got := fmt.Sprintf("%.3f", s["a"].CPUEfficiency); got != "0.200" { // (0.5*1 + 0.1*3) / 4
		t.Errorf("weighted cpu efficiency %s", got)
	}
}

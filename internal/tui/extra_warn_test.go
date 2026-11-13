package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/njahangirov/kube-console/internal/kube"
)

func TestWarningsText(t *testing.T) {
	d := &kube.EventsDashboard{Warnings: 30, Objects: 3, ByNs: []kube.EventCount{{Name: "ci", Count: 25}},
		ByPool: []kube.EventCount{{Name: "gitlab-runner-spot", Count: 25}},
		Groups: []kube.EventGroup{{Reason: "FailedMount", Kind: "Pod", Count: 25, Objects: 2, Level: kube.Failing,
			Message: "driver name fsx.openzfs.csi.aws.com not found", Hint: "CSI driver not running yet",
			Namespaces: []string{"ci"}, Pools: []string{"gitlab-runner-spot"}, Last: time.Now().Add(-2 * time.Minute),
			Examples: []kube.EventObj{{Kind: "Pod", Namespace: "ci", Name: "runner-1"}}}}}
	out := warningsText(d)
	for _, want := range []string{"FailedMount", "×25", "pool gitlab-runner-spot", "ci/runner-1", "+1 more", "CSI driver"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(warningsText(&kube.EventsDashboard{}), "No warnings") {
		t.Fatal("empty dashboard should say no warnings")
	}
}

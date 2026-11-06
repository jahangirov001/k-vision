package kube

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/yaml"
)

// Helm 3 keeps each revision of a release in a Secret of type helm.sh/release.v1,
// named sh.helm.release.v1.<release>.v<revision>, labelled owner=helm. Its "release"
// key is base64 of gzipped JSON. Reading those is all `helm list` does, so Kube
// Console needs neither the helm binary nor Tiller-era permissions -- only read
// access to Secrets in the namespaces you can already see.

// HelmRelease is one release as the list shows it: its newest revision.
type HelmRelease struct {
	Name         string    `json:"name"`
	Namespace    string    `json:"namespace"`
	Revision     int       `json:"revision"`
	Status       string    `json:"status"`
	Chart        string    `json:"chart"`
	ChartVersion string    `json:"chartVersion"`
	AppVersion   string    `json:"appVersion"`
	Updated      time.Time `json:"updated"`
	Description  string    `json:"description,omitempty"`
	Health       Health    `json:"health"`
}

// HelmChart is a chart as it is deployed in this cluster: every release that uses
// it, and which versions are out there.
type HelmChart struct {
	Name     string   `json:"name"`
	Versions []string `json:"versions"`
	Releases []string `json:"releases"` // namespace/name
	Icon     string   `json:"icon,omitempty"`
	Desc     string   `json:"description,omitempty"`
}

// HelmDetail is one revision in full, for the release drawer.
type HelmDetail struct {
	HelmRelease
	Values      string        `json:"values"`    // the user-supplied values (helm get values)
	AllValues   string        `json:"allValues"` // with the chart's defaults merged
	Manifest    string        `json:"manifest"`  // helm get manifest
	Notes       string        `json:"notes"`
	History     []HelmRelease `json:"history"`
	ChartSource string        `json:"chartSource,omitempty"`
}

// The fields of Helm's release JSON that are read here.
type helmRaw struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Version   int    `json:"version"`
	Info      struct {
		Status        string    `json:"status"`
		Description   string    `json:"description"`
		LastDeployed  time.Time `json:"last_deployed"`
		FirstDeployed time.Time `json:"first_deployed"`
		Notes         string    `json:"notes"`
	} `json:"info"`
	Chart struct {
		Metadata struct {
			Name        string   `json:"name"`
			Version     string   `json:"version"`
			AppVersion  string   `json:"appVersion"`
			Description string   `json:"description"`
			Icon        string   `json:"icon"`
			Home        string   `json:"home"`
			Sources     []string `json:"sources"`
		} `json:"metadata"`
		Values map[string]any `json:"values"`
	} `json:"chart"`
	Config   map[string]any `json:"config"`
	Manifest string         `json:"manifest"`
}

func decodeHelm(data []byte) (*helmRaw, error) {
	// The Secret's value is base64 of base64-of-gzip: client-go undoes the outer
	// layer, Helm's own encoding is the inner one.
	b, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return nil, err
	}
	if len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		if b, err = io.ReadAll(io.LimitReader(zr, 64<<20)); err != nil {
			return nil, err
		}
	}
	var r helmRaw
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func (r *helmRaw) summary() HelmRelease {
	h := HelmRelease{
		Name: r.Name, Namespace: r.Namespace, Revision: r.Version, Status: r.Info.Status,
		Chart: r.Chart.Metadata.Name, ChartVersion: r.Chart.Metadata.Version, AppVersion: r.Chart.Metadata.AppVersion,
		Updated: r.Info.LastDeployed, Description: r.Info.Description,
	}
	switch r.Info.Status {
	case "deployed":
		h.Health = Healthy
	case "failed":
		h.Health = Failing
	case "pending-install", "pending-upgrade", "pending-rollback", "uninstalling":
		h.Health = Warning
	}
	return h
}

// helmSecrets lists the release Secrets, newest revision first per release.
func (c *Cluster) helmSecrets(ctx context.Context, namespace, release string) ([]*helmRaw, error) {
	sel := "owner=helm"
	if release != "" {
		sel += ",name=" + release
	}
	list, err := c.Typed.CoreV1().Secrets(namespace).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return nil, err
	}
	var out []*helmRaw
	for _, s := range list.Items {
		if s.Type != "helm.sh/release.v1" {
			continue
		}
		r, err := decodeHelm(s.Data["release"])
		if err != nil {
			continue // one corrupt revision must not hide the rest
		}
		if r.Namespace == "" {
			r.Namespace = s.Namespace
		}
		if r.Version == 0 {
			r.Version, _ = strconv.Atoi(s.Labels["version"])
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Namespace != out[j].Namespace {
			return out[i].Namespace < out[j].Namespace
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].Version > out[j].Version
	})
	return out, nil
}

// HelmReleases is `helm list -A`: the newest revision of every release.
func (c *Cluster) HelmReleases(ctx context.Context, namespace string) ([]HelmRelease, error) {
	raws, err := c.helmSecrets(ctx, namespace, "")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := []HelmRelease{}
	for _, r := range raws {
		k := r.Namespace + "/" + r.Name
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r.summary())
	}
	return out, nil
}

// HelmCharts groups the releases by chart: which charts are deployed in this
// cluster, in which versions, by which releases.
func (c *Cluster) HelmCharts(ctx context.Context, namespace string) ([]HelmChart, error) {
	raws, err := c.helmSecrets(ctx, namespace, "")
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	byName := map[string]*HelmChart{}
	for _, r := range raws {
		k := r.Namespace + "/" + r.Name
		if seen[k] {
			continue // only the release's current revision counts
		}
		seen[k] = true
		md := r.Chart.Metadata
		ch := byName[md.Name]
		if ch == nil {
			ch = &HelmChart{Name: md.Name, Icon: md.Icon, Desc: md.Description}
			byName[md.Name] = ch
		}
		ch.Releases = append(ch.Releases, k)
		found := false
		for _, v := range ch.Versions {
			found = found || v == md.Version
		}
		if !found {
			ch.Versions = append(ch.Versions, md.Version)
		}
	}
	out := make([]HelmChart, 0, len(byName))
	for _, ch := range byName {
		sort.Strings(ch.Versions)
		out = append(out, *ch)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// HelmRelease returns one release in full: values, manifest, notes and its history.
// revision 0 means the newest.
func (c *Cluster) HelmRelease(ctx context.Context, namespace, name string, revision int) (*HelmDetail, error) {
	raws, err := c.helmSecrets(ctx, namespace, name)
	if err != nil {
		return nil, err
	}
	var pick *helmRaw
	d := &HelmDetail{}
	for _, r := range raws {
		if r.Name != name || r.Namespace != namespace {
			continue
		}
		d.History = append(d.History, r.summary())
		if pick == nil && (revision == 0 || r.Version == revision) {
			pick = r
		}
	}
	if pick == nil {
		return nil, fmt.Errorf("no Helm release %s/%s (revision %d)", namespace, name, revision)
	}
	d.HelmRelease = pick.summary()
	d.Values = toYAML(pick.Config)
	d.AllValues = toYAML(merge(pick.Chart.Values, pick.Config))
	d.Manifest = pick.Manifest
	d.Notes = pick.Info.Notes
	if len(pick.Chart.Metadata.Sources) > 0 {
		d.ChartSource = pick.Chart.Metadata.Sources[0]
	} else {
		d.ChartSource = pick.Chart.Metadata.Home
	}
	return d, nil
}

func toYAML(v map[string]any) string {
	if len(v) == 0 {
		return ""
	}
	b, err := yaml.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

// merge is Helm's values coalescing, simplified: user values win, maps merge deeply.
func merge(base, over map[string]any) map[string]any {
	out := make(map[string]any, len(base))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range over {
		if bm, ok := out[k].(map[string]any); ok {
			if om, ok := v.(map[string]any); ok {
				out[k] = merge(bm, om)
				continue
			}
		}
		out[k] = v
	}
	return out
}

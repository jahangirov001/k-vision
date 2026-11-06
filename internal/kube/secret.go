package kube

import (
	"context"
	"sort"
	"strings"
	"unicode/utf8"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SecretEntry is one decoded key of a Secret.
type SecretEntry struct {
	Key    string `json:"key"`
	Value  string `json:"value"`  // decoded; "" for binary values
	Binary bool   `json:"binary"` // not valid UTF-8 text (a keystore, a .p12)
	Size   int    `json:"size"`   // bytes
}

// SecretData decodes a Secret's data. The API already returns it base64-decoded in
// the typed client, which is why this uses that and not the dynamic one (whose YAML
// is the base64 the user could not read).
//
// Reading values is recorded in the audit log -- key names only, never values -- so
// "who looked at the prod database password" has an answer.
func (c *Cluster) SecretData(ctx context.Context, namespace, name string) (out []SecretEntry, err error) {
	a := Audit{Context: c.Context, Action: "reveal-secret", Kind: "secrets", Namespace: namespace, Name: name}
	defer func() {
		keys := make([]string, 0, len(out))
		for _, e := range out {
			keys = append(keys, e.Key)
		}
		a.Detail = "keys=" + strings.Join(keys, ",")
		record(a, err)
	}()
	s, err := c.Typed.CoreV1().Secrets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	for k, v := range s.Data {
		e := SecretEntry{Key: k, Size: len(v)}
		if utf8.Valid(v) {
			e.Value = string(v)
		} else {
			e.Binary = true
		}
		out = append(out, e)
	}
	// stringData is write-only and never returned, so Data is the whole secret.
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// ConfigMapData returns a ConfigMap's keys. Not audited: ConfigMaps hold
// configuration, not credentials, and their YAML already shows the same text.
func (c *Cluster) ConfigMapData(ctx context.Context, namespace, name string) ([]SecretEntry, error) {
	cm, err := c.Typed.CoreV1().ConfigMaps(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	out := make([]SecretEntry, 0, len(cm.Data)+len(cm.BinaryData))
	for k, v := range cm.Data {
		out = append(out, SecretEntry{Key: k, Value: v, Size: len(v)})
	}
	for k, v := range cm.BinaryData {
		out = append(out, SecretEntry{Key: k, Binary: true, Size: len(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

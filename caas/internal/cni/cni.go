/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License").
*/

// Package cni installs a CNI into a freshly-provisioned tenant cluster so its
// nodes can reach Ready. The Cilium manifest is rendered once from the chart
// (helm template cilium/cilium 1.19.1, ipam=cluster-pool, pod CIDR 10.244.0.0/16 —
// the constant tenant pod CIDR) and embedded; it is applied with server-side
// apply, so re-applying is idempotent.
package cni

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//go:embed cilium.yaml
var ciliumManifest []byte

const cniFieldOwner = client.FieldOwner("caas-cni")

// InstallCilium applies the embedded Cilium manifest to the tenant cluster reachable
// via restCfg.
func InstallCilium(ctx context.Context, restCfg *rest.Config) error {
	objs, err := parseManifest(ciliumManifest)
	if err != nil {
		return fmt.Errorf("parse cilium manifest: %w", err)
	}
	c, err := client.New(restCfg, client.Options{})
	if err != nil {
		return fmt.Errorf("tenant client: %w", err)
	}
	for _, o := range objs {
		if err := c.Patch(ctx, o, client.Apply, cniFieldOwner, client.ForceOwnership); err != nil {
			return fmt.Errorf("apply %s/%s: %w", o.GetKind(), o.GetName(), err)
		}
	}
	return nil
}

func parseManifest(manifest []byte) ([]*unstructured.Unstructured, error) {
	dec := utilyaml.NewYAMLOrJSONDecoder(bytes.NewReader(manifest), 4096)
	var out []*unstructured.Unstructured
	for {
		raw := map[string]interface{}{}
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if len(raw) == 0 {
			continue
		}
		out = append(out, &unstructured.Unstructured{Object: raw})
	}
	return out, nil
}

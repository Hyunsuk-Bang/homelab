/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License").
*/

package network

import (
	"context"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Noop is the default Provider when no network controller is configured. It does
// not touch any network — it just logs the intent and reports Ready, so the
// network must be provisioned out-of-band (as we did by hand on UniFi). This keeps
// dev/unconfigured environments working and makes the provider boundary explicit.
type Noop struct{}

func (Noop) Name() string { return "noop" }

func (Noop) EnsureNetwork(ctx context.Context, spec Spec) (Status, error) {
	logf.FromContext(ctx).Info("noop network provider: skipping (provision out-of-band)",
		"cluster", spec.ClusterID, "vlan", spec.VLAN, "subnet", spec.Subnet, "gateway", spec.Gateway)
	return Status{Ready: true, Message: "noop provider: network assumed provisioned out-of-band"}, nil
}

func (Noop) DeleteNetwork(ctx context.Context, ref Ref) error {
	logf.FromContext(ctx).Info("noop network provider: skipping delete", "cluster", ref.ClusterID, "vlan", ref.VLAN)
	return nil
}

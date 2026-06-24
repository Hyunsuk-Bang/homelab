/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License").
*/

package network

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// UnifiConfig is the constructor input for the UniFi provider. Secrets come from
// the operator's environment/Secret, never from the Provider interface.
type UnifiConfig struct {
	URL    string // e.g. https://192.168.20.1
	APIKey string // X-API-KEY
	Site   string // e.g. default
	// InsecureSkipVerify accepts the controller's self-signed cert (typical for a
	// homelab UDM).
	InsecureSkipVerify bool
}

// Unifi realizes tenant networks on a UniFi controller via the legacy networkconf
// REST API (authenticated with an API key). It currently provisions the VLAN
// network only; firewall/isolation is a TODO (see EnsureNetwork) — the controller's
// integration API exposes no firewall endpoints, so isolation will be added later
// against the zone-based or legacy firewall API.
type Unifi struct {
	cfg  UnifiConfig
	http *http.Client
}

// NewUnifi builds a UniFi provider.
func NewUnifi(cfg UnifiConfig) *Unifi {
	return &Unifi{
		cfg: cfg,
		http: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify}, //nolint:gosec // homelab self-signed
			},
		},
	}
}

func (u *Unifi) Name() string { return "unifi" }

// networkName is the deterministic, canonical name for a tenant's network.
func networkName(vlan int) string { return fmt.Sprintf("caas-vlan-%d", vlan) }

// ownerPrefix marks networks the operator may manage. Any "caas"-named network on
// a managed VLAN is adopted (and renamed to the canonical form), so manually
// pre-created networks like "caas30" convert cleanly. Networks without this prefix
// are never touched.
const ownerPrefix = "caas"

// unifiNetwork is the subset of the networkconf object we read/write.
type unifiNetwork struct {
	ID           string `json:"_id,omitempty"`
	Name         string `json:"name"`
	Purpose      string `json:"purpose"`
	Networkgroup string `json:"networkgroup"`
	VLANEnabled  bool   `json:"vlan_enabled"`
	VLAN         int    `json:"vlan"`
	IPSubnet     string `json:"ip_subnet"` // "<gateway>/<prefix>"
	DHCPEnabled  bool   `json:"dhcpd_enabled"`
}

// EnsureNetwork converges the tenant VLAN network to spec (create or update).
func (u *Unifi) EnsureNetwork(ctx context.Context, spec Spec) (Status, error) {
	log := logf.FromContext(ctx)
	if len(spec.Isolation.AllowInternal) > 0 || spec.Isolation.AllowInternet {
		// Firewall not implemented yet; make the gap explicit rather than silently
		// pretending the tenant is isolated.
		log.Info("unifi: isolation/firewall not yet implemented — VLAN provides L2 separation only",
			"cluster", spec.ClusterID, "vlan", spec.VLAN)
	}

	prefix, err := prefixOf(spec.Subnet)
	if err != nil {
		return Status{}, err
	}
	desired := unifiNetwork{
		Name:         networkName(spec.VLAN),
		Purpose:      "corporate",
		Networkgroup: "LAN",
		VLANEnabled:  true,
		VLAN:         spec.VLAN,
		IPSubnet:     fmt.Sprintf("%s/%d", spec.Gateway, prefix),
		DHCPEnabled:  false, // capmox assigns static IPs in ManagedRange
	}

	existing, err := u.findByVLAN(ctx, spec.VLAN)
	if err != nil {
		return Status{}, err
	}
	if existing != nil && !strings.HasPrefix(existing.Name, ownerPrefix) {
		return Status{}, fmt.Errorf("VLAN %d already used by unmanaged UniFi network %q", spec.VLAN, existing.Name)
	}

	if existing == nil {
		created, err := u.do(ctx, http.MethodPost, "/rest/networkconf", desired)
		if err != nil {
			return Status{}, fmt.Errorf("create network: %w", err)
		}
		log.Info("unifi: created network", "vlan", spec.VLAN, "name", desired.Name)
		return Status{Ready: true, Message: "network created (no firewall)", ResourceRefs: map[string]string{"networkId": created}}, nil
	}

	desired.ID = existing.ID
	if *existing != desired {
		if _, err := u.do(ctx, http.MethodPut, "/rest/networkconf/"+existing.ID, desired); err != nil {
			return Status{}, fmt.Errorf("update network: %w", err)
		}
		log.Info("unifi: updated network", "vlan", spec.VLAN, "name", desired.Name)
	}
	return Status{Ready: true, Message: "network present (no firewall)", ResourceRefs: map[string]string{"networkId": existing.ID}}, nil
}

// DeleteNetwork removes the tenant's VLAN network (only if it's one we created).
func (u *Unifi) DeleteNetwork(ctx context.Context, ref Ref) error {
	existing, err := u.findByVLAN(ctx, ref.VLAN)
	if err != nil {
		return err
	}
	if existing == nil || !strings.HasPrefix(existing.Name, ownerPrefix) {
		return nil // nothing of ours to delete
	}
	if _, err := u.do(ctx, http.MethodDelete, "/rest/networkconf/"+existing.ID, nil); err != nil {
		return fmt.Errorf("delete network: %w", err)
	}
	logf.FromContext(ctx).Info("unifi: deleted network", "vlan", ref.VLAN, "name", existing.Name)
	return nil
}

// findByVLAN returns the networkconf with the given VLAN id, or nil if none.
func (u *Unifi) findByVLAN(ctx context.Context, vlan int) (*unifiNetwork, error) {
	body, err := u.get(ctx, "/rest/networkconf")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data []unifiNetwork `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode networkconf: %w", err)
	}
	for i := range resp.Data {
		if resp.Data[i].VLANEnabled && resp.Data[i].VLAN == vlan {
			return &resp.Data[i], nil
		}
	}
	return nil, nil
}

func (u *Unifi) get(ctx context.Context, path string) ([]byte, error) {
	return u.request(ctx, http.MethodGet, path, nil)
}

// do issues a write and returns the created/updated object's _id when present.
func (u *Unifi) do(ctx context.Context, method, path string, payload any) (string, error) {
	body, err := u.request(ctx, method, path, payload)
	if err != nil {
		return "", err
	}
	var resp struct {
		Data []struct {
			ID string `json:"_id"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &resp)
	if len(resp.Data) > 0 {
		return resp.Data[0].ID, nil
	}
	return "", nil
}

func (u *Unifi) request(ctx context.Context, method, path string, payload any) ([]byte, error) {
	url := fmt.Sprintf("%s/proxy/network/api/s/%s%s", strings.TrimRight(u.cfg.URL, "/"), u.cfg.Site, path)
	var rdr *bytes.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-KEY", u.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := u.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("unifi %s %s: HTTP %d: %s", method, path, resp.StatusCode, truncate(buf.String(), 300))
	}
	return buf.Bytes(), nil
}

func prefixOf(cidr string) (int, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return 0, fmt.Errorf("invalid subnet %q: %w", cidr, err)
	}
	ones, _ := ipnet.Mask.Size()
	return ones, nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

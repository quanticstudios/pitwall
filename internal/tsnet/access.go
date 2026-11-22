// Package tsnet runs an in-process userspace Tailscale node (tailscale.com/tsnet)
// for pitwall's phone page, keeping its state isolated inside pitwall's state
// directory so it never disturbs any system or userspace tailscaled already on
// the machine.
package tsnet

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"tailscale.com/client/tailscale/apitype"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

// Peer is the Tailscale identity of a connecting client, extracted from WhoIs.
type Peer struct {
	NodeID      string   // stable node ID, used for deterministic device IDs
	NodeName    string   // short device name such as "iphone"
	LoginName   string   // tailnet user login such as "alice@example.com" (empty for tagged nodes)
	DisplayName string   // human display name when available
	Tags        []string // ACL tags such as "tag:phones"
	IsTagged    bool     // true when the node is tagged rather than user-owned
}

// PeerFromWhoIs converts a Tailscale LocalAPI WhoIs response into a Peer.
func PeerFromWhoIs(who *apitype.WhoIsResponse) Peer {
	if who == nil {
		return Peer{}
	}
	var p Peer
	if who.Node != nil {
		p.NodeID = string(who.Node.StableID)
		if p.NodeID == "" && who.Node.ID != 0 {
			p.NodeID = fmt.Sprintf("node-%d", who.Node.ID)
		}
		p.NodeName = nodeShortName(who.Node)
		if len(who.Node.Tags) > 0 {
			p.IsTagged = true
			p.Tags = make([]string, len(who.Node.Tags))
			for i, t := range who.Node.Tags {
				p.Tags[i] = strings.ToLower(strings.TrimSpace(t))
			}
		}
	}
	if who.UserProfile != nil && !p.IsTagged {
		login := strings.TrimSpace(who.UserProfile.LoginName)
		if !strings.EqualFold(login, "tagged-devices") {
			p.LoginName = strings.ToLower(login)
			p.DisplayName = strings.TrimSpace(who.UserProfile.DisplayName)
		}
	}
	return p
}

func nodeShortName(n *tailcfg.Node) string {
	if n == nil {
		return ""
	}
	if n.ComputedName != "" {
		return n.ComputedName
	}
	if n.Name != "" {
		if label, _, ok := strings.Cut(strings.TrimSuffix(n.Name, "."), "."); ok {
			return label
		}
		return strings.TrimSuffix(n.Name, ".")
	}
	if n.Hostinfo.Valid() {
		return n.Hostinfo.Hostname()
	}
	return ""
}

// SelfUserFromStatus extracts the login name of the tailnet user who logged
// pitwall in. Tagged pitwall nodes return "" so "tagged-devices" never acts
// as an implicit wildcard user.
func SelfUserFromStatus(st *ipnstate.Status) string {
	if st == nil || st.Self == nil {
		return ""
	}
	if st.Self.Tags != nil && st.Self.Tags.Len() > 0 {
		return ""
	}
	u, ok := st.User[st.Self.UserID]
	if !ok {
		return ""
	}
	login := strings.ToLower(strings.TrimSpace(u.LoginName))
	if login == "" || login == "tagged-devices" {
		return ""
	}
	return login
}

// DeviceID returns a stable, filename-safe ID for p ("ts-<16hex>") that never
// collides with paired-device 16-hex IDs.
func (p Peer) DeviceID() string {
	key := p.NodeID
	if key == "" {
		key = p.LoginName + "|" + p.NodeName + "|" + strings.Join(p.Tags, ",")
	}
	sum := sha256.Sum256([]byte(key))
	return "ts-" + hex.EncodeToString(sum[:8])
}

// DeviceName returns a human-readable label for logs and the phone header.
func (p Peer) DeviceName() string {
	switch {
	case p.IsTagged:
		tags := strings.Join(p.Tags, ", ")
		if p.NodeName != "" && tags != "" {
			return fmt.Sprintf("%s (%s)", p.NodeName, tags)
		}
		if tags != "" {
			return tags
		}
		return p.NodeName
	case p.LoginName != "":
		if p.NodeName != "" {
			return fmt.Sprintf("%s (%s)", p.LoginName, p.NodeName)
		}
		return p.LoginName
	case p.NodeName != "":
		return p.NodeName
	default:
		return "tailscale peer"
	}
}

// Policy decides which Tailscale peers may access the phone page.
type Policy struct {
	SelfUser   string   // tailnet user who logged pitwall in (empty if pitwall is tagged)
	AllowUsers []string // additional allowed Tailscale user logins
	AllowTags  []string // allowed Tailscale node tags (such as "tag:phones")
}

// Check reports whether p is allowed by pol. When denied, reason explains why
// and which config key grants access.
func (pol Policy) Check(p Peer) (allowed bool, reason string) {
	if p.IsTagged || len(p.Tags) > 0 {
		for _, tag := range p.Tags {
			nt := strings.ToLower(strings.TrimSpace(tag))
			for _, want := range pol.AllowTags {
				if nt != "" && nt == strings.ToLower(strings.TrimSpace(want)) {
					return true, ""
				}
			}
		}
		if len(p.Tags) == 0 {
			return false, "tagged Tailscale nodes are not allowed; add their tag to [remote.tailscale] allow_tags"
		}
		return false, fmt.Sprintf("Tailscale node tags %s are not allowed; add to [remote.tailscale] allow_tags", strings.Join(p.Tags, ", "))
	}

	login := strings.ToLower(strings.TrimSpace(p.LoginName))
	if login == "" {
		return false, "could not identify the Tailscale user for this connection"
	}
	if self := strings.ToLower(strings.TrimSpace(pol.SelfUser)); self != "" && login == self {
		return true, ""
	}
	if slices.ContainsFunc(pol.AllowUsers, func(u string) bool { return strings.EqualFold(strings.TrimSpace(u), login) }) {
		return true, ""
	}
	if strings.TrimSpace(pol.SelfUser) == "" && len(pol.AllowUsers) == 0 {
		return false, fmt.Sprintf("Tailscale user %q is not allowed; this pitwall node has no owner user, so add %q to [remote.tailscale] allow_users", login, login)
	}
	return false, fmt.Sprintf("Tailscale user %q is not allowed; add it to [remote.tailscale] allow_users", login)
}

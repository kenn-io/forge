// Package fleet provides deterministic identity for fleet entities.
// Public IDs are RFC 4122 UUID v5 derived from a caller-chosen namespace.
package fleet

import (
	"crypto/sha1"
	"fmt"
)

// namespaceBytes is the default fixed UUID namespace (big-endian) for
// fleet identity. Embedders that do not supply their own namespace get
// this one; its bytes are wire-visible and must never change.
var namespaceBytes = [16]byte{
	0x18, 0x57, 0xd8, 0x13, 0x0d, 0x42, 0x45, 0xc2,
	0xaa, 0xd4, 0xc2, 0x76, 0x2b, 0x9d, 0x55, 0xe9,
}

// Identity derives deterministic RFC 4122 UUID v5 entity IDs from
// the fleet namespace; construct with DefaultIdentity.
type Identity struct {
	namespace [16]byte
}

// DefaultIdentity returns the Identity using fleet's default namespace.
func DefaultIdentity() Identity {
	return Identity{namespace: namespaceBytes}
}

// UUID derives a UUID v5 from name using this Identity's namespace.
func (id Identity) UUID(name string) string {
	h := sha1.New()
	h.Write(id.namespace[:])
	h.Write([]byte(name))
	digest := h.Sum(nil)

	// Truncate to 16 bytes, set version and variant bits.
	var raw [16]byte
	copy(raw[:], digest[:16])
	raw[6] = (raw[6] & 0x0F) | 0x50 // version 5
	raw[8] = (raw[8] & 0x3F) | 0x80 // RFC 4122 variant

	return fmt.Sprintf(
		"%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
		raw[0], raw[1], raw[2], raw[3],
		raw[4], raw[5],
		raw[6], raw[7],
		raw[8], raw[9],
		raw[10], raw[11], raw[12], raw[13], raw[14], raw[15],
	)
}

// HostID returns a stable UUID v5 for a host identified by hostKey.
func (id Identity) HostID(hostKey string) string {
	return id.UUID("host:" + hostKey)
}

// EntityID returns a stable UUID v5 for an entity scoped to a host and
// an entity-specific key.
func (id Identity) EntityID(hostKey, scopedKey string) string {
	return id.UUID(hostKey + "\x00" + scopedKey)
}

// HostID returns a stable host UUID using the default namespace. Retained
// for callers that do not thread an Identity (hub adapter, merge, tests).
func HostID(hostKey string) string {
	return DefaultIdentity().HostID(hostKey)
}

// EntityID returns a stable entity UUID using the default namespace.
func EntityID(hostKey, scopedKey string) string {
	return DefaultIdentity().EntityID(hostKey, scopedKey)
}

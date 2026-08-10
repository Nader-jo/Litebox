// Package ids provides dependency-free UUIDv4 identifiers.
package ids

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"
)

// New returns a random RFC 9562 UUID version 4 string.
func New() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(fmt.Errorf("generate random identifier: %w", err))
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16])
}

// Stable returns a UUID-shaped deterministic identifier for idempotent external resources.
func Stable(namespace, value string) string {
	sum := sha256.Sum256([]byte(namespace + "\x00" + value))
	sum[6] = (sum[6] & 0x0f) | 0x50
	sum[8] = (sum[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", sum[0:4], sum[4:6], sum[6:8], sum[8:10], sum[10:16])
}

package address

import (
	"github.com/go-i2p/i2p-vanitygen/internal/destination"
)

// I2PScheme implements Scheme for I2P .b32.i2p addresses.
type I2PScheme struct{}

func (I2PScheme) Network() Network                        { return NetworkI2P }
func (I2PScheme) Suffix() string                          { return ".b32.i2p" }
func (I2PScheme) ValidatePrefix(prefix string) error      { return destination.ValidatePrefix(prefix) }
func (I2PScheme) EstimateAttempts(n int) float64           { return destination.EstimateAttempts(n) }
func (I2PScheme) MaxPrefixLen() int                        { return 52 }
func (I2PScheme) SupportsGPU() bool                        { return true }

func (I2PScheme) NewCandidate() (Candidate, error) {
	d, err := destination.NewRandom()
	if err != nil {
		return nil, err
	}
	return &I2PCandidate{Dest: d}, nil
}

// I2PCandidate wraps a destination.Destination to implement Candidate.
type I2PCandidate struct {
	Dest *destination.Destination
}

func (c *I2PCandidate) Address() string     { return c.Dest.B32Address() }
func (c *I2PCandidate) FullAddress() string { return c.Dest.FullB32Address() }
func (c *I2PCandidate) SaveKeys(path string) error { return c.Dest.SaveKeys(path) }

// MutateAndCheck mutates the encryption key with the given counter and checks the prefix.
func (c *I2PCandidate) MutateAndCheck(counter uint64, prefix string) bool {
	c.Dest.MutateEncryptionKey(counter)
	return c.Dest.HasPrefix(prefix)
}

// MutateAndCheckAny mutates the encryption key and checks against multiple prefixes.
func (c *I2PCandidate) MutateAndCheckAny(counter uint64, prefixes []string) bool {
	c.Dest.MutateEncryptionKey(counter)
	for _, p := range prefixes {
		if c.Dest.HasPrefix(p) {
			return true
		}
	}
	return false
}

// Raw returns the raw destination bytes (needed for GPU worker template).
func (c *I2PCandidate) Raw() [destination.DestinationSize]byte {
	return c.Dest.Raw
}

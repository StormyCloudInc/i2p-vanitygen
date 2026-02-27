package address

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha512"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/edwards25519"
	"golang.org/x/crypto/sha3"
)

var onionEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// TorV3Scheme implements Scheme for Tor v3 .onion addresses.
type TorV3Scheme struct{}

func (TorV3Scheme) Network() Network   { return NetworkTorV3 }
func (TorV3Scheme) Suffix() string     { return ".onion" }
func (TorV3Scheme) MaxPrefixLen() int  { return 56 }
func (TorV3Scheme) SupportsGPU() bool  { return true }

func (TorV3Scheme) ValidatePrefix(prefix string) error {
	if len(prefix) == 0 {
		return fmt.Errorf("prefix cannot be empty")
	}
	if len(prefix) > 56 {
		return fmt.Errorf("prefix cannot exceed 56 characters")
	}
	prefix = strings.ToLower(prefix)
	for i, c := range prefix {
		if !((c >= 'a' && c <= 'z') || (c >= '2' && c <= '7')) {
			return fmt.Errorf("invalid character '%c' at position %d (allowed: a-z, 2-7)", c, i)
		}
	}
	return nil
}

func (TorV3Scheme) EstimateAttempts(prefixLen int) float64 {
	if prefixLen <= 0 {
		return 1
	}
	attempts := 1.0
	for i := 0; i < prefixLen; i++ {
		attempts *= 32
	}
	return attempts / 2
}

func (TorV3Scheme) NewCandidate() (Candidate, error) {
	return NewTorV3Candidate()
}

// TorV3Candidate holds Ed25519 key material for Tor v3 vanity generation.
// It supports fast iteration via scalar/point addition on the curve.
type TorV3Candidate struct {
	// Base key material (from initial keygen)
	seed        [32]byte // original Ed25519 seed
	hashSuffix  [32]byte // second half of SHA-512(seed), needed for Tor key file

	// Current derived key (updated each iteration)
	scalar *edwards25519.Scalar // current private scalar
	point  *edwards25519.Point  // current public key point
	counter uint64

	// Precomputed
	oneScalar *edwards25519.Scalar // scalar = 1
	genPoint  *edwards25519.Point  // generator point G
}

// NewTorV3Candidate creates a new candidate with a random Ed25519 keypair.
func NewTorV3Candidate() (*TorV3Candidate, error) {
	// Generate random seed
	var seed [32]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, fmt.Errorf("generating random seed: %w", err)
	}

	// Derive Ed25519 scalar via SHA-512 + clamping (RFC 8032)
	h := sha512.Sum512(seed[:])

	scalar, err := edwards25519.NewScalar().SetBytesWithClamping(h[:32])
	if err != nil {
		return nil, fmt.Errorf("deriving scalar: %w", err)
	}

	// Compute public key point = scalar * G
	point := new(edwards25519.Point).ScalarBaseMult(scalar)

	// Precompute scalar(1) and generator point G
	var oneBuf [32]byte
	oneBuf[0] = 1
	oneScalar, _ := edwards25519.NewScalar().SetCanonicalBytes(oneBuf[:])

	c := &TorV3Candidate{
		seed:      seed,
		scalar:    scalar,
		point:     point,
		oneScalar: oneScalar,
		genPoint:  edwards25519.NewGeneratorPoint(),
	}
	copy(c.hashSuffix[:], h[32:])

	return c, nil
}

// Address returns the 56-character base32 onion address (without .onion suffix).
func (c *TorV3Candidate) Address() string {
	pubBytes := c.point.Bytes()

	// Build the 35-byte onion address payload:
	// pubkey (32) | checksum (2) | version (1)
	var payload [35]byte
	copy(payload[:32], pubBytes)

	// Checksum = SHA3-256(".onion checksum" | pubkey | version)[:2]
	checksum := torV3Checksum(pubBytes)
	payload[32] = checksum[0]
	payload[33] = checksum[1]
	payload[34] = 0x03 // version

	return strings.ToLower(onionEncoding.EncodeToString(payload[:]))
}

// FullAddress returns the complete .onion address.
func (c *TorV3Candidate) FullAddress() string {
	return c.Address() + ".onion"
}

// Advance increments the key by 1 (adds G to the point and 1 to the scalar).
func (c *TorV3Candidate) Advance() {
	c.point.Add(c.point, c.genPoint)
	c.scalar.Add(c.scalar, c.oneScalar)
	c.counter++
}

// AdvanceBy jumps the key forward by n steps.
func (c *TorV3Candidate) AdvanceBy(n uint64) {
	var buf [32]byte
	binary.LittleEndian.PutUint64(buf[:8], n)
	nScalar, _ := edwards25519.NewScalar().SetCanonicalBytes(buf[:])

	nG := new(edwards25519.Point).ScalarBaseMult(nScalar)
	c.point.Add(c.point, nG)
	c.scalar.Add(c.scalar, nScalar)
	c.counter += n
}

// CheckPrefix checks whether the current address starts with the given prefix.
func (c *TorV3Candidate) CheckPrefix(prefix string) bool {
	return strings.HasPrefix(c.Address(), prefix)
}

// CheckPrefixFast checks the prefix without computing the SHA3-256 checksum.
// For prefixes up to 51 characters, the checksum bytes (at positions 32-33
// in the 35-byte payload) cannot affect the base32-encoded prefix, so the
// expensive SHA3-256 can be skipped entirely.
func (c *TorV3Candidate) CheckPrefixFast(prefix string) bool {
	if len(prefix) <= 51 {
		pubBytes := c.point.Bytes()
		needed := (len(prefix)*5 + 7) / 8
		if needed > 32 {
			needed = 32
		}
		partial := strings.ToLower(onionEncoding.EncodeToString(pubBytes[:needed]))
		return strings.HasPrefix(partial, prefix)
	}
	return strings.HasPrefix(c.Address(), prefix)
}

// SaveKeys writes the Tor hidden service key files to a directory.
// Creates: hs_ed25519_secret_key, hs_ed25519_public_key, hostname
func (c *TorV3Candidate) SaveKeys(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	pubBytes := c.point.Bytes()

	// hs_ed25519_secret_key: 32-byte header + 64-byte expanded key
	// The expanded key is: clamped scalar (32) + nonce hash suffix (32)
	secretHeader := []byte("== ed25519v1-secret: type0 ==\x00\x00\x00")
	secretKey := make([]byte, 0, len(secretHeader)+64)
	secretKey = append(secretKey, secretHeader...)
	secretKey = append(secretKey, c.scalar.Bytes()...)
	secretKey = append(secretKey, c.hashSuffix[:]...)

	if err := os.WriteFile(filepath.Join(dir, "hs_ed25519_secret_key"), secretKey, 0600); err != nil {
		return fmt.Errorf("writing secret key: %w", err)
	}

	// hs_ed25519_public_key: 32-byte header + 32-byte public key
	pubHeader := []byte("== ed25519v1-public: type0 ==\x00\x00\x00")
	pubKey := make([]byte, 0, len(pubHeader)+32)
	pubKey = append(pubKey, pubHeader...)
	pubKey = append(pubKey, pubBytes...)

	if err := os.WriteFile(filepath.Join(dir, "hs_ed25519_public_key"), pubKey, 0600); err != nil {
		return fmt.Errorf("writing public key: %w", err)
	}

	// hostname
	hostname := c.FullAddress() + "\n"
	if err := os.WriteFile(filepath.Join(dir, "hostname"), []byte(hostname), 0600); err != nil {
		return fmt.Errorf("writing hostname: %w", err)
	}

	return nil
}

// PublicKeyBytes returns the current 32-byte public key.
func (c *TorV3Candidate) PublicKeyBytes() []byte {
	return c.point.Bytes()
}

// Clone creates a deep copy of the candidate, preserving the current scalar/point
// state. The clone can be advanced independently of the original.
func (c *TorV3Candidate) Clone() *TorV3Candidate {
	clone := &TorV3Candidate{
		seed:      c.seed,
		counter:   c.counter,
		oneScalar: c.oneScalar, // shared, immutable
		genPoint:  c.genPoint,  // shared, immutable
	}
	copy(clone.hashSuffix[:], c.hashSuffix[:])
	// Deep copy mutable scalar and point
	clone.scalar, _ = edwards25519.NewScalar().SetCanonicalBytes(c.scalar.Bytes())
	clone.point, _ = new(edwards25519.Point).SetBytes(c.point.Bytes())
	return clone
}

// ExpandedPrivateKey returns a 64-byte expanded Ed25519 private key
// compatible with crypto/ed25519 signing (scalar + public key).
func (c *TorV3Candidate) ExpandedPrivateKey() ed25519.PrivateKey {
	priv := make(ed25519.PrivateKey, ed25519.PrivateKeySize)
	copy(priv[:32], c.scalar.Bytes())
	copy(priv[32:], c.point.Bytes())
	return priv
}

// torV3Checksum computes the 2-byte checksum for a Tor v3 onion address.
func torV3Checksum(pubkey []byte) [2]byte {
	// SHA3-256(".onion checksum" | pubkey | version)
	h := sha3.New256()
	h.Write([]byte(".onion checksum"))
	h.Write(pubkey[:32])
	h.Write([]byte{0x03})
	sum := h.Sum(nil)
	return [2]byte{sum[0], sum[1]}
}

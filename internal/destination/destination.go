package destination

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
)

const (
	EncryptionKeySize  = 256
	SigningKeyPadding   = 96
	SigningKeySize      = 128
	Ed25519PubKeySize  = 32
	CertificateSize    = 7
	DestinationSize    = EncryptionKeySize + SigningKeySize + CertificateSize // 391

	CertTypeKeyCert         = 5
	CertPayloadLength       = 4
	SigTypeEdDSASHA512Ed25519 = 7
	CryptoTypeElGamal       = 0
)

var b32Encoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// Destination represents an I2P destination with its associated keys.
type Destination struct {
	// The 391-byte destination (encryption pubkey + signing pubkey area + certificate)
	Raw [DestinationSize]byte

	// Ed25519 private key (64 bytes: seed + public key)
	SigningPrivateKey ed25519.PrivateKey

	// 256-byte encryption private key placeholder
	EncryptionPrivateKey [EncryptionKeySize]byte
}

// NewRandom generates a new random I2P destination with Ed25519 signing keys.
func NewRandom() (*Destination, error) {
	d := &Destination{}

	// Generate Ed25519 signing keypair
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating Ed25519 key: %w", err)
	}
	d.SigningPrivateKey = priv

	// Fill encryption public key with random bytes (ElGamal placeholder)
	if _, err := rand.Read(d.Raw[:EncryptionKeySize]); err != nil {
		return nil, fmt.Errorf("generating encryption key: %w", err)
	}

	// Fill encryption private key with random bytes
	if _, err := rand.Read(d.EncryptionPrivateKey[:]); err != nil {
		return nil, fmt.Errorf("generating encryption private key: %w", err)
	}

	// Signing public key area: 96 bytes zero padding + 32 bytes Ed25519 public key
	// Zero padding is already zero from array initialization
	copy(d.Raw[EncryptionKeySize+SigningKeyPadding:], pub)

	// Certificate: type(1) + length(2) + sigtype(2) + cryptotype(2) = 7 bytes
	certOffset := EncryptionKeySize + SigningKeySize
	d.Raw[certOffset] = CertTypeKeyCert
	// Length as big-endian uint16 = 4
	d.Raw[certOffset+1] = 0
	d.Raw[certOffset+2] = CertPayloadLength
	// Signing key type as big-endian uint16
	d.Raw[certOffset+3] = 0
	d.Raw[certOffset+4] = SigTypeEdDSASHA512Ed25519
	// Crypto type as big-endian uint16
	d.Raw[certOffset+5] = 0
	d.Raw[certOffset+6] = CryptoTypeElGamal

	return d, nil
}

// B32Address returns the 52-character base32 address (without .b32.i2p suffix).
func (d *Destination) B32Address() string {
	hash := sha256.Sum256(d.Raw[:])
	return b32Encoding.EncodeToString(hash[:])
}

// FullB32Address returns the complete .b32.i2p address.
func (d *Destination) FullB32Address() string {
	return d.B32Address() + ".b32.i2p"
}

// MutateEncryptionKey embeds a counter into the encryption key area to produce
// a different destination hash without regenerating the Ed25519 signing key.
func (d *Destination) MutateEncryptionKey(counter uint64) {
	binary.LittleEndian.PutUint64(d.Raw[0:8], counter)
}

// SaveKeys writes the destination and private keys to a file.
// Format: destination (391) + encryption private key (256) + Ed25519 private seed (32) = 679 bytes
func (d *Destination) SaveKeys(path string) error {
	buf := make([]byte, 0, DestinationSize+EncryptionKeySize+ed25519.SeedSize)
	buf = append(buf, d.Raw[:]...)
	buf = append(buf, d.EncryptionPrivateKey[:]...)
	buf = append(buf, d.SigningPrivateKey.Seed()...)
	return os.WriteFile(path, buf, 0600)
}

// ValidatePrefix checks that a vanity prefix contains only valid base32 characters.
func ValidatePrefix(prefix string) error {
	if len(prefix) == 0 {
		return fmt.Errorf("prefix cannot be empty")
	}
	if len(prefix) > 52 {
		return fmt.Errorf("prefix cannot exceed 52 characters")
	}
	prefix = strings.ToLower(prefix)
	for i, c := range prefix {
		if !((c >= 'a' && c <= 'z') || (c >= '2' && c <= '7')) {
			return fmt.Errorf("invalid character '%c' at position %d (allowed: a-z, 2-7)", c, i)
		}
	}
	return nil
}

// EstimateAttempts returns the average number of attempts needed to find a prefix of the given length.
func EstimateAttempts(prefixLen int) float64 {
	if prefixLen <= 0 {
		return 1
	}
	// 32^n / 2 average attempts
	attempts := 1.0
	for i := 0; i < prefixLen; i++ {
		attempts *= 32
	}
	return attempts / 2
}

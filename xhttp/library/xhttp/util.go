package xhttp

import (
	cryptoRand "crypto/rand"
	"math/big"
	"math/rand"

	"github.com/gofrs/uuid/v5"
)

// rangeRand returns a uniformly random int32 in [r.From, r.To].
//
// The previous implementation took a random uint32 modulo the span, which
// is biased toward small residues (and silently ignored crypto/rand
// errors). crypto/rand.Int performs rejection sampling, so every value in
// the range is equally likely; on a (practically impossible) rand failure
// it falls back to From, matching the degenerate-range behavior.
func rangeRand(r Range) int32 {
	if r.To <= r.From {
		return r.From
	}
	span := int64(r.To) - int64(r.From) + 1
	n, err := cryptoRand.Int(cryptoRand.Reader, big.NewInt(span))
	if err != nil {
		return r.From
	}
	return r.From + int32(n.Int64())
}

// randomSeed returns a non-cryptographic int32, used for misc jitter.
func randomSeed() int32 { return int32(rand.Int31()) }

// newUUID returns a random UUID v4 string.
func newUUID() string {
	id, _ := uuid.NewV4()
	return id.String()
}

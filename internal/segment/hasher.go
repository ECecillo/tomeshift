package segment

import (
	"crypto/sha256"
	"encoding/hex"
)

// Hash returns the lowercase hex sha256 of the normalized segment text. It is
// the primary key used for exact TM lookups.
func Hash(normalized string) string {
	sum := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(sum[:])
}

package common

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

func CalSha256(input string) string {
	str := sha256.Sum256([]byte(input))
	return strings.ToUpper(hex.EncodeToString(str[:]))
}

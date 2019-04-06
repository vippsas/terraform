package rand

import (
	"crypto/rand"
	"fmt"
)

var chars = []rune("abcdefghijklmnopqrstuvwxyz0123456789")
var letters = []rune("abcdefghijklmnopqrstuvwxyz")

// genRandBytes securely generates random bytes.
func genRandBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	_, err := rand.Read(b)
	if err != nil {
		return nil, fmt.Errorf("error reading from secure random generator: %s", err)
	}
	return b, nil
}

// GenLowerAlphanums generates a random lowercase alphanumeric string of len n.
func GenLowerAlphanums(n int) (string, error) {
	b, err := genRandBytes(n)
	if err != nil {
		return "", fmt.Errorf("error generating random bytes: %s", err)
	}

	var s []rune
	for _, number := range b {
		s = append(s, chars[int(number)%len(chars)])
	}
	return string(s), nil
}

// GenLowerAlphas generates a random lowercase alpha string of len n.
func GenLowerAlphas(n int) (string, error) {
	b, err := genRandBytes(n)
	if err != nil {
		return "", fmt.Errorf("error generating random bytes: %s", err)
	}

	var s []rune
	for _, number := range b {
		s = append(s, letters[int(number)%len(letters)])
	}
	return string(s), nil
}

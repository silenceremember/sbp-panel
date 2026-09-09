package agent

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
)

const amneziaWGDefaultI1 = "<r 2><b 0x858000010001000000000669636c6f756403636f6d0000010001c00c000100010000105a00044d583737>"

type generatedAmneziaWGSettings struct {
	server string
	client string
}

func newAmneziaWG3HeaderProtectionKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	key[0] &= 248
	key[31] &= 127
	key[31] |= 64
	return base64.StdEncoding.EncodeToString(key), nil
}

func newAmneziaWG3Settings() (generatedAmneziaWGSettings, error) {
	key, err := newAmneziaWG3HeaderProtectionKey()
	if err != nil {
		return generatedAmneziaWGSettings{}, err
	}
	// Header protection hides message types. Equal padding and fixed headers
	// avoid the AWG 3.1 classifier ambiguities with custom header ranges.
	server := fmt.Sprintf("Jc = 6\nJmin = 10\nJmax = 50\nS1 = 12\nS2 = 12\nS3 = 12\nS4 = 12\nH1 = 1\nH2 = 2\nH3 = 3\nH4 = 4\nHeaderProtectionKey = %s\nRandomTrailers = off\nDisableCookies = off\n", key)
	return generatedAmneziaWGSettings{server: server, client: server + "I1 = " + amneziaWGDefaultI1 + "\n"}, nil
}

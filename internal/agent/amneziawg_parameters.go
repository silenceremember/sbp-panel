package agent

import (
	"crypto/rand"
	"encoding/base64"
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
	server := canonicalAmneziaWGServerSettings(defaultAmneziaWGSettings(key))
	return generatedAmneziaWGSettings{server: server, client: server + "I1 = " + amneziaWGDefaultI1 + "\n"}, nil
}

func defaultAmneziaWGSettings(headerKey string) amneziaWGServerSettings {
	return amneziaWGServerSettings{Jc: 6, Jmin: 10, Jmax: 50, S1: 12, S2: 12, S3: 12, S4: 12,
		H1: "1", H2: "2", H3: "3", H4: "4", HeaderProtectionKey: headerKey, RandomTrailers: "off", DisableCookies: "off"}
}

package agent

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"sort"
)

const (
	amneziaWGParameterAttempts = 128
	amneziaWGHeaderLimit       = uint32(1 << 31)
	amneziaWG2DefaultI1        = "<r 2><b 0x858000010001000000000669636c6f756403636f6d0000010001c00c000100010000105a00044d583737>"
)

type generatedAmneziaWGSettings struct {
	server string
	client string
}

func randomUint32Range(minimum, maximum uint32) (uint32, error) {
	if minimum >= maximum {
		return 0, errors.New("invalid random range")
	}
	value, err := randomUint32()
	if err != nil {
		return 0, err
	}
	return minimum + value%(maximum-minimum), nil
}

func randomIntMatching(minimum, maximum int, allowed func(int) bool) (int, error) {
	for range amneziaWGParameterAttempts {
		value, err := randomUint32Range(uint32(minimum), uint32(maximum))
		if err != nil {
			return 0, err
		}
		if allowed(int(value)) {
			return int(value), nil
		}
	}
	return 0, errors.New("could not generate distinct AmneziaWG padding parameters")
}

func newAmneziaWGHeaderRanges() ([4]string, error) {
	var headers [4]string
	const width = uint32(1023)
	for range amneziaWGParameterAttempts {
		starts := make([]uint32, len(headers))
		for index := range starts {
			value, err := randomUint32Range(5, amneziaWGHeaderLimit-width)
			if err != nil {
				return headers, err
			}
			starts[index] = value
		}
		sort.Slice(starts, func(i, j int) bool { return starts[i] < starts[j] })
		valid := true
		for index := 1; index < len(starts); index++ {
			valid = valid && starts[index] > starts[index-1]+width
		}
		if !valid {
			continue
		}
		for index, start := range starts {
			headers[index] = fmt.Sprintf("%d-%d", start, start+width)
		}
		return headers, nil
	}
	return headers, errors.New("could not generate non-overlapping AmneziaWG header ranges")
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
	jc, err := randomIntMatching(4, 7, func(int) bool { return true })
	if err != nil {
		return generatedAmneziaWGSettings{}, err
	}
	s1, err := randomIntMatching(12, 150, func(int) bool { return true })
	if err != nil {
		return generatedAmneziaWGSettings{}, err
	}
	s2, err := randomIntMatching(12, 150, func(value int) bool {
		return value != s1 && value+92 != s1+148
	})
	if err != nil {
		return generatedAmneziaWGSettings{}, err
	}
	s3, err := randomIntMatching(12, 64, func(value int) bool {
		return value != s1 && value != s2 && value+64 != s1+148 && value+64 != s2+92
	})
	if err != nil {
		return generatedAmneziaWGSettings{}, err
	}
	s4, err := randomIntMatching(12, 20, func(value int) bool {
		return value != s1 && value != s2 && value != s3
	})
	if err != nil {
		return generatedAmneziaWGSettings{}, err
	}
	headers, err := newAmneziaWGHeaderRanges()
	if err != nil {
		return generatedAmneziaWGSettings{}, err
	}
	headerProtectionKey, err := newAmneziaWG3HeaderProtectionKey()
	if err != nil {
		return generatedAmneziaWGSettings{}, err
	}
	server := fmt.Sprintf(
		"Jc = %d\nJmin = 10\nJmax = 50\nS1 = %d\nS2 = %d\nS3 = %d\nS4 = %d\nH1 = %s\nH2 = %s\nH3 = %s\nH4 = %s\nHeaderProtectionKey = %s\nRandomTrailers = off\nDisableCookies = off\n",
		jc, s1, s2, s3, s4, headers[0], headers[1], headers[2], headers[3], headerProtectionKey,
	)
	return generatedAmneziaWGSettings{
		server: server,
		client: server + "I1 = " + amneziaWG2DefaultI1 + "\n",
	}, nil
}

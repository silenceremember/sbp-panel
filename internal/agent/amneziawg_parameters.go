package agent

import (
	"errors"
	"fmt"
)

const (
	amneziaWGParameterAttempts = 128
	amneziaWGHeaderLimit       = uint32(1 << 31)
	amneziaWG2DefaultI1        = "<r 2><b 0x858000010001000000000669636c6f756403636f6d0000010001c00c000100010000105a00044d583737>"
)

type amneziaWG2Settings struct {
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
	minimum := uint32(5)
	for index := range headers {
		remaining := uint32(len(headers) - index - 1)
		lowLimit := amneziaWGHeaderLimit - remaining*2 - 1
		low, err := randomUint32Range(minimum, lowLimit)
		if err != nil {
			return headers, err
		}
		highLimit := amneziaWGHeaderLimit - remaining*2
		high, err := randomUint32Range(low+1, highLimit)
		if err != nil {
			return headers, err
		}
		headers[index] = fmt.Sprintf("%d-%d", low, high)
		minimum = high + 1
	}
	return headers, nil
}

func newAmneziaWG2Settings() (amneziaWG2Settings, error) {
	jc, err := randomIntMatching(4, 7, func(int) bool { return true })
	if err != nil {
		return amneziaWG2Settings{}, err
	}
	s1, err := randomIntMatching(15, 150, func(int) bool { return true })
	if err != nil {
		return amneziaWG2Settings{}, err
	}
	s2, err := randomIntMatching(15, 150, func(value int) bool {
		return value != s1 && value+92 != s1+148
	})
	if err != nil {
		return amneziaWG2Settings{}, err
	}
	s3, err := randomIntMatching(0, 64, func(value int) bool {
		return value != s1 && value != s2 && value+64 != s1+148 && value+64 != s2+92
	})
	if err != nil {
		return amneziaWG2Settings{}, err
	}
	s4, err := randomIntMatching(0, 20, func(value int) bool {
		return value != s1 && value != s2 && value != s3
	})
	if err != nil {
		return amneziaWG2Settings{}, err
	}
	headers, err := newAmneziaWGHeaderRanges()
	if err != nil {
		return amneziaWG2Settings{}, err
	}
	server := fmt.Sprintf(
		"Jc = %d\nJmin = 10\nJmax = 50\nS1 = %d\nS2 = %d\nS3 = %d\nS4 = %d\nH1 = %s\nH2 = %s\nH3 = %s\nH4 = %s\n",
		jc, s1, s2, s3, s4, headers[0], headers[1], headers[2], headers[3],
	)
	return amneziaWG2Settings{
		server: server,
		client: server + "I1 = " + amneziaWG2DefaultI1 + "\n",
	}, nil
}

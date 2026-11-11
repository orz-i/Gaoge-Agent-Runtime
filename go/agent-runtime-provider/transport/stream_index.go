package transport

import (
	"encoding/json"
	"errors"
	"math"
	"strconv"
)

// A single response cannot need an unbounded sparse index. This limit bounds
// slice/map growth to 65,536 entries on both 32- and 64-bit hosts.
const maxStreamIndex = 65535

var errInvalidStreamIndex = errors.New("invalid or oversized provider stream index")

func streamIndex(value any) (int, error) {
	var integer int64
	switch value := value.(type) {
	case int:
		integer = int64(value)
	case int64:
		integer = value
	case json.Number:
		parsed, err := strconv.ParseInt(value.String(), 10, 64)
		if err != nil {
			return 0, errInvalidStreamIndex
		}
		integer = parsed
	case float64:
		if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > maxStreamIndex || math.Trunc(value) != value {
			return 0, errInvalidStreamIndex
		}
		return int(value), nil
	default:
		return 0, errInvalidStreamIndex
	}
	if integer < 0 || integer > maxStreamIndex {
		return 0, errInvalidStreamIndex
	}
	return int(integer), nil
}
func streamIndexField(value map[string]any, key string) (int, error) {
	raw, present := value[key]
	if !present {
		return 0, nil
	}
	return streamIndex(raw)
}

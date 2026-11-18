package transport

import (
	"encoding/json"
	"math"
	"math/big"
)

// Preserve the existing integer parameter conversion (including decimal and
// exponent JSON syntax) without rounding integer values through float64.
func exactModelParamNumberInt(value json.Number) (int, bool) {
	integer, err := value.Int64()
	if err == nil {
		return int(integer), int64(int(integer)) == integer
	}
	// Reject enormous exponents before big.Rat can allocate their numerator.
	approximate, err := value.Float64()
	if err != nil || math.IsNaN(approximate) || math.IsInf(approximate, 0) || len(value) > 512 {
		return 0, false
	}
	if approximate > -1 && approximate < 1 {
		return 0, true
	}
	return decimalModelParamNumberInt(value)
}

func decimalModelParamNumberInt(value json.Number) (int, bool) {
	parsed, ok := new(big.Rat).SetString(value.String())
	if !ok {
		return 0, false
	}
	whole := new(big.Int).Quo(parsed.Num(), parsed.Denom())
	if !whole.IsInt64() {
		return 0, false
	}
	integer := whole.Int64()
	converted := int(integer)
	return converted, int64(converted) == integer
}

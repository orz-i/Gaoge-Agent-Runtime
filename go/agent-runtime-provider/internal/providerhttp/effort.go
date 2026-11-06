package providerhttp

// OpenAIEffortLevels enumerates the Responses protocol's valid effort
// vocabulary, not a vendor model registry. The subset for any actual model
// comes only from the Host-supplied ModelProfileSource.
func OpenAIEffortLevels() []string {
	return []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
}

func validEffort(level string) bool {
	switch level {
	case "none", "minimal", "low", "medium", "high", "xhigh", "max":
		return true
	default:
		return false
	}
}

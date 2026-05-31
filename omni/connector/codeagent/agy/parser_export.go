package agy

// ParseHookInput exposes the Agy hook JSON round-trip parser for subpackages
// that implement connector-specific hook transforms.
func ParseHookInput[T any](raw any) (*T, error) {
	return parseHookInput[T](raw)
}

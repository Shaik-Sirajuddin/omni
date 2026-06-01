package utils

import "github.com/Shaik-Sirajuddin/omni/connector/codeagent"

// TODO : move utils to specific exporters
func BuildModel(provider, name string) *codeagent.Model {
	if provider == "" && name == "" {
		return nil
	}
	return &codeagent.Model{Provider: codeagent.Provider(provider), Model: name}
}

func ModelFields(m *codeagent.Model) (provider, name string) {
	if m == nil {
		return "", ""
	}
	return string(m.Provider), m.Model
}

// Common utils

func BoolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

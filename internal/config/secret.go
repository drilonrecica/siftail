package config

import "encoding/json"

// Secret is a configuration value that must never be exposed in logs,
// diagnostics, or sanitized output.
type Secret string

func (s Secret) String() string {
	if s == "" {
		return ""
	}
	return "[redacted]"
}

func (s Secret) GoString() string {
	return s.String()
}

func (s Secret) MarshalText() ([]byte, error) {
	return []byte(s.String()), nil
}

func (s Secret) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

func (s Secret) Raw() string {
	return string(s)
}

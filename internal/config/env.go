package config

import (
	"fmt"
	"os"
	"strings"
)

// readString returns a string value from the environment.
// If fileEnv is non-empty, it may be read from a file pointed to by fileEnv.
// Setting both env and fileEnv is an error.
func readString(env, fileEnv, fallback string) (string, error) {
	v, hasV := os.LookupEnv(env)
	f, hasF := "", false
	if fileEnv != "" {
		f, hasF = os.LookupEnv(fileEnv)
	}

	if hasV && hasF {
		return "", fmt.Errorf("both %s and %s are set; provide only one", env, fileEnv)
	}
	if hasF {
		content, err := os.ReadFile(f)
		if err != nil {
			return "", fmt.Errorf("reading %s from %q: %w", fileEnv, f, err)
		}
		return strings.TrimRight(string(content), "\r\n"), nil
	}
	if hasV {
		return v, nil
	}
	return fallback, nil
}

// hasEnv reports whether env or its _FILE variant is set.
func hasEnv(env, fileEnv string) bool {
	_, hasV := os.LookupEnv(env)
	if fileEnv == "" {
		return hasV
	}
	_, hasF := os.LookupEnv(fileEnv)
	return hasV || hasF
}

// listUnknownVars returns SIFTAIL_-prefixed environment variables that are not
// in the known set.
func listUnknownVars(known map[string]struct{}) []string {
	var unknown []string
	for _, e := range os.Environ() {
		name, _, found := strings.Cut(e, "=")
		if !found {
			continue
		}
		if !strings.HasPrefix(name, envPrefix) {
			continue
		}
		if _, ok := known[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	return unknown
}

const envPrefix = "SIFTAIL_"

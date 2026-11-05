package config

import (
	"os"
	"sort"
	"strings"
)

// readString returns a non-secret setting from the environment, or fallback
// when it is unset. Non-secret settings have no _FILE variant (ADR 0010 §17).
func readString(env, fallback string) string {
	if v, ok := os.LookupEnv(env); ok {
		return v
	}
	return fallback
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
	sort.Strings(unknown)
	return unknown
}

const envPrefix = "SIFTAIL_"

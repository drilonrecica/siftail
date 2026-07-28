// Package auth owns the single administrator credential boundary.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemoryKiB = 32 * 1024
	argonTime      = 3
	argonThreads   = 1
	argonSaltBytes = 16
	argonKeyBytes  = 32
)

var hashOperations = make(chan struct{}, 2)

var dummyPasswordHash = "$argon2id$v=19$m=32768,t=3,p=1$c2lmdGFpbC1kdW1teS1zYWx0$AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

type passwordParameters struct {
	memory  uint32
	time    uint32
	threads uint8
}

func ValidateUsername(username string) error {
	if len(username) < 3 || len(username) > 64 {
		return errors.New("username must be 3 to 64 ASCII characters")
	}
	for _, char := range []byte(username) {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '.' || char == '_' || char == '-' {
			continue
		}
		return errors.New("username contains an unsupported character")
	}
	return nil
}

func ValidatePassword(password []byte) error {
	if len(password) < 12 || len(password) > 1024 {
		return errors.New("password must be 12 to 1024 UTF-8 bytes")
	}
	if !utf8.Valid(password) {
		return errors.New("password must be valid UTF-8")
	}
	return nil
}

func HashPassword(ctx context.Context, password []byte) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}
	salt := make([]byte, argonSaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.New("generate password salt")
	}
	parameters := passwordParameters{
		memory: argonMemoryKiB, time: argonTime, threads: argonThreads,
	}
	key, err := deriveKey(ctx, password, salt, parameters, argonKeyBytes)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, parameters.memory, parameters.time, parameters.threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

func VerifyPassword(ctx context.Context, password []byte, encoded string) (bool, error) {
	parameters, salt, expected, err := parsePasswordHash(encoded)
	if err != nil {
		return false, errors.New("stored password hash is invalid")
	}
	key, err := deriveKey(ctx, password, salt, parameters, uint32(len(expected)))
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(key, expected) == 1, nil
}

func deriveKey(
	ctx context.Context,
	password, salt []byte,
	parameters passwordParameters,
	keyBytes uint32,
) ([]byte, error) {
	select {
	case hashOperations <- struct{}{}:
		defer func() { <-hashOperations }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return argon2.IDKey(password, salt, parameters.time, parameters.memory, parameters.threads, keyBytes), nil
}

func parsePasswordHash(encoded string) (passwordParameters, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" ||
		parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return passwordParameters{}, nil, nil, errors.New("invalid Argon2id encoding")
	}
	var memory, iterations uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &threads); err != nil {
		return passwordParameters{}, nil, nil, errors.New("invalid Argon2id parameters")
	}
	if parts[3] != fmt.Sprintf("m=%d,t=%d,p=%d", memory, iterations, threads) {
		return passwordParameters{}, nil, nil, errors.New("invalid Argon2id parameters")
	}
	if memory < 8*1024 || memory > 64*1024 || iterations < 1 || iterations > 10 ||
		threads < 1 || threads > 4 {
		return passwordParameters{}, nil, nil, errors.New("unsafe Argon2id parameters")
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return passwordParameters{}, nil, nil, errors.New("invalid Argon2id salt")
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil || len(key) < 16 || len(key) > 64 {
		return passwordParameters{}, nil, nil, errors.New("invalid Argon2id key")
	}
	return passwordParameters{memory: memory, time: iterations, threads: threads}, salt, key, nil
}

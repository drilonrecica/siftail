package auth

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestUsernameAndPasswordValidationExactness(t *testing.T) {
	for _, username := range []string{"abc", "Admin.Name_1-test", strings.Repeat("a", 64)} {
		if err := ValidateUsername(username); err != nil {
			t.Errorf("valid username %q: %v", username, err)
		}
	}
	for _, username := range []string{"ab", " admin", "admin ", "éclair", "admin/name", strings.Repeat("a", 65)} {
		if err := ValidateUsername(username); err == nil {
			t.Errorf("invalid username %q accepted", username)
		}
	}
	for _, password := range [][]byte{
		[]byte("123456789012"),
		[]byte(" spaces stay "),
		[]byte(strings.Repeat("é", 512)),
	} {
		if err := ValidatePassword(password); err != nil {
			t.Errorf("valid password length %d: %v", len(password), err)
		}
	}
	for _, password := range [][]byte{
		[]byte("short"),
		[]byte(strings.Repeat("a", 1025)),
		{0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8, 0xf7, 0xf6, 0xf5, 0xf4},
	} {
		if err := ValidatePassword(password); err == nil {
			t.Errorf("invalid password length %d accepted", len(password))
		}
	}
}

func TestArgon2idEncodedParametersAndVerification(t *testing.T) {
	password := []byte("correct horse battery staple")
	encoded, err := HashPassword(context.Background(), password)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, string(password)) ||
		!strings.HasPrefix(encoded, "$argon2id$v=19$m=32768,t=3,p=1$") {
		t.Fatalf("unexpected encoded hash format")
	}
	matched, err := VerifyPassword(context.Background(), password, encoded)
	if err != nil || !matched {
		t.Fatalf("correct password matched=%v err=%v", matched, err)
	}
	matched, err = VerifyPassword(context.Background(), []byte("wrong password value"), encoded)
	if err != nil || matched {
		t.Fatalf("wrong password matched=%v err=%v", matched, err)
	}
	if _, err := VerifyPassword(
		context.Background(), password, strings.Replace(encoded, "p=1$", "p=1junk$", 1),
	); err == nil {
		t.Fatal("malformed stored hash accepted")
	}
}

func TestArgon2idOperationsAreCappedAtTwo(t *testing.T) {
	const workers = 6
	start := make(chan struct{})
	var group sync.WaitGroup
	for i := 0; i < workers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			if _, err := HashPassword(context.Background(), []byte("parallel-password")); err != nil {
				t.Error(err)
			}
		}()
	}
	close(start)
	maximum := 0
	done := make(chan struct{})
	go func() {
		group.Wait()
		close(done)
	}()
	for {
		select {
		case <-done:
			if maximum != 2 {
				t.Fatalf("maximum concurrent hash slots = %d", maximum)
			}
			return
		default:
			if active := len(hashOperations); active > maximum {
				maximum = active
			}
			if active := len(hashOperations); active > 2 {
				t.Fatalf("active hash operations = %d", active)
			}
			time.Sleep(time.Millisecond)
		}
	}
}

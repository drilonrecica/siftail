package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
)

func runSessionCommand(args []string, stdout io.Writer) error {
	if len(args) != 1 || args[0] != "revoke-all" {
		return errors.New("usage: siftail sessions revoke-all")
	}
	var result struct {
		Revoked int64 `json:"revoked"`
	}
	if err := runAdminOperation(http.MethodPost, "/sessions/revoke-all", struct{}{}, &result); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "revoked %d administrator session(s)\n", result.Revoked)
	return nil
}

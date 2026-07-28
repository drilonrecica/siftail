package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/drilonrecica/siftail/internal/audit"
	"github.com/drilonrecica/siftail/internal/auth"
	"github.com/drilonrecica/siftail/internal/config"
	"github.com/drilonrecica/siftail/internal/database"
	"github.com/drilonrecica/siftail/internal/ingest"
	"github.com/drilonrecica/siftail/internal/sessions"
	"github.com/drilonrecica/siftail/internal/sources"
)

func runServerCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing server subcommand")
	}
	switch args[0] {
	case "create":
		flags := flag.NewFlagSet("server create", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		name := flags.String("name", "", "")
		hostname := flags.String("hostname", "", "")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return errors.New("usage: siftail server create --name <name> [--hostname <hostname>]")
		}
		var server sources.Server
		if err := runAdminOperation(http.MethodPost, "/servers", map[string]any{
			"name": *name, "hostname": *hostname,
		}, &server); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "server %d created: %s\n", server.ID, server.Name)
		return nil
	case "list":
		if len(args) != 1 {
			return errors.New("usage: siftail server list")
		}
		var servers []sources.Server
		if err := runAdminOperation(http.MethodGet, "/servers", nil, &servers); err != nil {
			return err
		}
		for _, server := range servers {
			fmt.Fprintf(stdout, "%d\t%s\t%s\n", server.ID, server.Name, server.Hostname)
		}
		return nil
	default:
		return fmt.Errorf("unknown server subcommand %q", args[0])
	}
}

func runTokenCommand(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing token subcommand")
	}
	switch args[0] {
	case "create":
		flags := flag.NewFlagSet("token create", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		serverID := flags.Int64("server", 0, "")
		name := flags.String("name", "", "")
		output := flags.String("output", "token", "")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return errors.New("usage: siftail token create --server <id> --name <name> [--output token|coolify|generic|curl]")
		}
		switch *output {
		case "token", "coolify", "generic", "curl":
		default:
			return errors.New("token create output must be token, coolify, generic, or curl")
		}
		var guide ingest.Guide
		if *output != "token" {
			cfg, err := config.Parse()
			if err != nil {
				return fmt.Errorf("configuration invalid: %w", err)
			}
			if cfg.IngestPublicURL == "" {
				return errors.New("SIFTAIL_INGEST_PUBLIC_URL must be configured for generated token output")
			}
			eventID, err := ingest.NewGuideEventID()
			if err != nil {
				return errors.New("prepare guided token output")
			}
			guide, err = ingest.GenerateGuide(cfg.IngestPublicURL, eventID, time.Now())
			if err != nil {
				return errors.New("prepare guided token output")
			}
		}
		var token sources.CreatedToken
		if err := runAdminOperation(http.MethodPost, "/tokens", map[string]any{
			"server_id": *serverID, "name": *name,
		}, &token); err != nil {
			return err
		}
		if *output == "token" {
			fmt.Fprintf(stdout, "token %d created for server %d\n", token.ID, token.ServerID)
			fmt.Fprintf(stdout, "fingerprint: %s\n", token.Fingerprint)
			fmt.Fprintf(stdout, "token (shown once): %s\n", token.Token)
			return nil
		}
		var err error
		guide, err = guide.Materialize(token.Token)
		if err != nil {
			return errors.New("token created but guided output could not be prepared; create a replacement token")
		}
		switch *output {
		case "coolify":
			fmt.Fprint(stdout, guide.CoolifyTemplate)
		case "generic":
			fmt.Fprint(stdout, guide.GenericTemplate)
		case "curl":
			fmt.Fprintln(stdout, guide.CurlTemplate)
		}
		return nil
	case "revoke":
		flags := flag.NewFlagSet("token revoke", flag.ContinueOnError)
		flags.SetOutput(io.Discard)
		id := flags.Int64("id", 0, "")
		if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
			return errors.New("usage: siftail token revoke --id <id>")
		}
		var result struct{}
		if err := runAdminOperation(http.MethodPost, "/tokens/revoke", map[string]any{"id": *id}, &result); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "token %d revoked\n", *id)
		return nil
	default:
		return fmt.Errorf("unknown token subcommand %q", args[0])
	}
}

func runAdminOperation(method, path string, input, output any) error {
	cfg, err := config.Parse()
	if err != nil {
		return fmt.Errorf("configuration invalid: %w", err)
	}
	socket := filepath.Join(cfg.DataDir, "siftail-control.sock")
	if info, statErr := os.Lstat(socket); statErr == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("control path exists but is not a socket")
		}
		return controlRequest(method, socket, path, input, output)
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return errors.New("cannot inspect control socket")
	}

	db, err := database.Open(context.Background(), cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("database unavailable: %w", err)
	}
	defer db.Close()
	if _, err := os.Lstat(socket); err == nil {
		return errors.New("server became active; retry the command")
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("cannot inspect control socket")
	}
	store := sources.NewStore(db.Writer())
	operationCtx := audit.ContextWithAttribution(
		context.Background(),
		audit.Attribution{ActorType: audit.ActorLocalOperator},
	)
	switch path {
	case "/administrator":
		value := input.(map[string]any)
		administrator, err := auth.NewStore(db.Writer()).Create(
			operationCtx, value["username"].(string), []byte(value["password"].(string)),
		)
		return assignJSON(administrator, output, err)
	case "/administrator/reset-password":
		value := input.(map[string]any)
		return auth.NewStore(db.Writer()).ResetPassword(
			operationCtx, []byte(value["password"].(string)),
		)
	case "/sessions/revoke-all":
		affected, err := sessions.NewStore(db.Writer()).RevokeAll(operationCtx, 1)
		return assignJSON(struct {
			Revoked int64 `json:"revoked"`
		}{Revoked: affected}, output, err)
	case "/servers":
		if method == http.MethodGet {
			servers, err := store.ListServers(context.Background())
			return assignJSON(servers, output, err)
		}
		value := input.(map[string]any)
		server, err := store.CreateServer(operationCtx, value["name"].(string), value["hostname"].(string))
		return assignJSON(server, output, err)
	case "/tokens":
		value := input.(map[string]any)
		token, err := store.CreateToken(operationCtx, value["server_id"].(int64), value["name"].(string))
		return assignJSON(token, output, err)
	case "/tokens/revoke":
		value := input.(map[string]any)
		return store.RevokeToken(operationCtx, value["id"].(int64))
	default:
		return errors.New("unsupported administrative operation")
	}
}

func controlRequest(method, socket, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return errors.New("encode administrative request")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, "http://unix"+path, body)
	if err != nil {
		return errors.New("create administrative request")
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		}},
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("server control request failed")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("administrative operation failed with status %s", strconv.Itoa(response.StatusCode))
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(output); err != nil {
		return errors.New("invalid server control response")
	}
	return nil
}

func assignJSON(value, output any, err error) error {
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(encoded, output)
}

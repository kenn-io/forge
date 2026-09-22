package devbox

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// RunCredential implements Git's credential helper protocol. Tokens leave this
// process only through Git's credential pipe, never through argv or storage.
func RunCredential(ctx context.Context, client *BrokerClient, action string, input io.Reader, output, stderr io.Writer) error {
	if action != "get" && action != "store" && action != "erase" {
		return errors.New("credential action must be get, store, or erase")
	}
	fields := make(map[string]string)
	scanner := bufio.NewScanner(io.LimitReader(input, 64<<10))
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return errors.New("invalid Git credential request")
		}
		fields[key] = value
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read Git credential request: %w", err)
	}
	if action == "store" {
		return nil
	}
	if fields["protocol"] != "https" || fields["host"] != "github.com" {
		return errors.New("devbox credentials require an HTTPS github.com remote")
	}
	owner, name, err := ParseRepository(fields["path"])
	if err != nil {
		return fmt.Errorf("git credential path: %w (set credential.useHttpPath=true)", err)
	}
	repository := owner + "/" + name
	if action == "erase" {
		return client.Erase(ctx, repository)
	}
	credential, err := client.Credential(ctx, repository, "git")
	if err != nil {
		return err
	}
	if !credential.Writable {
		if _, err := fmt.Fprintln(stderr, "devbox GitHub: this repository is read-only; pushes are unavailable"); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(output, "username=x-access-token\npassword=%s\n\n", credential.Token)
	return err
}

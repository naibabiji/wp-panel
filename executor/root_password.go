package executor

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

const rootPasswordChangeTimeout = 15 * time.Second

var (
	ErrRootPasswordChangeTimeout = errors.New("root password change timed out")
	ErrRootPasswordChangeFailed  = errors.New("root password change failed")
)

func runRootPasswordCommand(ctx context.Context, password []byte) error {
	cmd := exec.CommandContext(ctx, "chpasswd")
	input := make([]byte, 0, len(password)+6)
	input = append(input, "root:"...)
	input = append(input, password...)
	input = append(input, '\n')
	defer clear(input)
	cmd.Stdin = bytes.NewReader(input)
	return cmd.Run()
}

// ChangeRootPassword changes only the fixed root account through chpasswd's
// standard input. The password must never be passed as a command argument or
// included in an error returned to the caller.
func ChangeRootPassword(ctx context.Context, password string) error {
	return changeRootPasswordWithTimeout(ctx, password, rootPasswordChangeTimeout)
}

func changeRootPasswordWithTimeout(ctx context.Context, password string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	secret := []byte(password)
	defer clear(secret)
	if err := runRootPasswordCommand(ctx, secret); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return ErrRootPasswordChangeTimeout
		}
		return ErrRootPasswordChangeFailed
	}
	return nil
}

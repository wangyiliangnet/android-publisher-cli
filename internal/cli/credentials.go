package cli

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/wangyiliangnet/android-publisher-cli/internal/account"
	"github.com/wangyiliangnet/android-publisher-cli/internal/credentials"
	"github.com/wangyiliangnet/android-publisher-cli/internal/privatefs"
)

func runCredentials(ctx context.Context, mode string, args []string, stdout, stderr io.Writer) int {
	manager := &credentials.Manager{Vault: credentials.NewKeychain(), Accounts: account.Default()}
	return credentialsCommand(ctx, mode, args, os.Stdin, stdout, stderr, manager)
}
func credentialsCommand(ctx context.Context, mode string, args []string, stdin io.Reader, stdout, stderr io.Writer, manager *credentials.Manager) int {
	bad := func() int { return failure(stdout, mode, "fix_config", 2, credentials.ErrInvalid) }
	if mode != "production" || len(args) == 0 {
		return bad()
	}
	action := args[0]
	if action != "import" && action != "check" && action != "delete" {
		return bad()
	}
	f := flags("credentials "+action, io.Discard)
	channel := f.String("channel", "", "channel ID")
	accountID := f.String("account", "", "stable account identity")
	name := f.String("name", "", "credential name")
	file := f.String("file", "", "private credential file (exact bytes)")
	fromStdin := f.Bool("stdin", false, "read exact bytes from a controlled pipe")
	replace := f.Bool("replace", false, "explicitly replace existing credential")
	if parse(f, args[1:]) != nil {
		return bad()
	}
	ref := credentials.Ref{Scope: account.Scope{Channel: *channel, Account: *accountID}, Name: *name}
	if !ref.Valid() || !credentials.ValidCredentialName(ref.Name) || action != "import" && (*file != "" || *fromStdin || *replace) || *file != "" && *fromStdin {
		return bad()
	}
	var err error
	inputFailed := false
	switch action {
	case "import":
		var secret *credentials.Secret
		if *file != "" {
			var input *os.File
			input, err = privatefs.Open(*file, os.O_RDONLY)
			if err == nil {
				info, e := input.Stat()
				if e != nil || info.Mode().Perm()&0077 != 0 {
					err = credentials.ErrInvalid
				} else {
					secret, err = credentials.ReadInput(input)
				}
				input.Close()
			}
			if err != nil {
				err = credentials.ErrInvalid
			}
		} else if *fromStdin {
			if pipe, ok := stdin.(*os.File); ok {
				secret, err = credentials.ReadPipe(ctx, pipe)
			} else {
				secret, err = credentials.ReadInput(stdin)
			}
		} else {
			secret, err = credentials.Prompt(ctx)
		}
		if secret != nil {
			defer secret.Destroy()
		}
		inputFailed = err != nil
		if err == nil {
			err = manager.Put(ctx, ref, secret, *replace)
		}
	case "check":
		err = manager.Check(ctx, ref)
	case "delete":
		err = manager.Delete(ctx, ref)
	}
	if err != nil {
		safe := credentials.PublicError(err)
		code, next := 4, "review_keychain_access"
		effect := safe.Effect
		if inputFailed {
			effect, next = "not_sent", "retry_credential_input"
		}
		if action == "check" {
			effect = "not_sent"
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				next = "retry_credential_check"
			}
		}
		if errors.Is(err, credentials.ErrInvalid) || errors.Is(err, credentials.ErrExists) {
			code, next = 2, "fix_config"
		}
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			code = 130
		}
		return failureEffect(stdout, mode, next, code, safe, effect)
	}
	effect := "confirmed_applied"
	if action == "check" {
		effect = "not_sent"
	}
	state := map[string]string{"import": "stored", "check": "readable", "delete": "deleted"}[action]
	if encode(stdout, map[string]any{"schema_version": 1, "mode": mode, "operation": "credential_" + action, "effect": effect, "credential_state": state, "channel": ref.Scope.Channel, "account": ref.Scope.Account, "name": ref.Name, "channel_permissions": "not_verified"}) != nil {
		return 5
	}
	return 0
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/josegonzalez/litterrobot4-cli/internal/config"
	"github.com/josegonzalez/litterrobot4-cli/litterrobot/cloud"
)

var errNoAccount = errors.New("not signed in to Whisker: run \"litterrobot4-cli login\"")

// cloudClient builds a cloud client from the saved account, or from the
// LITTERROBOT_EMAIL and LITTERROBOT_PASSWORD environment variables.
func cloudClient(cfg *config.Config) (*cloud.Client, error) {
	if cfg.Account == nil {
		email, password := os.Getenv("LITTERROBOT_EMAIL"), os.Getenv("LITTERROBOT_PASSWORD")
		if email == "" || password == "" {
			return nil, errNoAccount
		}
		auth := cloud.NewAuth(cloud.Tokens{})
		auth.Email, auth.Password = email, password
		return cloud.NewClient(auth), nil
	}

	acct := cfg.Account
	refresh, _, err := cfg.Secret(config.RefreshTokenKey(acct.Email), acct.RefreshToken)
	if err != nil && !errors.Is(err, config.ErrNoSecret) {
		return nil, err
	}
	password, _, err := cfg.Secret(config.PasswordKey(acct.Email), acct.Password)
	if err != nil && !errors.Is(err, config.ErrNoSecret) {
		return nil, err
	}
	if p := os.Getenv("LITTERROBOT_PASSWORD"); p != "" {
		password = p
	}
	if refresh == "" && password == "" {
		return nil, errNoAccount
	}

	auth := cloud.NewAuth(cloud.Tokens{RefreshToken: refresh})
	auth.Email, auth.Password = acct.Email, password
	auth.OnRefresh = func(t cloud.Tokens) {
		if t.RefreshToken == "" || t.RefreshToken == refresh {
			return
		}
		// Cognito issued a new refresh token; keep it.
		insecure := acct.RefreshToken != ""
		if _, err := cfg.SetSecret(config.RefreshTokenKey(acct.Email), t.RefreshToken, insecure, &acct.RefreshToken); err == nil {
			if err := cfg.Save(); err != nil {
				fmt.Fprintln(os.Stderr, "warning: could not save the new refresh token:", err)
			}
		}
		refresh = t.RefreshToken
	}
	return cloud.NewClient(auth), nil
}

func cmdLogin(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("login", flag.ContinueOnError)
	email := fs.String("email", os.Getenv("LITTERROBOT_EMAIL"), "Whisker account email (env LITTERROBOT_EMAIL)")
	savePassword := fs.Bool("save-password", false, "also save the password, so the tool can sign in again on its own when the session expires")
	insecure := fs.Bool("insecure-storage", false, "store secrets in the config file instead of the keychain")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	p := newPrompter(out)
	return login(ctx, p, cfg, *email, *savePassword, *insecure)
}

// login signs in, asking for anything missing, and saves the account.
func login(ctx context.Context, p *prompter, cfg *config.Config, email string, savePassword, insecure bool) error {
	var err error
	if email == "" {
		if email, err = p.ask(ctx, "Whisker account email: "); err != nil {
			return err
		}
	}
	email = strings.TrimSpace(email)
	if email == "" {
		return errors.New("an email is required")
	}
	password := os.Getenv("LITTERROBOT_PASSWORD")
	if password == "" {
		if password, err = p.readSecret(ctx, "Whisker password: "); err != nil {
			return err
		}
	}
	if password == "" {
		return errors.New("a password is required")
	}

	auth := cloud.NewAuth(cloud.Tokens{})
	tokens, err := auth.Login(ctx, email, password)
	if err != nil {
		return err
	}
	robots, err := cloud.NewClient(auth).Robots(ctx)
	if err != nil {
		return fmt.Errorf("signed in, but could not list robots: %w", err)
	}

	if cfg.Account != nil && !strings.EqualFold(cfg.Account.Email, email) {
		cfg.Logout()
	}
	acct := &config.Account{Email: email}
	cfg.Account = acct
	storage, err := cfg.SetSecret(config.RefreshTokenKey(email), tokens.RefreshToken, insecure, &acct.RefreshToken)
	if err != nil {
		return err
	}
	if savePassword {
		if _, err := cfg.SetSecret(config.PasswordKey(email), password, insecure, &acct.Password); err != nil {
			return err
		}
	} else {
		config.DeleteSecret(config.PasswordKey(email))
	}
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(p.out, "Signed in as %s (session saved in %s). Found %d Litter-Robot 4 %s.\n", email, storage, len(robots), plural(len(robots), "unit", "units"))
	return nil
}

func cmdLogout(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("logout", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Account == nil {
		fmt.Fprintln(out, "not signed in")
		return nil
	}
	email := cfg.Account.Email
	cfg.Logout()
	if err := cfg.Save(); err != nil {
		return err
	}
	fmt.Fprintf(out, "signed out of %s; saved robots are kept\n", email)
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

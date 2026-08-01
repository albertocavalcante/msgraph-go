// Command msgraph-live-smoke verifies msauth-go plus msgraph-go against the
// signed-in user's real Microsoft Graph account. It never prints tokens.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	msauth "github.com/albertocavalcante/msauth-go"
	"github.com/albertocavalcante/msauth-go/cache"
	"github.com/albertocavalcante/msgraph-go"
)

func main() {
	scopesFlag := flag.String("scopes", "User.Read,Mail.Read", "comma-separated delegated Graph scopes")
	device := flag.Bool("device", false, "use device-code login when a login is required")
	top := flag.Int("top", 5, "number of messages to read from /me/messages")
	timeout := flag.Duration("timeout", 30*time.Second, "Graph request timeout")
	flag.Parse()

	if err := run(*scopesFlag, *device, *top, *timeout); err != nil {
		fmt.Fprintf(os.Stderr, "msgraph live smoke failed: %v\n", err)
		os.Exit(1)
	}
}

func run(scopesFlag string, device bool, top int, timeout time.Duration) error {
	scopes := splitScopes(scopesFlag)
	if len(scopes) == 0 {
		return errors.New("at least one scope is required")
	}

	auth, err := msauth.New(msauth.Config{Scopes: scopes}, cache.NewKeychainStore())
	if err != nil {
		return fmt.Errorf("new authenticator: %w", err)
	}
	ctx := context.Background()
	identity, err := auth.Identity(ctx)
	if errors.Is(err, msauth.ErrLoginRequired) {
		opts := []msauth.LoginOption{}
		if device {
			opts = append(opts, msauth.WithDevice())
		}
		identity, err = auth.Login(ctx, opts...)
	}
	if err != nil {
		return fmt.Errorf("identity/login: %w", err)
	}

	graph, err := msgraph.New(msgraph.TokenSourceFunc(func(ctx context.Context) (string, error) {
		return auth.Token(ctx)
	}))
	if err != nil {
		return fmt.Errorf("new graph client: %w", err)
	}
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var me graphUser
	if _, err := graph.Get(requestCtx, "/me", msgraph.Params{
		Select: []string{"id", "displayName", "userPrincipalName", "mail"},
	}, &me); err != nil {
		return fmt.Errorf("get /me: %w", err)
	}

	var messages msgraph.Page[graphMessage]
	if _, err := graph.Do(requestCtx, msgraph.Request{
		Method: httpMethodGet,
		URL:    "/me/messages",
		Params: msgraph.Params{
			Select:  []string{"id", "subject", "from", "receivedDateTime"},
			OrderBy: []string{"receivedDateTime desc"},
			Top:     top,
		},
		Prefer: []string{msgraph.PreferIDTypeImmutableID},
	}, &messages); err != nil {
		return fmt.Errorf("get /me/messages: %w", err)
	}

	fmt.Printf("auth_ok=true username=%q home_account_id=%q\n", identity.Username, identity.HomeAccountID)
	fmt.Printf("graph_me_ok=true id=%q display_name=%q mail=%q upn=%q\n", me.ID, me.DisplayName, me.Mail, me.UserPrincipalName)
	fmt.Printf("messages_ok=true count=%d\n", len(messages.Value))
	for _, message := range messages.Value {
		from := message.From.EmailAddress.Address
		if from == "" {
			from = "(no sender)"
		}
		fmt.Printf("  %s  %-32s  %s\n",
			message.Received.Local().Format("2006-01-02 15:04"),
			truncate(from, 32),
			truncate(message.Subject, 72),
		)
	}
	return nil
}

const httpMethodGet = "GET"

type graphUser struct {
	ID                string `json:"id"`
	DisplayName       string `json:"displayName"`
	UserPrincipalName string `json:"userPrincipalName"`
	Mail              string `json:"mail"`
}

type graphMessage struct {
	ID       string    `json:"id"`
	Subject  string    `json:"subject"`
	Received time.Time `json:"receivedDateTime"`
	From     struct {
		EmailAddress struct {
			Name    string `json:"name"`
			Address string `json:"address"`
		} `json:"emailAddress"`
	} `json:"from"`
}

func splitScopes(value string) []string {
	var scopes []string
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			scopes = append(scopes, part)
		}
	}
	return scopes
}

func truncate(value string, maxLen int) string {
	if len(value) <= maxLen {
		return value
	}
	if maxLen <= 3 {
		return value[:maxLen]
	}
	return value[:maxLen-3] + "..."
}

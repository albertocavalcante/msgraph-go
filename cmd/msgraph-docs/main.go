// Command msgraph-docs works with personal OneDrive documents through Graph.
// Word document editing remains a Word-session/add-in operation.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	msauth "github.com/albertocavalcante/msauth-go"
	"github.com/albertocavalcante/msauth-go/cache"
	msgraph "github.com/albertocavalcante/msgraph-go"
)

const documentLimit = 2 << 20

var itemID = regexp.MustCompile(`^[A-Za-z0-9_!.-]+$`)

type profile struct {
	Account   string `json:"account"`
	ClientID  string `json:"clientId,omitempty"`
	Authority string `json:"authority,omitempty"`
}
type item struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	WebURL string `json:"webUrl,omitempty"`
	File   *struct {
		MIMEType string `json:"mimeType"`
	} `json:"file,omitempty"`
}
type uploadPlan struct {
	Operation string `json:"operation"`
	Apply     bool   `json:"apply"`
	Account   string `json:"account"`
	Name      string `json:"name"`
	Bytes     int    `json:"bytes"`
	SHA256    string `json:"sha256"`
}

type command struct {
	profile profile
	timeout time.Duration
	write   bool
	action  string
	query   string
	id      string
	output  string
	format  string
	file    string
	name    string
	apply   bool
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func parse(args []string, stderr io.Writer) (command, error) {
	var c command
	root := flag.NewFlagSet("msgraph-docs", flag.ContinueOnError)
	root.SetOutput(stderr)
	var profilePath, account string
	root.StringVar(&profilePath, "profile", "", "private JSON account/client profile")
	root.StringVar(&account, "account", "", "expected Microsoft account username")
	root.DurationVar(&c.timeout, "timeout", 60*time.Second, "request timeout")
	root.BoolVar(&c.write, "write", false, "request Files.ReadWrite for explicit login")
	root.Usage = func() {
		_, _ = fmt.Fprintln(stderr, "Usage: msgraph-docs [--profile FILE] [--account USER] [--write] doctor|login|list|find|get|download|upload [flags]")
		root.PrintDefaults()
	}
	if err := root.Parse(args); err != nil {
		return c, err
	}
	if c.timeout <= 0 {
		return c, errors.New("timeout must be positive")
	}
	if profilePath != "" {
		var err error
		c.profile, err = readProfile(profilePath)
		if err != nil {
			return c, err
		}
	}
	if account != "" {
		c.profile.Account = account
	}
	if root.NArg() == 0 {
		root.Usage()
		return c, flag.ErrHelp
	}
	c.action = root.Arg(0)
	sub := flag.NewFlagSet(c.action, flag.ContinueOnError)
	sub.SetOutput(stderr)
	switch c.action {
	case "doctor", "login":
	case "find":
		sub.StringVar(&c.query, "query", "", "document search text (required)")
	case "list":
		sub.StringVar(&c.id, "id", "root", "parent folder item ID or root")
	case "get":
		sub.StringVar(&c.id, "id", "", "drive item ID (required)")
	case "download":
		sub.StringVar(&c.id, "id", "", "drive item ID (required)")
		sub.StringVar(&c.output, "out", "", "new local output file (required; never overwritten)")
		sub.StringVar(&c.format, "format", "original", "original or pdf")
	case "upload":
		sub.StringVar(&c.file, "file", "", "local document (required, at most 2 MiB)")
		sub.StringVar(&c.name, "name", "", "new filename in OneDrive root (required)")
		sub.BoolVar(&c.apply, "apply", false, "perform this upload; otherwise print an offline plan")
	default:
		return c, errors.New("unknown command; use doctor, login, list, find, get, download or upload")
	}
	if err := sub.Parse(root.Args()[1:]); err != nil {
		return c, err
	}
	if sub.NArg() != 0 {
		return c, errors.New("unexpected positional arguments")
	}
	if (c.action == "get" || c.action == "download" || c.action == "list") && (!itemID.MatchString(c.id) || c.id == "." || c.id == "..") {
		return c, errors.New("a valid item ID is required")
	}
	if c.action == "find" && (strings.TrimSpace(c.query) == "" || len(c.query) > 200) {
		return c, errors.New("search query must contain 1 to 200 bytes")
	}
	if c.action == "download" && (c.output == "" || (c.format != "original" && c.format != "pdf")) {
		return c, errors.New("download needs --out and --format original|pdf")
	}
	if c.action == "upload" && (c.file == "" || !validName(c.name)) {
		return c, errors.New("upload needs --file and a plain --name without path or control characters")
	}
	if (c.action != "upload" || c.apply) && strings.TrimSpace(c.profile.Account) == "" {
		return c, errors.New("network operations require an explicit --account or profile account")
	}
	if c.write && c.action != "login" && c.action != "doctor" {
		return c, errors.New("--write is only for login or doctor; upload requests its required scope")
	}
	return c, nil
}

func readProfile(path string) (profile, error) {
	var p profile
	f, err := os.Open(path)
	if err != nil {
		return p, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return p, err
	}
	if !stat.Mode().IsRegular() || stat.Size() > 16384 {
		return p, errors.New("profile must be a regular file of at most 16 KiB")
	}
	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return p, errors.New("invalid document profile JSON")
	}
	var tail any
	if decoder.Decode(&tail) != io.EOF {
		return p, errors.New("profile must contain a single JSON object")
	}
	if strings.TrimSpace(p.Account) == "" {
		return p, errors.New("profile account is required")
	}
	return p, nil
}

func validName(name string) bool {
	if strings.ContainsFunc(name, unicode.IsControl) {
		return false
	}
	return name != "" && name != "." && name != ".." && len(name) <= 200 && !strings.ContainsAny(name, "/\\:*?\"<>|#\r\n\t") && strings.TrimSpace(name) == name
}

func uploadData(c command) ([]byte, uploadPlan, error) {
	plan := uploadPlan{Operation: "upload", Apply: c.apply, Account: c.profile.Account, Name: c.name}
	f, err := os.Open(c.file)
	if err != nil {
		return nil, plan, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, plan, err
	}
	if !stat.Mode().IsRegular() {
		return nil, plan, errors.New("upload source must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, documentLimit+1))
	if err != nil {
		return nil, plan, err
	}
	if len(data) > documentLimit {
		return nil, plan, errors.New("document exceeds the 2 MiB conversion limit")
	}
	digest := sha256.Sum256(data)
	plan.Bytes, plan.SHA256 = len(data), hex.EncodeToString(digest[:])
	return data, plan, nil
}

func run(args []string, stdout, stderr io.Writer) error {
	c, err := parse(args, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	var data []byte
	if c.action == "upload" {
		var plan uploadPlan
		data, plan, err = uploadData(c)
		if err != nil {
			return err
		}
		if !c.apply {
			return writeJSON(stdout, plan)
		}
	}
	scope := "Files.Read"
	if c.write || c.action == "upload" {
		scope = "Files.ReadWrite"
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	store := &isolatedStore{
		primary: cache.NewKeychainStoreWithService("msgraph-docs-msauth"),
		seed:    cache.NewKeychainStore(),
	}
	cfg := msauth.Config{Scopes: []string{scope}, Account: c.profile.Account, ClientID: c.profile.ClientID, Authority: c.profile.Authority}
	if c.action == "login" {
		username, err := loginSafely(ctx, c.profile.Account, store, func(ctx context.Context, staged cache.Store) (string, error) {
			auth, err := msauth.New(cfg, staged)
			if err != nil {
				return "", err
			}
			identity, err := auth.Login(ctx)
			if err != nil {
				return "", err
			}
			return identity.Username, nil
		})
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]string{"account": username, "requestedScope": scope})
	}
	auth, err := msauth.New(cfg, store)
	if err != nil {
		return &safeFailure{message: "could not initialize Microsoft authentication", cause: err}
	}
	identity, err := auth.Identity(ctx)
	if err != nil {
		return authenticationError(err, scope)
	}
	if c.profile.Account != "" && !strings.EqualFold(identity.Username, c.profile.Account) {
		return errors.New("cached account does not match the configured account")
	}
	account, err := auth.Account(ctx)
	if err != nil {
		return authenticationError(err, scope)
	}
	auth, err = boundAuthenticator(cfg, store, account.Ref)
	if err != nil {
		return authenticationError(err, scope)
	}
	token, err := auth.TokenInfo(ctx)
	if err != nil {
		return authenticationError(err, scope)
	}
	graph, err := msgraph.New(auth.TokenSource(scope), msgraph.WithMaxResponseBytes(documentLimit))
	if err != nil {
		return err
	}
	return execute(ctx, c, graph, identity.Username, token.GrantedScopes, data, stdout)
}

func execute(ctx context.Context, c command, graph *msgraph.Client, username string, granted []string, data []byte, stdout io.Writer) error {
	switch c.action {
	case "list":
		path := "/me/drive/items/" + url.PathEscape(c.id) + "/children"
		if c.id == "root" {
			path = "/me/drive/root/children"
		}
		items, err := msgraph.Collect[item](ctx, graph, path, msgraph.Params{Select: []string{"id", "name", "size", "webUrl", "file"}, Top: 50}, msgraph.WithMaxPages(5))
		if err != nil {
			return graphError(err)
		}
		return writeJSON(stdout, items)
	case "doctor":
		drive, _, err := msgraph.Get[struct {
			ID   string `json:"id"`
			Type string `json:"driveType"`
		}](ctx, graph, "/me/drive", msgraph.Params{Select: []string{"id", "driveType"}})
		if err != nil {
			return graphError(err)
		}
		return writeJSON(stdout, struct {
			Account             string   `json:"account"`
			Scopes              []string `json:"grantedScopes"`
			DriveType           string   `json:"driveType"`
			DriveReady          bool     `json:"driveReady"`
			WordSessionVerified bool     `json:"wordSessionVerified"`
		}{username, granted, drive.Type, drive.ID != "", false})
	case "find":
		path := "/me/drive/root/search(q='" + url.PathEscape(strings.ReplaceAll(c.query, "'", "''")) + "')"
		items, err := msgraph.Collect[item](ctx, graph, path, msgraph.Params{Select: []string{"id", "name", "size", "webUrl", "file"}, Top: 25}, msgraph.WithMaxPages(5))
		if err != nil {
			return graphError(err)
		}
		return writeJSON(stdout, items)
	case "get":
		result, _, err := msgraph.Get[item](ctx, graph, "/me/drive/items/"+url.PathEscape(c.id), msgraph.Params{Select: []string{"id", "name", "size", "webUrl", "file"}})
		if err != nil {
			return graphError(err)
		}
		return writeJSON(stdout, result)
	case "download":
		params := msgraph.Params{}
		if c.format == "pdf" {
			params.Custom = url.Values{"format": {"pdf"}}
		}
		if err := downloadFile(ctx, graph, "/me/drive/items/"+url.PathEscape(c.id)+"/content", params, c.output); err != nil {
			return graphError(err)
		}
		return writeJSON(stdout, map[string]string{"output": c.output, "format": c.format})
	case "upload":
		path := "/me/drive/root:/" + url.PathEscape(c.name)
		_, _, err := msgraph.Get[item](ctx, graph, path, msgraph.Params{})
		if err == nil {
			return errors.New("destination exists; choose a new filename")
		}
		if !msgraph.IsNotFound(err) {
			return graphError(err)
		}
		result, _, err := msgraph.Do[item](ctx, graph, msgraph.Request{Method: http.MethodPut, URL: path + ":/content", Query: url.Values{"@microsoft.graph.conflictBehavior": {"fail"}}, IfNoneMatch: "*", RawBody: data, ContentType: "application/octet-stream"})
		if err != nil {
			return graphError(err)
		}
		return writeJSON(stdout, result)
	}
	return nil
}

func graphError(err error) error {
	var api *msgraph.APIError
	if errors.As(err, &api) {
		return &safeFailure{message: fmt.Sprintf("microsoft graph request failed: status %d, code %s", api.StatusCode, api.Code), cause: err}
	}
	return err
}

func downloadFile(ctx context.Context, graph *msgraph.Client, path string, params msgraph.Params, output string) error {
	f, err := os.CreateTemp(filepath.Dir(output), ".msgraph-document-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	defer f.Close()
	if _, err := graph.Download(ctx, path, params, f); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// Linking publishes a complete file without ever replacing an existing path.
	return os.Link(f.Name(), output)
}

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

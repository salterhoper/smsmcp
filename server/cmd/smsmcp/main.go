// Command smsmcp is the SMS gateway server and its admin CLI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/salterhoper/smsmcp/server/internal/api"
	"github.com/salterhoper/smsmcp/server/internal/config"
	"github.com/salterhoper/smsmcp/server/internal/hub"
	"github.com/salterhoper/smsmcp/server/internal/mcpserver"
	"github.com/salterhoper/smsmcp/server/internal/outbox"
	"github.com/salterhoper/smsmcp/server/internal/pairing"
	"github.com/salterhoper/smsmcp/server/internal/store"
)

// version is set at build time: -ldflags "-X main.version=v0.1.0"
var version = "dev"

const usage = `smsmcp - give AI agents SMS through your Android phone

Usage:
  smsmcp init                    first-run setup: API key, pairing QR, agent config
  smsmcp serve                   run the server (MCP + phone gateway)
  smsmcp stdio                   run MCP over stdio for a local agent
  smsmcp pair                    print a new pairing QR for a phone
  smsmcp devices [disable <id>]  list or disable paired phones
  smsmcp keys create <name>      create an API key for an agent
  smsmcp keys list | revoke <id>
  smsmcp webhooks add <url>      send SMS events to a URL
  smsmcp webhooks list
  smsmcp version

Global flags (or env): -data (SMSMCP_DATA), -addr (SMSMCP_ADDR), -public-url (SMSMCP_PUBLIC_URL)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]

	cfg := config.Default()
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.StringVar(&cfg.DataDir, "data", cfg.DataDir, "data directory")
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "listen address")
	fs.StringVar(&cfg.PublicURL, "public-url", cfg.PublicURL, "URL phones use to reach this server")
	fs.Parse(args)
	args = fs.Args()

	var err error
	switch cmd {
	case "serve":
		err = serve(cfg)
	case "stdio":
		err = stdio(cfg)
	case "init":
		err = initCmd(cfg)
	case "pair":
		err = withStore(cfg, func(st *store.Store) error { return printPairing(st, cfg) })
	case "devices":
		err = withStore(cfg, func(st *store.Store) error { return devicesCmd(st, args) })
	case "keys":
		err = withStore(cfg, func(st *store.Store) error { return keysCmd(st, args) })
	case "webhooks":
		err = withStore(cfg, func(st *store.Store) error { return webhooksCmd(st, args) })
	case "version", "-v", "--version":
		fmt.Println(version)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func withStore(cfg config.Config, fn func(*store.Store) error) error {
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()
	return fn(st)
}

func serve(cfg config.Config) error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	h := hub.New(st, log)
	go h.Run(ctx)
	go outbox.New(st, log).Run(ctx)

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           api.Router(api.Deps{Store: st, Hub: h, MCP: &mcpserver.Server{Store: st, Notify: h, Version: version}, Version: version}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()
	log.Info("smsmcp listening", "addr", cfg.Addr, "public_url", cfg.ResolvedPublicURL(), "data", cfg.DataDir, "version", version)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// stdio serves MCP on stdin/stdout. It shares the database with `serve`,
// whose dispatcher polls for new messages, so the server must be running for
// SMS to actually leave the phone.
func stdio(cfg config.Config) error {
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return err
	}
	defer st.Close()
	srv := (&mcpserver.Server{Store: st, Version: version}).New()
	return srv.Run(context.Background(), &mcp.StdioTransport{})
}

func initCmd(cfg config.Config) error {
	return withStore(cfg, func(st *store.Store) error {
		ctx := context.Background()
		keys, err := st.ListAPIKeys(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("Data directory: %s\n\n", cfg.DataDir)
		if len(keys) == 0 {
			_, secret, err := st.CreateAPIKey(ctx, "default")
			if err != nil {
				return err
			}
			fmt.Println("Created API key (shown once, store it somewhere safe):")
			fmt.Printf("  %s\n\n", secret)
			fmt.Println("Connect an agent:")
			fmt.Printf("  claude mcp add --transport http smsmcp %s/mcp --header \"Authorization: Bearer %s\"\n\n", cfg.ResolvedPublicURL(), secret)
		} else {
			fmt.Println("API keys already exist; create another with `smsmcp keys create <name>`.")
			fmt.Println()
		}
		return printPairing(st, cfg)
	})
}

func printPairing(st *store.Store, cfg config.Config) error {
	link, err := pairing.NewLink(context.Background(), st, cfg.ResolvedPublicURL())
	if err != nil {
		return err
	}
	fmt.Println("Pair a phone: open the smsmcp Gateway app and scan this code.")
	fmt.Printf("It expires in %d minutes and works once.\n\n", int(pairing.TokenTTL.Minutes()))
	qrterminal.GenerateHalfBlock(link, qrterminal.L, os.Stdout)
	fmt.Printf("\nOr paste this link into the app:\n  %s\n\n", link)
	fmt.Printf("The phone must be able to reach %s. Set -public-url if that's wrong.\n", cfg.ResolvedPublicURL())
	return nil
}

func devicesCmd(st *store.Store, args []string) error {
	ctx := context.Background()
	if len(args) == 2 && args[0] == "disable" {
		return st.SetDeviceStatus(ctx, args[1], store.DeviceDisabled)
	}
	devs, err := st.ListDevices(ctx)
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		fmt.Println("No phones paired yet. Run `smsmcp pair`.")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tNUMBER\tSTATUS\tLAST SEEN")
	for _, d := range devs {
		seen := "never"
		if d.LastSeenAt > 0 {
			seen = time.UnixMilli(d.LastSeenAt).Format(time.DateTime)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", d.ID, d.Name, d.PhoneNumber, d.Status, seen)
	}
	return tw.Flush()
}

func keysCmd(st *store.Store, args []string) error {
	ctx := context.Background()
	switch {
	case len(args) == 2 && args[0] == "create":
		k, secret, err := st.CreateAPIKey(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Created %s (%s). Shown once:\n  %s\n", k.ID, k.Name, secret)
		return nil
	case len(args) == 2 && args[0] == "revoke":
		return st.RevokeAPIKey(ctx, args[1])
	case len(args) == 0 || (len(args) == 1 && args[0] == "list"):
		keys, err := st.ListAPIKeys(ctx)
		if err != nil {
			return err
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tPREFIX\tCREATED\tREVOKED")
		for _, k := range keys {
			rev := ""
			if k.RevokedAt > 0 {
				rev = time.UnixMilli(k.RevokedAt).Format(time.DateTime)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s…\t%s\t%s\n", k.ID, k.Name, k.Prefix, time.UnixMilli(k.CreatedAt).Format(time.DateTime), rev)
		}
		return tw.Flush()
	}
	return errors.New("usage: smsmcp keys create <name> | list | revoke <id>")
}

func webhooksCmd(st *store.Store, args []string) error {
	ctx := context.Background()
	switch {
	case len(args) == 2 && args[0] == "add":
		e, err := st.AddWebhookEndpoint(ctx, args[1])
		if err != nil {
			return err
		}
		fmt.Printf("Added %s -> %s\nSigning secret (verify Webhook-Signature with it):\n  %s\n", e.ID, e.URL, e.Secret)
		return nil
	case len(args) == 0 || (len(args) == 1 && args[0] == "list"):
		eps, err := st.ListWebhookEndpoints(ctx)
		if err != nil {
			return err
		}
		for _, e := range eps {
			fmt.Printf("%s  %s\n", e.ID, e.URL)
		}
		return nil
	}
	return errors.New("usage: smsmcp webhooks add <url> | list")
}

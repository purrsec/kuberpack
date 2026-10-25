package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

var Version = "dev"

type Options struct {
	Admin           bool
	Install, Doctor func([]string) error
}
type App struct {
	forgeURL string
	Options
	In                                                                     io.Reader
	Out, Err                                                               io.Writer
	WorkDir, ConfigDir                                                     string
	Interactive                                                            bool
	config                                                                 configuration
	ctx                                                                    context.Context
	reader                                                                 *bufio.Reader
	contextName, project, service, output, caFile, resolvedContext, secret string
	nonInteractive, yes                                                    bool
	timeout                                                                time.Duration
}
type exitError struct {
	code    int
	message string
}

// resultError carries a failed operation into the single JSON error document.
type resultError struct {
	error
	result any
}

func (e *resultError) Unwrap() error { return e.error }

func (e *exitError) Error() string { return e.message }
func fail(code int, format string, args ...any) error {
	return &exitError{code, fmt.Sprintf(format, args...)}
}
func Code(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return 6
	}
	var e *exitError
	if errors.As(err, &e) {
		return e.code
	}
	return 1
}

func Main(options Options) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	a := &App{Options: options, In: os.Stdin, Out: os.Stdout, Err: os.Stderr, Interactive: term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))}
	err := a.Run(ctx, os.Args[1:])
	if err != nil {
		a.reportError(err)
	}
	return Code(err)
}

func (a *App) reportError(err error) {
	if a.output == "json" {
		value := map[string]any{"error": map[string]any{"code": Code(err), "message": clean(err.Error(), a.secret)}}
		var result *resultError
		if errors.As(err, &result) {
			value["result"] = result.result
		}
		_ = a.print(value)
	} else {
		fmt.Fprintln(a.Err, clean(err.Error(), a.secret))
	}
}
func (a *App) Run(ctx context.Context, args []string) error {
	a.ctx = ctx
	if a.In == nil {
		a.In = os.Stdin
	}
	if a.Out == nil {
		a.Out = os.Stdout
	}
	if a.Err == nil {
		a.Err = os.Stderr
	}
	a.reader = bufio.NewReader(a.In)
	if a.WorkDir == "" {
		d, err := os.Getwd()
		if err != nil {
			return err
		}
		a.WorkDir = d
	}
	a.WorkDir = filepath.Clean(a.WorkDir)
	if err := a.loadConfig(); err != nil {
		return err
	}
	root := a.command()
	root.SetArgs(args)
	root.SetIn(a.In)
	root.SetOut(a.Out)
	root.SetErr(a.Err)
	err := root.ExecuteContext(ctx)
	if err != nil {
		var e *exitError
		if errors.As(err, &e) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if strings.Contains(err.Error(), "unknown command") || strings.Contains(err.Error(), "unknown flag") || strings.Contains(err.Error(), "arg(s)") {
			return fail(2, "%s", err)
		}
	}
	return err
}
func (a *App) command() *cobra.Command {
	name := "kuberpack"
	if a.Admin {
		name = "kuberpack-admin"
	}
	r := &cobra.Command{Use: name, Short: "Deploy and observe your services through Kuberpack", SilenceUsage: true, SilenceErrors: true, Version: Version, Args: cobra.NoArgs}
	r.PersistentFlags().StringVar(&a.contextName, "context", "", "Connection name")
	r.PersistentFlags().StringVar(&a.project, "project", "", "Project name (otherwise linked or selected)")
	r.PersistentFlags().StringVar(&a.service, "service", "", "Service name (otherwise linked or selected)")
	r.PersistentFlags().StringVar(&a.output, "output", "text", "Output: text or json")
	r.PersistentFlags().StringVar(&a.caFile, "ca-file", "", "Additional trusted CA bundle")
	r.PersistentFlags().BoolVar(&a.nonInteractive, "non-interactive", false, "Never prompt")
	r.PersistentFlags().BoolVar(&a.yes, "yes", false, "Accept a fully specified action")
	r.PersistentFlags().DurationVar(&a.timeout, "timeout", 15*time.Minute, "Maximum wait duration")
	r.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if a.output != "text" && a.output != "json" {
			return fail(2, "--output must be text or json")
		}
		if a.timeout <= 0 {
			return fail(2, "--timeout must be positive")
		}
		if a.yes && cmd.Annotations["confirm"] != "true" {
			return fail(2, "--yes is not supported by this command")
		}
		return nil
	}
	r.RunE = func(cmd *cobra.Command, _ []string) error {
		if a.interactive() && !a.Admin {
			return a.wizard()
		}
		return cmd.Help()
	}
	r.AddCommand(a.loginCommand(), a.contextCommand(), a.logoutCommand())
	r.AddCommand(&cobra.Command{Use: "whoami", Short: "Show this credential and its permissions", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		_, session, err := a.connect()
		if err != nil {
			return err
		}
		return a.print(session)
	}})
	r.AddCommand(&cobra.Command{Use: "version", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return a.print(map[string]string{"version": Version}) }})
	completion := &cobra.Command{Use: "completion [bash|zsh|fish|powershell]", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return r.GenBashCompletion(a.Out)
		case "zsh":
			return r.GenZshCompletion(a.Out)
		case "fish":
			return r.GenFishCompletion(a.Out, true)
		case "powershell":
			return r.GenPowerShellCompletion(a.Out)
		}
		return fail(2, "unsupported shell")
	}}
	r.AddCommand(completion)
	if a.Admin {
		a.adminCommands(r)
	} else {
		r.AddCommand(&cobra.Command{Use: "wizard", Short: "Open the guided task assistant", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error { return a.wizard() }})
		r.AddCommand(a.initCommand(), a.linkCommand(), a.unlinkCommand(), a.deployCommand(false), a.deployCommand(true))
		a.readCommands(r)
	}
	return r
}

// Cobra commands use an annotation rather than letting --yes select missing
// fields. Keeping this check central prevents silent confirmation creep.
func confirms(c *cobra.Command) *cobra.Command {
	if c.Annotations == nil {
		c.Annotations = map[string]string{}
	}
	c.Annotations["confirm"] = "true"
	return c
}
func (a *App) interactive() bool {
	return a.Interactive && !a.nonInteractive && a.output != "json" && os.Getenv("CI") == ""
}
func (a *App) ask(label, def string) (string, error) {
	if !a.interactive() {
		if def != "" {
			return def, nil
		}
		return "", fail(2, "%s is required in non-interactive mode", label)
	}
	if err := a.ctx.Err(); err != nil {
		return "", err
	}
	fmt.Fprint(a.Err, label)
	if def != "" {
		fmt.Fprintf(a.Err, " [%s]", clean(def, a.secret))
	}
	fmt.Fprint(a.Err, ": ")
	type inputLine struct {
		line string
		err  error
	}
	lines := make(chan inputLine, 1)
	go func() { line, err := a.reader.ReadString('\n'); lines <- inputLine{line, err} }()
	var line string
	select {
	case <-a.ctx.Done():
		return "", a.ctx.Err()
	case result := <-lines:
		if result.err != nil {
			return "", fail(2, "input closed before %s was provided", label)
		}
		line = result.line
	}
	if strings.ContainsRune(line, '\x1b') {
		return "", fail(2, "cancelled; the unfinished form was not saved")
	}

	line = strings.TrimSpace(line)
	if line == "" {
		line = def
	}
	if line == "" {
		return "", fail(2, "%s is required", label)
	}
	return line, nil
}
func (a *App) confirm(message string) error {
	if a.yes {
		return nil
	}
	if !a.interactive() {
		return fail(2, "confirmation required; review the plan and supply --yes")
	}
	v, err := a.ask(message+" [y/N]", "n")
	if err != nil {
		return err
	}
	if strings.ToLower(v) != "y" && strings.ToLower(v) != "yes" {
		return fail(2, "cancelled; no remote action was requested")
	}
	return nil
}
func (a *App) selectOne(label string, items []string) (string, error) {
	if len(items) == 0 {
		return "", fail(2, "no %s available", label)
	}
	if len(items) == 1 {
		return items[0], nil
	}
	if !a.interactive() {
		return "", fail(2, "ambiguous %s; specify it explicitly", label)
	}
	for i, v := range items {
		fmt.Fprintf(a.Err, "  %d. %s\n", i+1, clean(v, a.secret))
	}
	value, err := a.ask(label, "")
	if err != nil {
		return "", err
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > len(items) {
		return "", fail(2, "choose a number from 1 to %d", len(items))
	}
	return items[n-1], nil
}

var terminalEscape = regexp.MustCompile(`\x1b(?:\][^\x07]*(?:\x07|\x1b\\)|\[[0-?]*[ -/]*[@-~]|.)`)

func clean(s, secret string) string {
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	s = terminalEscape.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		if r == 127 {
			return -1
		}
		return r
	}, s)
}
func (a *App) print(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	var data any
	if err := json.Unmarshal(b, &data); err != nil {
		return err
	}
	var sanitize func(any) any
	sanitize = func(v any) any {
		switch x := v.(type) {
		case string:
			return clean(x, a.secret)
		case []any:
			for i := range x {
				x[i] = sanitize(x[i])
			}
		case map[string]any:
			for k, val := range x {
				x[k] = sanitize(val)
			}
		}
		return v
	}
	data = sanitize(data)
	if a.output == "json" {
		return json.NewEncoder(a.Out).Encode(data)
	}
	b, err = yaml.Marshal(data)
	if err != nil {
		return err
	}
	_, err = a.Out.Write(b)
	return err
}

package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// runToken administre les jetons API. Le jeton global KUBERPACK_API_TOKEN
// reste administrateur ; un jeton créé avec --project est limité à ce projet.
func runToken(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: kuberpack token create|list|revoke [flags]")
	}
	switch args[0] {
	case "create":
		return runTokenCreate(args[1:])
	case "list":
		return runTokenList(args[1:])
	case "revoke":
		return runTokenRevoke(args[1:])
	default:
		return fmt.Errorf("unknown token subcommand %q (create, list, revoke)", args[0])
	}
}

type createdAPIToken struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Project   string `json:"project"`
	Role      string `json:"role"`
	Token     string `json:"token"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}

// runTokenCreate crée un jeton et affiche son secret une seule fois.
func runTokenCreate(args []string) error {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	name := fs.String("name", "", "nom du jeton (obligatoire)")
	project := fs.String("project", "", "projet auquel le jeton est limité (vide : administrateur)")
	expires := fs.String("expires", "", "durée de validité Go, par exemple 720h (vide : jamais)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: kuberpack token create --name NAME [--project PROJECT] [--expires 720h]")
	}
	if strings.TrimSpace(*name) == "" {
		return fmt.Errorf("--name is required")
	}
	payload := map[string]any{"name": strings.TrimSpace(*name)}
	if value := strings.TrimSpace(*project); value != "" {
		payload["project"] = value
	}
	if value := strings.TrimSpace(*expires); value != "" {
		// La durée est validée localement : une valeur invalide ne doit pas
		// créer un jeton sans expiration par erreur.
		if duration, err := time.ParseDuration(value); err != nil || duration <= 0 {
			return fmt.Errorf("--expires must be a positive duration, for example 720h")
		}
		payload["expires_in"] = value
	}
	body, err := apiRequest(http.MethodPost, "/api/v1/tokens", payload)
	if err != nil {
		return err
	}
	var out createdAPIToken
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("the platform answered something unexpected: %w", err)
	}
	if strings.TrimSpace(out.Token) == "" {
		return fmt.Errorf("the platform did not return a token")
	}
	fmt.Fprintf(os.Stderr, "Ce secret ne sera plus affiché : conservez-le maintenant.\n")
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "TOKEN\t%s\n", out.Token)
	fmt.Fprintf(w, "ID\t%d\n", out.ID)
	fmt.Fprintf(w, "NAME\t%s\n", out.Name)
	if out.Project != "" {
		fmt.Fprintf(w, "PROJECT\t%s\n", out.Project)
	}
	fmt.Fprintf(w, "ROLE\t%s\n", out.Role)
	if out.ExpiresAt != "" {
		fmt.Fprintf(w, "EXPIRES\t%s\n", out.ExpiresAt)
	}
	return w.Flush()
}

// tokenSummary est la vue publique d'un jeton : jamais d'empreinte ni de
// secret.
type tokenSummary struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Project    string `json:"project"`
	Role       string `json:"role"`
	Status     string `json:"status"`
	CreatedAt  string `json:"created_at"`
	LastUsedAt string `json:"last_used_at"`
	ExpiresAt  string `json:"expires_at"`
	RevokedAt  string `json:"revoked_at"`
}

// runTokenList affiche les jetons existants.
func runTokenList(args []string) error {
	fs := flag.NewFlagSet("token list", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("usage: kuberpack token list")
	}
	body, err := apiRequest(http.MethodGet, "/api/v1/tokens", nil)
	if err != nil {
		return err
	}
	var out struct {
		Tokens []tokenSummary `json:"tokens"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("the platform answered something unexpected: %w", err)
	}
	if len(out.Tokens) == 0 {
		fmt.Println("aucun jeton API")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tNAME\tPROJECT\tROLE\tCREATED\tEXPIRES\tLAST_USED\tSTATUS")
	for _, token := range out.Tokens {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			token.ID, token.Name, token.Project, token.Role,
			token.CreatedAt, orDash(token.ExpiresAt), orDash(token.LastUsedAt), token.Status)
	}
	return w.Flush()
}

// runTokenRevoke invalide un jeton.
func runTokenRevoke(args []string) error {
	fs := flag.NewFlagSet("token revoke", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: kuberpack token revoke <id>")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(fs.Arg(0)), 10, 64)
	if err != nil || id <= 0 {
		return fmt.Errorf("the token id must be a positive number")
	}
	if _, err := apiRequest(http.MethodDelete, fmt.Sprintf("/api/v1/tokens/%d", id), nil); err != nil {
		return err
	}
	fmt.Printf("jeton %d révoqué\n", id)
	return nil
}

func orDash(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// apiRequest appelle l'API avec le jeton de l'environnement.
func apiRequest(method, path string, payload any) ([]byte, error) {
	token := apiToken()
	if token == "" {
		return nil, fmt.Errorf("KUBERPACK_API_TOKEN is required")
	}
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, apiBase()+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	return out, nil
}

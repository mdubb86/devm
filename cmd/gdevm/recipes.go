package main

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const recipesEndpoint = "http://192.168.127.1:81/recipes"

// recipesMain implements `gdevm recipes <sub> [args...]`. Dispatches to
// runRecipes with the softnet base endpoint; runRecipes is the
// test-driven seam.
func recipesMain(args []string) int {
	return runRecipes(recipesEndpoint, args)
}

// runRecipes routes the subcommand and calls the endpoint-specific
// runner. baseEndpoint is the recipes base URL — each subcommand
// appends its own sub-path and query string.
func runRecipes(baseEndpoint string, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "gdevm recipes: subcommand required (list|get|asset)")
		return 2
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		return runRecipesList(baseEndpoint, rest)
	case "get":
		return runRecipesGet(baseEndpoint, rest)
	case "asset":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "gdevm recipes asset: subcommand required (ls|get)")
			return 2
		}
		asub := rest[0]
		arest := rest[1:]
		switch asub {
		case "ls":
			return runRecipesAssetLs(baseEndpoint, arest)
		case "get":
			return runRecipesAssetGet(baseEndpoint, arest)
		default:
			fmt.Fprintf(os.Stderr, "gdevm recipes asset: unknown subcommand %q\n", asub)
			return 2
		}
	default:
		fmt.Fprintf(os.Stderr, "gdevm recipes: unknown subcommand %q\n", sub)
		return 2
	}
}

func runRecipesList(base string, args []string) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "gdevm recipes list: no arguments accepted")
		return 2
	}
	return recipesGET("list", base+"/list")
}

func runRecipesGet(base string, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "gdevm recipes get: exactly one argument required (name)")
		return 2
	}
	q := url.Values{"name": {args[0]}}
	return recipesGET("get", base+"/get?"+q.Encode())
}

func runRecipesAssetLs(base string, args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "gdevm recipes asset ls: exactly one argument required (name)")
		return 2
	}
	q := url.Values{"name": {args[0]}}
	return recipesGET("asset ls", base+"/asset/ls?"+q.Encode())
}

func runRecipesAssetGet(base string, args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "gdevm recipes asset get: exactly two arguments required (name path)")
		return 2
	}
	q := url.Values{"name": {args[0]}, "path": {args[1]}}
	return recipesGET("asset get", base+"/asset/get?"+q.Encode())
}

// recipesGET performs an HTTP GET and streams a 200 body to stdout, or
// maps the daemon's status code onto the recipes exit-code contract:
// 400 → 2 (caller supplied invalid input), 404 → 3 (daemon rejected
// valid input), transport / other → 1.
func recipesGET(label, endpoint string) int {
	client := &http.Client{Timeout: 60 * time.Second}
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm recipes %s: %v\n", label, err)
		return 1
	}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "gdevm recipes %s: cannot reach devm daemon on 192.168.127.1:81 — is the VM properly started?\n%v\n", label, err)
		return 1
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		if _, err := io.Copy(os.Stdout, resp.Body); err != nil {
			fmt.Fprintf(os.Stderr, "gdevm recipes %s: %v\n", label, err)
			return 1
		}
		return 0
	case http.StatusBadRequest:
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "gdevm recipes %s: %s\n", label, strings.TrimSpace(string(body)))
		return 2
	case http.StatusNotFound:
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "gdevm recipes %s: %s\n", label, strings.TrimSpace(string(body)))
		return 3
	default:
		body, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "gdevm recipes %s: daemon returned %d: %s\n", label, resp.StatusCode, strings.TrimSpace(string(body)))
		return 1
	}
}

package main

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"flag"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/google/go-github/v55/github"
)

//go:embed usage.txt
var usage string

//go:embed copy-github-to-github.service
var unit string

func main() {
	fs := flag.NewFlagSet("global", flag.ContinueOnError)
	srcAccessTokenFlag := fs.String("src-token", "", "Personal access token for pulling from github.com")
	srcURLFlag := fs.String("src-url", "", "URL of source organization or repo, e.g. https://github.com/org")
	tgtAccessTokenFlag := fs.String("tgt-token", "", "Personal access token for pushing to Github Enterprise")
	tgtURLFlag := fs.String("tgt-url", "", "URL of target org to push to, e.g. https://github.enterprise.com/org")
	tgtVisibilityFlag := fs.String("tgt-visibility", "public", "Set the visibility of new repos created, can be public, internal or private")
	everyFlag := fs.Duration("every", time.Duration(0), "If set, keep running, and sync again after a delay.")
	printSystemdUnitFlag := fs.Bool("print-systemd-unit", false, "Set to true to output the systemd unit file instead of running the program")
	helpFlag := fs.Bool("help", false, "Show help.")
	fs.Parse(os.Args[1:])
	if *helpFlag {
		fmt.Print(usage)
		fs.PrintDefaults()
		os.Exit(0)
	}

	var errors []string
	if *srcAccessTokenFlag == "" {
		errors = append(errors, "Missing src-token flag")
	}
	if *srcURLFlag == "" {
		errors = append(errors, "Missing src-url flag")
	}
	if *tgtAccessTokenFlag == "" {
		errors = append(errors, "Missing tgt-token flag")
	}
	if *tgtURLFlag == "" {
		errors = append(errors, "Missing tgt-url flag")
	}
	if msg := isOneOf(*tgtVisibilityFlag, "public", "internal", "private"); msg != "" {
		errors = append(errors, "tgt-visibility: "+msg)
	}
	if len(errors) > 0 {
		fmt.Println("Invalid or missing params:")
		fmt.Println("\n -" + strings.Join(errors, "\n -"))
		os.Exit(1)
	}

	if *printSystemdUnitFlag {
		cmd := new(strings.Builder)
		cmd.WriteString("/usr/local/bin/copy-github-to-github")
		cmd.WriteString(" -src-token ")
		cmd.WriteString(*srcAccessTokenFlag)
		cmd.WriteString(" -src-url ")
		cmd.WriteString(*srcURLFlag)
		cmd.WriteString(" -tgt-token ")
		cmd.WriteString(*tgtAccessTokenFlag)
		cmd.WriteString(" -tgt-url ")
		cmd.WriteString(*tgtURLFlag)
		cmd.WriteString(" -tgt-visibility ")
		cmd.WriteString(*tgtVisibilityFlag)
		if *everyFlag > time.Duration(0) {
			cmd.WriteString(" -every ")
			cmd.WriteString((*everyFlag).String())
		}
		unit = strings.Replace(unit, "$CMD", cmd.String(), -1)
		fmt.Println(unit)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT)
	go func() {
		<-sig
		cancel()
	}()

loop:
	for {
		fmt.Printf("Listing repos for URL: %v\n", *srcURLFlag)
		repos, err := listRepos(ctx, *srcURLFlag, *srcAccessTokenFlag)
		if err != nil {
			fmt.Printf("Failed to list repos: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Copying %d repos.\n", len(repos))

		for _, repo := range repos {
			tgt, err := rewriteURL(repo, *tgtURLFlag)
			if err != nil {
				fmt.Printf("Failed to rewrite URL %q: %v\n", repo.URL, err)
				os.Exit(1)
			}
			fmt.Printf("Copying %q to %q...\n", repo.URL, tgt)
			if err = copy(ctx, *srcAccessTokenFlag, repo.URL, *tgtAccessTokenFlag, tgt, *tgtVisibilityFlag); err != nil {
				fmt.Printf("Failed to copy: %v\n", err)
				os.Exit(1)
			}
		}

		if *everyFlag == time.Duration(0) {
			break loop
		}
		fmt.Printf("Process complete. Running again in %v.\n", *everyFlag)
		select {
		case <-ctx.Done():
			fmt.Printf("Context closed.\n")
			break loop
		case <-time.After(*everyFlag):
			fmt.Printf("Wait complete.\n")
		}
	}
}

func isOneOf(v string, allowed ...string) (msg string) {
	for _, vv := range allowed {
		if v == vv {
			return ""
		}
	}
	return fmt.Sprintf("value %q was not one of the allowed values: %v", v, strings.Join(quoteAll(allowed), ", "))
}

func quoteAll(v []string) (vv []string) {
	vv = make([]string, len(v))
	for i, v := range v {
		vv[i] = fmt.Sprintf("%q", v)
	}
	return vv
}

func rewriteURL(r Repo, tgt string) (updated string, err error) {
	tgtURL, err := url.Parse(tgt)
	if err != nil {
		return updated, fmt.Errorf("failed to parse target URL: %w", err)
	}
	org := strings.Split(strings.Trim(tgtURL.Path, "/"), "/")[0]
	tgtURL = &url.URL{
		Scheme:  tgtURL.Scheme,
		Host:    tgtURL.Host,
		Path:    "/" + strings.Join([]string{org, r.Name}, "/"),
		RawPath: "/" + strings.Join([]string{org, r.Name}, "/"),
	}
	return tgtURL.String(), nil
}

type Repo struct {
	Name string
	URL  string
}

func listRepos(ctx context.Context, ghURL, token string) (repos []Repo, err error) {
	u, err := url.Parse(ghURL)
	if err != nil {
		return repos, fmt.Errorf("failed to parse url: %w", err)
	}
	segments := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(segments) == 1 {
		return listReposForOrg(ctx, u, token)
	}
	if len(segments) > 2 {
		return repos, fmt.Errorf("unexpected number of path segments in URL, expected /<org> or /<org>/<repo>, got %q", ghURL)
	}
	repos = append(repos, Repo{
		Name: segments[1],
		URL:  ghURL,
	})
	return repos, nil
}

func listReposForOrg(ctx context.Context, ghURL *url.URL, token string) (repos []Repo, err error) {
	// Create the client.
	host := strings.ToLower(ghURL.Hostname())
	client := github.NewClient(nil).WithAuthToken(token)
	if host != "github.com" {
		client, err = client.WithEnterpriseURLs(ghURL.Scheme+"://"+host, ghURL.Scheme+"://"+host)
		if err != nil {
			return repos, fmt.Errorf("failed to set enterprise domain: %w", err)
		}
	}
	// Get the org name.
	org := strings.Split(strings.Trim(ghURL.Path, "/"), "/")[0]

	var pageIndex int
	for {
		r, _, err := client.Repositories.ListByOrg(ctx, org, &github.RepositoryListByOrgOptions{
			Sort:      "updated",
			Direction: "desc",
			ListOptions: github.ListOptions{
				Page:    pageIndex,
				PerPage: 100,
			},
		})
		if err != nil {
			return repos, fmt.Errorf("failed to list repos: %w", err)
		}
		if len(r) == 0 {
			break
		}
		for _, rr := range r {
			repos = append(repos, Repo{
				Name: rr.GetName(),
				URL:  rr.GetHTMLURL(),
			})
		}
		pageIndex++
	}
	return repos, nil
}

func copy(ctx context.Context, srcAccessToken, src, tgtAccessToken, tgt, tgtVisibility string) error {
	// Clone to local.
	dir, err := os.MkdirTemp(os.TempDir(), "src_repo_")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	repo, err := git.PlainClone(dir, false, &git.CloneOptions{
		URL: src,
		Auth: &http.BasicAuth{
			Username: "git",
			Password: srcAccessToken,
		},
		Progress: os.Stdout,
	})
	if err != nil {
		return fmt.Errorf("failed to clone: %w", err)
	}

	// Get the enterprise domain.
	u, err := url.Parse(tgt)
	if err != nil {
		return fmt.Errorf("failed to parse url: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	client := github.NewClient(nil).WithAuthToken(tgtAccessToken)
	if host != "github.com" {
		client, err = client.WithEnterpriseURLs(u.Scheme+"://"+host, u.Scheme+"://"+host)
		if err != nil {
			return fmt.Errorf("failed to set enterprise domain: %w", err)
		}
	}

	// Get the name.
	owner, name := path.Split(u.Path)
	owner = strings.Trim(owner, "/")
	name = strings.Trim(name, "/")
	_, _, err = client.Repositories.Create(ctx, owner, &github.Repository{
		Name:        &name,
		Description: ptr(fmt.Sprintf("Mirror of %s", src)),
		Visibility:  &tgtVisibility,
	})
	if err != nil && !strings.Contains(err.Error(), "name already exists on this account") {
		return fmt.Errorf("failed to create target repo: %w", err)
	}

	// Push to target.
	err = repo.Push(&git.PushOptions{
		RemoteURL: tgt,
		Auth: &http.BasicAuth{
			Username: "git",
			Password: tgtAccessToken,
		},
		Force:      true,
		FollowTags: true,
		Progress:   os.Stdout,
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("failed to push to target: %w", err)
	}

	return nil
}

func ptr[T any](v T) *T {
	return &v
}

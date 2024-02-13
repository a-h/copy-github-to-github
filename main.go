package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"

	"flag"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/google/go-github/v55/github"
)

const usage = `Copy a Github repo or full organization between accounts.

  copy-github-to-github repo -src-token <TOKEN> -src-url <https://github.com/ORG/REPO> -tgt-token <TOKEN> -tgt-url <https://github.enterprise.com/ORG/REPO>
  copy-github-to-github org -src-token <TOKEN> -src-url <https://github.com/ORG> -tgt-token <TOKEN> -tgt-url <https://github.enterprise.com/ORG>
`

func main() {
	fs := flag.NewFlagSet("global", flag.ContinueOnError)
	helpFlag := fs.Bool("help", false, "Show help.")
	fs.Parse(os.Args)
	if *helpFlag || len(os.Args) == 0 {
		fmt.Print(usage)
		os.Exit(0)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT)
	go func() {
		<-sig
		cancel()
	}()

	subcommand := os.Args[1]
	switch subcommand {
	case "repo":
		RepoCmd(ctx, os.Args[2:])
	case "org":
		OrgCmd(ctx, os.Args[2:])
	default:
		fmt.Print(usage)
		os.Exit(1)
	}
}

func OrgCmd(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("org", flag.ContinueOnError)
	srcAccessTokenFlag := fs.String("src-token", "", "Personal access token for pulling from github.com")
	srcURLFlag := fs.String("src-url", "", "URL of source organization, e.g. https://github.com/org")
	tgtAccessTokenFlag := fs.String("tgt-token", "", "Personal access token for pulling from Github Enterprise")
	tgtURLFlag := fs.String("tgt-url", "", "URL of target org to push to, e.g. https://github.enterprise.com/org")
	helpFlag := fs.Bool("help", false, "Show help")
	if err := fs.Parse(args); err != nil || *helpFlag {
		fs.PrintDefaults()
		os.Exit(1)
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
	if len(errors) > 0 {
		fmt.Println("Invalid or missing params:")
		fmt.Println("\n -" + strings.Join(errors, "\n -"))
		os.Exit(1)
	}

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
		if err = copy(ctx, *srcAccessTokenFlag, repo.URL, *tgtURLFlag, tgt); err != nil {
			fmt.Printf("Failed to copy: %v\n", err)
			os.Exit(1)
		}
	}
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

func listRepos(ctx context.Context, orgURL, token string) (repos []Repo, err error) {
	// Create the client.
	u, err := url.Parse(orgURL)
	if err != nil {
		return repos, fmt.Errorf("failed to parse url: %w", err)
	}
	host := strings.ToLower(u.Hostname())
	client := github.NewClient(nil).WithAuthToken(token)
	if host != "github.com" {
		client, err = client.WithEnterpriseURLs(u.Scheme+"://"+host, u.Scheme+"://"+host)
		if err != nil {
			return repos, fmt.Errorf("failed to set enterprise domain: %w", err)
		}
	}
	// Get the org name.
	org := strings.Split(strings.Trim(u.Path, "/"), "/")[0]

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

func RepoCmd(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("repo", flag.ContinueOnError)
	srcAccessTokenFlag := fs.String("src-token", "", "Personal access token for pulling from github.com")
	srcURLFlag := fs.String("src-url", "", "URL of source repository to pull from github.com")
	tgtAccessTokenFlag := fs.String("tgt-token", "", "Personal access token for pulling from Github Enterprise")
	tgtURLFlag := fs.String("tgt-url", "", "URL of source repository to pull from Github Enterprise")
	helpFlag := fs.Bool("help", false, "Show help")
	if err := fs.Parse(args); err != nil || *helpFlag {
		fs.PrintDefaults()
		os.Exit(1)
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
	if len(errors) > 0 {
		fmt.Println("Invalid or missing params:")
		fmt.Println("\n -" + strings.Join(errors, "\n -"))
		os.Exit(1)
	}

	fmt.Printf("Copying from %s to %s\n", *srcURLFlag, *tgtURLFlag)

	err := copy(ctx, *srcAccessTokenFlag, *srcURLFlag, *tgtAccessTokenFlag, *tgtURLFlag)
	if err != nil {
		fmt.Printf("Failed to copy: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Copy complete.")
}

func copy(ctx context.Context, srcAccessToken, src, tgtAccessToken, tgt string) error {
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
	if err != nil {
		return fmt.Errorf("failed to push to target: %w", err)
	}

	return nil
}

func ptr[T any](v T) *T {
	return &v
}

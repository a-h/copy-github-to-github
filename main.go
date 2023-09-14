package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"strings"

	"flag"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/google/go-github/v55/github"
)

var srcAccessTokenFlag = flag.String("src-token", "", "Personal access token for pulling from github.com")
var srcURLFlag = flag.String("src-url", "", "URL of source repository to pull from github.com")
var tgtAccessTokenFlag = flag.String("tgt-token", "", "Personal access token for pulling from Github Enterprise")
var tgtURLFlag = flag.String("tgt-url", "", "URL of source repository to pull from Github Enterprise")

func main() {
	flag.Parse()
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

	err := copy(*srcAccessTokenFlag, *srcURLFlag, *tgtAccessTokenFlag, *tgtURLFlag)
	if err != nil {
		fmt.Printf("Failed to copy: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("Copy complete.")
}

func copy(srcAccessToken, src, tgtAccessToken, tgt string) error {
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
	_, _, err = client.Repositories.Create(context.Background(), owner, &github.Repository{
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

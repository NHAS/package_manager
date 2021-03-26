package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

func fetch(p Package, oauthToken string) (Name string, Path string, err error) {

	auth := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: oauthToken},
	)

	u, err := url.Parse(p.Repository)
	if err != nil {
		return "", "", err
	}

	parts := strings.Split(u.Path[1:], "/")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("Repository %s wasnt in the required https://github/owner/repo format", p.Repository)
	}

	path, err := getLatestPackage(parts[0], parts[1], auth)
	if err != nil {
		return "", "", err
	}

	return p.Name, path, nil
}

func getLatestPackage(owner, name string, oAuth oauth2.TokenSource) (string, error) {

	httpClient := oauth2.NewClient(context.Background(), oAuth)

	client := githubv4.NewClient(httpClient)

	var query struct {
		Repository struct {
			Description string
			Refs        struct {
				Edges []struct {
					Node struct {
						Target struct {
							CommitResourcePath githubv4.URI
							Tag                struct {
								Name string
							} `graphql:"... on Tag"`
						}
					}
				}
			} `graphql:"refs(refPrefix: \"refs/tags/\", last: 1, orderBy: {field: TAG_COMMIT_DATE, direction: ASC})"`
		} `graphql:"repository(owner: $repoOwner, name: $repoName)"`
	}

	variables := map[string]interface{}{
		"repoOwner": githubv4.String(owner),
		"repoName":  githubv4.String(name),
	}

	err := client.Query(context.Background(), &query, variables)
	if err != nil {
		return "", err
	}

	if len(query.Repository.Refs.Edges) != 1 {
		return "", fmt.Errorf("Unable to request tags")
	}

	pkgName := path.Base(query.Repository.Refs.Edges[0].Node.Target.Tag.Name)
	commitHash := path.Base(query.Repository.Refs.Edges[0].Node.Target.CommitResourcePath.Path)

	outputFile := "./source/" + name + "-" + pkgName + ".tar.gz"

	err = DownloadFile(outputFile, fmt.Sprintf("https://github.com/%s/%s/archive/%s.tar.gz", owner, name, commitHash))
	if err != nil {
		return "", err
	}

	return filepath.Abs(outputFile)
}

func DownloadFile(filepath string, url string) error {

	resp, err := http.Head(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	etag := resp.Header.Get("ETag") //This is for storing/retrieving etag values for caching purposes

	hash := sha1.Sum([]byte(url))
	v := hex.EncodeToString(hash[:])

	contents, err := ioutil.ReadFile("cache/" + v)
	if err == nil && string(contents) == etag {
		return nil
	}

	resp, err = http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	out, err := os.Create(filepath)
	if err != nil {
		return err
	}
	defer out.Close()

	// Write the body to file
	_, err = io.Copy(out, resp.Body)

	ioutil.WriteFile("cache/"+v, []byte(etag), 0600)

	return err
}

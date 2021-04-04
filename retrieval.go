package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
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

const sourceCacheFile = "./source/valid_sources"

func directoryExists(path string) bool {
	if fs, err := os.Stat(path); err != nil || !fs.IsDir() {
		return false
	}
	return true
}

func pullPackages(oauth string, packages []*Package) error {

	if len(oauth) == 0 {
		return fmt.Errorf("No ouath token specified")
	}

	if !directoryExists("source") && os.Mkdir("source", 0700) != nil {
		return fmt.Errorf("Unable to make source directory")
	}

	if !directoryExists("cache") && os.Mkdir("cache", 0700) != nil {
		return fmt.Errorf("Unable to make cache directory")
	}

	cachedPackageSources := make(map[string]string)

	source, err := ioutil.ReadFile(sourceCacheFile)
	if err == nil {
		fmt.Printf("Cache exists, using cached resources\n")
		err = json.Unmarshal(source, &cachedPackageSources)
		if err != nil {
			return err
		}
	}

	for _, pkg := range packages {
		if _, ok := cachedPackageSources[pkg.Name]; !ok { // TODO add check to make sure that source/ actually has the files
			fmt.Printf("[Missing %s] Downloading %s...", pkg.Name, pkg.Repository)
			pkg.Source, err = fetch(*pkg, oauth)
			if err != nil {
				return err
			}
			fmt.Printf("Done!\n")
		} else {
			pkg.Source = cachedPackageSources[pkg.Name]
			fmt.Printf("[Found %s] %s\n", pkg.Name, pkg.Source)
		}
	}

	fmt.Printf("Extracting archives...")
	newPackageSources, err := extractPackages(packages)
	if err != nil {
		return err
	}
	fmt.Printf("Done!\n")

	for k, v := range newPackageSources { // Merge the cached maps as to not trample cached sources in single build mode
		cachedPackageSources[k] = v
	}

	//Write package cache file
	b, err := json.Marshal(cachedPackageSources)
	if err != nil {
		return err
	}

	err = ioutil.WriteFile(sourceCacheFile, b, 0600)
	if err != nil {
		return err
	}

	return nil
}

func fetch(p Package, oauthToken string) (Path string, err error) {

	auth := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: oauthToken},
	)

	u, err := url.Parse(p.Repository)
	if err != nil {
		return "", err
	}

	parts := strings.Split(u.Path[1:], "/")
	if len(parts) != 2 {
		return "", fmt.Errorf("Repository %s wasnt in the required https://github/owner/repo format", p.Repository)
	}

	path, err := getLatestPackage(parts[0], parts[1], auth)
	if err != nil {
		return "", err
	}

	return path, nil
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

	err = downloadFile(outputFile, fmt.Sprintf("https://github.com/%s/%s/archive/%s.tar.gz", owner, name, commitHash))
	if err != nil {
		return "", err
	}

	return filepath.Abs(outputFile)
}

func downloadFile(filepath string, url string) error {

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

func extractPackages(packages []*Package) (extractedSourcesPaths map[string]string, err error) {
	if len(packages) == 0 {
		return extractedSourcesPaths, fmt.Errorf("No archive paths defined for any packages....")
	}

	type packageNameToExtractedSource struct {
		Name string
		Path string
	}

	extractedSourcesPaths = make(map[string]string)

	newDirectories := make(chan packageNameToExtractedSource)
	defer close(newDirectories)
	errorsChannel := make(chan error)
	for _, v := range packages {

		go func(pkg *Package) {

			if fsi, err := os.Stat(pkg.Source); err == nil && fsi.IsDir() {
				newDirectories <- packageNameToExtractedSource{pkg.Name, pkg.Source}
				return // Isnt a archive so dont extract
			}

			r, err := os.Open(pkg.Source)
			if err != nil {
				errorsChannel <- err
				return
			}

			outputDirect, err := extractTarGz(r)
			if err != nil {
				errorsChannel <- err
				return
			}

			pkg.Source = outputDirect

			newDirectories <- packageNameToExtractedSource{pkg.Name, pkg.Source}
		}(v)
	}

	for i := 0; i < len(packages); i++ {
		select {
		case t := <-newDirectories:
			extractedSourcesPaths[t.Name] = t.Path
		case m := <-errorsChannel:
			return extractedSourcesPaths, m
		}
	}

	return extractedSourcesPaths, nil
}

func extractTarGz(gzipStream io.Reader) (outputDirectory string, err error) {
	uncompressedStream, err := gzip.NewReader(gzipStream)
	if err != nil {
		return "", fmt.Errorf("ExtractTarGz: %s", err)
	}

	tarReader := tar.NewReader(uncompressedStream)

	firstDirectory := true

	for true {
		header, err := tarReader.Next()

		if err == io.EOF {
			break
		}

		if err != nil {
			return "", fmt.Errorf("ExtractTarGz: Next() failed: %s", err)
		}

		path := "source/" + header.Name
		switch header.Typeflag {

		case tar.TypeDir:
			if firstDirectory {
				firstDirectory = false
				outputDirectory = path
			}

			if fsinfo, err := os.Stat(path); err == nil && fsinfo.IsDir() {
				continue
			}

			if err := os.Mkdir(path, fs.FileMode(header.Mode)); err != nil {
				return "", fmt.Errorf("ExtractTarGz: Mkdir() failed: %s", err.Error())
			}

		case tar.TypeReg:

			outFile, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, fs.FileMode(header.Mode))
			if err != nil {
				return "", fmt.Errorf("ExtractTarGz: Create() failed: %s", err.Error())
			}
			if _, err := io.Copy(outFile, tarReader); err != nil {
				return "", fmt.Errorf("ExtractTarGz: Copy() failed: %s", err.Error())
			}
			outFile.Close()

		default:
			continue
		}

	}
	return outputDirectory, nil
}

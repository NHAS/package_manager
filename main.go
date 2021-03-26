package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/shurcooL/githubv4"
	"golang.org/x/oauth2"
)

const validSourcesPath = "./source/valid_sources"

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

type Package struct {
	Name                 string
	Repository           string   `json:"repo"`
	ConfigurationOptions string   `json:"configure_opts"`
	Depends              []string `json:"depends"`
	Install              bool     `json:"install"`
}

type Management struct {
	BuildReplacements map[string]string   `json:"replacements"`
	OauthToken        string              `json:"oauth_token"`
	Packages          map[string]*Package `json:"packages"`
}

func main() {

	flag.Bool("no_configure", false, "Dont configure")
	flag.Bool("no_build", false, "Dont build")
	flag.Bool("clean", false, "Delete everything and start again")

	flag.Parse()

	build, configure := true, true
	clean := false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "no_configure", "nc":
			configure = false
		case "no_build", "nb":
			build = false
		case "clean":
			clean = true
		}
	})

	if len(flag.Args()) != 1 {
		fmt.Println("Enter pkg file path")
		return
	}

	if clean {
		os.RemoveAll("source/")
		os.RemoveAll("cache/")
		fmt.Println("All clean!")
		return
	}

	pkgFile, err := ioutil.ReadFile(os.Args[1])
	check(err)

	var settings Management
	err = json.Unmarshal([]byte(pkgFile), &settings)
	check(err)

	if len(settings.OauthToken) == 0 {
		log.Fatal("No ouath token specified")
	}

	extractedSource := make(map[string]string)
	for k, v := range settings.Packages {
		v.Name = k
	}

	source, err := ioutil.ReadFile(validSourcesPath)
	if err != nil {

		auth := oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: settings.OauthToken},
		)

		type repoTuple struct {
			Name string
			Path string
		}

		os.Mkdir("source", 0700)
		os.Mkdir("cache", 0700)

		var sourcePaths []repoTuple
		for k, v := range settings.Packages {

			v.Name = k

			u, err := url.Parse(v.Repository)
			check(err)

			parts := strings.Split(u.Path[1:], "/")
			if len(parts) != 2 {
				log.Fatalf("Repository %s wasnt in the required https://github/owner/repo format", v.Repository)
			}

			fmt.Printf("Downloading %s (%s)...", k, v.Repository)
			path, err := getLatestPackage(parts[0], parts[1], auth)
			check(err)

			sourcePaths = append(sourcePaths, repoTuple{k, path})
			fmt.Printf("Done!\n")
		}

		fmt.Printf("Extracting archives...")
		newDirectories := make(chan repoTuple)
		for _, v := range sourcePaths {

			go func(s repoTuple) {

				r, err := os.Open(s.Path)
				check(err)

				outputDirect, err := ExtractTarGz(r)
				check(err)

				newDirectories <- repoTuple{s.Name, outputDirect}
			}(v)
		}

		for i := 0; i < len(sourcePaths); i++ {
			t := <-newDirectories
			extractedSource[t.Name] = t.Path
		}
		close(newDirectories)

		b, err := json.Marshal(extractedSource)
		check(err)

		err = ioutil.WriteFile(validSourcesPath, b, 0600)
		check(err)

		fmt.Printf("Done!\n")
	} else {
		fmt.Printf("It appears sources have already been downloaded using these instead (assuming they have been extracted)\n")
		err = json.Unmarshal(source, &extractedSource)
		check(err)
		fmt.Println(extractedSource)
	}

	fmt.Printf("Creating build order...")
	order, _ := createOrder(settings.Packages)
	fmt.Printf("Done!\n")

	fmt.Printf("Building packages: \n")

	for i := range order {

		config := order[i].ConfigurationOptions
		for k, v := range settings.BuildReplacements {
			config = strings.ReplaceAll(config, "$"+k+"$", v)
		}

		actions := ""

		if configure {
			actions += config
		}

		if configure && build {
			actions += " && "
		}

		if build {
			actions += "make -j " + strconv.Itoa(runtime.NumCPU())
			if order[i].Install {
				actions += " && make install; "
			}
		}

		fmt.Printf("\n%s\n", order[i].Name)
		fmt.Printf("Configuration: %s\n", config)
		fmt.Printf("Directory:     %s\n\n", extractedSource[order[i].Name])
		cmd := exec.Command("bash", "-c", "cd "+extractedSource[order[i].Name]+"; "+actions)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		check(err)
	}

}

func ExtractTarGz(gzipStream io.Reader) (outputDirectory string, err error) {
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

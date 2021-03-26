package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

const sourceCacheFile = "./source/valid_sources"

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
	BuildReplacements map[string]string `json:"replacements"`
	OauthToken        string            `json:"oauth_token"`
	Packages          []*Package        `json:"packages"`
}

func main() {

	flag.Bool("configure", false, "Just configure, no build")
	flag.Bool("build", false, "Just build, dont reconfigure the package")
	flag.Bool("clean", false, "Delete everything and start again")

	flag.Parse()

	build, configure := true, true
	clean := false
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "configure":
			build = false
		case "build":
			configure = false // Yes these appear around the wrong way, except that when you just want to build you dont configure. And when you just want to configure you dont build
		case "clean":
			clean = true
		}
	})

	if len(flag.Args()) < 1 {
		fmt.Println("Enter pkg file path")
		return
	}

	if clean {
		os.RemoveAll("source/")
		os.RemoveAll("cache/")
		fmt.Println("All clean!")
		return
	}

	pkgFile, err := ioutil.ReadFile(flag.Args()[0])
	check(err)

	var settings Management
	err = json.Unmarshal([]byte(pkgFile), &settings)
	check(err)

	if len(settings.OauthToken) == 0 {
		log.Fatal("No ouath token specified")
	}

	if len(flag.Args()) == 2 {
		var singleBuild *Package = nil
		for i := range settings.Packages {
			if strings.TrimSpace(settings.Packages[i].Name) == strings.TrimSpace(flag.Args()[1]) {
				singleBuild = settings.Packages[i]
				break
			}
		}

		if singleBuild == nil {
			fmt.Printf("Package %s not found\n", flag.Args()[1])
			return
		}

		fmt.Printf("Building single package [%s] (This may not work if the packages dependancies have not been build)\n", singleBuild.Name)
		settings.Packages = []*Package{singleBuild}

	}

	os.Mkdir("source", 0700)
	os.Mkdir("cache", 0700)

	cachedPackageSources := make(map[string]string)

	source, err := ioutil.ReadFile(sourceCacheFile)
	if err == nil {
		fmt.Printf("Cache exists, using cached resources\n")
		err = json.Unmarshal(source, &cachedPackageSources)
		check(err)
	}

	for _, v := range settings.Packages {
		if _, ok := cachedPackageSources[v.Name]; !ok { // TODO add check to make sure that source/ actually has the files
			fmt.Printf("[Missing %s] Downloading %s...", v.Name, v.Repository)
			packageName, sourcePath, err := fetch(*v, settings.OauthToken)
			check(err)
			cachedPackageSources[packageName] = sourcePath
			fmt.Printf("Done!\n")
		}
	}

	fmt.Printf("Extracting archives...")
	cachedPackageSources, err = extractPackages(cachedPackageSources)
	check(err)
	fmt.Printf("Done!\n")

	//Write package cache file
	b, err := json.Marshal(cachedPackageSources)
	check(err)
	err = ioutil.WriteFile(sourceCacheFile, b, 0600)
	check(err)

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
		fmt.Printf("Directory:     %s\n\n", cachedPackageSources[order[i].Name])
		cmd := exec.Command("bash", "-c", "cd "+cachedPackageSources[order[i].Name]+"; "+actions)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		check(err)
	}

}

func extractPackages(archivePaths map[string]string) (extractedSourcesPaths map[string]string, err error) {
	type pnameTopath struct {
		Name string
		Path string
	}

	extractedSourcesPaths = make(map[string]string)

	newDirectories := make(chan pnameTopath)
	defer close(newDirectories)
	errorsChannel := make(chan error)
	for k, v := range archivePaths {

		go func(name, path string) {

			if fsi, err := os.Stat(path); err == nil && fsi.IsDir() {
				newDirectories <- pnameTopath{name, path}
				return // Isnt a archive so dont extract
			}

			r, err := os.Open(path)
			if err != nil {
				errorsChannel <- err
				return
			}

			outputDirect, err := ExtractTarGz(r)
			if err != nil {
				errorsChannel <- err
				return
			}

			newDirectories <- pnameTopath{name, outputDirect}
		}(k, v)
	}

	for i := 0; i < len(archivePaths); i++ {
		select {
		case t := <-newDirectories:
			extractedSourcesPaths[t.Name] = t.Path
		case m := <-errorsChannel:
			return extractedSourcesPaths, m
		}
	}

	return extractedSourcesPaths, nil
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

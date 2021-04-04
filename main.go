package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}

type Bits uint8

func (b *Bits) Set(flag Bits) {
	*b |= flag
}

func (b *Bits) ExclSet(flag Bits) {
	*b = 0
	b.Set(flag)
}

func (b *Bits) Clear(flag Bits) {
	*b &^= flag
}
func (b *Bits) Toggle(flag Bits) {
	*b ^= flag
}
func (b *Bits) Has(flag Bits) bool {
	return *b&flag != 0
}

const (
	CLEAN Bits = 1 << iota
	CONFIGURE
	BUILD
	IMAGE
)

func main() {

	flag.Bool("configure", false, "Just configure, no build")
	flag.Bool("build", false, "Just build, dont reconfigure the package")
	flag.Bool("image", false, "Just create image from build directory")
	flag.Bool("clean", false, "Delete everything and start again")

	flag.Parse()

	var buildOptions Bits
	flag.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "image":
			buildOptions.Set(IMAGE)
		case "configure":
			buildOptions.Set(CONFIGURE)
		case "build":
			buildOptions.Set(BUILD)
		case "clean":
			buildOptions.ExclSet(CLEAN) // Clear all other flags and set this one
		}
	})

	if buildOptions == 0 { // Default if nothing is set, do all steps
		buildOptions.Set(CONFIGURE)
		buildOptions.Set(BUILD)
		buildOptions.Set(IMAGE)
	}

	if len(flag.Args()) < 1 {
		fmt.Println("Enter pkg file path")
		return
	}

	if buildOptions.Has(CLEAN) {
		clean()
		fmt.Println("All clean!")
		return
	}

	settings, err := loadPackageManifest(flag.Args()[0])
	check(err)

	if buildOptions.Has(BUILD) || buildOptions.Has(CONFIGURE) {

		if len(flag.Args()) == 2 {
			var singleBuild *Package = nil
			for i := range settings.Packages {
				if strings.TrimSpace(settings.Packages[i].Name) == strings.TrimSpace(flag.Args()[1]) {
					singleBuild = settings.Packages[i]
					break
				}
			}

			if singleBuild == nil {
				log.Fatalf("Package %s not found\n", flag.Args()[1])
			}

			fmt.Printf("Single package mode [%s] (This may not work if the packages dependancies have not been built)\n", singleBuild.Name)
			settings.Packages = []*Package{singleBuild}
		}

		err := pullPackages(settings.OauthToken, settings.Packages)
		check(err)

		err = configureAndBuild(settings.Packages, buildOptions)
		check(err)
	}

	if buildOptions.Has(IMAGE) {
		err := createImage(settings)
		check(err)
	}

}

func clean() {
	os.RemoveAll("source/")
	os.RemoveAll("cache/")
	os.RemoveAll("build/")
	os.RemoveAll("image/")
}

func createImage(settings pkgManifest) error {

	for _, v := range settings.ImageSettings.LdSearch {
		if fs, err := os.Stat(v); err != nil || !fs.IsDir() {
			return fmt.Errorf("Invalid library search path [%s] %s", v, err)
		}
	}

	if !directoryExists("image") && os.Mkdir("image", 0700) != nil {
		return fmt.Errorf("Unable to make image directory for creating squash")
	}

	if len(settings.ImageSettings.KeyExecutables) == 0 {
		return fmt.Errorf("No executables marked for packaging")
	}

	files := []string{}
	for _, v := range settings.ImageSettings.KeyExecutables {
		matches, err := filepath.Glob(filepath.Join("build/", v))
		if err != nil {
			return err
		}
		for _, vv := range matches {
			if fs, err := os.Stat(vv); err != nil || fs.IsDir() {
				log.Printf("[WARN] Not adding %s \n", vv)
				continue
			}

			files = append(files, vv)
		}
	}

	if _, err := os.Stat(settings.ImageSettings.CrossCompilerLibRoot); err != nil {
		return err
	}

	os.Mkdir("image/lib", 0700)

	settings.ImageSettings.LdSearch = append(settings.ImageSettings.LdSearch, settings.ImageSettings.CrossCompilerLibRoot)

	// Copy all selected binary files, and their required dynamic libraries. As given by objdump
	executableDependances := make(map[string]bool)
	for _, binaryFile := range files {
		deps, err := getDependacies(settings.CrossCompiler, binaryFile)
		if err != nil {
			log.Println("[WARN] Skipping file as objdump complained: ", binaryFile, " Err: ", err)
			continue
		}

		for _, dependancy := range deps {
			if _, ok := executableDependances[dependancy]; ok {
				continue
			}

			libraryPath, err := findLibrary(dependancy, settings.ImageSettings.LdSearch)
			if err != nil {
				return err
			}

			_, err = copyFile(libraryPath, "image/lib/")
			if err != nil {
				return err
			}

			executableDependances[dependancy] = true

			log.Println("Adding library: ", dependancy)

		}

		realitivePath, err := filepath.Rel("build/", binaryFile)
		if err != nil {
			return err
		}
		imageDirectory := filepath.Dir(filepath.Join("image/", realitivePath))
		os.MkdirAll(imageDirectory, 0700)

		_, err = copyFile(binaryFile, imageDirectory)
		if err != nil {
			return err
		}
	}

	for k := range executableDependances {

		libraryPath, err := findLibrary(k, settings.ImageSettings.LdSearch)
		if err != nil {
			return err
		}

		deps, err := getDependacies(settings.CrossCompiler, libraryPath)
		if err != nil {
			log.Printf("Getting dependancy of library %s failed %s", k, err)
			continue
		}

		for _, v := range deps {
			if _, ok := executableDependances[v]; !ok {
				libraryPath, err := findLibrary(v, settings.ImageSettings.LdSearch)
				if err != nil {
					return err
				}

				_, err = copyFile(libraryPath, "image/lib/")
				if err != nil {
					return err
				}

				executableDependances[v] = true
				log.Println("Adding library: ", v)

			}
		}
	}

	squash := exec.Command("mksquashfs", "image", "image.sqfs", "-comp", "xz", "-noappend", "-no-xattrs", "-all-root", "-progress", "-always-use-fragments", "-no-exports")
	squash.Stdout = os.Stdout
	squash.Stderr = os.Stderr

	return squash.Run()
}

func findLibrary(library string, searchPaths []string) (libraryPath string, err error) {

	for _, searchPath := range searchPaths {
		_, err := os.Stat(filepath.Join(searchPath, library))
		if err == nil {
			return filepath.Join(searchPath, library), nil
		}
	}

	return libraryPath, fmt.Errorf("Unable to find %s in ld_library_paths", library)

}

func getDependacies(crossCompile, binaryFile string) (deps []string, err error) {
	cmd := exec.Command(crossCompile+"-objdump", "-p", binaryFile)
	objDmpOut, err := cmd.Output()
	if err != nil {
		log.Println(objDmpOut)
		return deps, err
	}

	for _, objDumpLines := range bytes.Split(objDmpOut, []byte{'\n'}) {
		if bytes.Contains(objDumpLines, []byte("NEEDED")) {
			l := bytes.ReplaceAll(objDumpLines, []byte("NEEDED"), []byte(""))
			l = bytes.TrimSpace(l)

			deps = append(deps, string(l))
		}
	}

	return deps, nil
}

func copyFile(src, dst string) (int64, error) {
	sourceFileStat, err := os.Stat(src)
	if err != nil {
		return 0, err
	}

	if !sourceFileStat.Mode().IsRegular() {
		return 0, fmt.Errorf("%s is not a regular file", src)
	}

	source, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer source.Close()

	file := filepath.Base(src)

	destination, err := os.OpenFile(filepath.Join(dst, file), os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0700)
	if err != nil {
		return 0, err
	}
	defer destination.Close()
	nBytes, err := io.Copy(destination, source)
	return nBytes, err
}

func configureAndBuild(packages []*Package, buildOptions Bits) error {

	if !directoryExists("build") && os.Mkdir("build", 0700) != nil {
		return fmt.Errorf("Unable to make build directory")
	}

	fmt.Printf("Creating build order...")
	order, _ := createOrder(packages)
	fmt.Printf("Done!\n")

	fmt.Printf("Building packages: \n")

	for i := range order {

		actions := ""

		if buildOptions.Has(CONFIGURE) {
			actions += order[i].ConfigurationOptions
		}

		if buildOptions.Has(CONFIGURE) && buildOptions.Has(BUILD) {
			actions += " && "
		}

		if buildOptions.Has(BUILD) {
			buildInstruction := "make -j " + strconv.Itoa(runtime.NumCPU())
			if len(order[i].Build) != 0 {
				buildInstruction = order[i].Build
			}

			actions += buildInstruction

			if len(order[i].Install) != 0 {
				actions += " && " + order[i].Install
			}
		}

		fmt.Printf("\n%s\n", order[i].Name)
		fmt.Printf("Configuration: %s\n", order[i].ConfigurationOptions)
		fmt.Printf("Directory:     %s\n\n", order[i].Source)
		cmd := exec.Command("bash", "-c", "cd "+order[i].Source+"; "+actions)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		err := cmd.Run()
		if err != nil {
			return err
		}
	}

	return nil
}

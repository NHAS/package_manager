package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
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
		createImage(settings)
	}

}

func clean() {
	os.RemoveAll("source/")
	os.RemoveAll("cache/")
	os.RemoveAll("build/")
	os.RemoveAll("image/")
}

func createImage(settings pkgManifest) {
	if fs, err := os.Stat(settings.CrossCompileLib); err != nil || !fs.IsDir() {
		log.Fatalf("Invalid cross compiler lib [%s]", settings.CrossCompileLib)
	}

	if len(settings.KeyExecutables) == 0 && len(settings.KeyLibraries) == 0 {
		log.Println("[WARN] No key executables or libraries are selected this may be bigger images by including unused libraries")
	}

	if os.Mkdir("image", 0600) != nil {
		log.Fatal("Unable to make image directory for creating squash")
	}
}

func configureAndBuild(packages []*Package, buildOptions Bits) error {

	os.Mkdir("build", 0700)

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

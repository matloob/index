package main

import (
	"io/ioutil"
	"log"

	"github.com/matloob/index"
)

func main() {
	const moduleDir = "/Users/matloob/go/pkg/mod/golang.org/x/tools/gopls@v0.7.4"
	const outfile = "/Users/matloob/Desktop/tools.index"
	if err := writeModulesTo(moduleDir, outfile); err != nil {
		log.Fatal(err)
	}
	if _, err := index.DoModuleIndex(outfile); err != nil {
		log.Fatal(err)
	}
}

func writeModulesTo(moduleDir string, outfile string) error {
	rawModule := index.IndexModule(moduleDir)
	return WriteIndexRawModule(outfile, rawModule)
}

// Remove me... this is for tryitout.
func WriteIndexRawModule(filepath string, rm *index.RawModule) error {
	var packages []*index.RawPackage
	for _, m := range rm.Dirs {
		packages = append(packages, m)
	}
	b, err := index.EncodeModule(packages)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(filepath, b, 0644)
}

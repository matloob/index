package main

import (
	"encoding/json"
	"go/build"
	"io/ioutil"
	"log"
	"os"

	"github.com/matloob/index"
)

func main() {
	const moduleDir = "/Users/matloob/go/pkg/mod/golang.org/x/tools/gopls@v0.7.4"
	const outfile = "/Users/matloob/Desktop/tools.index"
	if err := writeModulesTo(moduleDir, outfile); err != nil {
		log.Fatal(err)
	}
	mi, err := index.Open(outfile, "/Users/matloob/go/pkg/mod/golang.org/x/tools/gopls@v0.7.4/go.index")
	if err != nil {
		log.Fatal(err)
	}
	p, err := mi.ImportPackage(build.Default, "/Users/matloob/go/pkg/mod/golang.org/x/tools/gopls@v0.7.4", 0)
	if err != nil {
		log.Fatal(err)
	}
	json.NewEncoder(os.Stdout).Encode(p)
}

func writeModulesTo(moduleDir string, outfile string) error {
	rawModule, err := index.IndexModule(moduleDir)
	if err != nil {
		return err
	}
	return WriteIndexRawModule(outfile, rawModule, moduleDir)
}

// Remove me... this is for tryitout.
func WriteIndexRawModule(filepath string, rm *index.RawModule, moduleDir string) error {
	var packages []*index.RawPackage
	for _, m := range rm.Dirs {
		packages = append(packages, m)
	}
	b, err := index.EncodeModule(packages, moduleDir)
	if err != nil {
		return err
	}

	return ioutil.WriteFile(filepath, b, 0644)
}

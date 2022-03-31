package main

import (
	"encoding/json"
	"fmt"
	"go/build"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/matloob/index"
	"github.com/matloob/index/internal/diff"
	"golang.org/x/mod/module"
)

func modVers(modcache string, path string) (module.Version, string, bool) {
	relToCache := path[len(modcache):]
	at := strings.IndexRune(relToCache, '@')
	if at == -1 {
		return module.Version{}, "", false
	}
	modulePath := filepath.ToSlash(relToCache[:at])
	rest := relToCache[at+1:]
	sep := strings.IndexRune(rest, filepath.Separator)
	version, pathInModule := rest, ""
	if sep != -1 {
		version = rest[:sep]
		pathInModule = filepath.ToSlash(rest[sep+1:])
	}
	return module.Version{modulePath, version}, pathInModule, true
}

func main() {
	modcache := "/users/matloob/go/pkg/mod/"
	modcachecache := filepath.Join(modcache, "cache")
	dir := "/users/matloob/go/pkg/mod/"

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("\t\t", "\t")

	var pass, all int

	modules := make(map[module.Version]*index.RawModule)

	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if path == modcachecache {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			return nil
		}
		modVers, _, ok := modVers(modcache, path)
		if !ok {
			return nil
		}
		ind := index.IndexModule(path)
		// roundtrip it
		mm, err := json.MarshalIndent(ind, "", "\t")
		var ind2 index.RawModule
		json.Unmarshal(mm, &ind2)
		_ = ind2
		modules[modVers] = &ind2
		return filepath.SkipDir
	})

	fmt.Println("done indexing")

	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if path == modcachecache {
			return filepath.SkipDir
		}
		if !d.IsDir() {
			return nil
		}

		v, pkgpath, ok := modVers(modcache, path)
		if !ok {
			return nil
		}

		all++
		if all%100 == 0 {
			fmt.Println(all)
		}

		got, gotErr := index.Cook(build.Default, modules[v].Dirs[pkgpath], 0)
		// got, gotErr := getPackageRC(path)
		want, wantErr := build.ImportDir(path, 0)

		if gotErr != nil || wantErr != nil {
			if (gotErr == nil) != (wantErr == nil) || gotErr.Error() != wantErr.Error() {
				fmt.Printf("ERROR(%s): got error %v; want error %v\n", path, gotErr, wantErr)
			}
		}
		if !reflect.DeepEqual(got, want) {
			fmt.Println("ERROR(" + path + ")\n\tgot package")
			enc.Encode(got)
			got, _ := json.MarshalIndent(got, "", "\t")

			fmt.Println("\twant package")
			enc.Encode(want)
			want, _ := json.MarshalIndent(want, "", "\t")
			b, _ := diff.Diff("", got, want)
			os.Stdout.Write(b)
			return nil
		}
		pass++
		return nil
	})
	if pass == all {
		fmt.Print("PASS")
	} else {
		fmt.Print("FAIL")
	}
	fmt.Printf(" %v/%v passing packages\n", pass, all)
}

func getPackageRC(path string) (*build.Package, error) {
	// mode seems to be zero for ModulesEnabled
	return index.Cook(build.Default, index.ImportDirRaw(path), 0)
}

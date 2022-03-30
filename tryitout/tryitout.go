package main

import (
	"encoding/json"
	"fmt"
	"go/build"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"

	"github.com/matloob/index"
	"github.com/matloob/index/internal/diff"
)

func main() {
	dir := "/users/matloob/go/pkg/mod/"

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("\t\t", "\t")

	var pass, all int

	filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		all++
		got, gotErr := getPackageRC(path)
		want, wantErr := build.ImportDir(path, 0)

		if gotErr != nil || wantErr != nil {
			if (gotErr == nil) != (wantErr == nil) || gotErr.Error() != wantErr.Error() {
				fmt.Println("ERROR(%s): got error %v; want error %v", path, gotErr, wantErr)
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

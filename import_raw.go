package index

import (
	"bytes"
	"fmt"
	"go/build/constraint"
	"go/doc"
	"go/token"
	"io/fs"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

type TaggedFile struct {
	Name                    string
	Synopsis                string // doc.Synopsis of package comment... Compute synopsis on all of these?
	PkgName                 string
	IgnoreFile              bool // starts with _ or . or should otherwise always be ignored
	BinaryOnly              bool // cannot be rebuilt from source (has //go:binary-only-package comment)
	GoBuildConstraint       string
	PlusBuildConstraints    []string
	QuotedImportComment     string
	QuotedImportCommentLine int
	Imports                 []TFImport
	Embeds                  map[string][]token.Position

	Error      string
	ParseError string //
}

type TFImport struct {
	Path     string
	Doc      string // TODO(matloob): only save for cgo?
	Position token.Position
}

// todo doc
type RawPackage struct {
	// TODO(matloob): Do we need AllTags in RawPackage?
	// We can produce it from contstraints when we evaluate them.

	Error string

	// Arguments to build.Import. Is path always "."?
	Path   string
	SrcDir string

	Dir string // directory containing package sources

	// Source files
	SourceFiles []*TaggedFile

	// No ConflictDir-- only relevant togopath
}

type RawModule struct {
	Dirs map[string]*RawPackage
}

func IndexModule(dir string) (*RawModule, error) {
	rm := &RawModule{Dirs: make(map[string]*RawPackage)}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(filepath.ToSlash(path[len(dir):]), "/")
		rm.Dirs[rel] = ImportDirRaw(path)
		return nil
	})
	return rm, err
}

func ImportDirRaw(dir string) *RawPackage {
	return ImportRaw(".", dir)
}

// Import returns details about the Go package named by the import path,
// interpreting local import paths relative to the srcDir directory.
// If the path is a local import path naming a package that can be imported
// using a standard import path, the returned package will set p.ImportPath
// to that path.
//
// In the directory containing the package, .go, .c, .h, and .s files are
// considered part of the package except for:
//
//	- .go files in package documentation
//	- files starting with _ or . (likely editor temporary files)
//	- files with build constraints not satisfied by the context
//
// If an error occurs, Import returns a non-nil error and a non-nil
// *Package containing partial information.
//
func ImportRaw(path string, srcDir string) *RawPackage {
	p := &RawPackage{
		Path:   path,
		SrcDir: srcDir,
	}
	if path == "" {
		p.Error = fmt.Errorf("import %q: invalid import path", path).Error()
		return p
	}

	if !IsLocalImport(path) {
		panic(path)
	} else {
		if srcDir == "" {
			p.Error = fmt.Errorf("import %q: import relative to unknown directory", path).Error()
			return p
		}
		if !filepath.IsAbs(path) {
			p.Dir = filepath.Join(srcDir, path)
		}
	}

	// If it's a local import path, by the time we get here, we still haven't checked
	// that p.Dir directory exists. This is the right time to do that check.
	// We can't do it earlier, because we want to gather partial information for the
	// non-nil *Package returned when an error occurs.
	// We need to do this before we return early on FindOnly flag.
	if IsLocalImport(path) && !isDir(p.Dir) {
		// package was not found
		p.Error = fmt.Errorf("cannot find package %q in:\n\t%s", path, p.Dir).Error()
		return p
	}

	// TODO: use os.ReadDir
	dirs, err := ioutil.ReadDir(p.Dir)
	if err != nil {
		p.Error = err.Error()
		return p
	}

	fset := token.NewFileSet()
	for _, d := range dirs {
		if d.IsDir() {
			continue
		}
		if d.Mode()&fs.ModeSymlink != 0 {
			if isDir(filepath.Join(p.Dir, d.Name())) {
				// Symlinks to directories are not source files.
				continue
			}
		}

		name := d.Name()
		ext := nameExt(name)

		info, err := getInfo(p.Dir, name, fset)
		if err != nil {
			p.SourceFiles = append(p.SourceFiles, &TaggedFile{Name: name, Error: err.Error()})
			continue
		} else if info == nil {
			p.SourceFiles = append(p.SourceFiles, &TaggedFile{Name: name, IgnoreFile: true})
			continue
		}
		tf := &TaggedFile{
			Name:                 name,
			GoBuildConstraint:    info.goBuildConstraint,
			PlusBuildConstraints: info.plusBuildConstraints,
			BinaryOnly:           info.binaryOnly,
		}
		if info.parsed != nil {
			tf.PkgName = info.parsed.Name.Name
		}
		data := info.header

		// Going to save the file. For non-Go files, can stop here.
		p.SourceFiles = append(p.SourceFiles, tf)
		if ext != ".go" {
			continue
		}

		if info.parseErr != nil {
			tf.ParseError = info.parseErr.Error()
			// Fall through: we might still have a partial AST in info.parsed,
			// and we want to list files with parse errors anyway.
		}

		if info.parsed != nil && info.parsed.Doc != nil {
			tf.Synopsis = doc.Synopsis(info.parsed.Doc.Text())
		}

		qcom, line := findImportComment(data)
		if line != 0 {
			tf.QuotedImportComment = qcom
			tf.QuotedImportCommentLine = line
		}

		for _, imp := range info.imports {
			// TODO(matloob): only save doc for cgo?
			// TODO(matloob): remove filename from position and add it back later to save space?
			tf.Imports = append(tf.Imports, TFImport{Path: imp.path, Doc: imp.doc.Text(), Position: fset.Position(imp.pos)})
		}
		tf.Embeds = make(map[string][]token.Position)
		for _, emb := range info.embeds {
			tf.Embeds[emb.pattern] = append(tf.Embeds[emb.pattern], emb.pos)
		}

	}
	return p
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

type fileInfoPlus struct {
	fileInfo
	binaryOnly           bool
	goBuildConstraint    string
	plusBuildConstraints []string
}

// matchFile determines whether the file with the given name in the given directory
// should be included in the package being constructed.
// If the file should be included, matchFile returns a non-nil *fileInfo (and a nil error).
// Non-nil errors are reserved for unexpected problems.
//
// If name denotes a Go program, matchFile reads until the end of the
// imports and returns that section of the file in the fileInfo's header field,
// even though it only considers text until the first non-comment
// for +build lines.
//
// If allTags is non-nil, matchFile records any encountered build tag
// by setting allTags[tag] = true.
func getInfo(dir, name string, fset *token.FileSet) (*fileInfoPlus, error) {
	if strings.HasPrefix(name, "_") ||
		strings.HasPrefix(name, ".") {
		return nil, nil
	}

	i := strings.LastIndex(name, ".")
	if i < 0 {
		i = len(name)
	}
	ext := name[i:]

	if ext != ".go" && fileListForExt(&dummyPkg, ext) == nil {
		// skip
		return nil, nil
	}

	info := &fileInfoPlus{fileInfo: fileInfo{name: filepath.Join(dir, name), fset: fset}}
	if ext == ".syso" {
		// binary, no reading
		return info, nil
	}

	f, err := os.Open(info.name)
	if err != nil {
		return nil, err
	}

	// TODO(matloob) should we decide whether to ignore binary only here or in
	var ignoreBinaryOnly bool
	if strings.HasSuffix(name, ".go") {
		err = readGoInfo(f, &info.fileInfo)
		if strings.HasSuffix(name, "_test.go") {
			ignoreBinaryOnly = true // ignore //go:binary-only-package comments in _test.go files
		}
	} else {
		info.header, err = readComments(f)
	}
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("read %s: %v", info.name, err)
	}

	// Look for +build comments to accept or reject the file.
	info.goBuildConstraint, info.plusBuildConstraints, info.binaryOnly, err = getConstraints(info.header)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}

	if ignoreBinaryOnly && info.binaryOnly {
		info.binaryOnly = false // override info.binaryOnly
	}

	return info, nil
}

func getConstraints(content []byte) (goBuild string, plusBuild []string, binaryOnly bool, err error) {
	// Identify leading run of // comments and blank lines,
	// which must be followed by a blank line.
	// Also identify any //go:build comments.
	content, goBuildBytes, sawBinaryOnly, err := parseFileHeader(content)
	if err != nil {
		return "", nil, false, err
	}

	// If //go:build line is present, it controls, so no need to look for +build .
	// Otherwise, get plusBuild constraints.
	if goBuildBytes == nil {
		p := content
		for len(p) > 0 {
			line := p
			if i := bytes.IndexByte(line, '\n'); i >= 0 {
				line, p = line[:i], p[i+1:]
			} else {
				p = p[len(p):]
			}
			line = bytes.TrimSpace(line)
			if !bytes.HasPrefix(line, bSlashSlash) || !bytes.Contains(line, bPlusBuild) {
				continue
			}
			text := string(line)
			if !constraint.IsPlusBuild(text) {
				continue
			}
			plusBuild = append(plusBuild, text)
		}
	}

	return string(goBuildBytes), plusBuild, sawBinaryOnly, nil
}

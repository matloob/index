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
	PkgName                 string
	IgnoreFile              bool // starts with _ or . or should otherwise always be ignored
	GoBuildConstraint       string
	PlusBuildConstraints    []string
	QuotedImportComment     string
	QuotedImportCommentLine int
	Imports                 []TFImport
	Embeds                  map[string][]token.Position
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

	SrcDir string

	Dir           string // directory containing package sources
	Name          string // package name
	ImportComment string // path in import comment on package statement
	Doc           string // documentation synopsis
	ImportPath    string // import path of package ("" if unknown)
	//Root          string // root of Go tree where this package lives
	//SrcRoot       string // package source root directory ("" if unknown)
	//PkgRoot       string // package install root directory ("" if unknown)
	//PkgTargetRoot string // architecture dependent install root directory ("" if unknown)
	//BinDir string // command install directory ("" if unknown)
	//Goroot bool // package found in Go root
	// PkgObj        string   // installed .a file
	// AllTags []string // tags that can influence file selection in this directory

	ConflictDir string // this directory shadows Dir in $GOPATH
	BinaryOnly  bool   // cannot be rebuilt from source (has //go:binary-only-package comment)

	//TODO(matloob): turn not go file types into strings?

	// Source files
	GoFiles []TaggedFile // .go source files (including CgoFiles, TestGoFiles, XTestGoFiles)
	// CgoFiles []TaggedFile // .go source files that import "C"
	// IgnoredGoFiles    []TaggedFile // .go source files ignored for this build (including ignored _test.go files)
	DocumentationGoFiles []string // .go source file that specifies "package documentation" and is always ignored
	InvalidGoFiles       []string // .go source files with detected problems (parse error, wrong package name, and so on)
	// IgnoredOtherFiles []TaggedFile // non-.go source files ignored for this build
	CFiles       []TaggedFile // .c source files
	CXXFiles     []TaggedFile // .cc, .cpp and .cxx source files
	MFiles       []TaggedFile // .m (Objective-C) source files
	HFiles       []TaggedFile // .h, .hh, .hpp and .hxx source files
	FFiles       []TaggedFile // .f, .F, .for and .f90 Fortran source files
	SFiles       []TaggedFile // .s source files
	SwigFiles    []TaggedFile // .swig files
	SwigCXXFiles []TaggedFile // .swigcxx files
	SysoFiles    []TaggedFile // .syso system object files to add to archive

	// Cgo directives
	CgoCFLAGS    []string // Cgo CFLAGS directives
	CgoCPPFLAGS  []string // Cgo CPPFLAGS directives
	CgoCXXFLAGS  []string // Cgo CXXFLAGS directives
	CgoFFLAGS    []string // Cgo FFLAGS directives
	CgoLDFLAGS   []string // Cgo LDFLAGS directives
	CgoPkgConfig []string // Cgo pkg-config directives

	// Test information
	TestGoFiles  []TaggedFile // _test.go files in package
	XTestGoFiles []TaggedFile // _test.go files outside package

	// Dependency information
	/*
		Imports        []string                    // import paths from GoFiles, CgoFiles
		ImportPos      map[string][]token.Position // line information for Imports
		TestImports    []string                    // import paths from TestGoFiles
		TestImportPos  map[string][]token.Position // line information for TestImports
		XTestImports   []string                    // import paths from XTestGoFiles
		XTestImportPos map[string][]token.Position // line information for XTestImports

		// //go:embed patterns found in Go source files
		// For example, if a source file says
		//	//go:embed a* b.c
		// then the list will contain those two strings as separate entries.
		// (See package embed for more details about //go:embed.)
		EmbedPatterns        []string                    // patterns from GoFiles, CgoFiles
		EmbedPatternPos      map[string][]token.Position // line information for EmbedPatterns
		TestEmbedPatterns    []string                    // patterns from TestGoFiles
		TestEmbedPatternPos  map[string][]token.Position // line information for TestEmbedPatterns
		XTestEmbedPatterns   []string                    // patterns from XTestGoFiles
		XTestEmbedPatternPos map[string][]token.Position // line information for XTestEmbedPatternPos
	*/
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
func ImportRaw(path string, srcDir string) (*RawPackage, error) {
	p := &RawPackage{
		SrcDir: srcDir,
	}
	if path == "" {
		return p, fmt.Errorf("import %q: invalid import path", path)
	}

	if !IsLocalImport(path) {
		panic(path)
		if srcDir == "" {
			return p, fmt.Errorf("import %q: import relative to unknown directory", path)
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
		// TODO(matloob): figure this out... Since we're not looking for goroot packages,
		// always return error below.
		/*if ctxt.Compiler == "gccgo" && p.Goroot {
			// gccgo has no sources for GOROOT packages.
			return p, nil
		}*/

		// package was not found
		return p, fmt.Errorf("cannot find package %q in:\n\t%s", p.ImportPath, p.Dir)
	}

	// TODO: use os.ReadDir
	dirs, err := ioutil.ReadDir(p.Dir)
	if err != nil {
		return p, err
	}

	var badGoError error
	badFiles := make(map[string]bool)
	badFile := func(tf TaggedFile, err error) {
		if badGoError == nil {
			badGoError = err
		}
		if !badFiles[tf.Name] {
			p.InvalidGoFiles = append(p.InvalidGoFiles, tf.Name)
			badFiles[tf.Name] = true
		}
	}

	var Sfiles []TaggedFile // files with ".S"(capital S)/.sx(capital s equivalent for case insensitive filesystems)
	var firstFile /*, firstCommentFile*/ string

	/*
		embedPos := make(map[string][]token.Position)
		testEmbedPos := make(map[string][]token.Position)
		xTestEmbedPos := make(map[string][]token.Position)
		importPos := make(map[string][]token.Position)
		testImportPos := make(map[string][]token.Position)
		xTestImportPos := make(map[string][]token.Position)
		allTags := make(map[string]bool)*/
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

		info, err := getInfo(p.Dir, name, &p.BinaryOnly, fset)
		if err != nil {
			badFile(TaggedFile{Name: name}, err)
			continue
		}

		/*
			if info == nil {

				if strings.HasPrefix(tf, "_") || strings.HasPrefix(tf, ".") {
					// not due to build constraints - don't report
				} else if ext == ".go" {
					p.IgnoredGoFiles = append(p.IgnoredGoFiles, tf)
				} else if fileListForExt(p, ext) != nil {
					p.IgnoredOtherFiles = append(p.IgnoredOtherFiles, tf)
				}
				continue
			}
		*/
		if info == nil {
			p.GoFiles = append(p.GoFiles, TaggedFile{Name: name, IgnoreFile: true})
		}
		tf := TaggedFile{
			Name:                 name,
			PkgName:              info.parsed.Name.Name,
			GoBuildConstraint:    info.goBuildConstraint,
			PlusBuildConstraints: info.plusBuildConstraints,
		}
		data := info.header

		// Going to save the file. For non-Go files, can stop here.
		switch ext {
		case ".go":
			// keep going
		case ".S", ".sx":
			// special case for cgo, handled at end
			Sfiles = append(Sfiles, tf)
			continue
		default:
			if list := fileListForExtRaw(p, ext); list != nil {
				*list = append(*list, tf)
			}
			continue
		}

		if info.parseErr != nil {
			badFile(tf, info.parseErr)
			// Fall through: we might still have a partial AST in info.parsed,
			// and we want to list files with parse errors anyway.
		}

		var pkg string
		if info.parsed != nil {
			pkg = info.parsed.Name.Name
			if pkg == "documentation" {
				p.DocumentationGoFiles = append(p.DocumentationGoFiles, name)
				continue
			}
		}

		isTest := strings.HasSuffix(name, "_test.go")
		isXTest := false
		if isTest && strings.HasSuffix(pkg, "_test") && p.Name != pkg {
			isXTest = true
			pkg = pkg[:len(pkg)-len("_test")]
		}

		if p.Name == "" {
			p.Name = pkg
			firstFile = name
		} else if pkg != p.Name {
			// TODO(#45999): The choice of p.Name is arbitrary based on file iteration
			// order. Instead of resolving p.Name arbitrarily, we should clear out the
			// existing tf and mark the existing files as also invalid.
			badFile(tf, &MultiplePackageError{
				Dir:      p.Dir,
				Packages: []string{p.Name, pkg},
				Files:    []string{firstFile, name},
			})
		}
		// Grab the first package comment as docs, provided it is not from a test file.
		if info.parsed != nil && info.parsed.Doc != nil && p.Doc == "" && !isTest && !isXTest {
			p.Doc = doc.Synopsis(info.parsed.Doc.Text())
		}

		qcom, line := findImportComment(data)
		if line != 0 {
			tf.QuotedImportComment = qcom
			tf.QuotedImportCommentLine = line
		}
		// Add import comment?
		/*
				if mode&ImportComment != 0 {
					qcom, line := findImportComment(data)
					if line != 0 {
						com, err := strconv.Unquote(qcom)
						if err != nil {
							badFile(tf, fmt.Errorf("%s:%d: cannot parse import comment", filename, line))
						} else if p.ImportComment == "" {
							p.ImportComment = com
							firstCommentFile = tf
						} else if p.ImportComment != com {
							badFile(tf, fmt.Errorf("found import comments %q (%s) and %q (%s) in %s", p.ImportComment, firstCommentFile, com, tf, p.Dir))
						}
					}
				}

			// Record imports and information about cgo.
			isCgo := false
			for _, imp := range info.imports {
				if imp.path == "C" {
					if isTest {
						badFile(tf, fmt.Errorf("use of cgo in test %s not supported", name))
						continue
					}
					isCgo = true

						if imp.doc != nil {
							if err := ctxt.saveCgo(filename, p, imp.doc); err != nil {
								badFile(tf, err)
							}
						}


				}
			}

				var fileList *[]string
				//var importMap, embedMap map[string][]token.Position
				switch {
				case isCgo:
					//allTags["cgo"] = true
					if ctxt.CgoEnabled {
						fileList = &p.CgoFiles
						importMap = importPos
						embedMap = embedPos
					} else {
						// Ignore imports and embeds from cgo files if cgo is disabled.
						fileList = &p.IgnoredGoFiles
					}
				case isXTest:
					fileList = &p.XTestGoFiles
					importMap = xTestImportPos
					embedMap = xTestEmbedPos
				case isTest:
					fileList = &p.TestGoFiles
					importMap = testImportPos
					embedMap = testEmbedPos
				default:
					fileList = &p.GoFiles
					importMap = importPos
					embedMap = embedPos
				}*/
		for _, imp := range info.imports {
			// TODO(matloob): only save doc for cgo?
			// TODO(matloob): remove filename from position and add it back later to save space?
			tf.Imports = append(tf.Imports, TFImport{Path: imp.path, Doc: imp.doc.Text(), Position: fset.Position(imp.pos)})
		}
		tf.Embeds = make(map[string][]token.Position)
		for _, emb := range info.embeds {
			tf.Embeds[emb.pattern] = append(tf.Embeds[emb.pattern], emb.pos)
		}
		/*if importMap != nil {
			for _, imp := range info.imports {
				importMap[imp.path] = append(importMap[imp.path], fset.Position(imp.pos))
			}
		}
		if embedMap != nil {
			for _, emb := range info.embeds {
				embedMap[emb.pattern] = append(embedMap[emb.pattern], emb.pos)
			}
		}*/
		p.GoFiles = append(p.GoFiles, tf)
		// *fileList = append(*fileList, tf)
	}

	/*
		for tag := range allTags {
			p.AllTags = append(p.AllTags, tag)
		}
		sort.Strings(p.AllTags)

		p.EmbedPatterns, p.EmbedPatternPos = cleanDecls(embedPos)
		p.TestEmbedPatterns, p.TestEmbedPatternPos = cleanDecls(testEmbedPos)
		p.XTestEmbedPatterns, p.XTestEmbedPatternPos = cleanDecls(xTestEmbedPos)

		p.Imports, p.ImportPos = cleanDecls(importPos)
		p.TestImports, p.TestImportPos = cleanDecls(testImportPos)
		p.XTestImports, p.XTestImportPos = cleanDecls(xTestImportPos)
	*/
	// TODO Remove these if we're not using cgo.
	p.SFiles = append(p.SFiles, Sfiles...)

	// add the .S/.sx files only if we are using cgo
	// (which means gcc will compile them).
	// The standard assemblers expect .s files.
	/*
		if len(p.CgoFiles) > 0 {
			p.SFiles = append(p.SFiles, Sfiles...)
			sort.Strings(p.SFiles)
		} else {
			p.IgnoredOtherFiles = append(p.IgnoredOtherFiles, Sfiles...)
			sort.Strings(p.IgnoredOtherFiles)
		}
	*/

	if badGoError != nil {
		return p, badGoError
	}
	/*
		if len(p.GoFiles)+len(p.CgoFiles)+len(p.TestGoFiles)+len(p.XTestGoFiles) == 0 {
			return p, &NoGoError{p.Dir}
		}*/
	return p, nil
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}

type fileInfoPlus struct {
	fileInfo
	sawBinaryOnly        bool
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
func getInfo(dir, name string, binaryOnly *bool, fset *token.FileSet) (*fileInfoPlus, error) {
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

	if strings.HasSuffix(name, ".go") {
		err = readGoInfo(f, &info.fileInfo)
		if strings.HasSuffix(name, "_test.go") {
			binaryOnly = nil // ignore //go:binary-only-package comments in _test.go files
		}
	} else {
		binaryOnly = nil // ignore //go:binary-only-package comments in non-Go sources
		info.header, err = readComments(f)
	}
	f.Close()
	if err != nil {
		return nil, fmt.Errorf("read %s: %v", info.name, err)
	}

	// Look for +build comments to accept or reject the file.
	info.goBuildConstraint, info.plusBuildConstraints, info.sawBinaryOnly, err = getConstraints(info.header)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}

	if binaryOnly != nil && info.sawBinaryOnly {
		*binaryOnly = true
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

func TODOToExpand() {
	/*
		var pkg string
		if info.parsed != nil {
			pkg = info.parsed.Name.Name
			if pkg == "documentation" {
				p.IgnoredGoFiles = append(p.IgnoredGoFiles, name)
				continue
			}
		}

	*/
}

func fileListForExtRaw(p *RawPackage, ext string) *[]TaggedFile {
	switch ext {
	case ".c":
		return &p.CFiles
	case ".cc", ".cpp", ".cxx":
		return &p.CXXFiles
	case ".m":
		return &p.MFiles
	case ".h", ".hh", ".hpp", ".hxx":
		return &p.HFiles
	case ".f", ".F", ".for", ".f90":
		return &p.FFiles
	case ".s", ".S", ".sx":
		return &p.SFiles
	case ".swig":
		return &p.SwigFiles
	case ".swigcxx":
		return &p.SwigCXXFiles
	case ".syso":
		return &p.SysoFiles
	}
	return nil
}

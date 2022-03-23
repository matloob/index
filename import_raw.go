package index

import (
	"fmt"
	"go/doc"
	"go/token"
	"io/fs"
	pathpkg "path"
	"sort"
	"strconv"
	"strings"

	"index/internal/goroot"
)

type TaggedFile struct {
	Name    string
	Tags    []string
	Imports map[string]token.Position
	Embeds  map[string]token.Position
}

// todo doc
type RawPackage struct {
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
	AllTags     []string // tags that can influence file selection in this directory
	ConflictDir string   // this directory shadows Dir in $GOPATH
	BinaryOnly  bool     // cannot be rebuilt from source (has //go:binary-only-package comment)

	//TODO(matloob): turn not go file types into strings?

	// Source files
	GoFiles  []TaggedFile // .go source files (including TestGoFiles, XTestGoFiles)
	CgoFiles []TaggedFile // .go source files that import "C"
	// IgnoredGoFiles    []TaggedFile // .go source files ignored for this build (including ignored _test.go files)
	InvalidGoFiles []TaggedFile // .go source files with detected problems (parse error, wrong package name, and so on)
	// IgnoredOtherFiles []TaggedFile // non-.go source files ignored for this build
	CFiles       []string // .c source files
	CXXFiles     []string // .cc, .cpp and .cxx source files
	MFiles       []string // .m (Objective-C) source files
	HFiles       []string // .h, .hh, .hpp and .hxx source files
	FFiles       []string // .f, .F, .for and .f90 Fortran source files
	SFiles       []string // .s source files
	SwigFiles    []string // .swig files
	SwigCXXFiles []string // .swigcxx files
	SysoFiles    []string // .syso system object files to add to archive

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
func ImportRaw(path string, srcDir string, mode ImportMode) (*RawPackage, error) {
	p := &RawPackage{
		ImportPath: path,
	}
	if path == "" {
		return p, fmt.Errorf("import %q: invalid import path", path)
	}

	var pkgtargetroot string
	var pkga string
	var pkgerr error
	suffix := ""
	if ctxt.InstallSuffix != "" {
		suffix = "_" + ctxt.InstallSuffix
	}
	switch ctxt.Compiler {
	case "gccgo":
		pkgtargetroot = "pkg/gccgo_" + ctxt.GOOS + "_" + ctxt.GOARCH + suffix
	case "gc":
		pkgtargetroot = "pkg/" + ctxt.GOOS + "_" + ctxt.GOARCH + suffix
	default:
		// Save error for end of function.
		pkgerr = fmt.Errorf("import %q: unknown compiler %q", path, ctxt.Compiler)
	}
	setPkga := func() {
		switch ctxt.Compiler {
		case "gccgo":
			dir, elem := pathpkg.Split(p.ImportPath)
			pkga = pkgtargetroot + "/" + dir + "lib" + elem + ".a"
		case "gc":
			pkga = pkgtargetroot + "/" + p.ImportPath + ".a"
		}
	}
	setPkga()

	binaryOnly := false
	if IsLocalImport(path) {
		pkga = "" // local imports have no installed path
		if srcDir == "" {
			return p, fmt.Errorf("import %q: import relative to unknown directory", path)
		}
		if !ctxt.isAbsPath(path) {
			p.Dir = ctxt.joinPath(srcDir, path)
		}
		// p.Dir directory may or may not exist. Gather partial information first, check if it exists later.
		// Determine canonical import path, if any.
		// Exclude results where the import path would include /testdata/.
		inTestdata := func(sub string) bool {
			return strings.Contains(sub, "/testdata/") || strings.HasSuffix(sub, "/testdata") || strings.HasPrefix(sub, "testdata/") || sub == "testdata"
		}
		if ctxt.GOROOT != "" {
			root := ctxt.joinPath(ctxt.GOROOT, "src")
			if sub, ok := ctxt.hasSubdir(root, p.Dir); ok && !inTestdata(sub) {
				p.Goroot = true
				p.ImportPath = sub
				p.Root = ctxt.GOROOT
				setPkga() // p.ImportPath changed
				goto Found
			}
		}
		all := ctxt.gopath()
		for i, root := range all {
			rootsrc := ctxt.joinPath(root, "src")
			if sub, ok := ctxt.hasSubdir(rootsrc, p.Dir); ok && !inTestdata(sub) {
				// We found a potential import path for dir,
				// but check that using it wouldn't find something
				// else first.
				if ctxt.GOROOT != "" && ctxt.Compiler != "gccgo" {
					if dir := ctxt.joinPath(ctxt.GOROOT, "src", sub); ctxt.isDir(dir) {
						p.ConflictDir = dir
						goto Found
					}
				}
				for _, earlyRoot := range all[:i] {
					if dir := ctxt.joinPath(earlyRoot, "src", sub); ctxt.isDir(dir) {
						p.ConflictDir = dir
						goto Found
					}
				}

				// sub would not name some other directory instead of this one.
				// Record it.
				p.ImportPath = sub
				p.Root = root
				setPkga() // p.ImportPath changed
				goto Found
			}
		}
		// It's okay that we didn't find a root containing dir.
		// Keep going with the information we have.
	} else {
		if strings.HasPrefix(path, "/") {
			return p, fmt.Errorf("import %q: cannot import absolute path", path)
		}

		if err := ctxt.importGo(p, path, srcDir, mode); err == nil {
			goto Found
		} else if err != errNoModules {
			return p, err
		}

		gopath := ctxt.gopath() // needed twice below; avoid computing many times

		// tried records the location of unsuccessful package lookups
		var tried struct {
			vendor []string
			goroot string
			gopath []string
		}

		// Vendor directories get first chance to satisfy import.
		if mode&IgnoreVendor == 0 && srcDir != "" {
			searchVendor := func(root string, isGoroot bool) bool {
				sub, ok := ctxt.hasSubdir(root, srcDir)
				if !ok || !strings.HasPrefix(sub, "src/") || strings.Contains(sub, "/testdata/") {
					return false
				}
				for {
					vendor := ctxt.joinPath(root, sub, "vendor")
					if ctxt.isDir(vendor) {
						dir := ctxt.joinPath(vendor, path)
						if ctxt.isDir(dir) && hasGoFiles(ctxt, dir) {
							p.Dir = dir
							p.ImportPath = strings.TrimPrefix(pathpkg.Join(sub, "vendor", path), "src/")
							p.Goroot = isGoroot
							p.Root = root
							setPkga() // p.ImportPath changed
							return true
						}
						tried.vendor = append(tried.vendor, dir)
					}
					i := strings.LastIndex(sub, "/")
					if i < 0 {
						break
					}
					sub = sub[:i]
				}
				return false
			}
			if ctxt.Compiler != "gccgo" && searchVendor(ctxt.GOROOT, true) {
				goto Found
			}
			for _, root := range gopath {
				if searchVendor(root, false) {
					goto Found
				}
			}
		}

		// Determine directory from import path.
		if ctxt.GOROOT != "" {
			// If the package path starts with "vendor/", only search GOROOT before
			// GOPATH if the importer is also within GOROOT. That way, if the user has
			// vendored in a package that is subsequently included in the standard
			// distribution, they'll continue to pick up their own vendored copy.
			gorootFirst := srcDir == "" || !strings.HasPrefix(path, "vendor/")
			if !gorootFirst {
				_, gorootFirst = ctxt.hasSubdir(ctxt.GOROOT, srcDir)
			}
			if gorootFirst {
				dir := ctxt.joinPath(ctxt.GOROOT, "src", path)
				if ctxt.Compiler != "gccgo" {
					isDir := ctxt.isDir(dir)
					binaryOnly = !isDir && mode&AllowBinary != 0 && pkga != "" && ctxt.isFile(ctxt.joinPath(ctxt.GOROOT, pkga))
					if isDir || binaryOnly {
						p.Dir = dir
						p.Goroot = true
						p.Root = ctxt.GOROOT
						goto Found
					}
				}
				tried.goroot = dir
			}
		}
		if ctxt.Compiler == "gccgo" && goroot.IsStandardPackage(ctxt.GOROOT, ctxt.Compiler, path) {
			p.Dir = ctxt.joinPath(ctxt.GOROOT, "src", path)
			p.Goroot = true
			p.Root = ctxt.GOROOT
			goto Found
		}
		for _, root := range gopath {
			dir := ctxt.joinPath(root, "src", path)
			isDir := ctxt.isDir(dir)
			binaryOnly = !isDir && mode&AllowBinary != 0 && pkga != "" && ctxt.isFile(ctxt.joinPath(root, pkga))
			if isDir || binaryOnly {
				p.Dir = dir
				p.Root = root
				goto Found
			}
			tried.gopath = append(tried.gopath, dir)
		}

		// If we tried GOPATH first due to a "vendor/" prefix, fall back to GOPATH.
		// That way, the user can still get useful results from 'go list' for
		// standard-vendored paths passed on the command line.
		if ctxt.GOROOT != "" && tried.goroot == "" {
			dir := ctxt.joinPath(ctxt.GOROOT, "src", path)
			if ctxt.Compiler != "gccgo" {
				isDir := ctxt.isDir(dir)
				binaryOnly = !isDir && mode&AllowBinary != 0 && pkga != "" && ctxt.isFile(ctxt.joinPath(ctxt.GOROOT, pkga))
				if isDir || binaryOnly {
					p.Dir = dir
					p.Goroot = true
					p.Root = ctxt.GOROOT
					goto Found
				}
			}
			tried.goroot = dir
		}

		// package was not found
		var paths []string
		format := "\t%s (vendor tree)"
		for _, dir := range tried.vendor {
			paths = append(paths, fmt.Sprintf(format, dir))
			format = "\t%s"
		}
		if tried.goroot != "" {
			paths = append(paths, fmt.Sprintf("\t%s (from $GOROOT)", tried.goroot))
		} else {
			paths = append(paths, "\t($GOROOT not set)")
		}
		format = "\t%s (from $GOPATH)"
		for _, dir := range tried.gopath {
			paths = append(paths, fmt.Sprintf(format, dir))
			format = "\t%s"
		}
		if len(tried.gopath) == 0 {
			paths = append(paths, "\t($GOPATH not set. For more details see: 'go help gopath')")
		}
		return p, fmt.Errorf("cannot find package %q in any of:\n%s", path, strings.Join(paths, "\n"))
	}

Found:
	if p.Root != "" {
		p.SrcRoot = ctxt.joinPath(p.Root, "src")
		p.PkgRoot = ctxt.joinPath(p.Root, "pkg")
		p.BinDir = ctxt.joinPath(p.Root, "bin")
		if pkga != "" {
			p.PkgTargetRoot = ctxt.joinPath(p.Root, pkgtargetroot)
			p.PkgObj = ctxt.joinPath(p.Root, pkga)
		}
	}

	// If it's a local import path, by the time we get here, we still haven't checked
	// that p.Dir directory exists. This is the right time to do that check.
	// We can't do it earlier, because we want to gather partial information for the
	// non-nil *Package returned when an error occurs.
	// We need to do this before we return early on FindOnly flag.
	if IsLocalImport(path) && !ctxt.isDir(p.Dir) {
		if ctxt.Compiler == "gccgo" && p.Goroot {
			// gccgo has no sources for GOROOT packages.
			return p, nil
		}

		// package was not found
		return p, fmt.Errorf("cannot find package %q in:\n\t%s", p.ImportPath, p.Dir)
	}

	if mode&FindOnly != 0 {
		return p, pkgerr
	}
	if binaryOnly && (mode&AllowBinary) != 0 {
		return p, pkgerr
	}

	if ctxt.Compiler == "gccgo" && p.Goroot {
		// gccgo has no sources for GOROOT packages.
		return p, nil
	}

	dirs, err := ctxt.readDir(p.Dir)
	if err != nil {
		return p, err
	}

	var badGoError error
	badFiles := make(map[string]bool)
	badFile := func(name string, err error) {
		if badGoError == nil {
			badGoError = err
		}
		if !badFiles[name] {
			p.InvalidGoFiles = append(p.InvalidGoFiles, name)
			badFiles[name] = true
		}
	}

	var Sfiles []string // files with ".S"(capital S)/.sx(capital s equivalent for case insensitive filesystems)
	var firstFile, firstCommentFile string
	embedPos := make(map[string][]token.Position)
	testEmbedPos := make(map[string][]token.Position)
	xTestEmbedPos := make(map[string][]token.Position)
	importPos := make(map[string][]token.Position)
	testImportPos := make(map[string][]token.Position)
	xTestImportPos := make(map[string][]token.Position)
	allTags := make(map[string]bool)
	fset := token.NewFileSet()
	for _, d := range dirs {
		if d.IsDir() {
			continue
		}
		if d.Mode()&fs.ModeSymlink != 0 {
			if ctxt.isDir(ctxt.joinPath(p.Dir, d.Name())) {
				// Symlinks to directories are not source files.
				continue
			}
		}

		name := d.Name()
		ext := nameExt(name)

		info, err := ctxt.matchFile(p.Dir, name, allTags, &p.BinaryOnly, fset)
		if err != nil {
			badFile(name, err)
			continue
		}
		if info == nil {
			if strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
				// not due to build constraints - don't report
			} else if ext == ".go" {
				p.IgnoredGoFiles = append(p.IgnoredGoFiles, name)
			} else if fileListForExt(p, ext) != nil {
				p.IgnoredOtherFiles = append(p.IgnoredOtherFiles, name)
			}
			continue
		}
		data, filename := info.header, info.name

		// Going to save the file. For non-Go files, can stop here.
		switch ext {
		case ".go":
			// keep going
		case ".S", ".sx":
			// special case for cgo, handled at end
			Sfiles = append(Sfiles, name)
			continue
		default:
			if list := fileListForExt(p, ext); list != nil {
				*list = append(*list, name)
			}
			continue
		}

		if info.parseErr != nil {
			badFile(name, info.parseErr)
			// Fall through: we might still have a partial AST in info.parsed,
			// and we want to list files with parse errors anyway.
		}

		var pkg string
		if info.parsed != nil {
			pkg = info.parsed.Name.Name
			if pkg == "documentation" {
				p.IgnoredGoFiles = append(p.IgnoredGoFiles, name)
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
			// existing name and mark the existing files as also invalid.
			badFile(name, &MultiplePackageError{
				Dir:      p.Dir,
				Packages: []string{p.Name, pkg},
				Files:    []string{firstFile, name},
			})
		}
		// Grab the first package comment as docs, provided it is not from a test file.
		if info.parsed != nil && info.parsed.Doc != nil && p.Doc == "" && !isTest && !isXTest {
			p.Doc = doc.Synopsis(info.parsed.Doc.Text())
		}

		if mode&ImportComment != 0 {
			qcom, line := findImportComment(data)
			if line != 0 {
				com, err := strconv.Unquote(qcom)
				if err != nil {
					badFile(name, fmt.Errorf("%s:%d: cannot parse import comment", filename, line))
				} else if p.ImportComment == "" {
					p.ImportComment = com
					firstCommentFile = name
				} else if p.ImportComment != com {
					badFile(name, fmt.Errorf("found import comments %q (%s) and %q (%s) in %s", p.ImportComment, firstCommentFile, com, name, p.Dir))
				}
			}
		}

		// Record imports and information about cgo.
		isCgo := false
		for _, imp := range info.imports {
			if imp.path == "C" {
				if isTest {
					badFile(name, fmt.Errorf("use of cgo in test %s not supported", filename))
					continue
				}
				isCgo = true
				if imp.doc != nil {
					if err := ctxt.saveCgo(filename, p, imp.doc); err != nil {
						badFile(name, err)
					}
				}
			}
		}

		var fileList *[]string
		var importMap, embedMap map[string][]token.Position
		switch {
		case isCgo:
			allTags["cgo"] = true
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
		}
		*fileList = append(*fileList, name)
		if importMap != nil {
			for _, imp := range info.imports {
				importMap[imp.path] = append(importMap[imp.path], fset.Position(imp.pos))
			}
		}
		if embedMap != nil {
			for _, emb := range info.embeds {
				embedMap[emb.pattern] = append(embedMap[emb.pattern], emb.pos)
			}
		}
	}

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

	// add the .S/.sx files only if we are using cgo
	// (which means gcc will compile them).
	// The standard assemblers expect .s files.
	if len(p.CgoFiles) > 0 {
		p.SFiles = append(p.SFiles, Sfiles...)
		sort.Strings(p.SFiles)
	} else {
		p.IgnoredOtherFiles = append(p.IgnoredOtherFiles, Sfiles...)
		sort.Strings(p.IgnoredOtherFiles)
	}

	if badGoError != nil {
		return p, badGoError
	}
	if len(p.GoFiles)+len(p.CgoFiles)+len(p.TestGoFiles)+len(p.XTestGoFiles) == 0 {
		return p, &NoGoError{p.Dir}
	}
	return p, pkgerr
}

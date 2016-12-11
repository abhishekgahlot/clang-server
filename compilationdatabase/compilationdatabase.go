// Copyright 2016 The clang-server Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package compilationdatabase

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-clang/v3.9/clang"
	"github.com/pkg/errors"
	"github.com/uber-go/zap"
	"github.com/zchee/clang-server/log"
)

// DefaultJSONName default of compile_commands.json filename.
const DefaultJSONName = "compile_commands.json"

// CompilationDatabase represents a compilation database.
type CompilationDatabase struct {
	projectRoot string

	cd    clang.CompilationDatabase
	found bool

	flags  map[string][]string
	flagMu sync.Mutex
}

// NewCompilationDatabase return the new CompilationDatabase struct.
func NewCompilationDatabase(root string) *CompilationDatabase {
	return &CompilationDatabase{
		projectRoot: root,
		flags:       make(map[string][]string),
	}
}

// findFile finds the filename on pathRange recursively.
func (c *CompilationDatabase) findFile(filename string, pathRange []string) string {
	if pathRange == nil {
		parent := filepath.Dir(c.projectRoot)
		buildDir := filepath.Join(c.projectRoot, "build")
		pathRange = []string{c.projectRoot, parent, buildDir}
	}

	pathCh := make(chan string, len(pathRange))
	for _, d := range pathRange {
		go func(d string) {
			_, err := os.Stat(filepath.Join(d, filename))
			if !os.IsNotExist(err) {
				log.Debug("found", zap.String("filepath", filepath.Join(d, filename)))
				pathCh <- d
			}
		}(d)
	}

	return <-pathCh
}

// Parse parses the project root directory recursively, and cache the compile
// flags to flags map.
func (c *CompilationDatabase) Parse(jsonfile string, pathRange ...string) error {
	if jsonfile == "" {
		jsonfile = DefaultJSONName
	}

	dir := c.findFile(jsonfile, pathRange)
	if dir == "" {
		return errors.Errorf("couldn't find the %s file", jsonfile)
	}
	c.found = true

	cErr, cd := clang.FromDirectory(dir)
	if cErr != clang.CompilationDatabase_NoError {
		return errors.WithStack(cErr)
	}
	c.cd = cd
	defer c.cd.Dispose()

	if err := c.parseAllFlags(); err != nil {
		return errors.WithStack(err)
	}

	return nil
}

// parseAllFlags parses the all of project files compile flag.
func (c *CompilationDatabase) parseAllFlags() error {
	c.flagMu.Lock()
	defer c.flagMu.Unlock()

	cmds := c.cd.AllCompileCommands()
	ncmds := cmds.Size()

	for i := uint32(0); i < ncmds; i++ {
		cmd := cmds.Command(i)
		args := c.parseFlags(cmd)
		c.flags[cmd.Filename()] = args
	}

	return nil
}

// Flags return the compile flags by filename.
func (c *CompilationDatabase) Flags(filename string) []string {
	c.flagMu.Lock()
	defer c.flagMu.Unlock()

	if c.flags[filename] != nil {
		return c.flags[filename]
	}

	cmds := c.cd.CompileCommands(filename)
	flags := c.parseFlags(cmds.Command(0))
	c.flags[filename] = flags

	return flags
}

// parseFlags parses the compile flag from cmd.
func (c *CompilationDatabase) parseFlags(cmd clang.CompileCommand) []string {
	n := cmd.NumArgs()
	flags := make([]string, 0, n)

	for i := uint32(0); i < n; i++ {
		f := cmd.Arg(i)
		switch {
		case f == "-D":
			flags = append(flags, f, cmd.Arg(i+1))
		case strings.HasPrefix(f, "-D"):
			flags = append(flags, f)
		case f == "-I":
			includeDir := c.absDir(cmd.Arg(i+1), cmd.Directory())
			flags = append(flags, "-I", includeDir)
		case strings.HasPrefix(f, "-I"):
			includeDir := c.absDir(strings.Replace(f, "-I", "", 1), cmd.Directory())
			flags = append(flags, "-I", includeDir)
		}
	}

	return flags
}

// absDir return the absolube directory path based by buildDir.
func (c *CompilationDatabase) absDir(includePath, buildDir string) string {
	if filepath.IsAbs(includePath) {
		return includePath
	}

	abs, err := filepath.Abs(includePath)
	if err != nil {
		return includePath
	}

	return filepath.Clean(abs)
}